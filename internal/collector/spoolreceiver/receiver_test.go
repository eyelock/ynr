package spoolreceiver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/internal/stamp"
)

const traceLine = `{"resourceSpans":[{"resource":{"attributes":[` +
	`{"key":"service.name","value":{"stringValue":"ynh"}},` +
	`{"key":"ynf.lane","value":{"stringValue":"github.com/x#forged"}},` +
	`{"key":"ynr.provenance","value":{"stringValue":"factory"}}]},` +
	`"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",` +
	`"name":"ynh.run","startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`

const logLine = `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynh"}}]},` +
	`"scopeLogs":[{"logRecords":[{"eventName":"ynh.run.started","body":{"stringValue":"started"}}]}]}]}`

type sinks struct {
	traces  consumer.Traces
	logs    consumer.Logs
	metrics consumer.Metrics
}

func start(t *testing.T, root string, s sinks) *spoolReceiver {
	t.Helper()
	f := NewFactory()
	cfg := f.CreateDefaultConfig().(*Config)
	cfg.Root, cfg.CollectorID, cfg.CollectorInstance = root, "gha-eyelock", "job-9"
	set := receivertest.NewNopSettings(Type)
	ctx := context.Background()
	tr, err := f.CreateTraces(ctx, set, cfg, s.traces)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.CreateLogs(ctx, set, cfg, s.logs); err != nil {
		t.Fatal(err)
	}
	if _, err := f.CreateMetrics(ctx, set, cfg, s.metrics); err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(ctx, componenttest.NewNopHost()); err != nil {
		t.Fatal(err)
	}
	r := tr.(*spoolReceiver)
	r.cancel() // the tests poll by hand
	<-r.done
	t.Cleanup(func() { _ = tr.Shutdown(ctx) })
	return r
}

func spoolWith(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func resourceAttr(td ptrace.Traces, k string) string {
	v, ok := td.ResourceSpans().At(0).Resource().Attributes().Get(k)
	if !ok {
		return ""
	}
	return v.Str()
}

func TestRunRecordsAreStampedFromFolderAndManifest(t *testing.T) {
	root := spoolWith(t, map[string]string{
		"manifests/r1.json":   `{"run":"r1","lane":"github.com/acme/cfg#lint","harness":"ynh-lint","focus":"tidy","item":"github.com/eyelock/ynh#77","step":"s3"}`,
		"runs/r1/ynh-a.jsonl": traceLine + "\n" + logLine + "\nnot json\n",
		"runs/r2/ynh-b.jsonl": traceLine + "\n",
	})
	traces, logs := new(consumertest.TracesSink), new(consumertest.LogsSink)
	r := start(t, root, sinks{traces, logs, consumertest.NewNop()})
	if err := r.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	all := traces.AllTraces()
	if len(all) != 2 || logs.LogRecordCount() != 1 {
		t.Fatalf("traces %d logs %d", len(all), logs.LogRecordCount())
	}
	r1, r2 := all[0], all[1]
	for k, want := range map[string]string{
		stamp.Provenance: "run", stamp.CollectorID: "gha-eyelock", stamp.CollectorInstance: "job-9",
		stamp.Lane: "github.com/acme/cfg#lint", stamp.RunID: "r1", "service.name": "ynh",
	} {
		if got := resourceAttr(r1, k); got != want {
			t.Errorf("r1 %s = %q, want %q", k, got, want)
		}
	}
	if got := resourceAttr(r2, stamp.Lane); got != "" {
		t.Errorf("r2 kept its forged lane %q", got)
	}
	if got := resourceAttr(r2, stamp.ProvenanceWarning); got != stamp.NoManifest {
		t.Errorf("r2 warning = %q", got)
	}
	if s := r.reader.Counters.Snapshot(); s.Malformed != 1 || s.Deleted != 2 {
		t.Errorf("counters %+v", s)
	}
}

func TestFailedExportKeepsTheLineForTheNextPoll(t *testing.T) {
	root := spoolWith(t, map[string]string{"local/x.jsonl": traceLine + "\n"})
	r := start(t, root, sinks{consumertest.NewErr(errors.New("upstream down")), consumertest.NewNop(), consumertest.NewNop()})
	if err := r.PollOnce(context.Background()); err == nil {
		t.Fatal("want the export error")
	}
	if _, err := os.Stat(filepath.Join(root, "local", "x.jsonl")); err != nil {
		t.Fatalf("the line was lost: %v", err)
	}
}

func TestPermanentRejectionIsSkipped(t *testing.T) {
	root := spoolWith(t, map[string]string{"local/x.jsonl": traceLine + "\n"})
	perm := consumererror.NewPermanent(errors.New("400 bad request"))
	r := start(t, root, sinks{consumertest.NewErr(perm), consumertest.NewNop(), consumertest.NewNop()})
	if err := r.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.reader.Counters.Snapshot(); s.Malformed != 1 || s.Deleted != 1 {
		t.Fatalf("counters %+v", s)
	}
}

func TestRunUserComesFromTheManifest(t *testing.T) {
	root := spoolWith(t, map[string]string{
		"runs/r1/.keep":     "",
		"manifests/r1.json": `{"run":"r1","lane":"github.com/x#a","uid":4242}`,
		"runs/r2/.keep":     "",
		"manifests/r2.json": `{"run":"r2","lane":"github.com/x#a"}`,
	})
	r := start(t, root, sinks{new(consumertest.TracesSink), new(consumertest.LogsSink), new(consumertest.MetricsSink)})
	r.manifests = map[string]manifestResult{}
	run := func(name string) spool.Writer {
		return spool.Writer{Class: spool.Run, Name: name, Dir: filepath.Join(root, "runs", name), Rel: "runs/" + name}
	}
	if uid, ok := r.runUser(run("r1")); !ok || uid != 4242 {
		t.Errorf("r1 = %d, %v; want 4242", uid, ok)
	}
	if _, ok := r.runUser(run("r2")); ok {
		t.Error("r2 has no uid in its manifest")
	}
	if _, ok := r.runUser(spool.Writer{Class: spool.Local, Name: "local"}); ok {
		t.Error("only runs have a run user")
	}
}
