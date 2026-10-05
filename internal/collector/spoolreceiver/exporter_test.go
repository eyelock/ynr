package spoolreceiver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/spoolexporter"
)

// TestReadsWhatTheExporterWrites proves both ends of the spool agree: every signal the spool
// exporter writes, while its file is open and after it is closed, decodes with the collector's
// own OTLP JSON parser and reaches the pipelines stamped, and the closed file is deleted.
func TestReadsWhatTheExporterWrites(t *testing.T) {
	ctx := context.Background()
	root := spoolWith(t, nil)
	traces, logs, metrics := new(consumertest.TracesSink), new(consumertest.LogsSink), new(consumertest.MetricsSink)
	r := start(t, root, sinks{traces, logs, metrics})

	w := spoolexporter.NewWriter(spoolexporter.Options{
		Dir: filepath.Join(root, spool.LocalDir), Service: "ynh", InstanceID: "i1",
	})
	res := resource.NewSchemaless(
		attribute.String("service.name", "ynh"),
		attribute.String("ynr.provenance", "factory"),
	)
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSyncer(spoolexporter.NewTraceExporter(w)))
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(spoolexporter.NewLogExporter(w))))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(spoolexporter.NewMetricExporter(w))),
		sdkmetric.WithView(sdkmetric.NewView(sdkmetric.Instrument{Name: "size"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationBase2ExponentialHistogram{MaxSize: 160, MaxScale: 20}})))

	var rec otellog.Record
	rec.SetEventName("ynh.run.started")
	lp.Logger("ynh").Emit(ctx, rec)

	// The file is still open: the reader takes what is complete and leaves the file.
	if err := r.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if logs.LogRecordCount() != 1 {
		t.Fatalf("logs = %d", logs.LogRecordCount())
	}

	_, span := tp.Tracer("ynh").Start(ctx, "ynh.run")
	span.End()
	m := mp.Meter("ynh")
	runs, _ := m.Int64Counter("runs")
	runs.Add(ctx, 2)
	cost, _ := m.Float64Gauge("cost")
	cost.Record(ctx, 0.5)
	dur, _ := m.Float64Histogram("duration")
	dur.Record(ctx, 3)
	size, _ := m.Int64Histogram("size")
	size.Record(ctx, 7)
	for _, shut := range []func(context.Context) error{tp.Shutdown, lp.Shutdown, mp.Shutdown} {
		if err := shut(ctx); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	if err := r.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}

	if traces.SpanCount() != 1 || logs.LogRecordCount() != 1 || metrics.DataPointCount() != 4 {
		t.Fatalf("spans = %d, logs = %d, points = %d",
			traces.SpanCount(), logs.LogRecordCount(), metrics.DataPointCount())
	}
	if c := r.reader.Counters.Snapshot(); c.Malformed != 0 || c.Oversized != 0 {
		t.Fatalf("counters = %+v", c)
	}
	attrs := traces.AllTraces()[0].ResourceSpans().At(0).Resource().Attributes()
	if v, _ := attrs.Get("ynr.provenance"); v.Str() != "local" {
		t.Fatalf("provenance = %q, want local", v.Str())
	}
	types := map[string]pmetric.MetricType{}
	ms := metrics.AllMetrics()[0].ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
	for i := range ms.Len() {
		types[ms.At(i).Name()] = ms.At(i).Type()
	}
	want := map[string]pmetric.MetricType{
		"runs": pmetric.MetricTypeSum, "cost": pmetric.MetricTypeGauge,
		"duration": pmetric.MetricTypeHistogram, "size": pmetric.MetricTypeExponentialHistogram,
	}
	for name, typ := range want {
		if types[name] != typ {
			t.Errorf("%s: type %v, want %v", name, types[name], typ)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, spool.LocalDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}
