// Package cli is the ynr command line (ADR-001). Every role runs in the foreground until it is
// stopped (ADR-004); every command that reports takes --format json.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eyelock/ynr"
	"github.com/eyelock/ynr/internal/collector"
	"github.com/eyelock/ynr/internal/relay"
	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/internal/stamp"
	"github.com/eyelock/ynr/internal/store"
)

// Exit codes, following ynf's.
const (
	ExitOK      = 0
	ExitUsage   = 2
	ExitAdapter = 20 // the spool, the collector or the upstream failed
	ExitConfig  = 30 // settings invalid
)

const usage = `ynr: your named reporting

Usage:
  ynr version
  ynr info [--spool <root>] [--format text|json]
  ynr serve [--spool <root>] [--store <url>] [--upstream <otlp-http-endpoint>]
            [--collector-id <id>] [--collector-instance <id>] [--poll 1s]
            [--max-line <bytes>] [--hot-window 168h] [--ui 127.0.0.1:4319] [--debug]
  ynr relay --spool <writer folder> [--listen 127.0.0.1:0] [--format text|json]
            [--max-request <bytes>] [--max-memory <bytes>] [--rate <per second>]
            [--exit-on-stdin-eof]
  ynr tail [--spool <root>] [--service <name>] [--item <key>] [--from-start] [--format text|json]
  ynr query [<name> [<argument>] [--store <url>] [--since 7d] [--until <time>] [--lane <id>]
            [--format text|json]]

ynr relay prints its OTLP/HTTP endpoint as its first line of output, then runs until it is
stopped (Ctrl-C, SIGTERM, or with --exit-on-stdin-eof its standard input closing), flushing what
it received into the folder.

ynr tail follows what tools write to the spool as it is written, until Ctrl-C. It only watches:
ynr serve still ships everything.

ynr query with no name lists the named queries. It reads the store directly and needs the full
build, which includes DuckDB.

ynr serve ships to a store, by default a folder on this machine, and optionally also forwards to
an OTLP/HTTP endpoint. --store "" ships only to the upstream. In the full build it also keeps the
store's recent records in a hot tier and answers ynr query from it.

Environment fallbacks: YNR_SPOOL_ROOT, YNR_STORE, YNR_UPSTREAM, YNR_COLLECTOR_ID,
YNR_COLLECTOR_INSTANCE, and YNR_SPOOL for the relay's folder.
`

// stdin is the relay's standard input, replaced in tests.
var stdin io.Reader = os.Stdin

// Run runs one command and returns its exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintf(stdout, "%s (%s)\n", ynr.Version, ynr.Build)
		return ExitOK
	case "info":
		return info(args[1:], stdout, stderr)
	case "serve":
		return serve(ctx, args[1:], stderr)
	case "relay":
		return relayCmd(ctx, args[1:], stdin, stdout, stderr)
	case "query":
		return queryCmd(ctx, args[1:], stdout, stderr)
	case "tail":
		return tailCmd(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return ExitOK
	default:
		_, _ = fmt.Fprintf(stderr, "ynr: unknown command %q\n\n%s", args[0], usage)
		return ExitUsage
	}
}

func flags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("ynr "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// env returns the environment fallback for a flag.
func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func defaultRoot() string {
	if v := os.Getenv("YNR_SPOOL_ROOT"); v != "" {
		return v
	}
	r, err := spool.DefaultRoot()
	if err != nil {
		return ""
	}
	return r
}

// envOr is env for a flag whose default is computed, where an empty variable still counts:
// YNR_STORE="" turns the store off.
func envOr(name, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}

// defaultStore is the laptop's folder store: $XDG_DATA_HOME/ynr/store, or
// ~/.local/share/ynr/store.
func defaultStore() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return store.FolderURL(filepath.Join(base, "ynr", "store"))
}

// infoReport is ynr info's JSON (ADR-004).
type infoReport struct {
	Version      string          `json:"version"`
	Build        string          `json:"build"`
	Capabilities string          `json:"capabilities"`
	Spool        string          `json:"spool"`
	Serving      *spool.LockInfo `json:"serving"`
}

func info(args []string, stdout, stderr io.Writer) int {
	fs := flags("info", stderr)
	root := fs.String("spool", defaultRoot(), "spool root")
	format := fs.String("format", "text", "text or json")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	rep := infoReport{Version: ynr.Version, Build: ynr.Build, Capabilities: ynr.Capabilities, Spool: *root}
	holder, err := spool.Holder(*root)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: reading the spool lock: %v\n", err)
		return ExitAdapter
	}
	rep.Serving = holder
	switch *format {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	case "text":
		_, _ = fmt.Fprintf(stdout, "version       %s (%s)\ncapabilities  %s\nspool         %s\n", rep.Version, rep.Build, rep.Capabilities, rep.Spool)
		if holder != nil {
			_, _ = fmt.Fprintf(stdout, "serving       pid %d since %s\n", holder.PID, holder.Since.Format(time.RFC3339))
		} else {
			_, _ = fmt.Fprintln(stdout, "serving       no")
		}
	default:
		_, _ = fmt.Fprintf(stderr, "ynr: --format must be text or json\n")
		return ExitUsage
	}
	return ExitOK
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// defaultCollectorID names a laptop's collector after its host.
func defaultCollectorID() string {
	h, err := os.Hostname()
	if err != nil {
		return "local"
	}
	h = strings.ToLower(strings.SplitN(h, ".", 2)[0])
	h = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(h, "-")
	return strings.Trim("local-"+h, "-")
}

func serve(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flags("serve", stderr)
	root := fs.String("spool", defaultRoot(), "spool root (YNR_SPOOL_ROOT)")
	id := fs.String("collector-id", env("YNR_COLLECTOR_ID", defaultCollectorID()), "this collector's identity: a runner pool or host (YNR_COLLECTOR_ID)")
	instance := fs.String("collector-instance", env("YNR_COLLECTOR_INSTANCE", ""), "the job within the pool, recorded as data (YNR_COLLECTOR_INSTANCE)")
	storeURL := fs.String("store", envOr("YNR_STORE", defaultStore()), "object store to ship batches to, such as file:///path; empty for none (YNR_STORE)")
	upstream := fs.String("upstream", env("YNR_UPSTREAM", ""), "OTLP/HTTP endpoint to also forward to, such as http://localhost:4318 (YNR_UPSTREAM)")
	poll := fs.Duration("poll", time.Second, "how often to read the spool")
	maxLine := fs.Int("max-line", spool.DefaultMaxLine, "longest line accepted, in bytes")
	debug := fs.Bool("debug", false, "also print a summary of what is shipped")
	hotWindow := fs.Duration("hot-window", 7*24*time.Hour, "how far back the hot tier holds records, for queries (full build only)")
	ui := fs.String("ui", env("YNR_UI", ""), "serve the local dashboard on this loopback address, such as 127.0.0.1:4319 (full build only) (YNR_UI)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *ui != "" && ynr.Build != "full" {
		_, _ = fmt.Fprintf(stderr, "ynr: this %s build has no dashboard; --ui needs the full build\n", ynr.Build)
		return ExitConfig
	}
	switch {
	case *root == "":
		_, _ = fmt.Fprintln(stderr, "ynr: no spool root: set --spool or YNR_SPOOL_ROOT")
		return ExitConfig
	case !idPattern.MatchString(*id):
		_, _ = fmt.Fprintf(stderr, "ynr: --collector-id %q must be lower-case letters, digits, '.', '_' or '-'\n", *id)
		return ExitConfig
	case *upstream == "" && *storeURL == "":
		_, _ = fmt.Fprintln(stderr, "ynr: nowhere to ship: set --store, --upstream, or both")
		return ExitConfig
	case *maxLine <= 0:
		_, _ = fmt.Fprintln(stderr, "ynr: --max-line must be positive")
		return ExitConfig
	case *hotWindow <= 0:
		_, _ = fmt.Fprintln(stderr, "ynr: --hot-window must be positive")
		return ExitConfig
	}
	if *storeURL != "" {
		if _, err := store.Open(*storeURL); err != nil {
			_, _ = fmt.Fprintf(stderr, "ynr: --store: %v\n", err)
			return ExitConfig
		}
	}
	if err := spool.Init(*root); err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: creating the spool: %v\n", err)
		return ExitAdapter
	}
	lock, err := spool.Acquire(*root)
	if errors.Is(err, spool.ErrServing) {
		holder, _ := spool.Holder(*root)
		pid := "unknown"
		if holder != nil {
			pid = strconv.Itoa(holder.PID)
		}
		_, _ = fmt.Fprintf(stderr, "ynr: %s is already served by pid %s\n", *root, pid)
		return ExitAdapter
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: locking the spool: %v\n", err)
		return ExitAdapter
	}
	defer lock.Release()
	var uiLn net.Listener
	if *ui != "" {
		if uiLn, err = listenLoopback(*ui); err != nil {
			_, _ = fmt.Fprintf(stderr, "ynr: --ui: %v\n", err)
			return ExitConfig
		}
		if *storeURL == "" {
			_ = uiLn.Close()
			_, _ = fmt.Fprintln(stderr, "ynr: --ui needs a store to read: set --store")
			return ExitConfig
		}
		_, _ = fmt.Fprintf(stderr, "ynr: dashboard at http://%s/\n", uiLn.Addr())
	}
	stopHot := startHot(ctx, *root, *storeURL, *hotWindow, *poll, uiLn, stderr)
	defer stopHot()
	err = collector.Run(ctx, collector.Settings{
		SpoolRoot:    *root,
		PollInterval: *poll,
		MaxLine:      *maxLine,
		Identity:     stamp.Identity{ID: *id, Instance: *instance},
		Store:        *storeURL,
		Upstream:     *upstream,
		Debug:        *debug,
	})
	if err != nil && ctx.Err() == nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitAdapter
	}
	return ExitOK
}

// relayReady is ynr relay's first line of output with --format json.
type relayReady struct {
	Endpoint string `json:"endpoint"`
	PID      int    `json:"pid"`
}

func relayCmd(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flags("relay", stderr)
	dir := fs.String("spool", os.Getenv("YNR_SPOOL"), "writer folder to write into, such as a run's folder (YNR_SPOOL)")
	listen := fs.String("listen", relay.DefaultListen, "loopback address; port 0 picks a free port")
	format := fs.String("format", "text", "first line of output: the endpoint as text, or json")
	maxRequest := fs.Int64("max-request", relay.DefaultMaxRequest, "largest request accepted, in bytes after decompression")
	maxMemory := fs.Int64("max-memory", relay.DefaultMaxMemory, "most bytes of requests held at once")
	perSecond := fs.Float64("rate", relay.DefaultRate, "requests accepted a second, sustained")
	stdinEOF := fs.Bool("exit-on-stdin-eof", false, "also stop when standard input closes, so the relay ends with the process that started it")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	switch {
	case *dir == "":
		_, _ = fmt.Fprintln(stderr, "ynr: no folder: set --spool or YNR_SPOOL")
		return ExitConfig
	case *format != "text" && *format != "json":
		_, _ = fmt.Fprintln(stderr, "ynr: --format must be text or json")
		return ExitUsage
	case *maxRequest <= 0 || *maxMemory <= 0 || *perSecond <= 0:
		_, _ = fmt.Fprintln(stderr, "ynr: --max-request, --max-memory and --rate must be positive")
		return ExitConfig
	}
	r, err := relay.New(relay.Config{
		Dir: *dir, Listen: *listen, MaxRequest: *maxRequest, MaxMemory: *maxMemory, Rate: *perSecond,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitConfig
	}
	// The first line is the contract with whoever started the relay: it reads the endpoint
	// from it, then configures the vendor CLI.
	if *format == "json" {
		_ = json.NewEncoder(stdout).Encode(relayReady{Endpoint: r.Endpoint(), PID: os.Getpid()})
	} else {
		_, _ = fmt.Fprintln(stdout, r.Endpoint())
	}
	if *stdinEOF {
		// Whoever started the relay holds the other end of stdin; when it exits, even by
		// SIGKILL, the pipe closes and the relay drains and stops as on SIGTERM.
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			_, _ = io.Copy(io.Discard, stdin)
			cancel()
		}()
	}
	err = r.Serve(ctx)
	s := r.Stats()
	_, _ = fmt.Fprintf(stderr, "ynr relay: %d accepted; refused %d too large, %d busy, %d rate limited, %d malformed; %d records dropped by the spool\n",
		s.Accepted, s.TooLarge, s.Busy, s.Limited, s.Malformed, s.Spool.Dropped)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitAdapter
	}
	return ExitOK
}
