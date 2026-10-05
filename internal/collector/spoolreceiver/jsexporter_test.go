package spoolreceiver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/consumer/consumertest"

	"github.com/eyelock/ynr/internal/spool"
)

// TestReadsWhatTheJSExporterWrites runs the npm spool exporter and reads its output, so both
// exporters are held to the same reader. It needs node and the package built (make js); CI has
// both, and a machine without them skips it.
func TestReadsWhatTheJSExporterWrites(t *testing.T) {
	fixture, err := filepath.Abs("../../../spoolexporter/js/test/fixture.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(fixture), "..", "dist", "index.js")); err != nil {
		t.Skip("the npm spool exporter is not built: run make js")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}

	root := spoolWith(t, nil)
	traces, logs, metrics := new(consumertest.TracesSink), new(consumertest.LogsSink), new(consumertest.MetricsSink)
	r := start(t, root, sinks{traces, logs, metrics})

	out, err := exec.Command(node, fixture, filepath.Join(root, spool.LocalDir)).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	if err := r.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if traces.SpanCount() != 1 || logs.LogRecordCount() != 1 || metrics.DataPointCount() != 3 {
		t.Fatalf("spans = %d, logs = %d, points = %d",
			traces.SpanCount(), logs.LogRecordCount(), metrics.DataPointCount())
	}
	if c := r.reader.Counters.Snapshot(); c.Malformed != 0 || c.Oversized != 0 {
		t.Fatalf("counters = %+v", c)
	}
	span := traces.AllTraces()[0].ResourceSpans().At(0)
	if v, _ := span.Resource().Attributes().Get("ynr.provenance"); v.Str() != "local" {
		t.Fatalf("provenance = %q, want local", v.Str())
	}
	if v, _ := span.ScopeSpans().At(0).Spans().At(0).Attributes().Get("i"); v.Int() != 42 {
		t.Fatalf("int attribute = %v", v.AsRaw())
	}
	entries, err := os.ReadDir(filepath.Join(root, spool.LocalDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}
