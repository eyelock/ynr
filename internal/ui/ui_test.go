package ui

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/query"
	"github.com/eyelock/ynr/internal/spool"
)

var at = time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)

// fake answers each named query with fixed rows.
type fake map[string]*query.Result

func (f fake) Run(_ context.Context, q *query.Query, _ query.Params) (*query.Result, error) {
	if r, ok := f[q.Name]; ok {
		return r, nil
	}
	return &query.Result{Columns: []string{"x"}}, nil
}

var answers = fake{
	"runs": {Columns: []string{"lane", "outcome", "runs", "median_s", "cost_usd", "last"}, Rows: [][]any{
		{"github.com/eyelock/ynr#lint", "converged", int64(3), 90.0, 4.5, at},
		{"github.com/eyelock/ynr#lint", "budget", int64(1), 120.0, 2.0, at},
	}},
	"cost": {Columns: []string{"model", "runs", "cost_usd", "input_tokens", "output_tokens"}, Rows: [][]any{
		{"claude-opus-5-5", int64(4), 6.5, int64(4000), int64(800)},
	}},
	"recent": {Columns: []string{"time", "lane", "item", "outcome", "model", "duration_s", "cost_usd", "trace_id"}, Rows: [][]any{
		{at, "github.com/eyelock/ynr#lint", "github.com/eyelock/ynr#12", "converged", "claude-opus-5-5", 90.0, 1.5, "5b8efff798038103d269b633813fc60c"},
	}},
	"trace": {Columns: []string{"time", "depth", "name", "service", "kind", "status", "outcome", "duration_ms", "span_id", "parent_span_id"}, Rows: [][]any{
		{at, int32(0), "ynf.step", "ynf", "internal", "unset", "completed", 120000.0, "1111111111111111", nil},
		{at.Add(30 * time.Second), int32(1), "ynh.run", "ynh", "internal", "error", "budget", 60000.0, "2222222222222222", "1111111111111111"},
	}},
}

func get(t *testing.T, h http.Handler, path, host string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result(), rec.Body.String()
}

func TestPagesRender(t *testing.T) {
	h := Handler(Config{Runner: answers, Now: func() time.Time { return at }})
	resp, body := get(t, h, "/", "127.0.0.1:4319")
	if resp.StatusCode != 200 || !strings.Contains(body, "github.com/eyelock/ynr#lint") || !strings.Contains(body, "75%") ||
		!strings.Contains(body, "data-chart=") || !strings.Contains(body, `href="/trace/5b8efff798038103d269b633813fc60c"`) {
		t.Fatalf("lanes = %d\n%s", resp.StatusCode, body)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("csp = %q", csp)
	}
	resp, body = get(t, h, "/trace/5B8EFFF798038103D269B633813FC60C", "localhost:4319")
	if resp.StatusCode != 200 || strings.Count(body, `class="span"`) != 2 || !strings.Contains(body, "left:25.000%;width:50.000%") {
		t.Fatalf("trace = %d\n%s", resp.StatusCode, body)
	}
	for _, path := range []string{"/items", "/item?key=github.com%2Feyelock%2Fynr%2312", "/tail", "/static/htmx.min.js", "/static/echarts.min.js", "/static/app.js"} {
		if resp, _ := get(t, h, path, "[::1]:4319"); resp.StatusCode != 200 {
			t.Errorf("%s = %d", path, resp.StatusCode)
		}
	}
	if resp, _ := get(t, h, "/item", "127.0.0.1"); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("an item without a key = %d", resp.StatusCode)
	}
}

// TestOnlyLoopbackHostsAreAnswered: a page elsewhere that rebinds its name to 127.0.0.1 still
// sends its own name as the host, and is refused.
func TestOnlyLoopbackHostsAreAnswered(t *testing.T) {
	h := Handler(Config{Runner: answers})
	for _, host := range []string{"evil.example:4319", "evil.example", "10.0.0.5:4319"} {
		if resp, _ := get(t, h, "/", host); resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("host %s = %d", host, resp.StatusCode)
		}
	}
}

func TestLiveTailStreamsRows(t *testing.T) {
	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Config{Runner: answers, Spool: root}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/tail/stream?service=ynh", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %s", resp.Header.Get("Content-Type"))
	}
	time.Sleep(700 * time.Millisecond) // the follower's first look, after which new lines stream
	line := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynh"}}]},` +
		`"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",` +
		`"name":"ynh.run","startTimeUnixNano":"1791235800000000000","endTimeUnixNano":"1791235890000000000"}]}]}]}`
	if err := os.WriteFile(filepath.Join(root, "local", "ynh-1-000001.open.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(resp.Body)
	var event string
	for {
		l, err := br.ReadString('\n')
		if err == io.EOF || err != nil {
			t.Fatalf("stream ended: %v", err)
		}
		if strings.HasPrefix(l, "data: ") {
			event = l
			break
		}
	}
	if !strings.Contains(event, "<tr") || !strings.Contains(event, "ynh.run") || !strings.Contains(event, "/trace/5b8efff798038103d269b633813fc60c") {
		t.Fatalf("event = %s", event)
	}
}
