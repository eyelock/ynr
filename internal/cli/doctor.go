package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/eyelock/ynr"
	"github.com/eyelock/ynr/internal/query"
	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/internal/store"
)

// A check's result.
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// ExitDoctor is ynr doctor's exit code when any check warns or fails.
const ExitDoctor = 1

// doctor checks this machine's ynr setup (ADR-001, ADR-004). It only reads: it never starts,
// stops or changes anything.
func doctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flags("doctor", stderr)
	root := fs.String("spool", defaultRoot(), "spool root (YNR_SPOOL_ROOT)")
	storeURL := fs.String("store", envOr("YNR_STORE", defaultStore()), "the store ynr serve ships to (YNR_STORE)")
	format := fs.String("format", "text", "text or json")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *format != "text" && *format != "json" {
		_, _ = fmt.Fprintln(stderr, "ynr: --format must be text or json")
		return ExitUsage
	}
	checks := []check{
		checkOTLP(os.Environ()),
		checkPath(),
		checkSpool(*root),
		checkServing(*root),
		checkBacklog(*root, time.Now()),
		checkStore(*storeURL),
		checkQueries(ctx, *root),
	}
	code := ExitOK
	for _, c := range checks {
		if c.Status != statusOK {
			code = ExitDoctor
		}
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Version string  `json:"version"`
			Build   string  `json:"build"`
			Checks  []check `json:"checks"`
		}{ynr.Version, ynr.Build, checks})
		return code
	}
	_, _ = fmt.Fprintf(stdout, "ynr %s (%s)\n", ynr.Version, ynr.Build)
	for _, c := range checks {
		_, _ = fmt.Fprintf(stdout, "%-5s %-9s %s\n", c.Status, c.Name, c.Detail)
	}
	return code
}

// checkOTLP warns when the operator's OTLP variables are set: tools then export to that
// collector instead of to ynr, and the spool receives nothing from them (ADR-004).
func checkOTLP(env []string) check {
	var set []string
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "OTEL_EXPORTER_OTLP_") && strings.HasSuffix(k, "ENDPOINT") {
			set = append(set, k)
		}
	}
	if len(set) == 0 {
		return check{"otlp", statusOK, "no OTEL_EXPORTER_OTLP_*ENDPOINT set, so tools write to ynr's spool"}
	}
	sort.Strings(set)
	return check{"otlp", statusWarn, strings.Join(set, ", ") + " set: tools export there instead of to ynr's spool"}
}

// checkPath reports which ynr the shell finds, since tools find each other on PATH alone.
func checkPath() check {
	found, err := exec.LookPath("ynr")
	if err != nil {
		return check{"path", statusWarn, "no ynr on PATH, so other tools will not find one"}
	}
	self, _ := os.Executable()
	a, _ := filepath.EvalSymlinks(found)
	b, _ := filepath.EvalSymlinks(self)
	if a != "" && a == b {
		return check{"path", statusOK, "ynr on PATH is this one: " + found}
	}
	return check{"path", statusWarn, "ynr on PATH is " + found + ", not this one (" + self + ")"}
}

func checkSpool(root string) check {
	if root == "" {
		return check{"spool", statusFail, "no spool root: set --spool or YNR_SPOOL_ROOT"}
	}
	fi, err := os.Stat(filepath.Join(root, spool.StateDir))
	if errors.Is(err, os.ErrNotExist) {
		return check{"spool", statusWarn, root + " does not exist yet; ynr serve creates it, and tools write nothing until then"}
	}
	if err != nil {
		return check{"spool", statusFail, err.Error()}
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return check{"spool", statusWarn, fmt.Sprintf("%s is mode %v; only its owner should open it", filepath.Join(root, spool.StateDir), fi.Mode().Perm())}
	}
	return check{"spool", statusOK, root}
}

func checkServing(root string) check {
	info, err := spool.Holder(root)
	switch {
	case err != nil:
		return check{"serve", statusFail, err.Error()}
	case info == nil:
		return check{"serve", statusWarn, "no ynr serve holds the spool, so nothing is shipped"}
	}
	return check{"serve", statusOK, fmt.Sprintf("pid %d since %s", info.PID, info.Since.Format(time.RFC3339))}
}

// checkBacklog warns when closed files wait long in the spool: ynr serve ships a closed file at
// once, so an old one means it is not running or cannot reach its store.
func checkBacklog(root string, now time.Time) check {
	writers, err := spool.Writers(root, &spool.Counters{})
	if err != nil {
		return check{"backlog", statusOK, "no spool to read"}
	}
	var files int
	var bytes int64
	var oldest time.Time
	for _, w := range writers {
		entries, _ := os.ReadDir(w.Dir)
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), spool.ClosedSuffix) {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			files++
			bytes += fi.Size()
			if !strings.HasSuffix(e.Name(), spool.OpenSuffix) && (oldest.IsZero() || fi.ModTime().Before(oldest)) {
				oldest = fi.ModTime()
			}
		}
	}
	detail := fmt.Sprintf("%d files, %.1f MiB", files, float64(bytes)/(1<<20))
	if !oldest.IsZero() && now.Sub(oldest) > 5*time.Minute {
		return check{"backlog", statusWarn, detail + fmt.Sprintf("; a closed file has waited %s to be shipped", now.Sub(oldest).Round(time.Second))}
	}
	return check{"backlog", statusOK, detail}
}

func checkStore(raw string) check {
	if raw == "" {
		return check{"store", statusWarn, "no store: ynr serve ships only to an upstream, if one is set"}
	}
	r, err := store.OpenReader(raw)
	if err != nil {
		return check{"store", statusFail, err.Error()}
	}
	keys, err := r.List(context.Background(), "rollups/")
	if err != nil {
		return check{"store", statusFail, err.Error()}
	}
	return check{"store", statusOK, fmt.Sprintf("%s (%d rollup files)", raw, len(keys))}
}

// checkQueries asks the running ynr serve for its list of queries, which only a full build's
// hot tier answers.
func checkQueries(ctx context.Context, root string) check {
	if root == "" {
		return check{"queries", statusWarn, "no spool root"}
	}
	_, socket := hotPaths(root)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _, err := query.Ask(ctx, socket, "runs", query.Raw{Since: "1h"})
	switch {
	case err == nil:
		return check{"queries", statusOK, "ynr serve answers ynr query from its hot tier"}
	case errors.Is(err, query.ErrNoServer):
		return check{"queries", statusWarn, "no hot tier answering: ynr query reads the store directly, and needs the full build"}
	}
	return check{"queries", statusWarn, err.Error()}
}
