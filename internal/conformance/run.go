package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eyelock/ynr/internal/relay"
)

// The ways a scenario is run, each a separate run of the same command (ADR-008).
const (
	runSpool      = "spool"             // only YNR_SPOOL set
	runEndpoint   = "operator endpoint" // OTEL_EXPORTER_OTLP_ENDPOINT set as well as YNR_SPOOL
	runNeither    = "neither"           // no target at all
	runUnwritable = "unwritable spool"  // YNR_SPOOL names a place nothing can be written
	runHung       = "hung endpoint"     // an endpoint that accepts connections and never answers
	runKill       = "kill"              // killed as soon as a started event is in the spool
)

// stubVendorService is the service.name the stub vendor reports as, so its records can be told
// from the tool's.
const stubVendorService = "claude-code"

// killHold is how long the scenario's stub vendor, and any tool that honours the variable,
// waits in a turn in the kill run: long enough that the kill always lands mid-run.
const killHold = "30s"

// runOutcome is one run of a scenario and what it left behind.
type runOutcome struct {
	Kind        string
	Exit        int
	Duration    time.Duration
	TimedOut    bool // still running at the limit and killed
	Killed      bool // killed on purpose, by the kill run
	ExitedEarly bool // the kill run's command ended before it could be killed
	Output      string

	TraceID, SpanID string // the TRACEPARENT the command was given
	Marker          string // the OTEL_RESOURCE_ATTRIBUTES value it was given

	Spool, Endpoint *Capture
	Stray           []string // spool-format files anywhere under the run's folder
	ShimCalls       []string // times a ynr on the path was started
}

// sandbox is one scenario's temporary folders and the lookups it needs.
type sandbox struct {
	root       string
	service    string
	vendorPath string // the stub vendor binary, "" if not on PATH
	vendorName []string
	invoked    string // where the working directory for conformance was invoked
	timeout    time.Duration
	killWait   time.Duration // how long the kill run waits for a started event
	env        map[string]string
	limit12    time.Duration // how long a rule 12 run may take before it counts as blocked
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// execRun runs the scenario's command once in the given way.
func (s *sandbox) execRun(ctx context.Context, kind, command string) (*runOutcome, error) {
	dir := filepath.Join(s.root, strings.ReplaceAll(kind, " ", "-"))
	out := &runOutcome{Kind: kind, TraceID: randHex(16), SpanID: randHex(8), Marker: randHex(6)}
	spoolRoot := filepath.Join(dir, "spool")
	writer := filepath.Join(spoolRoot, "services", s.service)
	epRoot := filepath.Join(dir, "endpoint")
	cwd := filepath.Join(dir, "cwd")
	bin := filepath.Join(dir, "bin")
	state := filepath.Join(dir, "state")
	for _, d := range []string{cwd, bin, state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	// A ynr on the path that records being started and does nothing else (rule 14), and the
	// stub vendor under the names the scenarios use.
	log := filepath.Join(dir, "ynr-started.log")
	shim := fmt.Sprintf("#!/bin/sh\necho \"ynr $*\" >> '%s'\nexit 0\n", log)
	if err := os.WriteFile(filepath.Join(bin, "ynr"), []byte(shim), 0o700); err != nil {
		return nil, err
	}
	if s.vendorPath != "" {
		for _, n := range s.vendorName {
			if err := os.Symlink(s.vendorPath, filepath.Join(bin, n)); err != nil {
				return nil, err
			}
		}
	}

	env := map[string]string{
		"PATH":                     bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"XDG_STATE_HOME":           state,
		"TRACEPARENT":              "00-" + out.TraceID + "-" + out.SpanID + "-01",
		"OTEL_RESOURCE_ATTRIBUTES": "conformance.marker=" + out.Marker,
		"YNR_CONFORMANCE_ROOT":     s.invoked,
	}
	for k, v := range s.env {
		env[k] = v
	}
	stop := func() error { return nil }
	mkWriter := func() error { return os.MkdirAll(writer, 0o700) }
	limit := s.timeout
	var until func() bool
	switch kind {
	case runSpool:
		if err := mkWriter(); err != nil {
			return nil, err
		}
		env["YNR_SPOOL"] = writer
	case runEndpoint:
		if err := mkWriter(); err != nil {
			return nil, err
		}
		env["YNR_SPOOL"] = writer
		url, st, err := startEndpoint(epRoot)
		if err != nil {
			return nil, err
		}
		stop = st
		env["OTEL_EXPORTER_OTLP_ENDPOINT"] = url
	case runNeither:
	case runUnwritable:
		// A full filesystem cannot be made without privileges; a path below a file fails every
		// write in the same way, for any user.
		blocked := filepath.Join(dir, "blocked")
		if err := os.WriteFile(blocked, []byte("not a folder"), 0o600); err != nil {
			return nil, err
		}
		env["YNR_SPOOL"] = filepath.Join(blocked, "spool")
		limit = s.limit12
	case runHung:
		url, st, err := startHung()
		if err != nil {
			return nil, err
		}
		stop = st
		env["OTEL_EXPORTER_OTLP_ENDPOINT"] = url
		limit = s.limit12
	case runKill:
		if err := mkWriter(); err != nil {
			return nil, err
		}
		env["YNR_SPOOL"] = writer
		env["YNR_STUB_TURN_DELAY"] = killHold
		until = func() bool { return hasStarted(spoolRoot) }
		limit = s.killWait
	default:
		return nil, fmt.Errorf("unknown run %q", kind)
	}

	res := runCommand(ctx, command, cwd, mergeEnv(env), limit, until, filepath.Join(dir, "output.log"))
	out.Exit, out.Duration, out.TimedOut, out.Killed, out.ExitedEarly, out.Output =
		res.exit, res.duration, res.timedOut, res.killed, res.exitedEarly, res.output
	if err := stop(); err != nil {
		return nil, err
	}
	var err error
	if out.Spool, err = readCapture(spoolRoot); err != nil {
		return nil, fmt.Errorf("reading the spool: %w", err)
	}
	if out.Endpoint, err = readCapture(epRoot); err != nil {
		return nil, fmt.Errorf("reading the test endpoint: %w", err)
	}
	for _, p := range strayFiles(dir) {
		// Only what the command itself left: not the endpoint's own files, which the harness
		// wrote, and not the writer folder the harness made for the runs that name one.
		if !strings.HasPrefix(p, "endpoint"+string(os.PathSeparator)) {
			out.Stray = append(out.Stray, p)
		}
	}
	if b, err := os.ReadFile(log); err == nil {
		out.ShimCalls = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	return out, nil
}

// mergeEnv is the inherited environment without anything that steers telemetry, then set.
func mergeEnv(set map[string]string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || k == "XDG_STATE_HOME" || k == "TRACEPARENT" || k == "TRACESTATE" ||
			strings.HasPrefix(k, "OTEL_") || strings.HasPrefix(k, "YNR_") {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return env
}

type cmdResult struct {
	exit                          int
	duration                      time.Duration
	timedOut, killed, exitedEarly bool
	output                        string
}

// runCommand runs command through sh -c until it exits, the limit passes, or until reports true,
// the last two killing everything it started.
func runCommand(ctx context.Context, command, dir string, env []string, limit time.Duration, until func() bool, logPath string) cmdResult {
	var res cmdResult
	f, err := os.Create(logPath)
	if err != nil {
		res.exit = -1
		res.output = err.Error()
		return res
	}
	defer func() { _ = f.Close() }()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, f, f
	ownGroup(cmd)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		res.exit = -1
		res.output = err.Error()
		return res
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	var werr error
loop:
	for {
		select {
		case werr = <-done:
			res.exitedEarly = until != nil
			break loop
		case <-tick.C:
			if until != nil && until() {
				killTree(cmd)
				werr = <-done
				res.killed = true
				break loop
			}
		case <-deadline.C:
			killTree(cmd)
			werr = <-done
			res.timedOut = true
			break loop
		case <-ctx.Done():
			killTree(cmd)
			werr = <-done
			res.timedOut = true
			break loop
		}
	}
	res.duration = time.Since(start)
	var ee *exec.ExitError
	switch {
	case werr == nil:
	case errors.As(werr, &ee):
		res.exit = ee.ExitCode()
	default:
		res.exit = -1
	}
	if b, err := os.ReadFile(logPath); err == nil {
		if len(b) > 2048 {
			b = b[len(b)-2048:]
		}
		res.output = strings.TrimSpace(string(b))
	}
	return res
}

// hasStarted reports whether any spool file holds a started event yet.
func hasStarted(root string) bool {
	found := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(`.started"`)) {
			found = true
		}
		return nil
	})
	return found
}

// startEndpoint starts the operator's endpoint: ynr's own relay writing into a folder the
// checks then read, so what arrives is decoded exactly as ynr decodes anything. It returns the
// endpoint URL and a function that stops it after a final flush.
func startEndpoint(root string) (string, func() error, error) {
	dir := filepath.Join(root, "services", "endpoint")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	r, err := relay.New(relay.Config{Dir: dir, Service: "conformance-endpoint", Drain: 2 * time.Second})
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx) }()
	return r.Endpoint(), func() error {
		cancel()
		return <-done
	}, nil
}

// startHung starts an endpoint that accepts connections and never answers: what a tool must
// survive when the operator's collector is stuck (rule 12).
func startHung() (string, func() error, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	return "http://" + ln.Addr().String(), func() error {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
		return nil
	}, nil
}
