//go:build full

package query

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"

	"github.com/eyelock/ynr/internal/store"
)

func metricLine(t *testing.T, name string, temporality pmetric.AggregationTemporality, value float64, at time.Time) []byte {
	t.Helper()
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "ynf")
	m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	m.SetName(name)
	sum := m.SetEmptySum()
	sum.SetAggregationTemporality(temporality)
	dp := sum.DataPoints().AppendEmpty()
	dp.SetDoubleValue(value)
	dp.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	dp.SetTimestamp(pcommon.NewTimestampFromTime(at))
	dp.Attributes().PutStr("gen_ai.response.model", "claude-opus-5-5")
	b, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(md)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAYearOfCostReadsOnlyTheRollups: closed days and months are rolled up as their hours are
// compacted, and a year of cost by model is answered from the rollups alone, even once every
// part and batch is gone.
func TestAYearOfCostReadsOnlyTheRollups(t *testing.T) {
	r := openStore(t)
	ctx := context.Background()
	days := []time.Time{
		time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 20, 23, 50, 0, 0, time.UTC),
		time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
	}
	for i, d := range days {
		id := strings.Repeat(string(rune('1'+i)), 16)
		ship(t, r, store.Traces, d.Add(5*time.Minute), "local.a",
			traceLine(t, runSpan(traceA, id, "github.com/eyelock/ynr#lint", "converged", "claude-opus-5-5", 1.5, d)))
		ship(t, r, store.Metrics, d.Add(5*time.Minute), "local.a",
			metricLine(t, "ynf.run.cost", pmetric.AggregationTemporalityDelta, 1.5, d),
			metricLine(t, "ynf.run.count", pmetric.AggregationTemporalityCumulative, float64(i+1), d))
	}
	c := Compaction{Grace: 10 * time.Minute, Keep: time.Hour, Lookback: 100 * 24 * time.Hour}
	if _, err := Compact(ctx, r, c, now); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		store.RollupKey(store.RollupRuns, store.Monthly, days[0]), store.RollupKey(store.RollupRuns, store.Monthly, days[1]),
		store.RollupKey(store.RollupRuns, store.Daily, days[3]), store.RollupKey(store.RollupMetrics, store.Monthly, days[1]),
	} {
		if ok, _ := exists(ctx, r, k); !ok {
			t.Errorf("no %s", k)
		}
	}
	if ok, _ := exists(ctx, r, store.RollupKey(store.RollupRuns, store.Monthly, days[3])); ok {
		t.Error("rolled up October before it closed")
	}

	// Remove every part, manifest and batch: the rollups alone must answer.
	for _, prefix := range []string{"compacted/", "traces/", "metrics/", "logs/"} {
		for _, k := range keys(t, r, prefix) {
			if err := r.Delete(ctx, k); err != nil {
				t.Fatal(err)
			}
		}
	}
	res := run(t, r, "cost", Params{Since: now.Add(-365 * 24 * time.Hour), Until: now})
	if !res.FromRollups || res.Files != 6 { // August and September by month, October 1 by day, for each kind
		t.Fatalf("from rollups %v, %d files", res.FromRollups, res.Files)
	}
	if got := rows(res); len(got) != 1 || got[0] != "claude-opus-5-5 4 6 4000 800" {
		t.Fatalf("cost = %v", got)
	}

	// A metric's monthly total sums a delta metric's days and takes a cumulative metric's last.
	q := &Query{Name: "metrics", Since: 365 * 24 * time.Hour, Signals: []string{store.Metrics},
		RollupSQL: `SELECT name, sum(total) FROM metric_rollups WHERE day = DATE '2026-09-01' GROUP BY name ORDER BY name`}
	mres, err := runRollups(ctx, r, q, Params{Since: now.Add(-365 * 24 * time.Hour), Until: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := rows(mres); len(got) != 2 || got[0] != "ynf.run.cost 3" || got[1] != "ynf.run.count 3" {
		t.Fatalf("September's metrics = %v", got)
	}
}
