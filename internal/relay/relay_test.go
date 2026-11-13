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
	"sync"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/spoolexporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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
		if e.Name() == spool.RelayLockName {
			continue
		}
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
		m.PutStr("user.email", "nobody@example.com")
		m.PutStr("user.full_name", "Ann Example")
		m.PutStr("codex.contact", "someone.else+tag@mail.example.org")
		people := m.PutEmptySlice("codex.reviewers")
		people.AppendEmpty().SetStr("octocat")
		people.AppendEmpty().SetStr("a@example.net")
		m.PutStr("keep.sentence", "write to a@example.net about it") // not wholly an address
		m.PutStr("keep.handle", "github.com/octocat")
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
		for _, bad := range []string{`"cwd"`, `"host.name"`, home + "/", `"home.itself"`, `"codex.args"`, `"dir"`,
			`"user.email"`, `"user.full_name"`, `"codex.contact"`, `"codex.reviewers"`, "Ann Example", "example.com"} {
			if strings.Contains(l, bad) {
				t.Errorf("line %d still carries %s: %s", i, bad, l)
			}
		}
		for _, good := range []string{`"keep.sibling"`, `"keep.tmp"`, `"service.name"`, `"model"`, `"keep.sentence"`, `"keep.handle"`} {
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
	// Nine attributes and the nested dir from each leaky map: five maps in the trace, three in
	// the metrics, two in the logs.
	if s := r.Stats(); s.Stripped != 10*10 || s.Accepted != 3 {
		t.Errorf("stats = %+v", s)
	}
}

func TestScrubber_Email(t *testing.T) {
	s := newScrubber("")
	for v, want := range map[string]bool{
		"nobody@example.com":            true,
		"first.last+tag@sub.example.io": true,
		"a@b":                           false, // no top-level domain
		"@example.com":                  false,
		"mail nobody@example.com":       false,
		"https://nobody@example.com/x":  false,
		"github.com/octocat":            false,
		"v1.2.3":                        false,
	} {
		m := pcommon.NewMap()
		m.PutStr("k", v)
		if got := s.attrs(m) == 1; got != want {
			t.Errorf("%q: dropped = %v, want %v", v, got, want)
		}
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

// The spool exporter's OTLP/HTTP sender, pointed at the relay as an operator's
// OTEL_EXPORTER_OTLP_ENDPOINT, delivers every signal and the relay decodes it.
func TestRelay_ReceivesSpoolexporterHTTPSender(t *testing.T) {
	dir := t.TempDir()
	r, stop := start(t, Config{Dir: dir})
	opts, ok, why := spoolexporter.HTTPOptionsFromEnv(func(k string) string {
		if k == "OTEL_EXPORTER_OTLP_ENDPOINT" {
			return r.Endpoint()
		}
		return ""
	})
	if !ok || why != "" {
		t.Fatalf("configured=%v why=%q", ok, why)
	}
	h := spoolexporter.NewHTTPSender(opts)
	ctx := context.Background()
	res := resource.NewSchemaless(attribute.String("service.name", "ynh"))

	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(spoolexporter.NewHTTPTraceExporter(h)))
	_, sp := tp.Tracer("scope").Start(ctx, "ynh.run")
	sp.End()
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(spoolexporter.NewHTTPLogExporter(h))))
	var rec otellog.Record
	rec.SetEventName("ynh.run")
	lp.Logger("scope").Emit(ctx, rec)
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(spoolexporter.NewHTTPMetricExporter(h))))
	c, _ := mp.Meter("scope").Int64Counter("ynh.runs")
	c.Add(ctx, 1)
	for _, shutdown := range []func(context.Context) error{tp.Shutdown, lp.Shutdown, mp.Shutdown} {
		if err := shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if s := h.Stats(); s != (spoolexporter.Stats{}) {
		t.Fatalf("sender stats = %+v", s)
	}

	stop()
	var gotSpan, gotLog, gotMetric bool
	for _, line := range spoolLines(t, dir) {
		if tr := ptraceotlp.NewExportRequest(); tr.UnmarshalJSON([]byte(line)) == nil && tr.Traces().SpanCount() > 0 {
			gotSpan = tr.Traces().ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name() == "ynh.run"
		}
		if lr := plogotlp.NewExportRequest(); lr.UnmarshalJSON([]byte(line)) == nil && lr.Logs().LogRecordCount() > 0 {
			gotLog = lr.Logs().ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).EventName() == "ynh.run"
		}
		if mr := pmetricotlp.NewExportRequest(); mr.UnmarshalJSON([]byte(line)) == nil && mr.Metrics().MetricCount() > 0 {
			gotMetric = mr.Metrics().ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().At(0).Name() == "ynh.runs"
		}
	}
	if !gotSpan || !gotLog || !gotMetric {
		t.Errorf("span=%v log=%v metric=%v in %v", gotSpan, gotLog, gotMetric, spoolLines(t, dir))
	}
	if s := r.Stats(); s.Accepted != 3 || s.Malformed != 0 {
		t.Errorf("relay stats = %+v", s)
	}
}

// freePort returns a loopback address on a port that was free a moment ago.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestRelay_ManySeatsAtDefaults: a Wheelhouse's seats share one standing relay. Claude Code
// exports logs and spans about every 5 seconds and metrics every 60, so a seat sends well under
// one request a second; 40 seats is about 16 a second against the default 100. Here all 40 flush
// four requests at the same instant, 160 at once, within the default burst of 200, and every one
// must reach the spool: none refused as busy or rate limited. Reserving the largest request size
// for each, the default memory limit held only 16 at once, and under load this test saw 16 of 160
// refused as busy; each now reserves its own length.
//
// Each seat is one client with one keep-alive connection, as an OTLP exporter is, so 40
// connections open at once and each carries its seat's four requests in turn. The test once opened
// 160 connections in the same instant instead. On macOS the kernel's listen queue
// (kern.ipc.somaxconn, 128) cannot hold that many unaccepted connections, and it reset the
// overflow ("connection reset by peer", about 3 runs in 20) before the relay ever saw it: a limit
// of the operating system that no relay code can raise, not a loss by the relay (ynr #120).
func TestRelay_ManySeatsAtDefaults(t *testing.T) {
	const seats, each = 40, 4
	r, stop := start(t, Config{Listen: freePort(t)})
	body := traceBody(t)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	codes := make(chan int, seats*each)
	for range seats {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr := &http.Transport{MaxConnsPerHost: 1}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr}
			<-gate
			for range each {
				req, _ := http.NewRequest(http.MethodPost, r.Endpoint()+"/v1/traces", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/x-protobuf")
				resp, err := client.Do(req)
				if err != nil {
					t.Log(err)
					codes <- 0
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				codes <- resp.StatusCode
			}
		}()
	}
	close(gate)
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != http.StatusOK {
			t.Errorf("request = %d, want 200", c)
		}
	}
	dir := r.cfg.Dir
	stop()
	s := r.Stats()
	if s.Accepted != seats*each || s.Busy != 0 || s.Limited != 0 || s.Spool.Dropped != 0 {
		t.Errorf("stats = %+v, want %d accepted and nothing refused or dropped", s, seats*each)
	}
	if got := len(spoolLines(t, dir)); got != seats*each {
		t.Errorf("spool lines = %d, want %d", got, seats*each)
	}
}

// TestRelay_StandingRelayLock: a relay on a fixed port records itself in its folder, refuses a
// second standing relay on that folder, says plainly when its port is taken, and lets go when it
// stops.
func TestRelay_StandingRelayLock(t *testing.T) {
	dir := t.TempDir()
	addr := freePort(t)
	r, stop := start(t, Config{Dir: dir, Listen: addr})

	h, err := spool.RelayHolder(dir)
	if err != nil || h == nil {
		t.Fatalf("holder = %v, %v; want the running relay", h, err)
	}
	if h.PID != os.Getpid() || h.Endpoint != r.Endpoint() {
		t.Errorf("holder = %+v, want pid %d at %s", h, os.Getpid(), r.Endpoint())
	}

	if _, err := New(Config{Dir: dir, Listen: freePort(t)}); err == nil || !strings.Contains(err.Error(), "already serves") {
		t.Errorf("second relay on the folder: %v, want a refusal naming the first", err)
	}
	if _, err := New(Config{Dir: t.TempDir(), Listen: addr}); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("second relay on the port: %v, want 'already in use'", err)
	}
	if _, err := New(Config{Dir: dir, Listen: addr}); err == nil || !strings.Contains(err.Error(), "ynr relay pid") {
		t.Errorf("same port and folder: %v, want the holder named", err)
	}

	// A port taken by something else is not blamed on the folder's relay on another port.
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	_, err = New(Config{Dir: dir, Listen: other.Addr().String()})
	if err == nil || !strings.Contains(err.Error(), "by another process") || strings.Contains(err.Error(), "serves") {
		t.Errorf("port held by another process: %v, want the 'another process' message", err)
	}
	// Listen may name localhost while the endpoint is 127.0.0.1: same port still names the relay.
	_, port, _ := net.SplitHostPort(addr)
	if _, err := New(Config{Dir: dir, Listen: "localhost:" + port}); err == nil || !strings.Contains(err.Error(), "ynr relay pid") {
		t.Errorf("localhost spelling of the same port: %v, want the holder named", err)
	}

	stop()
	if h, err := spool.RelayHolder(dir); err != nil || h != nil {
		t.Errorf("after stop: holder = %+v, %v; want none", h, err)
	}
}

// TestRelay_PerRunRelayRecordsNothing: a relay on port 0, such as ynh agent run's, leaves no
// lock in its folder, so parallel runs and run captures are unchanged.
func TestRelay_PerRunRelayRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	start(t, Config{Dir: dir})
	if _, err := os.Stat(filepath.Join(dir, spool.RelayLockName)); !os.IsNotExist(err) {
		t.Errorf("lock file: %v, want none", err)
	}
}

// TestRelay_StampsResourceAttrs: every resource of every signal carries the configured attribute,
// whether or not the vendor sent one, and a vendor's value for the same key is overridden. The
// stamp adds nothing else, and a vendor attribute on a data point, span or record is left alone.
func TestRelay_StampsResourceAttrs(t *testing.T) {
	const key = "ynh.session.id"
	dir := t.TempDir()
	r, stop := start(t, Config{Dir: dir, ResourceAttrs: map[string]string{key: "sess-1"}})

	td := ptrace.NewTraces()
	for i, vendor := range []string{"", "vendor-value"} {
		rs := td.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", "claude-code")
		if vendor != "" {
			rs.Resource().Attributes().PutStr(key, vendor)
		}
		sp := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		sp.SetName("claude_code.interaction")
		sp.SetSpanID([8]byte{byte(i + 1)})
	}
	tb, _ := ptraceotlp.NewExportRequestFromTraces(td).MarshalProto()
	post(t, r.Endpoint()+"/v1/traces", "application/x-protobuf", tb, false)

	md := pmetric.NewMetrics()
	for _, vendor := range []string{"", "vendor-value"} {
		rm := md.ResourceMetrics().AppendEmpty()
		if vendor != "" {
			rm.Resource().Attributes().PutStr(key, vendor)
		}
		m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
		m.SetName("claude_code.token.usage")
		dp := m.SetEmptyGauge().DataPoints().AppendEmpty()
		dp.SetIntValue(1)
		dp.Attributes().PutStr(key, "kept-on-the-point")
	}
	mb, _ := pmetricotlp.NewExportRequestFromMetrics(md).MarshalJSON()
	post(t, r.Endpoint()+"/v1/metrics", "application/json", mb, false)

	ld := plog.NewLogs()
	for _, vendor := range []string{"", "vendor-value"} {
		rl := ld.ResourceLogs().AppendEmpty()
		if vendor != "" {
			rl.Resource().Attributes().PutStr(key, vendor)
		}
		lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		lr.SetEventName("claude_code.api_request")
	}
	lb, _ := plogotlp.NewExportRequestFromLogs(ld).MarshalProto()
	post(t, r.Endpoint()+"/v1/logs", "application/x-protobuf", lb, true)

	stop()
	lines := spoolLines(t, dir)
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
	check := func(signal string, n int, get func(i int) pcommon.Map) {
		t.Helper()
		if n != 2 {
			t.Fatalf("%s: %d resources, want 2", signal, n)
		}
		for i := range n {
			v, ok := get(i).Get(key)
			if !ok || v.Str() != "sess-1" || get(i).Len() > 2 {
				t.Errorf("%s resource %d: %v", signal, i, get(i).AsRaw())
			}
		}
	}
	for _, l := range lines {
		switch {
		case strings.Contains(l, "resourceSpans"):
			req := ptraceotlp.NewExportRequest()
			if err := req.UnmarshalJSON([]byte(l)); err != nil {
				t.Fatal(err)
			}
			rs := req.Traces().ResourceSpans()
			check("traces", rs.Len(), func(i int) pcommon.Map { return rs.At(i).Resource().Attributes() })
		case strings.Contains(l, "resourceMetrics"):
			req := pmetricotlp.NewExportRequest()
			if err := req.UnmarshalJSON([]byte(l)); err != nil {
				t.Fatal(err)
			}
			rm := req.Metrics().ResourceMetrics()
			check("metrics", rm.Len(), func(i int) pcommon.Map { return rm.At(i).Resource().Attributes() })
			dp := rm.At(0).ScopeMetrics().At(0).Metrics().At(0).Gauge().DataPoints().At(0)
			if v, _ := dp.Attributes().Get(key); v.Str() != "kept-on-the-point" {
				t.Errorf("a data point's own attribute was touched: %v", dp.Attributes().AsRaw())
			}
		case strings.Contains(l, "resourceLogs"):
			req := plogotlp.NewExportRequest()
			if err := req.UnmarshalJSON([]byte(l)); err != nil {
				t.Fatal(err)
			}
			rl := req.Logs().ResourceLogs()
			check("logs", rl.Len(), func(i int) pcommon.Map { return rl.At(i).Resource().Attributes() })
		default:
			t.Errorf("unrecognised line: %s", l)
		}
	}
}

// TestRelay_StampsAfterTheStripList: a stamp is the last word on its key, so even a key the strip
// list drops is set; and a value the scrubber would drop is not what it stamps over.
func TestRelay_StampsAfterTheStripList(t *testing.T) {
	dir := t.TempDir()
	r, stop := start(t, Config{Dir: dir, ResourceAttrs: map[string]string{"ynh.session.id": "s"}})
	tb := traceBody(t)
	post(t, r.Endpoint()+"/v1/traces", "application/x-protobuf", tb, false)
	stop()
	if l := spoolLines(t, dir); len(l) != 1 || !strings.Contains(l[0], `"ynh.session.id"`) || strings.Contains(l[0], "user.email") {
		t.Errorf("lines = %v", l)
	}
	if s := r.Stats(); s.Stripped != 0 {
		t.Errorf("stamping counted as stripping: %+v", s)
	}
}

func TestNew_RefusesEmptyResourceAttr(t *testing.T) {
	for _, m := range []map[string]string{{"": "v"}, {"ynh.session.id": ""}} {
		if _, err := New(Config{Dir: t.TempDir(), ResourceAttrs: m}); err == nil {
			t.Errorf("accepted %v", m)
		}
	}
}

func TestNew_RefusesStripListedResourceAttr(t *testing.T) {
	for _, k := range Stripped {
		if _, err := New(Config{Dir: t.TempDir(), ResourceAttrs: map[string]string{k: "v"}}); err == nil {
			t.Errorf("accepted the strip-listed key %q", k)
		}
	}
}
