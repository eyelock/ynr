//go:build full

package query

import (
	"fmt"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// TestReceivedAtAndInstanceColumns: collector_instance and received_at, which ynr stamps when it
// receives a record (ADR-005), read the same from a batch and from the compacted part.
func TestReceivedAtAndInstanceColumns(t *testing.T) {
	r := openStore(t)
	hour := now.Truncate(time.Hour).Add(-3 * time.Hour)
	s := runSpan(traceA, "1111111111111111", "", "converged", "m", 1, hour.Add(5*time.Minute))
	s.res["ynr.collector.id"] = "gha"
	s.res["ynr.collector.instance"] = "job-9"
	s.res["ynr.received_at"] = "2026-10-08T10:30:31.250Z"
	ship(t, r, store.Traces, hour.Add(10*time.Minute), "local.a", traceLine(t, s))

	q := &Query{Name: "cols", Signals: []string{store.Traces}, Since: 24 * time.Hour,
		SQL: "SELECT collector_instance, CAST(received_at AS VARCHAR) FROM spans"}
	check := func(stage string) {
		t.Helper()
		res := run2(t, r, q)
		if got := rows(res); len(got) != 1 || got[0] != "job-9 2026-10-08 10:30:31.25" {
			t.Fatalf("%s: rows = %q", stage, got)
		}
	}
	check("batch")
	compact(t, r, hour.Add(time.Hour+11*time.Minute))
	check("part")
}

func run2(t *testing.T, r store.Reader, q *Query) *Result {
	t.Helper()
	p := Params{Until: now.Add(time.Minute)}
	p.Since = p.Until.Add(-q.Since)
	res, err := Run(t.Context(), r, q, p)
	if err != nil {
		t.Fatal(fmt.Errorf("run: %w", err))
	}
	return res
}
