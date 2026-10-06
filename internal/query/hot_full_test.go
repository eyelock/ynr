//go:build full

package query

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

func openHot(t *testing.T, path string, r store.Reader) *Hot {
	t.Helper()
	h, err := OpenHot(context.Background(), path, r, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h.now = func() time.Time { return now }
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func count(t *testing.T, h *Hot) map[string]int64 {
	t.Helper()
	c, err := h.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func hotRun(t *testing.T, h *Hot, name string, p Params) *Result {
	t.Helper()
	q, _ := Lookup(name)
	if p.Until.IsZero() {
		p.Until = now.Add(time.Minute)
	}
	if p.Since.IsZero() {
		p.Since = now.Add(-q.Since)
	}
	res, err := h.Run(context.Background(), q, p)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestHotTierFillsOnceAndRebuilds: the hot tier holds each record once however often it was
// shipped, picks up batches as they land, and a new hot tier rebuilds the same from the store.
func TestHotTierFillsOnceAndRebuilds(t *testing.T) {
	r := openStore(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hot.duckdb")
	line := traceLine(t,
		runSpan(traceA, "1111111111111111", "", "converged", "m", 1, now.Add(-time.Hour)),
		runSpan(traceB, "2222222222222222", "", "budget", "m", 1, now.Add(-time.Hour)))
	ship(t, r, store.Traces, now.Add(-50*time.Minute), "local.a", line)
	ship(t, r, store.Traces, now.Add(-40*time.Minute), "local.a", line) // re-shipped
	ship(t, r, store.Logs, now.Add(-40*time.Minute), "local.a",
		eventLine(t, "ynh.run.started", now.Add(-time.Hour), map[string]any{"service.name": "ynh"}, nil))

	h := openHot(t, path, r)
	if err := h.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if c := count(t, h); c["spans"] != 2 || c["logs"] != 1 {
		t.Fatalf("after the first sync: %v", c)
	}
	ship(t, r, store.Traces, now.Add(-time.Minute), "local.b",
		traceLine(t, runSpan("0af7651916cd43dd8448eb211c80319d", "3333333333333333", "", "converged", "m", 1, now.Add(-2*time.Minute))))
	if err := h.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if c := count(t, h); c["spans"] != 3 {
		t.Fatalf("after a batch landed: %v", c)
	}
	if res := hotRun(t, h, "runs", Params{}); len(res.Rows) != 2 {
		t.Fatalf("runs = %v", rows(res))
	}
	_ = h.Close()

	h2 := openHot(t, path, r)
	if err := h2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if c := count(t, h2); c["spans"] != 3 || c["logs"] != 1 {
		t.Fatalf("after a restart: %v", c)
	}
}

// TestDamagedBatchIsSkipped: a file that cannot be read is counted and skipped, and the files
// read with it still arrive.
func TestDamagedBatchIsSkipped(t *testing.T) {
	r := openStore(t)
	ship(t, r, store.Traces, now.Add(-time.Hour), "local.good",
		traceLine(t, runSpan(traceA, "1111111111111111", "", "converged", "m", 1, now.Add(-time.Hour))))
	key := store.BatchKey(store.Traces, now.Add(-time.Hour), "laptop", store.NewULID(now), "local.bad", 0, 9)
	if err := r.Put(context.Background(), key, []byte("not gzip")); err != nil {
		t.Fatal(err)
	}
	h := openHot(t, filepath.Join(t.TempDir(), "hot.duckdb"), r)
	if err := h.Sync(context.Background()); err == nil {
		t.Fatal("a damaged batch went unreported")
	}
	if c := count(t, h); c["spans"] != 1 || h.Failed != 1 {
		t.Fatalf("spans %v, failed %d", c, h.Failed)
	}
	if err := h.Sync(context.Background()); err != nil {
		t.Fatalf("the damaged batch was retried: %v", err)
	}
}

// TestHotTierEvictsAndFallsBack: records older than the window leave the hot tier, and a query
// reaching back further reads the store.
func TestHotTierEvictsAndFallsBack(t *testing.T) {
	r := openStore(t)
	ctx := context.Background()
	ship(t, r, store.Traces, now.Add(-2*time.Hour), "local.a",
		traceLine(t, runSpan(traceA, "1111111111111111", "", "converged", "m", 1, now.Add(-2*time.Hour))))
	h := openHot(t, filepath.Join(t.TempDir(), "hot.duckdb"), r)
	if err := h.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	h.now = func() time.Time { return now.Add(7 * 24 * time.Hour) }
	if err := h.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if c := count(t, h); c["spans"] != 0 {
		t.Fatalf("kept an old span: %v", c)
	}
	res := hotRun(t, h, "runs", Params{Since: now.Add(-24 * time.Hour), Until: now})
	if len(res.Rows) != 1 {
		t.Fatalf("a window before the hot tier's did not read the store: %v", rows(res))
	}
}

// TestServeHotAnswersOnItsSocket runs the hot tier as ynr serve does and asks it a query.
func TestServeHotAnswersOnItsSocket(t *testing.T) {
	r := openStore(t)
	at := time.Now().UTC()
	ship(t, r, store.Traces, at, "local.a",
		traceLine(t, runSpan(traceA, "1111111111111111", "", "converged", "claude-opus-5-5", 0.5, at.Add(-time.Minute))))
	dir, err := os.MkdirTemp("", "ynr") // short: a socket path is limited to about 100 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "serve.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil { // stale, from a server that died
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeHot(ctx, HotConfig{Path: filepath.Join(dir, "hot.duckdb"), Socket: socket, Store: r,
			Window: 7 * 24 * time.Hour, Poll: 50 * time.Millisecond, Logf: t.Logf})
	}()
	var res *Result
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, res, err = Ask(ctx, socket, "cost", Raw{})
		if err == nil && len(res.Rows) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no answer: %v %v", err, res)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if res.Columns[0] != "model" || res.Rows[0][0] != "claude-opus-5-5" {
		t.Fatalf("cost = %v %v", res.Columns, res.Rows)
	}
	if fi, err := os.Stat(socket); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, %v", fi.Mode(), err)
	}
	var se *ServerError
	if _, _, err := Ask(ctx, socket, "item", Raw{}); !errors.As(err, &se) || se.Status != 400 {
		t.Fatalf("item without a key = %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the socket was left behind")
	}
	if _, _, err := Ask(context.Background(), socket, "cost", Raw{}); !errors.Is(err, ErrNoServer) {
		t.Fatalf("asking a stopped server = %v", err)
	}
}
