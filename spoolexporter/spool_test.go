package spoolexporter

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, DefaultMaxLine)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func TestAllSignalsRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sp, err := Open(dir, WithService("tool"), WithInstance("i1"))
	if err != nil {
		t.Fatal(err)
	}
	res := resource.NewSchemaless(attribute.String("service.name", "tool"))

	se, err := sp.SpanExporter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSyncer(se))
	_, span := tp.Tracer("t").Start(ctx, "work")
	span.SetAttributes(attribute.String("k", "v"))
	span.End()

	me, err := sp.MetricExporter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(reader))
	c, _ := mp.Meter("m").Int64Counter("runs")
	c.Add(ctx, 3)
	var rm = collect(t, reader)
	if err := me.Export(ctx, rm); err != nil {
		t.Fatal(err)
	}

	le, err := sp.LogExporter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewSimpleProcessor(le)))
	var rec otellog.Record
	rec.SetEventName("tool.started")
	rec.SetBody(attribute.StringValue("hello"))
	lp.Logger("l").Emit(ctx, rec)

	sp.Sync(ctx)
	for _, f := range []func(context.Context) error{tp.Shutdown, mp.Shutdown, lp.Shutdown} {
		if err := f(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}

	if got := files(t, dir); len(got) != 1 || got[0] != "tool-i1-000001.jsonl" {
		t.Fatalf("files = %v", got)
	}
	ls := lines(t, filepath.Join(dir, "tool-i1-000001.jsonl"))
	if len(ls) != 3 {
		t.Fatalf("got %d lines: %v", len(ls), ls)
	}

	tr := ptraceotlp.NewExportRequest()
	if err := tr.UnmarshalJSON([]byte(ls[0])); err != nil {
		t.Fatal(err)
	}
	got := tr.Traces().ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	if got.Name() != "work" || [16]byte(got.TraceID()) != [16]byte(span.SpanContext().TraceID()) {
		t.Fatalf("span = %s %s", got.Name(), got.TraceID())
	}
	if !strings.Contains(ls[0], `"traceId":"`+span.SpanContext().TraceID().String()+`"`) {
		t.Fatalf("trace id not hex: %s", ls[0])
	}

	mr := pmetricotlp.NewExportRequest()
	if err := mr.UnmarshalJSON([]byte(ls[1])); err != nil {
		t.Fatal(err)
	}
	if n := mr.Metrics().ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics().At(0).Name(); n != "runs" {
		t.Fatalf("metric = %s", n)
	}

	lr := plogotlp.NewExportRequest()
	if err := lr.UnmarshalJSON([]byte(ls[2])); err != nil {
		t.Fatal(err)
	}
	if n := lr.Logs().ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).EventName(); n != "tool.started" {
		t.Fatalf("event = %s", n)
	}

	if s := sp.Stats(); s.Lines != 3 || s.Dropped+s.Oversized+s.Malformed != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestOpenFileUntilClosed(t *testing.T) {
	dir := t.TempDir()
	sp, err := Open(dir, WithService("tool"), WithInstance("i1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); len(got) != 0 {
		t.Fatalf("created before writing: %v", got)
	}
	sp.write([]byte(`{"a":1}`))
	if got := files(t, dir); len(got) != 1 || got[0] != "tool-i1-000001"+OpenSuffix {
		t.Fatalf("files = %v", got)
	}
	_ = sp.Close()
	sp.write([]byte(`{"a":2}`))
	if got := files(t, dir); len(got) != 1 || got[0] != "tool-i1-000001"+ClosedSuffix {
		t.Fatalf("files = %v", got)
	}
	if s := sp.Stats(); s.Lines != 1 || s.Dropped != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	sp, err := Open(dir, WithService("tool"), WithInstance("i1"), WithMaxFile(20))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		sp.write([]byte(`{"0123456789":1}`)) // 17 bytes with the newline
	}
	_ = sp.Close()
	want := []string{"tool-i1-000001.jsonl", "tool-i1-000002.jsonl", "tool-i1-000003.jsonl"}
	if got := files(t, dir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v", got)
	}
}

func TestTotalCapDropsUntilReaderRemoves(t *testing.T) {
	dir := t.TempDir()
	sp, err := Open(dir, WithService("tool"), WithInstance("i1"), WithMaxFile(20), WithMaxTotal(40))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		sp.write([]byte(`{"0123456789":1}`))
	}
	if s := sp.Stats(); s.Lines != 2 || s.Dropped != 1 {
		t.Fatalf("stats = %+v", s)
	}
	// A reader ships and removes the closed file; there is room again.
	if err := os.Remove(filepath.Join(dir, "tool-i1-000001.jsonl")); err != nil {
		t.Fatal(err)
	}
	sp.write([]byte(`{"0123456789":1}`))
	if s := sp.Stats(); s.Lines != 3 || s.Dropped != 1 {
		t.Fatalf("stats = %+v", s)
	}
	_ = sp.Close()
}

func TestOversizedDropped(t *testing.T) {
	dir := t.TempDir()
	sp, err := Open(dir, WithMaxLine(10))
	if err != nil {
		t.Fatal(err)
	}
	sp.write([]byte(`{"0123456789":1}`))
	if s := sp.Stats(); s.Oversized != 1 || s.Lines != 0 {
		t.Fatalf("stats = %+v", s)
	}
	_ = sp.Close()
	if got := files(t, dir); len(got) != 0 {
		t.Fatalf("files = %v", got)
	}
}

func TestOpenRefusesLinkAndFile(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("opened a link to a directory")
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(file); err == nil {
		t.Fatal("opened a file")
	}
}

func TestPlantedLinkSkipped(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Symlink(target, filepath.Join(dir, "tool-i1-000001"+OpenSuffix)); err != nil {
		t.Fatal(err)
	}
	sp, err := Open(dir, WithService("tool"), WithInstance("i1"))
	if err != nil {
		t.Fatal(err)
	}
	sp.write([]byte(`{}`))
	_ = sp.Close()
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("wrote through a planted link")
	}
	if _, err := os.Stat(filepath.Join(dir, "tool-i1-000002.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{"ynh": "ynh", "a/b c": "a_b_c", "..": "unknown", "-x-": "x"} {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSyncTimeoutNeverBlocks(t *testing.T) {
	sp, err := Open(t.TempDir(), WithSyncTimeout(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	sp.write([]byte(`{}`))
	start := time.Now()
	sp.Sync(context.Background())
	if time.Since(start) > time.Second {
		t.Fatal("sync blocked")
	}
	_ = sp.Close()
}
