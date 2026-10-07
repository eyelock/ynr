package collector

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"

	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/internal/stamp"
	"github.com/eyelock/ynr/internal/store"
)

const traceLine = `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynh"}}]},` +
	`"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",` +
	`"name":"ynh.run","startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`

// TestShipsStampedSpansThenDeletes runs the real Collector against a fake OTLP/HTTP upstream.
func TestShipsStampedSpansThenDeletes(t *testing.T) {
	var mu sync.Mutex
	var provenance []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var src io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			src = zr
		}
		body, _ := io.ReadAll(src)
		req := ptraceotlp.NewExportRequest()
		if err := req.UnmarshalProto(body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		rs := req.Traces().ResourceSpans()
		mu.Lock()
		for i := 0; i < rs.Len(); i++ {
			v, _ := rs.At(i).Resource().Attributes().Get(stamp.Provenance)
			provenance = append(provenance, v.Str())
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "local", "ynh-1.jsonl")
	if err := os.WriteFile(file, []byte(traceLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Settings{
			SpoolRoot: root, PollInterval: 50 * time.Millisecond, MaxLine: spool.DefaultMaxLine,
			Identity: stamp.Identity{ID: "test"}, Upstream: upstream.URL,
		})
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, err := os.Stat(file)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the spool file was never shipped and deleted")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(provenance) != 1 || provenance[0] != "local" {
		t.Fatalf("upstream saw provenance %v, want [local]", provenance)
	}
}

func TestSomewhereToShipIsRequired(t *testing.T) {
	if _, err := Config(Settings{SpoolRoot: "/x"}); err == nil {
		t.Fatal("want an error with neither a store nor an upstream")
	}
}

// TestShipsBatchesToTheStore runs the real Collector with a folder store and no upstream: an
// open file's lines are shipped once due, a closed file's at once, each as a compressed batch
// named by its source and byte range, and the closed file is deleted only after.
func TestShipsBatchesToTheStore(t *testing.T) {
	root, storeDir := t.TempDir(), t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	closed := filepath.Join(root, "local", "ynh-a-000001.jsonl")
	if err := os.WriteFile(closed, []byte(traceLine+"\n"+logLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(root, "local", "ynh-a-000002.open.jsonl")
	if err := os.WriteFile(open, []byte(traceLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Settings{
			SpoolRoot: root, PollInterval: 20 * time.Millisecond, MaxLine: spool.DefaultMaxLine,
			Identity: stamp.Identity{ID: "laptop"}, Store: store.FolderURL(storeDir), ShipAge: 300 * time.Millisecond,
		})
	}()
	deadline := time.Now().Add(15 * time.Second)
	for len(batches(t, storeDir)) < 3 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("batches = %v, want 3", batches(t, storeDir))
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(closed); !errors.Is(err, os.ErrNotExist) {
		t.Error("the closed file was not deleted after shipping")
	}
	if _, err := os.Stat(open); err != nil {
		t.Error("the open file was deleted")
	}
	var traces, logs int
	for _, key := range batches(t, storeDir) {
		parts := strings.Split(key, "/")
		if len(parts) != 7 || parts[5] != "laptop" {
			t.Errorf("key %s: want <signal>/<yyyy>/<mm>/<dd>/<hh>/laptop/<name>", key)
			continue
		}
		name := parts[6]
		switch {
		case strings.HasSuffix(name, "_local.ynh-a-000001-0-"+strconv.Itoa(len(traceLine)+len(logLine)+2)+".jsonl.gz"):
		case strings.HasSuffix(name, "_local.ynh-a-000002-0-"+strconv.Itoa(len(traceLine)+1)+".jsonl.gz"):
		default:
			t.Errorf("batch name %s: want the source and byte range", name)
		}
		body := gunzip(t, filepath.Join(storeDir, key))
		if !strings.Contains(body, `"ynr.provenance","value":{"stringValue":"local"}`) {
			t.Errorf("%s is not stamped: %s", key, body)
		}
		switch parts[0] {
		case "traces":
			traces++
		case "logs":
			logs++
		}
	}
	if traces != 2 || logs != 1 {
		t.Errorf("traces %d, logs %d; want 2 and 1", traces, logs)
	}
}

const logLine = `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynh"}}]},` +
	`"scopeLogs":[{"logRecords":[{"eventName":"ynh.run.started"}]}]}]}`

func batches(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl.gz") {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func gunzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// okUpstream is a fake OTLP/HTTP upstream that accepts everything and counts the trace exports.
func okUpstream(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/v1/traces" {
			mu.Lock()
			n++
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// TestStartsWithAStoreAndAnUpstream runs the real Collector with both: the upstream's retry
// settings must pass the Collector's validation, and both destinations must receive the data.
func TestStartsWithAStoreAndAnUpstream(t *testing.T) {
	upstream, seen := okUpstream(t)
	root, storeDir := t.TempDir(), t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "local", "ynh-a-000001.jsonl"), []byte(traceLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Settings{
			SpoolRoot: root, PollInterval: 20 * time.Millisecond, MaxLine: spool.DefaultMaxLine,
			Identity: stamp.Identity{ID: "laptop"}, Store: store.FolderURL(storeDir), Upstream: upstream.URL,
		})
	}()
	deadline := time.Now().Add(15 * time.Second)
	for len(batches(t, storeDir)) < 1 || seen() < 1 {
		select {
		case err := <-done:
			t.Fatalf("the collector stopped before shipping: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("batches = %v, upstream exports = %d; want both", batches(t, storeDir), seen())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
}

// TestShipsOnShutdownWithoutAStore writes a line after the last poll and stops: the final poll
// must still forward it to the upstream.
func TestShipsOnShutdownWithoutAStore(t *testing.T) {
	upstream, seen := okUpstream(t)
	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "local", "ynh-1.jsonl")
	if err := os.WriteFile(first, []byte(traceLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Settings{
			// Polls an hour apart: after the first, only the final poll can ship.
			SpoolRoot: root, PollInterval: time.Hour, MaxLine: spool.DefaultMaxLine,
			Identity: stamp.Identity{ID: "ci"}, Upstream: upstream.URL,
		})
	}()
	deadline := time.Now().Add(15 * time.Second)
	for seen() < 1 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the first poll never shipped")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(root, "local", "ynh-2.jsonl"), []byte(traceLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
	if n := seen(); n != 2 {
		t.Fatalf("upstream saw %d trace exports, want 2: the line written before shutdown was not shipped", n)
	}
}
