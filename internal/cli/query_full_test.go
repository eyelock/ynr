//go:build full

package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
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
