package spoolreceiver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/spoolexporter"
)

// TestReadsWhatTheExporterWrites proves both ends of the spool agree: records written by the
// spool exporter, while its file is open and after it is closed, reach the pipelines stamped, and
// the closed file is deleted.
func TestReadsWhatTheExporterWrites(t *testing.T) {
	ctx := context.Background()
	root := spoolWith(t, nil)
	traces, logs := new(consumertest.TracesSink), new(consumertest.LogsSink)
	r := start(t, root, sinks{traces, logs, new(consumertest.MetricsSink)})

	sp, err := spoolexporter.Open(filepath.Join(root, spool.LocalDir), spoolexporter.WithService("ynh"))
	if err != nil {
		t.Fatal(err)
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", "ynh"),
		attribute.String("ynr.provenance", "factory"),
	)
	se, err := sp.SpanExporter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSyncer(se))
	le, err := sp.LogExporter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewSimpleProcessor(le)))

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
	if err := tp.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lp.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}

	if traces.SpanCount() != 1 || logs.LogRecordCount() != 1 {
		t.Fatalf("spans = %d, logs = %d", traces.SpanCount(), logs.LogRecordCount())
	}
	attrs := traces.AllTraces()[0].ResourceSpans().At(0).Resource().Attributes()
	if v, _ := attrs.Get("ynr.provenance"); v.Str() != "local" {
		t.Fatalf("provenance = %q, want local", v.Str())
	}
	entries, err := os.ReadDir(filepath.Join(root, spool.LocalDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}
