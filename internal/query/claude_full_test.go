//go:build full

package query

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"

	"github.com/eyelock/ynr/internal/store"
)

// usageLine is one Claude Code metric point, from the seat whose resource is res.
func usageLine(t *testing.T, name string, temporality pmetric.AggregationTemporality, value float64, start, at time.Time, res, dp map[string]any) []byte {
	t.Helper()
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	put(rm.Resource().Attributes(), res)
	m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	m.SetName(name)
	sum := m.SetEmptySum()
	sum.SetAggregationTemporality(temporality)
	p := sum.DataPoints().AppendEmpty()
	p.SetDoubleValue(value)
	p.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
	p.SetTimestamp(pcommon.NewTimestampFromTime(at))
	put(p.Attributes(), dp)
	b, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(md)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sessionOf is the ynh.session.id of the launch a seat runs in. Claude Code's records carry that
// id and never the harness name: the name is only on the ynh.session span (ynw-17xrm).
func sessionOf(seat string) string {
	return map[string]string{"ryder": "sess-r", "ynw": "sess-d1"}[seat]
}

// sessionSpan is the ynh.session span ynh run writes for a launch, naming the seat.
func sessionSpan(t *testing.T, at time.Time, id, seat string) []byte {
	// one span id per session: records with the same trace and span id are one record
	sum := sha256.Sum256([]byte(id))
	return traceLine(t, span{trace: hex.EncodeToString(sum[:16]), id: hex.EncodeToString(sum[16:24]), name: "ynh.session",
		start: at, dur: time.Hour, res: map[string]any{"service.name": "ynh"},
		attrs: map[string]any{"ynh.session.id": id, "ynh.harness.name": seat}})
}

func apiRequest(t *testing.T, at time.Time, seat, agent string, in, out int, cost float64) []byte {
	attrs := map[string]any{"input_tokens": in, "output_tokens": out, "cache_read_tokens": 10, "cache_creation_tokens": 5,
		"cost_usd": cost, "duration_ms": 2000}
	if agent != "" {
		attrs["agent.name"] = agent
	}
	return eventLine(t, "api_request", at, map[string]any{"ynh.session.id": sessionOf(seat)}, attrs)
}

// claudeFixture is a launched seat (ryder, delta metrics), the desk (ynw, a cumulative cost
// series that spans two days) and two named desk subagents, hannett and reni.
func claudeFixture(t *testing.T) store.Reader {
	r := openStore(t)
	d1 := now.Add(-24 * time.Hour)
	d2 := now.Add(-time.Hour)
	delta, cum := pmetric.AggregationTemporalityDelta, pmetric.AggregationTemporalityCumulative
	ryder := map[string]any{"ynh.session.id": "sess-r", "user.account_uuid": "acct-a"}
	desk := map[string]any{"ynh.session.id": "sess-d1", "user.account_uuid": "acct-a"}
	tok := func(kind string) map[string]any { return map[string]any{"type": kind, "model": "claude-opus-5-5"} }
	// Each launch's ynh.session span is the only place its seat is named.
	ship(t, r, store.Traces, d1.Add(time.Minute), "local.a",
		sessionSpan(t, d1, "sess-r", "ryder"), sessionSpan(t, d1, "sess-d1", "ynw"), sessionSpan(t, d2, "sess-d2", "ynw"))
	ship(t, r, store.Metrics, d1.Add(time.Minute), "local.a",
		usageLine(t, "claude_code.token.usage", delta, 1000, d1, d1, ryder, tok("input")),
		usageLine(t, "claude_code.token.usage", delta, 200, d1, d1, ryder, tok("output")),
		usageLine(t, "claude_code.cost.usage", delta, 1.5, d1, d1, ryder, map[string]any{"model": "claude-opus-5-5"}),
		usageLine(t, "claude_code.active_time.total", delta, 60, d1, d1, ryder, map[string]any{"type": "cli"}),
		usageLine(t, "claude_code.cost.usage", cum, 2, d1, d1, desk, map[string]any{"session.id": "s-1"}))
	dup := usageLine(t, "claude_code.cost.usage", delta, 0.5, d2, d2, ryder, map[string]any{"model": "claude-opus-5-5"})
	ship(t, r, store.Metrics, d2.Add(time.Minute), "local.a",
		usageLine(t, "claude_code.token.usage", delta, 500, d2, d2, ryder, tok("input")),
		usageLine(t, "claude_code.token.usage", delta, 50, d2, d2, ryder, tok("cacheRead")),
		dup,
		usageLine(t, "claude_code.cost.usage", cum, 5, d1, d2, desk, map[string]any{"session.id": "s-1"}),
		usageLine(t, "claude_code.cost.usage", cum, 1, d2, d2, map[string]any{"ynh.session.id": "sess-d2", "user.account_uuid": "acct-b"}, map[string]any{"session.id": "s-2"}))
	// the same batch shipped twice counts once
	ship(t, r, store.Metrics, d2.Add(2*time.Minute), "local.a", dup)
	ship(t, r, store.Logs, d2.Add(time.Minute), "local.a",
		apiRequest(t, d2, "ynw", "hannett", 300, 30, 0.25),
		apiRequest(t, d2.Add(time.Second), "ynw", "hannett", 100, 10, 0.25),
		apiRequest(t, d2.Add(2*time.Second), "ynw", "reni", 50, 5, 0.1),
		apiRequest(t, d2.Add(3*time.Second), "ynw", "", 20, 2, 0.05),
		apiRequest(t, d2.Add(4*time.Second), "ryder", "", 70, 7, 0.2),
		eventLine(t, "tool_result", d2, map[string]any{"ynh.session.id": "sess-d1"}, map[string]any{"agent.name": "reni"}))
	return r
}

func TestSeatsByDay(t *testing.T) {
	r := claudeFixture(t)
	got := rows(run(t, r, "seats", Params{}))
	// day seat input output cache_read cache_creation cost active_s, the dearest seat first
	want := []string{
		"2026-10-05 ynw <nil> <nil> <nil> <nil> 4 <nil>",
		"2026-10-05 ryder 500 <nil> 50 <nil> 0.5 <nil>",
		"2026-10-04 ynw <nil> <nil> <nil> <nil> 2 <nil>",
		"2026-10-04 ryder 1000 200 <nil> <nil> 1.5 60",
	}
	// ynw's cumulative series (s-1) is 2 on the 4th and 5 on the 5th: growth 3, plus s-2's 1.
	if len(got) != len(want) {
		t.Fatalf("seats = %v\nwant %v", got, want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], strings.Fields(want[i])[0]) || !strings.Contains(got[i], strings.Fields(want[i])[1]) {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
		if g, w := strings.Fields(got[i])[2:], strings.Fields(want[i])[2:]; fmt.Sprint(g) != fmt.Sprint(w) {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSeatsFilterByLabelAndRefuseNothing(t *testing.T) {
	r := claudeFixture(t)
	if got := rows(run(t, r, "seats", Params{Labels: []string{"wheelhouse:other"}})); len(got) != 0 {
		t.Fatalf("a label nothing carries kept %v", got)
	}
	// Long windows read records too: no rollups, so no refusal for --label.
	long := Params{Since: now.Add(-30 * 24 * time.Hour), Labels: []string{"wheelhouse:ynw"}}
	q, _ := Lookup("seats")
	long.Until = now.Add(time.Minute)
	if err := q.Check(long); err != nil {
		t.Fatalf("Check = %v", err)
	}
}

func TestAccountsByDay(t *testing.T) {
	r := claudeFixture(t)
	res := run(t, r, "accounts", Params{})
	got := map[string]string{}
	day, acct, cost := col(t, res, "day"), col(t, res, "account"), col(t, res, "cost_usd")
	for _, row := range res.Rows {
		got[fmt.Sprint(row[day], " ", row[acct])] = fmt.Sprint(row[cost])
	}
	want := map[string]string{
		"2026-10-04 acct-a": "3.5", // ryder 1.5 and the desk's 2
		"2026-10-05 acct-a": "3.5", // ryder 0.5 and the desk's growth of 3
		"2026-10-05 acct-b": "1",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("accounts = %v\nwant %v", got, want)
	}
}

func TestSubagentsNamedAndMain(t *testing.T) {
	r := claudeFixture(t)
	res := run(t, r, "subagents", Params{})
	seat, agent := col(t, res, "seat"), col(t, res, "agent")
	req, in, out, cost, secs := col(t, res, "requests"), col(t, res, "input_tokens"), col(t, res, "output_tokens"), col(t, res, "cost_usd"), col(t, res, "request_s")
	got := map[string]string{}
	for _, row := range res.Rows {
		got[fmt.Sprint(row[seat], "/", row[agent])] = fmt.Sprint(row[req], " ", row[in], " ", row[out], " ", row[cost], " ", row[secs])
	}
	want := map[string]string{
		"ynw/hannett":  "2 400 40 0.5 4",
		"ynw/reni":     "1 50 5 0.1 2",
		"ynw/(main)":   "1 20 2 0.05 2",
		"ryder/(main)": "1 70 7 0.2 2",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("subagents = %v\nwant %v", got, want)
	}
}

// A record whose session has no span in the window is (unknown), and one that carries no
// session id at all is too. Neither is dropped.
func TestSeatsWithoutASessionSpanAreUnknown(t *testing.T) {
	r := openStore(t)
	d := now.Add(-time.Hour)
	delta := pmetric.AggregationTemporalityDelta
	ship(t, r, store.Traces, d, "local.a", sessionSpan(t, d, "sess-known", "ryder"))
	ship(t, r, store.Metrics, d.Add(time.Minute), "local.a",
		usageLine(t, "claude_code.cost.usage", delta, 1, d, d, map[string]any{"ynh.session.id": "sess-known"}, nil),
		usageLine(t, "claude_code.cost.usage", delta, 2, d, d, map[string]any{"ynh.session.id": "sess-gone"}, nil),
		usageLine(t, "claude_code.cost.usage", delta, 4, d, d, map[string]any{"user.account_uuid": "a"}, nil))
	ship(t, r, store.Logs, d.Add(time.Minute), "local.a",
		eventLine(t, "api_request", d, map[string]any{"ynh.session.id": "sess-known"}, map[string]any{"cost_usd": 0.5}),
		eventLine(t, "api_request", d, map[string]any{"ynh.session.id": "sess-gone"}, map[string]any{"cost_usd": 0.25}))
	res := run(t, r, "seats", Params{})
	seat, cost := col(t, res, "seat"), col(t, res, "cost_usd")
	got := map[string]string{}
	for _, row := range res.Rows {
		got[fmt.Sprint(row[seat])] = fmt.Sprint(row[cost])
	}
	if want := map[string]string{"ryder": "1", "(unknown)": "6"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("seats = %v\nwant %v", got, want)
	}
	res = run(t, r, "subagents", Params{})
	seat, cost = col(t, res, "seat"), col(t, res, "cost_usd")
	got = map[string]string{}
	for _, row := range res.Rows {
		got[fmt.Sprint(row[seat])] = fmt.Sprint(row[cost])
	}
	if want := map[string]string{"ryder": "0.5", "(unknown)": "0.25"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("subagents = %v\nwant %v", got, want)
	}
}

// A running seat has no ynh.session span yet, only the ynh.session.started event ynh flushed at
// launch (ynw-csly6): that names it.
func TestSeatsNamedByStartedEventWhileRunning(t *testing.T) {
	r := openStore(t)
	d := now.Add(-time.Hour)
	delta := pmetric.AggregationTemporalityDelta
	ship(t, r, store.Logs, d, "local.a",
		eventLine(t, "ynh.session.started", d, map[string]any{"service.name": "ynh"},
			map[string]any{"ynh.session.id": "sess-live", "ynh.harness.name": "reni"}),
		eventLine(t, "api_request", d, map[string]any{"ynh.session.id": "sess-live"}, map[string]any{"cost_usd": 0.5}))
	ship(t, r, store.Metrics, d.Add(time.Minute), "local.a",
		usageLine(t, "claude_code.cost.usage", delta, 3, d, d, map[string]any{"ynh.session.id": "sess-live"}, nil))
	for _, name := range []string{"seats", "subagents"} {
		res := run(t, r, name, Params{})
		if len(res.Rows) != 1 || fmt.Sprint(res.Rows[0][col(t, res, "seat")]) != "reni" {
			t.Errorf("%s = %v, want one row for reni", name, rows(res))
		}
	}
}

func TestClaudeQueriesOnAnEmptyStore(t *testing.T) {
	r := openStore(t)
	for _, name := range []string{"seats", "accounts", "subagents"} {
		if res := run(t, r, name, Params{}); len(res.Rows) != 0 {
			t.Errorf("%s on an empty store = %v", name, rows(res))
		}
	}
}
