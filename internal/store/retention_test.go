package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRetainByAgeAndSize(t *testing.T) {
	r, err := OpenReader(FolderURL(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 30, 0, 0, time.UTC)
	put := func(key string, n int) {
		t.Helper()
		if err := r.Put(ctx, key, []byte(strings.Repeat("x", n))); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-8 * 24 * time.Hour).Truncate(time.Hour)
	a, b := now.Add(-3*time.Hour).Truncate(time.Hour), now.Add(-2*time.Hour).Truncate(time.Hour)
	put(BatchKey(Traces, old, "laptop", NewULID(old), "local.x", 0, 1), 10)
	put(PartKey(Traces, old, 1), 10)
	put(IndexKey(old.Truncate(24*time.Hour)), 10)
	put(RollupKey(RollupRuns, Daily, old), 10)                           // rollups outlive records
	put(RollupKey(RollupRuns, Monthly, now.AddDate(-14, 0, 0)), 10)      // but not 13 months
	put(BatchKey(Traces, a, "laptop", NewULID(a), "local.x", 0, 1), 100) // the oldest hour kept by age
	put(PartKey(Logs, a, 1), 100)
	put(ManifestKey(Logs, a, 1), 1)
	put(BatchKey(Metrics, b, "laptop", NewULID(b), "local.x", 0, 1), 100)
	put("registries/laptop/ynh/1.0.0/abc.json", 10) // never retention's

	got, err := Retain(ctx, r, Retention{Records: 7 * 24 * time.Hour, Rollups: 396 * 24 * time.Hour, MaxBytes: 150}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.ByAge != 4 || got.BySize != 3 {
		t.Fatalf("retained = %+v, want 4 by age (old batch, part, index; 14-month rollup) and hour a's 3 by size", got)
	}
	var left []string
	for _, p := range []string{"traces/", "logs/", "metrics/", "compacted/", "index/", "rollups/", "registries/"} {
		ks, _ := r.List(ctx, p)
		left = append(left, ks...)
	}
	want := []string{"metrics/", "rollups/runs/daily/", "registries/"}
	if len(left) != 3 {
		t.Fatalf("left %v", left)
	}
	for i, w := range want {
		if !strings.HasPrefix(left[i], w) {
			t.Fatalf("left %v", left)
		}
	}
}

func TestSpanOnlyKnowsReadersKeys(t *testing.T) {
	at := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	for key, want := range map[string]time.Duration{
		RollupKey(RollupMetrics, Monthly, at): 31 * 24 * time.Hour,
		RollupKey(RollupMetrics, Daily, at):   24 * time.Hour,
		IndexKey(at):                          24 * time.Hour,
		ManifestKey(Traces, at, 2):            time.Hour,
	} {
		from, to, _, ok := Span(key)
		if !ok || to.Sub(from) != want {
			t.Errorf("%s = %v..%v %v", key, from, to, ok)
		}
	}
	for _, key := range []string{"registries/x/y/z.json", "index/items/2026/13/01.parquet", "rollups/other/daily/2026-10-06.parquet"} {
		if _, _, _, ok := Span(key); ok {
			t.Errorf("%s has a span", key)
		}
	}
}
