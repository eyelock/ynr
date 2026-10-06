package query

import (
	"testing"
	"time"
)

func TestHoursCoverTheWindowAndSkew(t *testing.T) {
	since := time.Date(2026, 10, 5, 10, 30, 0, 0, time.UTC)
	hs := Hours(since, since.Add(90*time.Minute))
	if len(hs) != 5 || !hs[0].Equal(time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)) || !hs[4].Equal(time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("hours = %v", hs)
	}
	if got := HourPrefix("traces", hs[0]); got != "traces/2026/10/05/09/" {
		t.Fatalf("prefix = %s", got)
	}
}

func TestEveryQueryIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, q := range Catalogue {
		if q.Name == "" || q.Help == "" || q.SQL == "" || q.Since <= 0 || len(q.Signals) == 0 || seen[q.Name] {
			t.Errorf("query %+v", q)
		}
		seen[q.Name] = true
	}
	q, _ := Lookup("item")
	now := time.Now()
	if q.Check(Params{Since: now.Add(-time.Hour), Until: now}) == nil {
		t.Error("item ran without its key")
	}
	if q.Check(Params{Arg: "k", Since: now, Until: now}) == nil {
		t.Error("an empty window was accepted")
	}
}
