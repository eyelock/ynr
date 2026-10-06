package query

import (
	"encoding/json"
	"strings"
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

func TestParseWhen(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"7d":                   at.Add(-7 * 24 * time.Hour),
		"90m":                  at.Add(-90 * time.Minute),
		"2026-10-01T00:00:00Z": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	} {
		if got, err := ParseWhen(in, at); err != nil || !got.Equal(want) {
			t.Errorf("%s = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"-1h", "soon", "d"} {
		if _, err := ParseWhen(bad, at); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// TestJSONRoundTrips: what the server encodes, ynr query decodes in the same column order.
func TestJSONRoundTrips(t *testing.T) {
	q, _ := Lookup("runs")
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	res := &Result{Columns: []string{"lane", "runs", "last"}, Rows: [][]any{{"x", int64(2), at}, {nil, int64(1), at}}}
	var b strings.Builder
	if err := EncodeJSON(&b, q, Params{Since: at.Add(-time.Hour), Until: at}, res); err != nil {
		t.Fatal(err)
	}
	doc, back, err := DecodeJSON(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Query != "runs" || !doc.Until.Equal(at) || strings.Join(back.Columns, ",") != "lane,runs,last" ||
		back.Rows[0][0] != "x" || back.Rows[0][1].(json.Number) != "2" || back.Rows[1][0] != nil {
		t.Fatalf("round trip = %+v %+v", doc, back)
	}
}
