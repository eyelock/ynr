package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/eyelock/ynr/internal/query"
	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/internal/store"
)

// maxSocket is the longest Unix socket path every platform accepts (macOS allows 104 bytes).
const maxSocket = 100

// hotPaths are where a spool's hot tier keeps its database and answers queries: in the spool's
// state folder, which only its owner can open. Where that path is too long for a socket, the
// socket goes in a folder of the user's own under the temporary directory, named for the root.
func hotPaths(root string) (db, socket string) {
	state := filepath.Join(root, spool.StateDir)
	db, socket = filepath.Join(state, "hot.duckdb"), filepath.Join(state, "serve.sock")
	if len(socket) <= maxSocket {
		return db, socket
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	sum := sha256.Sum256([]byte(abs))
	return db, filepath.Join(os.TempDir(), fmt.Sprintf("ynr-%d", os.Getuid()), hex.EncodeToString(sum[:8])+".sock")
}

// privateDir makes sure the socket's folder exists, belongs to this user and no one else can
// open it, so no other user can plant or reach the socket.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm() != 0o700 {
		return fmt.Errorf("%s must be a folder of this user's that only they can open", dir)
	}
	return nil
}

// startHot runs the hot tier beside ynr serve when the build has DuckDB and the store can be
// read, and compacts the store: a laptop's ynr serve is its folder's only reader (ADR-005). It never stops serve shipping: a failure is reported and serve carries on. The caller
// holds the spool's lock, so no other server owns the hot tier's files.
func startHot(ctx context.Context, root, storeURL string, window, poll time.Duration, stderr io.Writer) (stop func()) {
	if storeURL == "" {
		return func() {}
	}
	r, err := store.OpenReader(storeURL)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: no hot tier: %v\n", err)
		return func() {}
	}
	db, socket := hotPaths(root)
	if err := privateDir(filepath.Dir(socket)); err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: no hot tier: %v\n", err)
		return func() {}
	}
	var mu sync.Mutex
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(stderr, "ynr: "+format+"\n", args...)
	}
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c := query.DefaultCompaction
		c.Lookback = window
		err := query.ServeHot(hctx, query.HotConfig{Path: db, Socket: socket, Store: r, Window: window,
			Poll: max(poll, 2*time.Second), Compact: &c, Logf: logf})
		if err != nil && !errors.Is(err, query.ErrSlim) {
			logf("no hot tier: %v", err)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
