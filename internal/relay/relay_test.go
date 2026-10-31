package relay

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

// start runs a relay on a free loopback port until the test ends, and returns it with a stop
// function that shuts it down and waits.
func start(t *testing.T, cfg Config) (*Relay, func()) {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	}
	t.Cleanup(stop)
	return r, stop
}

func traceBody(t *testing.T) []byte {
	t.Helper()
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "claude-code")
	sp := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	sp.SetName("claude_code.interaction")
	sp.SetTraceID([16]byte{1, 2, 3})
	sp.SetSpanID([8]byte{4, 5, 6})
	b, err := ptraceotlp.NewExportRequestFromTraces(td).MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func post(t *testing.T, url, ctype string, body []byte, gz bool) *http.Response {
	t.Helper()
	if gz {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(body)
		_ = zw.Close()
		body = buf.Bytes()
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", ctype)
	if gz {
		req.Header.Set("Content-Encoding", "gzip")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func spoolLines(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") || strings.HasSuffix(e.Name(), ".open.jsonl") {
			t.Errorf("unexpected file after close: %s", e.Name())
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 8<<20)
		for sc.Scan() {
			out = append(out, sc.Text())
		}
		_ = f.Close()
	}
	return out
}

func TestRelay_WritesEverySignalAndEncoding(t *testing.T) {
	dir := t.TempDir()
	r, stop := start(t, Config{Dir: dir})
	if !strings.HasPrefix(r.Endpoint(), "http://127.0.0.1:") {
		t.Fatalf("endpoint = %s", r.Endpoint())
	}

	if resp := post(t, r.Endpoint()+"/v1/traces", "application/x-protobuf", traceBody(t), false); resp.StatusCode != 200 ||
		resp.Header.Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("traces: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	ld := plog.NewLogs()
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName("claude_code.api_request")
	logJSON, _ := plogotlp.NewExportRequestFromLogs(ld).MarshalJSON()
	if resp := post(t, r.Endpoint()+"/v1/logs", "application/json", logJSON, false); resp.StatusCode != 200 {
		t.Fatalf("logs: %d", resp.StatusCode)
	}

	md := pmetric.NewMetrics()
	m := md.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	m.SetName("claude_code.token.usage")
	m.SetEmptySum().DataPoints().AppendEmpty().SetIntValue(42)
	metricProto, _ := pmetricotlp.NewExportRequestFromMetrics(md).MarshalProto()
	if resp := post(t, r.Endpoint()+"/v1/metrics", "application/x-protobuf", metricProto, true); resp.StatusCode != 200 {
		t.Fatalf("metrics (gzip): %d", resp.StatusCode)
	}

	stop()
	lines := spoolLines(t, dir)
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
	tr := ptraceotlp.NewExportRequest()
	if err := tr.UnmarshalJSON([]byte(lines[0])); err != nil {
		t.Fatal(err)
	}
	if n := tr.Traces().ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name(); n != "claude_code.interaction" {
		t.Errorf("span = %s", n)
	}
	if !strings.Contains(lines[0], `"traceId":"01020300000000000000000000000000"`) {
		t.Errorf("trace id not hex: %s", lines[0])
	}
	if !strings.Contains(lines[2], `claude_code.token.usage`) {
		t.Errorf("metrics line = %s", lines[2])
	}
	if s := r.Stats(); s.Accepted != 3 || s.Spool.Dropped != 0 {
		t.Errorf("stats = %+v", s)
	}
}

func TestRelay_Refusals(t *testing.T) {
	r, _ := start(t, Config{MaxRequest: 1024, MaxMemory: 4096})
	tests := []struct {
		name, path, ctype string
		body              []byte
		gzip              bool
		want              int
	}{
		{"unknown path", "/v1/profiles", "application/x-protobuf", traceBody(t), false, http.StatusNotFound},
		{"content type", "/v1/traces", "text/plain", traceBody(t), false, http.StatusUnsupportedMediaType},
		{"too large", "/v1/traces", "application/x-protobuf", bytes.Repeat([]byte{0}, 2048), false, http.StatusRequestEntityTooLarge},
		// Small on the wire, too large once decompressed.
		{"gzip bomb", "/v1/traces", "application/x-protobuf", bytes.Repeat([]byte{0}, 1<<20), true, http.StatusRequestEntityTooLarge},
		{"not OTLP", "/v1/logs", "application/json", []byte(`{"resourceLogs":"no"}`), false, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if resp := post(t, r.Endpoint()+tt.path, tt.ctype, tt.body, tt.gzip); resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
	resp, err := http.Get(r.Endpoint() + "/v1/traces")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d", resp.StatusCode)
	}
	if s := r.Stats(); s.Accepted != 0 || s.TooLarge != 2 || s.Malformed != 2 {
		t.Errorf("stats = %+v", s)
	}
}

func TestRelay_RateLimit(t *testing.T) {
	r, _ := start(t, Config{Rate: 0.001, Burst: 2})
	var codes []int
	for range 3 {
		resp := post(t, r.Endpoint()+"/v1/traces", "application/x-protobuf", traceBody(t), false)
		codes = append(codes, resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests && resp.Header.Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != http.StatusTooManyRequests {
		t.Errorf("codes = %v", codes)
	}
}

func TestRelay_MemoryLimit(t *testing.T) {
	r, _ := start(t, Config{MaxRequest: 1024, MaxMemory: 1024})
	// Hold the only slot with a request whose body never finishes.
	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodPost, r.Endpoint()+"/v1/traces", pr)
	req.Header.Set("Content-Type", "application/x-protobuf")
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	_, _ = pw.Write([]byte{0x0a})
	deadline := time.Now().Add(5 * time.Second)
	for r.inflight.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if resp := post(t, r.Endpoint()+"/v1/traces", "application/x-protobuf", traceBody(t), false); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("second request = %d, want 503", resp.StatusCode)
	}
	_ = pw.Close()
}

func TestNew_Refuses(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]Config{
		"all interfaces":   {Dir: dir, Listen: ":0"},
		"public address":   {Dir: dir, Listen: "0.0.0.0:0"},
		"missing folder":   {Dir: filepath.Join(dir, "nope")},
		"linked folder":    {Dir: link},
		"memory below req": {Dir: dir, MaxRequest: 10, MaxMemory: 5},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestRelay_IdleConnectionDoesNotHoldShutdown: a connection opened but never used must not make
// a stop look like a loss.
func TestRelay_IdleConnectionDoesNotHoldShutdown(t *testing.T) {
	r, stop := start(t, Config{Drain: 200 * time.Millisecond})
	conn, err := net.Dial("tcp", strings.TrimPrefix(r.Endpoint(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	start := time.Now()
	stop() // reports an error through t.Errorf if Serve returns one
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("stop took %v", d)
	}
}

// TestRelay_StripsPathAndHostAttributes: a span, a data point and a log record carrying cwd,
// host.name and a path under the user's home, as Codex sends them, leave the relay without them,
// and everything else arrives untouched (ADR-006).
func TestRelay_StripsPathAndHostAttributes(t *testing.T) {
	const home = "/Users/someone"
	leaky := func(m pcommon.Map) {
		m.PutStr("cwd", home+"/.ynh/run/x")
		m.PutStr("host.name", "someones-laptop")
		m.PutStr("codex.file", home+"/src/secret.go")
		m.PutStr("home.itself", home)
		args := m.PutEmptySlice("codex.args")
		args.AppendEmpty().SetStr("--flag")
		args.AppendEmpty().SetStr(home + "/notes.md")
		nested := m.PutEmptyMap("codex.ctx")
		nested.PutStr("dir", home+"/src")
		nested.PutStr("model", "gpt-5")
		m.PutStr("keep.sibling", "/Users/someoneelse/x") // not under home: a prefix match only
		m.PutStr("keep.tmp", "/tmp/x")
		m.PutStr("service.name", "codex")
	}
	dir := t.TempDir()
	r, stop := start(t, Config{Dir: dir, Home: home + "/"})

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	leaky(rs.Resource().Attributes())
	ss := rs.ScopeSpans().AppendEmpty()
	leaky(ss.Scope().Attributes())
	sp := ss.Spans().AppendEmpty()
	sp.SetName("codex.turn")
	sp.SetTraceID([16]byte{1})
	sp.SetSpanID([8]byte{2})
	leaky(sp.Attributes())
	leaky(sp.Events().AppendEmpty().Attributes())
	leaky(sp.Links().AppendEmpty().Attributes())
	tb, _ := ptraceotlp.NewExportRequestFromTraces(td).MarshalProto()
	if resp := post(t, r.Endpoint()+"/v1/traces", "application/x-protobuf", tb, false); resp.StatusCode != 200 {
		t.Fatalf("traces: %d", resp.StatusCode)
	}

	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	leaky(rm.Resource().Attributes())
	m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	m.SetName("codex.tokens")
	dp := m.SetEmptyHistogram().DataPoints().AppendEmpty()
	dp.SetCount(1)
	leaky(dp.Attributes())
	leaky(dp.Exemplars().AppendEmpty().FilteredAttributes())
	mb, _ := pmetricotlp.NewExportRequestFromMetrics(md).MarshalJSON()
	if resp := post(t, r.Endpoint()+"/v1/metrics", "application/json", mb, false); resp.StatusCode != 200 {
		t.Fatalf("metrics: %d", resp.StatusCode)
	}

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	leaky(rl.Resource().Attributes())
	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName("codex.tool_result")
	leaky(lr.Attributes())
	lb, _ := plogotlp.NewExportRequestFromLogs(ld).MarshalProto()
	if resp := post(t, r.Endpoint()+"/v1/logs", "application/x-protobuf", lb, true); resp.StatusCode != 200 {
		t.Fatalf("logs: %d", resp.StatusCode)
	}

	stop()
	lines := spoolLines(t, dir)
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
	for i, l := range lines {
		for _, bad := range []string{`"cwd"`, `"host.name"`, home + "/", `"home.itself"`, `"codex.args"`, `"dir"`} {
			if strings.Contains(l, bad) {
				t.Errorf("line %d still carries %s: %s", i, bad, l)
			}
		}
		for _, good := range []string{`"keep.sibling"`, `"keep.tmp"`, `"service.name"`, `"model"`} {
			if !strings.Contains(l, good) {
				t.Errorf("line %d lost %s: %s", i, good, l)
			}
		}
	}
	tr := ptraceotlp.NewExportRequest()
	if err := tr.UnmarshalJSON([]byte(lines[0])); err != nil {
		t.Fatal(err)
	}
	if n := tr.Traces().ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name(); n != "codex.turn" {
		t.Errorf("span = %s", n)
	}
	// Five attributes and the nested dir from each leaky map: five maps in the trace, three in
	// the metrics, two in the logs.
	if s := r.Stats(); s.Stripped != 6*10 || s.Accepted != 3 {
		t.Errorf("stats = %+v", s)
	}
}

func TestScrubber_NoHome(t *testing.T) {
	for _, home := range []string{"", "/", "relative"} {
		s := newScrubber(home)
		m := pcommon.NewMap()
		m.PutStr("cwd", "/x")
		m.PutStr("path", "/Users/a")
		if n := s.attrs(m); n != 1 || m.Len() != 1 {
			t.Errorf("home %q: dropped %d, left %d", home, n, m.Len())
		}
	}
}
