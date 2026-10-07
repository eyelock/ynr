//go:build full

package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// A ynh.run span as Claude Code's run through ynh would ship it.
const runLine = `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynh"}}]},` +
	`"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",` +
	`"name":"ynh.run","kind":1,"startTimeUnixNano":"1791235800000000000","endTimeUnixNano":"1791235890000000000",` +
	`"attributes":[{"key":"ynh.run.outcome","value":{"stringValue":"converged"}},` +
	`{"key":"gen_ai.response.model","value":{"stringValue":"claude-opus-5-5"}},` +
	`{"key":"ynh.run.cost_usd","value":{"doubleValue":0.75}}]}]}]}]}`

func TestQueryReadsTheStore(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(store.FolderURL(dir))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(0, 1791235800000000000).UTC()
	now = func() time.Time { return at.Add(time.Hour) }
	defer func() { now = time.Now }()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(runLine + "\n"))
	_ = zw.Close()
	key := store.BatchKey(store.Traces, at.Add(2*time.Minute), "laptop", store.NewULID(at), "local.ynh-1", 0, 1)
	if err := s.Put(context.Background(), key, buf.Bytes()); err != nil {
		t.Fatal(err)
	}

	code, out, errs := run("query", "runs", "--store", store.FolderURL(dir))
	if code != ExitOK {
		t.Fatalf("runs = %d %s", code, errs)
	}
	if !strings.Contains(out, "(by hand)") || !strings.Contains(out, "converged") || !strings.Contains(out, "0.75") {
		t.Fatalf("runs text =\n%s", out)
	}

	code, out, errs = run("query", "cost", "--format", "json", "--store", store.FolderURL(dir))
	if code != ExitOK {
		t.Fatalf("cost = %d %s", code, errs)
	}
	var doc struct {
		Query string           `json:"query"`
		Rows  []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if doc.Query != "cost" || len(doc.Rows) != 1 || doc.Rows[0]["model"] != "claude-opus-5-5" || doc.Rows[0]["cost_usd"] != 0.75 {
		t.Fatalf("cost json = %s", out)
	}
	if i := strings.Index(out, `"model"`); i < 0 || i > strings.Index(out, `"runs"`) {
		t.Fatalf("columns out of order: %s", out)
	}

	code, out, _ = run("query", "trace", "5B8EFFF798038103D269B633813FC60C", "--store", store.FolderURL(dir))
	if code != ExitOK || !strings.Contains(out, "ynh.run") || strings.Contains(out, "depth") {
		t.Fatalf("trace = %d\n%s", code, out)
	}
}

// TestQueryAsksTheRunningServer: with ynr serve running, ynr query is answered by its hot tier.
// No store is configured for ynr query itself, so a direct read would fail.
func TestQueryAsksTheRunningServer(t *testing.T) {
	root, err := os.MkdirTemp("", "ynr") // short: the socket lives under it
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	dir := t.TempDir()
	s, err := store.Open(store.FolderURL(dir))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(strings.ReplaceAll(runLine, `"1791235800000000000"`, `"`+strconv.FormatInt(at.Add(-time.Minute).UnixNano(), 10)+`"`) + "\n"))
	_ = zw.Close()
	if err := s.Put(context.Background(), store.BatchKey(store.Traces, at, "laptop", store.NewULID(at), "local.a", 0, 1), buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		var out, errb bytes.Buffer
		done <- Run(ctx, []string{"serve", "--spool", root, "--store", store.FolderURL(dir), "--poll", "50ms"}, &out, &errb)
	}()
	t.Setenv("YNR_STORE", "")
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, out, errs := run("query", "cost", "--spool", root)
		if code == ExitOK && strings.Contains(out, "claude-opus-5-5") {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("query = %d %q %q", code, out, errs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code, _, errs := run("query", "item", "--spool", root); code != ExitUsage {
		t.Errorf("a bad request = %d %q", code, errs)
	}
	cancel()
	if code := <-done; code != ExitOK {
		t.Fatalf("serve = %d", code)
	}
}

// TestQueryAsksCentral: ynr central runs over a shared store and ynr query --socket asks it, with
// no store configured for the query itself. A second central refuses to share its socket.
func TestQueryAsksCentral(t *testing.T) {
	dir, err := os.MkdirTemp("", "ynr") // short: the socket lives under it
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	shared := t.TempDir()
	s, err := store.Open(store.FolderURL(shared))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(strings.ReplaceAll(runLine, `"1791235800000000000"`, `"`+strconv.FormatInt(at.Add(-time.Minute).UnixNano(), 10)+`"`) + "\n"))
	_ = zw.Close()
	if err := s.Put(context.Background(), store.BatchKey(store.Traces, at, "pool-a", store.NewULID(at), "local.a", 0, 1), buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "c.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		var out, errb bytes.Buffer
		done <- Run(ctx, []string{"central", "--store", store.FolderURL(shared), "--state", filepath.Join(dir, "state"),
			"--socket", socket, "--poll", "50ms"}, &out, &errb)
	}()
	t.Setenv("YNR_STORE", "")
	t.Setenv("YNR_SPOOL_ROOT", "")
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, out, errs := run("query", "cost", "--socket", socket)
		if code == ExitOK && strings.Contains(out, "claude-opus-5-5") {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("query = %d %q %q", code, out, errs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code, _, errs := run("central", "--store", store.FolderURL(shared), "--state", filepath.Join(dir, "state2"), "--socket", socket); code != ExitAdapter || !strings.Contains(errs, "already answering") {
		t.Errorf("a second central on the socket = %d %q", code, errs)
	}
	cancel()
	if code := <-done; code != ExitOK {
		t.Fatalf("central = %d", code)
	}
	if code, _, errs := run("query", "cost", "--socket", socket); code != ExitAdapter || !strings.Contains(errs, "central running") {
		t.Errorf("asking a stopped central = %d %q", code, errs)
	}
}
