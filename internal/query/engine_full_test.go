//go:build full

package query

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/eyelock/ynr/internal/store"
)

var now = time.Date(2026, 10, 5, 21, 30, 0, 0, time.UTC)

type span struct {
	trace, id, parent string
	name              string
	start             time.Time
	dur               time.Duration
	res, attrs        map[string]any
}

func put(m pcommon.Map, attrs map[string]any) {
	for k, v := range attrs {
		switch x := v.(type) {
		case string:
			m.PutStr(k, x)
		case int:
			m.PutInt(k, int64(x))
		case float64:
			m.PutDouble(k, x)
		}
	}
}

func traceLine(t *testing.T, spans ...span) []byte {
	t.Helper()
	td := ptrace.NewTraces()
	for _, s := range spans {
		rs := td.ResourceSpans().AppendEmpty()
		put(rs.Resource().Attributes(), s.res)
		sp := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		var tid pcommon.TraceID
		var sid, pid pcommon.SpanID
		mustHex(t, s.trace, tid[:])
		mustHex(t, s.id, sid[:])
		if s.parent != "" {
			mustHex(t, s.parent, pid[:])
			sp.SetParentSpanID(pid)
		}
		sp.SetTraceID(tid)
		sp.SetSpanID(sid)
		sp.SetName(s.name)
		sp.SetKind(ptrace.SpanKindInternal)
		sp.SetStartTimestamp(pcommon.NewTimestampFromTime(s.start))
		sp.SetEndTimestamp(pcommon.NewTimestampFromTime(s.start.Add(s.dur)))
		put(sp.Attributes(), s.attrs)
	}
	b, err := (&ptrace.JSONMarshaler{}).MarshalTraces(td)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustHex(t *testing.T, s string, dst []byte) {
	t.Helper()
	if _, err := hex.Decode(dst, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

func eventLine(t *testing.T, name string, at time.Time, res, attrs map[string]any) []byte {
	t.Helper()
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	put(rl.Resource().Attributes(), res)
	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName(name)
	lr.SetTimestamp(pcommon.NewTimestampFromTime(at))
	put(lr.Attributes(), attrs)
	b, err := (&plog.JSONMarshaler{}).MarshalLogs(ld)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ship puts lines into the store as one batch, the way ynr serve does.
func ship(t *testing.T, s store.Store, signal string, received time.Time, source string, lines ...[]byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, l := range lines {
		_, _ = zw.Write(append(l, '\n'))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	key := store.BatchKey(signal, received, "laptop", store.NewULID(received), source, 0, int64(buf.Len()))
	if err := s.Put(context.Background(), key, buf.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, r store.Reader, name string, p Params) *Result {
	t.Helper()
	q, ok := Lookup(name)
	if !ok {
		t.Fatalf("no query %s", name)
	}
	if p.Until.IsZero() {
		p.Until = now.Add(time.Minute)
	}
	if p.Since.IsZero() {
		p.Since = now.Add(-q.Since)
	}
	res, err := Run(context.Background(), r, q, p)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func rows(res *Result) []string {
	var out []string
	for _, row := range res.Rows {
		out = append(out, strings.TrimSuffix(fmt.Sprintln(row...), "\n"))
	}
	return out
}

func col(t *testing.T, res *Result, name string) int {
	t.Helper()
	for i, c := range res.Columns {
		if c == name {
			return i
		}
	}
	t.Fatalf("no column %s in %v", name, res.Columns)
	return -1
}

func openStore(t *testing.T) store.Reader {
	t.Helper()
	r, err := store.OpenReader(store.FolderURL(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const (
	traceA = "5b8efff798038103d269b633813fc60c"
	traceB = "0af7651916cd43dd8448eb211c80319c"
)

func runSpan(trace, id, lane, outcome, model string, cost float64, at time.Time) span {
	res := map[string]any{"service.name": "ynh"}
	if lane != "" {
		res["ynf.lane"] = lane
	}
	return span{trace: trace, id: id, name: "ynh.run", start: at, dur: 90 * time.Second, res: res,
		attrs: map[string]any{"ynh.run.outcome": outcome, "gen_ai.response.model": model, "ynh.run.cost_usd": cost,
			"gen_ai.usage.input_tokens": 1000, "gen_ai.usage.output_tokens": 200}}
}

// TestRunsCountsEachRunOnce: a batch re-shipped after a crash holds the same spans under another
// name, and a run in it is still counted once.
func TestRunsCountsEachRunOnce(t *testing.T) {
	r := openStore(t)
	lane := "github.com/eyelock/ynr#lint"
	line := traceLine(t,
		runSpan(traceA, "1111111111111111", lane, "converged", "claude-opus-5-5", 1.25, now.Add(-time.Hour)),
		runSpan(traceB, "2222222222222222", lane, "budget", "claude-opus-5-5", 2.5, now.Add(-30*time.Minute)),
	)
	ship(t, r, store.Traces, now.Add(-20*time.Minute), "local.ynh-1-000001", line)
	ship(t, r, store.Traces, now.Add(-10*time.Minute), "local.ynh-1-000001", line) // re-shipped
	ship(t, r, store.Traces, now.Add(-5*time.Minute), "local.ynh-2-000001",
		traceLine(t, runSpan("0af7651916cd43dd8448eb211c80319d", "3333333333333333", "", "converged", "claude-sonnet-5-5", 0.5, now.Add(-5*time.Minute))))

	res := run(t, r, "runs", Params{})
	runs, outcome, laneCol := col(t, res, "runs"), col(t, res, "outcome"), col(t, res, "lane")
	got := map[string]any{}
	for _, row := range res.Rows {
		got[fmt.Sprint(row[laneCol], "/", row[outcome])] = row[runs]
	}
	want := map[string]any{"(by hand)/converged": int64(1), lane + "/converged": int64(1), lane + "/budget": int64(1)}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("runs = %v\nwant %v", got, want)
	}
	if res := run(t, r, "runs", Params{Lane: lane}); len(res.Rows) != 2 {
		t.Fatalf("--lane kept %v", rows(res))
	}
}

func TestCostByModel(t *testing.T) {
	r := openStore(t)
	ship(t, r, store.Traces, now.Add(-time.Hour), "local.a",
		traceLine(t,
			runSpan(traceA, "1111111111111111", "", "converged", "claude-opus-5-5", 1.25, now.Add(-2*time.Hour)),
			runSpan(traceB, "2222222222222222", "", "converged", "claude-opus-5-5", 2.5, now.Add(-2*time.Hour)),
			runSpan(traceB, "3333333333333333", "", "converged", "claude-haiku-4-5", 0.25, now.Add(-2*time.Hour)),
		))
	res := run(t, r, "cost", Params{})
	want := []string{"claude-opus-5-5 2 3.75 2000 400", "claude-haiku-4-5 1 0.25 1000 200"}
	if got := rows(res); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("cost = %v\nwant %v", got, want)
	}
}

// TestItemHistory: an item's steps and events across traces, in time order, and nothing of
// another item's.
func TestItemHistory(t *testing.T) {
	r := openStore(t)
	item := "github.com/eyelock/ynr#12"
	step := func(trace, id, key string, at time.Time) span {
		return span{trace: trace, id: id, name: "ynf.step", start: at, dur: time.Minute,
			res: map[string]any{"service.name": "ynf"}, attrs: map[string]any{"ynf.item.key": key, "ynf.outcome": "completed"}}
	}
	ship(t, r, store.Traces, now.Add(-time.Hour), "factory.ynf-1",
		traceLine(t, step(traceA, "1111111111111111", item, now.Add(-3*time.Hour)),
			step(traceB, "2222222222222222", item, now.Add(-2*time.Hour)),
			step("0af7651916cd43dd8448eb211c80319d", "3333333333333333", "github.com/eyelock/ynr#13", now.Add(-2*time.Hour))))
	ship(t, r, store.Logs, now.Add(-time.Hour), "factory.ynf-1",
		eventLine(t, "ynf.step.started", now.Add(-3*time.Hour).Add(-time.Second), map[string]any{"service.name": "ynf"}, map[string]any{"ynf.item.key": item}))
	res := run(t, r, "item", Params{Arg: item})
	name := col(t, res, "name")
	var names []any
	for _, row := range res.Rows {
		names = append(names, row[name])
	}
	if fmt.Sprint(names) != "[ynf.step.started ynf.step ynf.step]" {
		t.Fatalf("history = %v", rows(res))
	}
}

func TestTraceIsATree(t *testing.T) {
	r := openStore(t)
	at := now.Add(-time.Hour)
	ship(t, r, store.Traces, now.Add(-30*time.Minute), "factory.x",
		traceLine(t,
			span{trace: traceA, id: "2222222222222222", parent: "1111111111111111", name: "ynh.run", start: at.Add(2 * time.Second), dur: time.Minute},
			span{trace: traceA, id: "1111111111111111", name: "ynf.step", start: at, dur: 2 * time.Minute},
			span{trace: traceA, id: "3333333333333333", parent: "1111111111111111", name: "ynf.claim", start: at.Add(time.Second), dur: time.Second},
			span{trace: traceA, id: "4444444444444444", parent: "2222222222222222", name: "claude_code.interaction", start: at.Add(3 * time.Second), dur: time.Second},
			span{trace: traceB, id: "5555555555555555", name: "other", start: at, dur: time.Second},
		))
	res := run(t, r, "trace", Params{Arg: traceA})
	depth, name := col(t, res, "depth"), col(t, res, "name")
	var got []string
	for _, row := range res.Rows {
		got = append(got, fmt.Sprint(row[depth], ":", row[name]))
	}
	if fmt.Sprint(got) != "[0:ynf.step 1:ynf.claim 1:ynh.run 2:claude_code.interaction]" {
		t.Fatalf("tree = %v", got)
	}
}

// TestReadsOnlyTheWindowsHours: a batch received outside the hours the window can touch is
// never opened, so it cannot be counted.
func TestReadsOnlyTheWindowsHours(t *testing.T) {
	r := openStore(t)
	ship(t, r, store.Traces, now.Add(-72*time.Hour), "local.old",
		traceLine(t, runSpan(traceA, "1111111111111111", "", "converged", "m", 1, now.Add(-time.Hour))))
	if res := run(t, r, "runs", Params{}); len(res.Rows) != 0 {
		t.Fatalf("read a batch outside the window: %v", rows(res))
	}
}

func TestEmptyStoreHasNoRows(t *testing.T) {
	r := openStore(t)
	for _, name := range Names() {
		p := Params{}
		if q, _ := Lookup(name); q.Arg != "" {
			p.Arg = "x"
		}
		if res := run(t, r, name, p); len(res.Rows) != 0 {
			t.Fatalf("%s on an empty store = %v", name, rows(res))
		}
	}
}

// TestMetricPointsDedupe: the views flatten every metric type, and a point shipped twice is
// one point.
func TestMetricPointsDedupe(t *testing.T) {
	r := openStore(t)
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "ynf")
	ms := rm.ScopeMetrics().AppendEmpty().Metrics()
	sum := ms.AppendEmpty()
	sum.SetName("ynf.run.cost")
	s := sum.SetEmptySum()
	s.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	dp := s.DataPoints().AppendEmpty()
	dp.SetDoubleValue(1.5)
	dp.SetTimestamp(pcommon.NewTimestampFromTime(now.Add(-time.Hour)))
	dp.Attributes().PutStr("gen_ai.response.model", "claude-opus-5-5")
	h := ms.AppendEmpty()
	h.SetName("ynm.command.duration")
	hp := h.SetEmptyHistogram().DataPoints().AppendEmpty()
	hp.SetCount(3)
	hp.SetSum(4.5)
	hp.SetTimestamp(pcommon.NewTimestampFromTime(now.Add(-time.Hour)))
	line, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(md)
	if err != nil {
		t.Fatal(err)
	}
	ship(t, r, store.Metrics, now.Add(-time.Hour), "local.m", line)
	ship(t, r, store.Metrics, now.Add(-50*time.Minute), "local.m", line)
	q := &Query{Name: "points", Since: 24 * time.Hour, Signals: []string{store.Metrics},
		SQL: `SELECT name, type, temporality, value, count, model FROM metric_points ORDER BY name`}
	res, err := Run(context.Background(), r, q, Params{Since: now.Add(-24 * time.Hour), Until: now})
	if err != nil {
		t.Fatal(err)
	}
	want := "[ynf.run.cost sum delta 1.5 <nil> claude-opus-5-5 ynm.command.duration histogram <nil> 4.5 3 <nil>]"
	if got := fmt.Sprint(rows(res)); got != want {
		t.Fatalf("points = %s\nwant %s", got, want)
	}
}
