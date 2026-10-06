package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/eyelock/ynr"
	"github.com/eyelock/ynr/internal/query"
	"github.com/eyelock/ynr/internal/store"
)

// centralState is where central keeps its hot tier's database and, by default, its socket:
// $XDG_STATE_HOME/ynr/central, or ~/.local/state/ynr/central. It is a cache; deleting it
// loses nothing (NFR-19).
func centralState() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "ynr", "central")
}

// centralSocket is the default socket in a state folder, moved under the temporary directory
// when the path would be too long for a Unix socket, as hotPaths does for a spool.
func centralSocket(state string) string {
	socket := filepath.Join(state, "central.sock")
	if len(socket) <= maxSocket {
		return socket
	}
	sum := sha256.Sum256([]byte(state))
	return filepath.Join(os.TempDir(), fmt.Sprintf("ynr-%d", os.Getuid()), "central-"+hex.EncodeToString(sum[:8])+".sock")
}

// holderID names this central instance in the leases it takes: host, pid and a random suffix, so
// a restarted instance never mistakes itself for the one that died.
func holderID() string {
	host, _ := os.Hostname()
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("central@%s/%d/%s", host, os.Getpid(), hex.EncodeToString(b[:]))
}

// centralCmd is ynr central (ADR-005): the reader of a shared store. It keeps the last hours in
// a hot tier fed by the change feed, compacts hours under leases, keeps the item index and the
// rollups, and answers ynr query on a Unix socket. Collectors never talk to it.
func centralCmd(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flags("central", stderr)
	storeURL := fs.String("store", env("YNR_STORE", ""), "the shared object store to read, such as s3://bucket?region=eu-west-2 (YNR_STORE)")
	hotWindow := fs.Duration("hot-window", 6*time.Hour, "how far back the hot tier holds records")
	ui := fs.String("ui", env("YNR_UI", ""), "serve the dashboard on this address (not available until sign-in is built) (YNR_UI)")
	erase := fs.String("erase", env("YNR_ERASE", ""), "a file of handles to erase, one per line (YNR_ERASE)")
	state := fs.String("state", env("YNR_CENTRAL_STATE", centralState()), "folder for the hot tier's database, which only this user can open (YNR_CENTRAL_STATE)")
	socket := fs.String("socket", env("YNR_CENTRAL_SOCKET", ""), "Unix socket to answer queries on; default central.sock in --state (YNR_CENTRAL_SOCKET)")
	poll := fs.Duration("poll", 5*time.Second, "how often to poll the store for new batches")
	lookback := fs.Duration("compact-lookback", 24*time.Hour, "how far back hours are compacted")
	every := fs.Duration("compact-every", 5*time.Minute, "how often compaction runs")
	retain := fs.Duration("retain", store.LaptopRetention.Records, "how long a folder store keeps records; a bucket's lifecycle rules do this on S3")
	retainBytes := fs.Int64("retain-bytes", 0, "the most a folder store's records may take, in bytes (0: no cap)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if ynr.Build != "full" {
		_, _ = fmt.Fprintf(stderr, "ynr: central needs the full build, which includes DuckDB; this is the %s build\n", ynr.Build)
		return ExitConfig
	}
	switch {
	case *ui != "":
		_, _ = fmt.Fprintln(stderr, "ynr: sign-in not built yet; --ui is refused rather than serve central's data unauthenticated")
		return ExitConfig
	case *storeURL == "":
		_, _ = fmt.Fprintln(stderr, "ynr: no store: set --store or YNR_STORE")
		return ExitConfig
	case *state == "":
		_, _ = fmt.Fprintln(stderr, "ynr: no state folder: set --state or YNR_CENTRAL_STATE")
		return ExitConfig
	case *hotWindow <= 0 || *poll <= 0 || *lookback <= 0 || *every <= 0:
		_, _ = fmt.Fprintln(stderr, "ynr: --hot-window, --poll, --compact-lookback and --compact-every must be positive")
		return ExitConfig
	case *retain <= 0 || *retainBytes < 0:
		_, _ = fmt.Fprintln(stderr, "ynr: --retain must be positive and --retain-bytes not negative")
		return ExitConfig
	}
	if code := loadErasure(*erase, stderr); code != ExitOK {
		return code
	}
	r, err := store.OpenReader(*storeURL)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: --store: %v\n", err)
		return ExitConfig
	}
	if err := privateDir(*state); err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: --state: %v\n", err)
		return ExitConfig
	}
	sock := *socket
	if sock == "" {
		sock = centralSocket(*state)
		if err := privateDir(filepath.Dir(sock)); err != nil {
			_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
			return ExitConfig
		}
	}
	if len(sock) > 103 {
		_, _ = fmt.Fprintf(stderr, "ynr: --socket %s is too long for a Unix socket\n", sock)
		return ExitConfig
	}
	// listenUnix replaces a stale socket, so first make sure no live central owns it.
	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		_ = c.Close()
		_, _ = fmt.Fprintf(stderr, "ynr: another ynr is already answering on %s; give this one its own --state or --socket\n", sock)
		return ExitAdapter
	}
	var mu sync.Mutex
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(stderr, "ynr: "+format+"\n", args...)
	}
	// A bucket's lifecycle rules are its retention (ADR-005); only a folder needs ynr to do it.
	if u, err := url.Parse(*storeURL); err == nil && u.Scheme == "file" {
		stop := startRetention(ctx, *storeURL, store.Retention{Records: *retain, Rollups: store.LaptopRetention.Rollups, MaxBytes: *retainBytes}, stderr)
		defer stop()
	}
	c := query.DefaultCompaction
	c.Lookback = *lookback
	c.Holder = holderID()
	logf("central on %s: hot tier %s, queries on %s, compacting as %s", r.URL(), *hotWindow, sock, c.Holder)
	err = query.ServeHot(ctx, query.HotConfig{
		Path: filepath.Join(*state, "hot.duckdb"), Socket: sock, Store: r, Window: *hotWindow, Poll: *poll,
		Compact: &c, CompactEvery: *every, Feed: true, Logf: logf,
	})
	if errors.Is(err, query.ErrSlim) {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitConfig
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: central: %v\n", err)
		return ExitAdapter
	}
	return ExitOK
}
