package spoolexporter

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)

// metricByName finds a metric in a decoded ExportMetricsServiceRequest.
func metricByName(t *testing.T, req map[string]any, name string) map[string]any {
	t.Helper()
	for _, m := range dig(t, req, "resourceMetrics", 0, "scopeMetrics", 0, "metrics").([]any) {
		if mm := m.(map[string]any); mm["name"] == name {
			return mm
		}
	}
	t.Fatalf("no metric %q in %v", name, req)
	return nil
}

func TestMetricExporter_WritesOTLPJSON(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w := NewWriter(Options{Dir: dir, Service: "svc", InstanceID: "inst"})
	reader := sdkmetric.NewPeriodicReader(NewMetricExporter(w))
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(resource.NewSchemaless(attribute.String("service.name", "svc"))),
		sdkmetric.WithReader(reader),
		sdkmetric.WithView(sdkmetric.NewView(sdkmetric.Instrument{Name: "size"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationBase2ExponentialHistogram{MaxSize: 160, MaxScale: 20}})),
	)
	m := mp.Meter("scope")
	runs, _ := m.Int64Counter("runs", metric.WithUnit("{run}"))
	runs.Add(ctx, 3, metric.WithAttributes(attribute.String("outcome", "ok")))
	cost, _ := m.Float64Gauge("cost")
	cost.Record(ctx, 0.25)
	dur, _ := m.Float64Histogram("duration", metric.WithExplicitBucketBoundaries(1, 10))
	dur.Record(ctx, 5)
	size, _ := m.Int64Histogram("size")
	size.Record(ctx, 4)
	if err := mp.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	w.Close()

	lines := readLines(t, dir)
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	req := lines[0]

	sum := metricByName(t, req, "runs")
	if sum["unit"] != "{run}" || dig(t, sum, "sum", "aggregationTemporality") != float64(2) ||
		dig(t, sum, "sum", "isMonotonic") != true || dig(t, sum, "sum", "dataPoints", 0, "asInt") != "3" {
		t.Errorf("sum = %v", sum)
	}
	if got := attrMap(t, dig(t, sum, "sum", "dataPoints", 0, "attributes"))["outcome"]["stringValue"]; got != "ok" {
		t.Errorf("sum attributes = %v", got)
	}
	if got := dig(t, metricByName(t, req, "cost"), "gauge", "dataPoints", 0, "asDouble"); got != 0.25 {
		t.Errorf("gauge = %v", got)
	}
	h := dig(t, metricByName(t, req, "duration"), "histogram", "dataPoints", 0).(map[string]any)
	if h["count"] != "1" || h["sum"] != float64(5) || h["min"] != float64(5) {
		t.Errorf("histogram = %v", h)
	}
	if b, _ := json.Marshal(h["bucketCounts"]); string(b) != `["0","1","0"]` {
		t.Errorf("bucket counts = %s", b)
	}
	if b, _ := json.Marshal(h["explicitBounds"]); string(b) != `[1,10]` {
		t.Errorf("bounds = %s", b)
	}
	e := dig(t, metricByName(t, req, "size"), "exponentialHistogram", "dataPoints", 0).(map[string]any)
	if e["count"] != "1" || e["positive"] == nil {
		t.Errorf("exponential histogram = %v", e)
	}
	if st := w.Stats(); st != (Stats{}) {
		t.Errorf("stats = %+v", st)
	}
}

func TestMetricsRequest_SummaryAndSpecialDoubles(t *testing.T) {
	rm := &metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{Metrics: []metricdata.Metrics{
		{Name: "s", Data: metricdata.Summary{DataPoints: []metricdata.SummaryDataPoint{{
			Count: 2, Sum: math.Inf(1),
			QuantileValues: []metricdata.QuantileValue{{Quantile: 0.5, Value: math.NaN()}},
		}}}},
		{Name: "empty", Data: metricdata.Gauge[int64]{}},
	}}}}
	req, points := metricsRequest(rm)
	if points != 1 {
		t.Fatalf("points = %d, want 1", points)
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"resourceMetrics":[{"resource":{},"scopeMetrics":[{"scope":{},"metrics":[{"name":"s","summary":` +
		`{"dataPoints":[{"count":"2","sum":"Infinity","quantileValues":[{"quantile":0.5,"value":"NaN"}]}]}}]}]}]}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

func TestMetricExporter_NothingToWrite(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(Options{Dir: dir})
	e := NewMetricExporter(w)
	if err := e.Export(context.Background(), &metricdata.ResourceMetrics{}); err != nil {
		t.Fatal(err)
	}
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Export(context.Background(), &metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{
		Metrics: []metricdata.Metrics{{Name: "g", Data: metricdata.Gauge[int64]{DataPoints: make([]metricdata.DataPoint[int64], 2)}}},
	}}}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if got := spoolFiles(t, dir); len(got) != 0 {
		t.Errorf("files = %v, want none", got)
	}
	if got := w.Stats().Dropped; got != 2 {
		t.Errorf("dropped = %d, want the 2 points exported after shutdown", got)
	}
}
