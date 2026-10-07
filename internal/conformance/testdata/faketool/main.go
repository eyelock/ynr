// Command fake is a tiny tool for ynr conformance's own tests. By default it meets the
// instrumentation contract (ADR-006); FAKE_BREAK, a comma-separated list, breaks one rule at a
// time so each check can be shown to fail.
//
//	fake run --task <text>
//	fake telemetry registry --format json
package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/eyelock/ynr/spoolexporter"
)

var breaks = map[string]bool{}

func main() {
	for _, b := range strings.Split(os.Getenv("FAKE_BREAK"), ",") {
		breaks[strings.TrimSpace(b)] = true
	}
	args := os.Args[1:]
	switch {
	case len(args) >= 2 && args[0] == "telemetry" && args[1] == "registry":
		registry()
	case len(args) >= 1 && args[0] == "run":
		os.Exit(run(args[1:]))
	default:
		fmt.Fprintln(os.Stderr, "usage: fake run --task <text> | fake telemetry registry --format json")
		os.Exit(2)
	}
}

func registry() {
	spans := `{"name":"fake.run"},{"name":"fake.git"}`
	if breaks["registrydrift"] {
		spans = `{"name":"fake.run"}`
	}
	fmt.Printf(`{"tool":"fake","version":"0.0.0","attributes":[{"id":"fake.run.outcome"},{"id":"fake.task_len"},{"id":"fake.outcome"}],`+
		`"standard_attributes":[],"spans":[%s],"events":[{"name":"fake.run.started"}],"metrics":[{"name":"fake.run.count"}]}`+"\n", spans)
}

// sink is where telemetry goes, chosen as ADR-004 says.
type sink struct {
	endpoint string
	writer   *spoolexporter.Writer
	extra    *spoolexporter.Writer // a second, wrong, destination when a rule is broken
	client   *http.Client
	wg       sync.WaitGroup
}

func newSink() *sink {
	s := &sink{client: &http.Client{Timeout: time.Second}}
	if breaks["hang"] {
		s.client = &http.Client{}
	}
	open := func(dir string) *spoolexporter.Writer {
		return spoolexporter.NewWriter(spoolexporter.Options{Dir: dir, Service: "fake", InstanceID: fmt.Sprint(os.Getpid())})
	}
	spoolDir := os.Getenv("YNR_SPOOL")
	if spoolDir == "" {
		if state := os.Getenv("XDG_STATE_HOME"); state != "" {
			d := filepath.Join(state, "ynr", "spool", "local")
			if _, err := os.Stat(d); err == nil {
				spoolDir = d
			}
		}
	}
	switch {
	case os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "":
		s.endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		if breaks["bothtargets"] && spoolDir != "" {
			s.extra = open(spoolDir)
		}
	case spoolDir != "":
		s.writer = open(spoolDir)
	case breaks["neither"]:
		s.writer = open("telemetry-out")
	}
	return s
}

// send writes one export request. Exports to an endpoint run together and are waited for at
// exit for a bounded time; with FAKE_BREAK=hang they are synchronous and unbounded.
func (s *sink) send(signal string, body []byte, records int) {
	if s.writer != nil {
		s.writer.WriteRequest(body, records)
	}
	if s.extra != nil {
		s.extra.WriteRequest(body, records)
	}
	if s.endpoint == "" {
		return
	}
	post := func() {
		resp, err := s.client.Post(strings.TrimRight(s.endpoint, "/")+"/v1/"+signal, "application/json", bytes.NewReader(body))
		if err == nil {
			_ = resp.Body.Close()
		}
	}
	if breaks["hang"] {
		post()
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		post()
	}()
}

// flush waits, for at most a second, for exports in flight, and syncs the spool.
func (s *sink) flush() {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
	for _, w := range []*spoolexporter.Writer{s.writer, s.extra} {
		if w != nil {
			w.Sync()
			w.Close()
		}
	}
}

func (s *sink) failed() bool {
	return s.writer != nil && s.writer.Stats().Errors > 0
}

func resource(r pcommon.Resource) {
	a := r.Attributes()
	if !breaks["noservice"] {
		a.PutStr("service.name", "fake")
	}
	a.PutStr("service.version", "0.0.0")
	if !breaks["noinstance"] {
		a.PutStr("service.instance.id", fmt.Sprint(os.Getpid()))
	}
	if !breaks["noresattrs"] {
		for _, kv := range strings.Split(os.Getenv("OTEL_RESOURCE_ATTRIBUTES"), ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				a.PutStr(k, v)
			}
		}
	}
}

func run(args []string) int {
	task := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--task" {
			task = args[i+1]
		}
	}
	s := newSink()

	var tid pcommon.TraceID
	var parent pcommon.SpanID
	if tp := strings.Split(os.Getenv("TRACEPARENT"), "-"); len(tp) == 4 && !breaks["ignoretrace"] {
		tb, _ := hex.DecodeString(tp[1])
		pb, _ := hex.DecodeString(tp[2])
		copy(tid[:], tb)
		copy(parent[:], pb)
	} else {
		tid = pcommon.TraceID{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, byte(os.Getpid())}
	}
	spanID := pcommon.SpanID{1, 2, 3, 4, 5, 6, 7, byte(os.Getpid())}
	gitID := pcommon.SpanID{8, 7, 6, 5, 4, 3, 2, byte(os.Getpid())}
	outcome := os.Getenv("FAKE_OUTCOME")
	if outcome == "" {
		outcome = "converged"
	}
	begin := time.Now()

	started := func() {
		ld := plog.NewLogs()
		rl := ld.ResourceLogs().AppendEmpty()
		resource(rl.Resource())
		l := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		l.SetEventName("fake.run.started")
		l.SetTimestamp(pcommon.NewTimestampFromTime(begin))
		l.SetTraceID(tid)
		l.SetSpanID(spanID)
		b, _ := (&plog.JSONMarshaler{}).MarshalLogs(ld)
		s.send("logs", b, 1)
		if s.writer != nil {
			s.writer.Sync()
		}
	}
	if !breaks["nostarted"] && !breaks["latestarted"] {
		started()
	}

	// A turn of work: the stub vendor, if one is on the path, in this trace.
	if d := os.Getenv("YNR_STUB_TURN_DELAY"); d != "" {
		if dur, err := time.ParseDuration(d); err == nil {
			time.Sleep(dur)
		}
	}
	if path, err := exec.LookPath("ynr-stub-vendor"); err == nil {
		cmd := exec.Command(path, "--turns", "1")
		cmd.Env = append(os.Environ(), "TRACEPARENT=00-"+tid.String()+"-"+spanID.String()+"-01")
		_ = cmd.Run()
	}
	if breaks["ynrstart"] {
		_ = exec.Command("ynr", "serve").Run()
	}
	if breaks["latestarted"] {
		started()
	}

	end := time.Now()
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	resource(rs.Resource())
	spans := rs.ScopeSpans().AppendEmpty().Spans()
	git := spans.AppendEmpty()
	git.SetName("fake.git")
	git.SetKind(ptrace.SpanKindClient)
	git.SetTraceID(tid)
	git.SetSpanID(gitID)
	git.SetParentSpanID(spanID)
	git.SetStartTimestamp(pcommon.NewTimestampFromTime(begin))
	git.SetEndTimestamp(pcommon.NewTimestampFromTime(end))
	{
		run := spans.AppendEmpty()
		run.SetName("fake.run")
		run.SetKind(ptrace.SpanKindInternal)
		run.SetTraceID(tid)
		run.SetSpanID(spanID)
		run.SetParentSpanID(parent)
		run.SetStartTimestamp(pcommon.NewTimestampFromTime(begin))
		run.SetEndTimestamp(pcommon.NewTimestampFromTime(end))
		if !breaks["nooutcome"] {
			run.Attributes().PutStr("fake.run.outcome", outcome)
		}
		run.Attributes().PutInt("fake.task_len", int64(len(task)))
		if breaks["canary"] {
			run.Attributes().PutStr("fake.task_len", task)
		}
		if breaks["unregistered"] {
			run.Attributes().PutStr("fake.mystery", "x")
		}
		if !breaks["nostatus"] {
			if outcome == "converged" {
				run.Status().SetCode(ptrace.StatusCodeOk)
			} else {
				run.Status().SetCode(ptrace.StatusCodeError)
			}
		}
	}
	b, _ := (&ptrace.JSONMarshaler{}).MarshalTraces(td)
	s.send("traces", b, spans.Len())

	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	resource(rm.Resource())
	m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	m.SetName("fake.run.count")
	sum := m.SetEmptySum()
	dp := sum.DataPoints().AppendEmpty()
	dp.SetTimestamp(pcommon.NewTimestampFromTime(end))
	dp.SetIntValue(1)
	dp.Attributes().PutStr("fake.outcome", outcome)
	if breaks["highcard"] {
		dp.Attributes().PutStr("fake.run.id", "3f2b8c1e-9a4d-4e7f-8b21-5c6d7e8f9a0b")
	}
	mb, _ := (&pmetric.JSONMarshaler{}).MarshalMetrics(md)
	s.send("metrics", mb, 1)

	if breaks["canary"] {
		// Also in a log body, as a careless bridge would.
		ld := plog.NewLogs()
		rl := ld.ResourceLogs().AppendEmpty()
		resource(rl.Resource())
		l := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		l.SetSeverityText("INFO")
		l.Body().SetStr("task: " + task)
		lb, _ := (&plog.JSONMarshaler{}).MarshalLogs(ld)
		s.send("logs", lb, 1)
	}
	s.flush()

	code := 0
	if v := os.Getenv("FAKE_EXIT"); v != "" {
		fmt.Sscan(v, &code)
	}
	if breaks["badexit"] && s.failed() {
		return 1
	}
	return code
}
