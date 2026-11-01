package spoolexporter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type received struct {
	path   string
	header http.Header
	body   map[string]any
}

// collector is an OTLP/HTTP endpoint that records what it is sent.
func collector(t *testing.T, status int) (*httptest.Server, func() []received) {
	t.Helper()
	var mu sync.Mutex
	var got []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
		mu.Lock()
		got = append(got, received{r.URL.Path, r.Header.Clone(), body})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []received {
		mu.Lock()
		defer mu.Unlock()
		return append([]received(nil), got...)
	}
}

func emitAll(t *testing.T, h *HTTPSender) {
	t.Helper()
	ctx := context.Background()
	res := resource.NewSchemaless(attribute.String("service.name", "svc"))

	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(NewHTTPTraceExporter(h)))
	_, sp := tp.Tracer("scope").Start(ctx, "work")
	sp.End()

	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(NewHTTPLogExporter(h))))
	var rec otellog.Record
	rec.SetBody(attribute.StringValue("hello"))
	lp.Logger("scope").Emit(ctx, rec)

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(NewHTTPMetricExporter(h))))
	c, _ := mp.Meter("scope").Int64Counter("runs")
	c.Add(ctx, 3)

	for _, shutdown := range []func(context.Context) error{tp.Shutdown, lp.Shutdown, mp.Shutdown} {
		if err := shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHTTPSender_AllSignalsArriveAndDecode(t *testing.T) {
	srv, got := collector(t, 200)
	opts, ok, why := HTTPOptionsFromEnv(env(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": srv.URL + "/base/",
		"OTEL_EXPORTER_OTLP_HEADERS":  "authorization=Bearer%20secret, x-team = a%2Cb",
	}))
	if !ok || why != "" {
		t.Fatalf("configured=%v why=%q", ok, why)
	}
	h := NewHTTPSender(opts)
	emitAll(t, h)

	byPath := map[string]received{}
	for _, r := range got() {
		byPath[r.path] = r
	}
	want := map[string]string{"/base/v1/traces": "resourceSpans", "/base/v1/logs": "resourceLogs", "/base/v1/metrics": "resourceMetrics"}
	for path, key := range want {
		r, ok := byPath[path]
		if !ok {
			t.Fatalf("nothing arrived at %s; got %v", path, got())
		}
		if _, ok := r.body[key]; !ok {
			t.Errorf("%s body has no %s: %v", path, key, r.body)
		}
		if ct := r.header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s content type = %q", path, ct)
		}
		if a := r.header.Get("Authorization"); a != "Bearer secret" {
			t.Errorf("%s authorization = %q", path, a)
		}
		if x := r.header.Get("X-Team"); x != "a,b" {
			t.Errorf("%s x-team = %q", path, x)
		}
	}
	rs := dig(t, byPath["/base/v1/traces"].body, "resourceSpans", 0, "scopeSpans", 0, "spans", 0)
	if rs.(map[string]any)["name"] != "work" {
		t.Errorf("span = %v", rs)
	}
	if s := h.Stats(); s != (Stats{}) {
		t.Errorf("stats = %+v", s)
	}
}

func TestHTTPOptionsFromEnv_PerSignalEndpointAndHeaders(t *testing.T) {
	srv, got := collector(t, 200)
	opts, ok, why := HTTPOptionsFromEnv(env(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":        srv.URL + "/ignored",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": srv.URL + "/custom/traces",
		"OTEL_EXPORTER_OTLP_HEADERS":         "k=general,keep=yes",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS":  "k=traces",
		"OTEL_EXPORTER_OTLP_TIMEOUT":         "5000",
	}))
	if !ok || why != "" {
		t.Fatalf("configured=%v why=%q", ok, why)
	}
	if opts.LogsEndpoint != srv.URL+"/ignored/v1/logs" || opts.TracesEndpoint != srv.URL+"/custom/traces" {
		t.Errorf("endpoints = %q %q", opts.TracesEndpoint, opts.LogsEndpoint)
	}
	if opts.Timeout != 5*time.Second {
		t.Errorf("timeout = %v", opts.Timeout)
	}
	emitAll(t, NewHTTPSender(opts))
	for _, r := range got() {
		switch r.path {
		case "/custom/traces":
			if r.header.Get("K") != "traces" || r.header.Get("Keep") != "yes" {
				t.Errorf("traces headers = %v", r.header)
			}
		case "/ignored/v1/logs":
			if r.header.Get("K") != "general" {
				t.Errorf("logs headers = %v", r.header)
			}
		}
	}
	if len(got()) != 3 {
		t.Errorf("got %d requests, want 3", len(got()))
	}
}

func TestHTTPOptionsFromEnv_OnlyOneSignalConfigured(t *testing.T) {
	srv, got := collector(t, 200)
	opts, ok, _ := HTTPOptionsFromEnv(env(map[string]string{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": srv.URL + "/l"}))
	if !ok {
		t.Fatal("not configured")
	}
	h := NewHTTPSender(opts)
	emitAll(t, h)
	if g := got(); len(g) != 1 || g[0].path != "/l" {
		t.Errorf("got %v", g)
	}
	if h.Stats() != (Stats{}) {
		t.Errorf("stats = %+v", h.Stats())
	}
}

func TestHTTPOptionsFromEnv_NothingSet(t *testing.T) {
	_, ok, why := HTTPOptionsFromEnv(env(nil))
	if ok || why != "" {
		t.Errorf("configured=%v why=%q", ok, why)
	}
}

func TestHTTPOptionsFromEnv_Unsupported(t *testing.T) {
	const secret = "s3cret-value"
	for name, tc := range map[string]struct{ key, val string }{
		"grpc":             {"OTEL_EXPORTER_OTLP_PROTOCOL", "grpc"},
		"protobuf":         {"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf"},
		"gzip":             {"OTEL_EXPORTER_OTLP_COMPRESSION", "gzip"},
		"signal gzip":      {"OTEL_EXPORTER_OTLP_LOGS_COMPRESSION", "gzip"},
		"ca":               {"OTEL_EXPORTER_OTLP_CERTIFICATE", "/etc/ca.pem"},
		"metrics ca":       {"OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE", "/etc/ca.pem"},
		"client cert":      {"OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE", "/etc/c.pem"},
		"client key":       {"OTEL_EXPORTER_OTLP_TRACES_CLIENT_KEY", "/etc/k.pem"},
		"endpoint not url": {"OTEL_EXPORTER_OTLP_ENDPOINT", "ftp://example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			srv, got := collector(t, 200)
			vars := map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": srv.URL,
				"OTEL_EXPORTER_OTLP_HEADERS":  "authorization=" + secret,
				tc.key:                        tc.val,
			}
			_, ok, why := HTTPOptionsFromEnv(env(vars))
			if ok || why == "" {
				t.Fatalf("configured=%v why=%q", ok, why)
			}
			if !strings.Contains(why, tc.key) {
				t.Errorf("reason %q does not name %s", why, tc.key)
			}
			if strings.Contains(why, secret) {
				t.Errorf("reason leaks a header value: %q", why)
			}
			if len(got()) != 0 {
				t.Errorf("sent %v", got())
			}
		})
	}
}

func TestHTTPOptionsFromEnv_SupportedValuesPass(t *testing.T) {
	_, ok, why := HTTPOptionsFromEnv(env(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":    "http://localhost:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL":    "http/json",
		"OTEL_EXPORTER_OTLP_COMPRESSION": "none",
	}))
	if !ok || why != "" {
		t.Errorf("configured=%v why=%q", ok, why)
	}
}

func TestHTTPSender_HungEndpointReturnsWithinBound(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	h := NewHTTPSender(HTTPOptions{TracesEndpoint: srv.URL, Timeout: 200 * time.Millisecond})
	start := time.Now()
	if err := NewHTTPTraceExporter(h).ExportSpans(context.Background(), oneSpan(t)); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("export took %v", d)
	}
	if s := h.Stats(); s.Errors != 1 || s.Dropped != 1 {
		t.Errorf("stats = %+v", s)
	}
}

func TestHTTPSender_RefusedConnectionIsCountedNotReturned(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	h := NewHTTPSender(HTTPOptions{TracesEndpoint: url})
	if err := NewHTTPTraceExporter(h).ExportSpans(context.Background(), oneSpan(t)); err != nil {
		t.Fatalf("returned %v", err)
	}
	if s := h.Stats(); s.Errors != 1 || s.Dropped != 1 {
		t.Errorf("stats = %+v", s)
	}
}

func TestHTTPSender_NonSuccessStatusIsCounted(t *testing.T) {
	srv, _ := collector(t, 503)
	h := NewHTTPSender(HTTPOptions{TracesEndpoint: srv.URL})
	_ = NewHTTPTraceExporter(h).ExportSpans(context.Background(), oneSpan(t))
	if s := h.Stats(); s.Errors != 1 || s.Dropped != 1 {
		t.Errorf("stats = %+v", s)
	}
}

func TestHTTPSender_FollowsNoRedirects(t *testing.T) {
	target, got := collector(t, 200)
	redir := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	t.Cleanup(redir.Close)
	h := NewHTTPSender(HTTPOptions{TracesEndpoint: redir.URL, TracesHeaders: map[string]string{"X-Key": "v"}})
	_ = NewHTTPTraceExporter(h).ExportSpans(context.Background(), oneSpan(t))
	if len(got()) != 0 || h.Stats().Errors != 1 {
		t.Errorf("followed a redirect: %v %+v", got(), h.Stats())
	}
}

func TestHTTPSender_StatsAndErrorsCarryNoHeaderValues(t *testing.T) {
	srv, _ := collector(t, 500)
	h := NewHTTPSender(HTTPOptions{TracesEndpoint: srv.URL, TracesHeaders: map[string]string{"Authorization": "topsecret"}})
	err := NewHTTPTraceExporter(h).ExportSpans(context.Background(), oneSpan(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(jsonOf(t, h.Stats())), "topsecret") {
		t.Error("stats leak a header value")
	}
}

func TestHTTPExporters_AfterShutdownDrop(t *testing.T) {
	srv, got := collector(t, 200)
	h := NewHTTPSender(HTTPOptions{TracesEndpoint: srv.URL})
	e := NewHTTPTraceExporter(h)
	_ = e.Shutdown(context.Background())
	_ = e.ExportSpans(context.Background(), oneSpan(t))
	if len(got()) != 0 || h.Stats().Dropped != 1 {
		t.Errorf("got %v stats %+v", got(), h.Stats())
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func oneSpan(t *testing.T) []sdktrace.ReadOnlySpan {
	t.Helper()
	rec := &spanRecorder{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	_, sp := tp.Tracer("s").Start(context.Background(), "x")
	sp.End()
	return rec.spans
}

type spanRecorder struct{ spans []sdktrace.ReadOnlySpan }

func (r *spanRecorder) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (r *spanRecorder) OnEnd(s sdktrace.ReadOnlySpan)                   { r.spans = append(r.spans, s) }
func (r *spanRecorder) Shutdown(context.Context) error                  { return nil }
func (r *spanRecorder) ForceFlush(context.Context) error                { return nil }
