package spoolexporter

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	colllogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	colltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// These tests hold the hand-written encoder to the official OTLP types:
// every request is decoded with go.opentelemetry.io/proto/otlp (a test-only
// dependency), re-encoded as protobuf JSON, and compared with the JSON
// encoding of the same input.

// captureSink records the requests an exporter sends it.
type captureSink struct {
	mu   sync.Mutex
	reqs []any
}

func (c *captureSink) send(_ context.Context, _ signal, req any, _ int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
}
func (c *captureSink) drop(int) {}
func (c *captureSink) sync()    {}

// oneofKeys are the JSON members of a oneof, kept even at their zero value.
var oneofKeys = map[string]bool{
	"stringValue": true, "boolValue": true, "intValue": true, "doubleValue": true,
	"asInt": true, "asDouble": true,
}

// normalise drops the defaults one side writes and the other omits: empty
// strings, "0", 0, false, empty lists and objects, except a oneof member.
func normalise(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			// An id is hex in OTLP/JSON; the official types' JSON writes base64.
			if n := normalise(e, k); n != nil {
				out[k] = n
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			if n := normalise(e, ""); n != nil {
				out[i] = n
			} else {
				out[i] = map[string]any{}
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		if x == "" || x == "0" {
			if oneofKeys[key] {
				return x
			}
			return nil
		}
	case float64:
		if x == 0 && !oneofKeys[key] {
			return nil
		}
	case bool:
		if !x && !oneofKeys[key] {
			return nil
		}
	}
	return v
}

// fixIDs turns the base64 ids protojson writes for bytes into hex... except
// bytesValue, which both sides write as base64.
func fixIDs(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = fixIDs(e, k)
		}
	case []any:
		for i, e := range x {
			x[i] = fixIDs(e, key)
		}
	case string:
		switch key {
		case "traceId", "spanId", "parentSpanId":
			return hexOfBase64(x)
		}
	}
	return v
}

func compareToJSON(t *testing.T, req any, decoded proto.Message) {
	t.Helper()
	wantRaw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	gotRaw, err := protojson.MarshalOptions{UseEnumNumbers: true}.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotRaw, &got); err != nil {
		t.Fatal(err)
	}
	want, got = normalise(want, ""), normalise(fixIDs(got, ""), "")
	if !reflect.DeepEqual(want, got) {
		w, _ := json.MarshalIndent(want, "", " ")
		g, _ := json.MarshalIndent(got, "", " ")
		t.Errorf("protobuf decodes differently from JSON\nJSON:\n%s\nprotobuf:\n%s", w, g)
	}
}

func richResource() *resource.Resource {
	return resource.NewWithAttributes("https://opentelemetry.io/schemas/1.26.0",
		attribute.String("service.name", "svc"),
		attribute.Bool("flag", true),
		attribute.Int64("n", -5),
		attribute.Float64("pi", 3.25),
		attribute.Float64("zero", 0),
		attribute.Float64("nan", math.NaN()),
		attribute.Float64("inf", math.Inf(1)),
		attribute.Float64("ninf", math.Inf(-1)),
		attribute.StringSlice("strs", []string{"a", "", "c"}),
		attribute.BoolSlice("bools", []bool{true, false}),
		attribute.Int64Slice("ints", []int64{0, 1, math.MinInt64, math.MaxInt64}),
		attribute.Float64Slice("floats", []float64{0, 1.5}),
		attribute.ByteSlice("bytes", []byte{0, 1, 2, 255}),
		attribute.String("empty", ""),
		attribute.Int64("zeroint", 0),
		attribute.Bool("false", false),
	)
}

func TestProtobuf_TracesRoundTrip(t *testing.T) {
	cs := &captureSink{}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(richResource()),
		sdktrace.WithSyncer(&TraceExporter{w: cs}),
	)
	tr := tp.Tracer("scope-a", trace.WithInstrumentationVersion("1.2.3"),
		trace.WithInstrumentationAttributes(attribute.String("k", "v")),
		trace.WithSchemaURL("https://opentelemetry.io/schemas/1.26.0"))

	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{9, 9, 9, 9, 9, 9, 9, 9},
		TraceFlags: trace.FlagsSampled,
		TraceState: mustTraceState(t, "vendor=value"),
		Remote:     true,
	})
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), remote)
	ctx, parent := tr.Start(ctx, "parent", trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("a", "b"), attribute.Int64("i", 7)),
		trace.WithLinks(trace.Link{
			SpanContext: remote,
			Attributes:  []attribute.KeyValue{attribute.Bool("linked", true)},
		}))
	parent.AddEvent("event", trace.WithTimestamp(time.Unix(1700000000, 123)),
		trace.WithAttributes(attribute.Float64("f", 2.5)))
	parent.SetStatus(2, "it broke")
	_, child := tr.Start(ctx, "child", trace.WithSpanKind(trace.SpanKindProducer))
	child.SetStatus(1, "")
	child.End()
	parent.End()
	// A second scope, so several scopeSpans arrive.
	_, other := tp.Tracer("scope-b").Start(context.Background(), "other")
	other.End()

	if len(cs.reqs) == 0 {
		t.Fatal("no requests captured")
	}
	for _, req := range cs.reqs {
		b, err := encodeProtobuf(req)
		if err != nil {
			t.Fatal(err)
		}
		var decoded colltrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("official types cannot decode the encoding: %v", err)
		}
		compareToJSON(t, req, &decoded)
		// Spot-check the typed values the JSON comparison reads as text.
		s := decoded.ResourceSpans[0].ScopeSpans[0].Spans[0]
		if len(s.TraceId) != 16 || len(s.SpanId) != 8 {
			t.Errorf("ids are %d and %d bytes", len(s.TraceId), len(s.SpanId))
		}
		if s.StartTimeUnixNano == 0 || s.EndTimeUnixNano < s.StartTimeUnixNano {
			t.Errorf("times %d..%d", s.StartTimeUnixNano, s.EndTimeUnixNano)
		}
	}
}

func mustTraceState(t *testing.T, s string) trace.TraceState {
	t.Helper()
	ts, err := trace.ParseTraceState(s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestProtobuf_LogsRoundTrip(t *testing.T) {
	cs := &captureSink{}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(richResource()),
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(&LogExporter{w: cs})),
	)
	lg := lp.Logger("scope", otellog.WithInstrumentationVersion("0.1"))

	var r otellog.Record
	r.SetTimestamp(time.Unix(1700000000, 42))
	r.SetObservedTimestamp(time.Unix(1700000001, 43))
	r.SetSeverity(otellog.SeverityWarn)
	r.SetSeverityText("WARN")
	r.SetBody(attribute.StringValue("hello"))
	r.SetEventName("my.event")
	r.AddAttributes(
		attribute.String("s", "x"), attribute.Int64("i", -3), attribute.Float64("f", 0),
		attribute.Bool("b", false), attribute.ByteSlice("by", []byte{7, 8}),
		attribute.Slice("sl", attribute.Int64Value(1), attribute.StringValue("two")),
		attribute.Map("m", attribute.String("in", "ner")),
	)
	lg.Emit(context.Background(), r)

	// A structured body, and a trace context.
	var r2 otellog.Record
	r2.SetBody(attribute.MapValue(attribute.Bool("ok", true), attribute.Float64("n", math.NaN())))
	lg.Emit(trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	})), r2)

	if len(cs.reqs) != 2 {
		t.Fatalf("captured %d requests", len(cs.reqs))
	}
	for _, req := range cs.reqs {
		b, err := encodeProtobuf(req)
		if err != nil {
			t.Fatal(err)
		}
		var decoded colllogs.ExportLogsServiceRequest
		if err := proto.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("official types cannot decode the encoding: %v", err)
		}
		compareToJSON(t, req, &decoded)
	}
}

func TestProtobuf_MetricsRoundTrip(t *testing.T) {
	start, now := time.Unix(1700000000, 1), time.Unix(1700000060, 2)
	attrs := attribute.NewSet(attribute.String("k", "v"), attribute.Int64("n", 1))
	ex := metricdata.Exemplar[float64]{
		FilteredAttributes: []attribute.KeyValue{attribute.String("f", "g")},
		Time:               now, Value: 0,
		SpanID: []byte{1, 2, 3, 4, 5, 6, 7, 8}, TraceID: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
	}
	exInt := metricdata.Exemplar[int64]{Time: now, Value: -9}
	rm := &metricdata.ResourceMetrics{
		Resource: richResource(),
		ScopeMetrics: []metricdata.ScopeMetrics{{
			Scope: instrumentation.Scope{Name: "scope", Version: "1", SchemaURL: "https://x/1"},
			Metrics: []metricdata.Metrics{
				{Name: "g_int", Unit: "1", Description: "d", Data: metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{
					{Attributes: attrs, StartTime: start, Time: now, Value: 0, Exemplars: []metricdata.Exemplar[int64]{exInt}},
					{Attributes: attrs, Time: now, Value: math.MinInt64},
				}}},
				{Name: "g_float", Data: metricdata.Gauge[float64]{DataPoints: []metricdata.DataPoint[float64]{
					{Time: now, Value: 0}, {Time: now, Value: math.NaN()}, {Time: now, Value: -1.5, Exemplars: []metricdata.Exemplar[float64]{ex}},
				}}},
				{Name: "sum_int", Data: metricdata.Sum[int64]{Temporality: metricdata.CumulativeTemporality, IsMonotonic: true,
					DataPoints: []metricdata.DataPoint[int64]{{StartTime: start, Time: now, Value: 5}}}},
				{Name: "sum_float", Data: metricdata.Sum[float64]{Temporality: metricdata.DeltaTemporality,
					DataPoints: []metricdata.DataPoint[float64]{{StartTime: start, Time: now, Value: 2.5}}}},
				{Name: "hist", Data: metricdata.Histogram[float64]{Temporality: metricdata.DeltaTemporality,
					DataPoints: []metricdata.HistogramDataPoint[float64]{{
						Attributes: attrs, StartTime: start, Time: now, Count: 6, Sum: 12.5,
						Bounds: []float64{0, 5, 10}, BucketCounts: []uint64{1, 2, 3, math.MaxUint64},
						Min: metricdata.NewExtrema(0.5), Max: metricdata.NewExtrema(9.5),
						Exemplars: []metricdata.Exemplar[float64]{ex},
					}, {Time: now}}}},
				{Name: "hist_int", Data: metricdata.Histogram[int64]{Temporality: metricdata.CumulativeTemporality,
					DataPoints: []metricdata.HistogramDataPoint[int64]{{Time: now, Count: 1, Sum: 3, Bounds: []float64{1}, BucketCounts: []uint64{0, 1}}}}},
				{Name: "exp", Data: metricdata.ExponentialHistogram[float64]{Temporality: metricdata.CumulativeTemporality,
					DataPoints: []metricdata.ExponentialHistogramDataPoint[float64]{{
						Attributes: attrs, StartTime: start, Time: now, Count: 9, Sum: 4, Scale: -3, ZeroCount: 2, ZeroThreshold: 0.001,
						PositiveBucket: metricdata.ExponentialBucket{Offset: -7, Counts: []uint64{1, 200, 70000}},
						NegativeBucket: metricdata.ExponentialBucket{Offset: 4, Counts: []uint64{3}},
						Min:            metricdata.NewExtrema(-2.0), Max: metricdata.NewExtrema(8.0),
						Exemplars: []metricdata.Exemplar[float64]{ex},
					}}}},
				{Name: "summary", Data: metricdata.Summary{DataPoints: []metricdata.SummaryDataPoint{{
					Attributes: attrs, StartTime: start, Time: now, Count: 4, Sum: 10,
					QuantileValues: []metricdata.QuantileValue{{Quantile: 0, Value: 1}, {Quantile: 0.5, Value: 2.5}, {Quantile: 1, Value: math.Inf(1)}},
				}}}},
			},
		}, {
			Scope:   instrumentation.Scope{Name: "scope-two"},
			Metrics: []metricdata.Metrics{{Name: "c", Data: metricdata.Sum[int64]{DataPoints: []metricdata.DataPoint[int64]{{Time: now, Value: 1}}}}},
		}},
	}
	req, points := metricsRequest(rm)
	if points == 0 {
		t.Fatal("no points")
	}
	b, err := encodeProtobuf(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded collmetrics.ExportMetricsServiceRequest
	if err := proto.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("official types cannot decode the encoding: %v", err)
	}
	compareToJSON(t, req, &decoded)

	// Presence the JSON comparison cannot see: an optional field set to zero
	// is present, and a oneof set to zero is present.
	ms := decoded.ResourceMetrics[0].ScopeMetrics[0].Metrics
	hp := ms[4].GetHistogram().DataPoints[1]
	if hp.Sum == nil || *hp.Sum != 0 {
		t.Errorf("histogram sum = %v, want a present zero", hp.Sum)
	}
	if hp.Min != nil || hp.Max != nil {
		t.Errorf("histogram min/max = %v/%v, want absent", hp.Min, hp.Max)
	}
	if _, ok := ms[0].GetGauge().DataPoints[0].Value.(*metricspb.NumberDataPoint_AsInt); !ok {
		t.Errorf("int gauge value type = %T", ms[0].GetGauge().DataPoints[0].Value)
	}
	if got := ms[0].GetGauge().DataPoints[1].GetAsInt(); got != math.MinInt64 {
		t.Errorf("asInt = %d", got)
	}
	if h := ms[4].GetHistogram().DataPoints[0]; h.BucketCounts[3] != math.MaxUint64 {
		t.Errorf("bucket counts %v", h.BucketCounts)
	}
}

func TestProtobuf_EmptyAndBadInput(t *testing.T) {
	// Empty requests are valid, empty messages.
	for _, req := range []any{jsonTraceRequest{}, jsonLogsRequest{}, jsonMetricsRequest{}} {
		b, err := encodeProtobuf(req)
		if err != nil || len(b) != 0 {
			t.Errorf("%T: %v, %d bytes", req, err, len(b))
		}
	}
	if _, err := encodeProtobuf(struct{}{}); err == nil {
		t.Error("an unknown request type encoded")
	}
	bad := jsonTraceRequest{ResourceSpans: []jsonResourceSpans{{ScopeSpans: []jsonScopeSpans{{Spans: []jsonSpan{{TraceID: "zz"}}}}}}}
	if _, err := encodeProtobuf(bad); err == nil {
		t.Error("a malformed id encoded")
	}
	bad = jsonTraceRequest{ResourceSpans: []jsonResourceSpans{{ScopeSpans: []jsonScopeSpans{{Spans: []jsonSpan{{StartTimeUnixNano: "-1"}}}}}}}
	if _, err := encodeProtobuf(bad); err == nil {
		t.Error("a malformed time encoded")
	}
}

func TestHTTPSender_ProtobufBodiesAndContentType(t *testing.T) {
	var mu sync.Mutex
	got := map[string]struct {
		ct   string
		body []byte
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path] = struct {
			ct   string
			body []byte
		}{r.Header.Get("Content-Type"), b}
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)

	// Protobuf for all, JSON for logs by the per-signal setting.
	opts, ok, why := HTTPOptionsFromEnv(env(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":      srv.URL,
		"OTEL_EXPORTER_OTLP_PROTOCOL":      "http/protobuf",
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL": "http/json",
	}))
	if !ok || why != "" {
		t.Fatalf("configured=%v why=%q", ok, why)
	}
	h := NewHTTPSender(opts)
	emitAll(t, h)

	mu.Lock()
	defer mu.Unlock()
	var tr colltrace.ExportTraceServiceRequest
	if g := got["/v1/traces"]; g.ct != "application/x-protobuf" {
		t.Errorf("traces content type %q", g.ct)
	} else if err := proto.Unmarshal(g.body, &tr); err != nil || tr.ResourceSpans[0].ScopeSpans[0].Spans[0].Name != "work" {
		t.Errorf("traces body: %v %v", err, &tr)
	}
	var mr collmetrics.ExportMetricsServiceRequest
	if g := got["/v1/metrics"]; g.ct != "application/x-protobuf" {
		t.Errorf("metrics content type %q", g.ct)
	} else if err := proto.Unmarshal(g.body, &mr); err != nil || mr.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].Name != "runs" {
		t.Errorf("metrics body: %v %v", err, &mr)
	}
	if g := got["/v1/logs"]; g.ct != "application/json" || !json.Valid(g.body) {
		t.Errorf("logs content type %q, body %q", g.ct, g.body)
	}
	if s := h.Stats(); s != (Stats{}) {
		t.Errorf("stats = %+v", s)
	}
}

func TestHTTPOptionsFromEnv_Protocols(t *testing.T) {
	base := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4318"}
	with := func(kv ...string) func(string) string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return env(m)
	}
	opts, ok, why := HTTPOptionsFromEnv(with())
	if !ok || why != "" || opts.TracesProtocol != ProtocolJSON || opts.LogsProtocol != ProtocolJSON || opts.MetricsProtocol != ProtocolJSON {
		t.Errorf("default: %+v %v %q", opts, ok, why)
	}
	opts, ok, why = HTTPOptionsFromEnv(with("OTEL_EXPORTER_OTLP_PROTOCOL", "HTTP/Protobuf", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "http/json"))
	if !ok || why != "" || opts.TracesProtocol != ProtocolProtobuf || opts.LogsProtocol != ProtocolProtobuf || opts.MetricsProtocol != ProtocolJSON {
		t.Errorf("per signal: %+v %v %q", opts, ok, why)
	}
	// grpc is still refused, general or per signal, and named.
	for _, name := range []string{"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"} {
		_, ok, why = HTTPOptionsFromEnv(with(name, "grpc"))
		if ok || why == "" || !contains(why, name) {
			t.Errorf("%s=grpc: %v %q", name, ok, why)
		}
	}
	// A per-signal setting overrides a general grpc: that signal is fine, the others refuse.
	_, ok, why = HTTPOptionsFromEnv(with("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc",
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf",
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "http/protobuf",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "http/protobuf"))
	if !ok || why != "" {
		t.Errorf("every signal overridden: %v %q", ok, why)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func hexOfBase64(s string) string {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return s
	}
	return hex.EncodeToString(raw)
}
