package tail

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/spool"
)

const (
	spanLine = `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynh"}}]},` +
		`"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",` +
		`"name":"ynh.run","startTimeUnixNano":"1791235800000000000","endTimeUnixNano":"1791235890000000000",` +
		`"attributes":[{"key":"ynh.run.outcome","value":{"stringValue":"converged"}}],"status":{"code":2}}]}]}]}`
	eventLine = `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynf"}}]},` +
		`"scopeLogs":[{"logRecords":[{"timeUnixNano":"1791235800000000000","eventName":"ynf.step.started",` +
		`"body":{"stringValue":"started"},"attributes":[{"key":"ynf.item.key","value":{"stringValue":"github.com/eyelock/ynr#12"}}]}]}]}]}`
	metricLine = `{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynf"}}]},` +
		`"scopeMetrics":[{"metrics":[{"name":"ynf.run.cost","sum":{"aggregationTemporality":1,` +
		`"dataPoints":[{"timeUnixNano":"1791235800000000000","asDouble":1.5}]}}]}]}]}`
)

func TestDecodeEverySignal(t *testing.T) {
	w := spool.Writer{Class: spool.Local, Rel: "local"}
	var got []Record
	for _, l := range []string{spanLine, eventLine, metricLine} {
		recs, err := Decode([]byte(l), w, nil)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, recs...)
	}
	if len(got) != 3 {
		t.Fatalf("records = %+v", got)
	}
	s, e, m := got[0], got[1], got[2]
	if s.Kind != "span" || s.Name != "ynh.run" || s.Outcome != "converged" || s.Status != "error" || s.DurationMS != 90000 || s.TraceID == "" {
		t.Errorf("span = %+v", s)
	}
	if e.Kind != "event" || e.Name != "ynf.step.started" || e.Item != "github.com/eyelock/ynr#12" || e.Body != "started" {
		t.Errorf("event = %+v", e)
	}
	if m.Kind != "metric" || m.Name != "ynf.run.cost" || m.Value == nil || *m.Value != 1.5 {
		t.Errorf("metric = %+v", m)
	}
	var b bytes.Buffer
	if err := Write(&b, s, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "span   ynh        ynh.run  outcome=converged  status=error  1m30s  trace=5b8e") {
		t.Errorf("text = %q", b.String())
	}
	if _, err := Decode([]byte(`{"x":1}`), w, nil); err == nil {
		t.Error("decoded a line that is not OTLP")
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestFollowShowsNewRecordsWithTheirLane: the stream shows what is written after it starts,
// stamps a run's records from ynf's manifest, and filters by item.
func TestFollowShowsNewRecordsWithTheirLane(t *testing.T) {
	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(root, spool.RunsDir, "run-1")
	for _, d := range []string{run, filepath.Join(root, spool.ManifestsDir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := json.Marshal(map[string]string{"run": "run-1", "lane": "github.com/eyelock/ynr#lint", "item": "github.com/eyelock/ynr#12"})
	if err := os.WriteFile(filepath.Join(root, spool.ManifestsDir, "run-1.json"), m, 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(run, "ynh-1-000001"+spool.OpenSuffix)
	if err := os.WriteFile(file, []byte(spanLine+"\n"), 0o644); err != nil { // before the tail: not shown
		t.Fatal(err)
	}
	var out syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- Follow(ctx, Options{Root: root, Poll: 20 * time.Millisecond, JSON: true,
			Filter: Filter{Item: "github.com/eyelock/ynr#12"}, Ready: ready}, &out)
	}()
	<-ready
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(eventLine + "\n" + spanLine + "\n")
	_ = f.Close()
	if err := os.WriteFile(filepath.Join(root, "local", "ynm-1-000001"+spool.OpenSuffix), []byte(eventLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(out.String(), "\n") < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var recs []Record
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r Record
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("%v: %s", err, l)
		}
		recs = append(recs, r)
	}
	// The run's event and span, both stamped with the run's lane and item, and the local event,
	// in whatever order the polls found them.
	got := map[string]Record{}
	for _, r := range recs {
		got[r.Writer+" "+r.Kind] = r
	}
	if len(recs) != 3 || got["runs/run-1 event"].Lane != "github.com/eyelock/ynr#lint" ||
		got["runs/run-1 span"].Item != "github.com/eyelock/ynr#12" || got["local event"].Lane != "" {
		t.Fatalf("stream = %+v", recs)
	}
}
