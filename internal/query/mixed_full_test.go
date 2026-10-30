//go:build full

package query

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pmetric"

	"github.com/eyelock/ynr/internal/store"
)

// Two days of records, a month apart in what wrote them: the old day's Parquet is rewritten to
// the layout v0.2.2 and earlier wrote (schema version 1: no version key, no collector_instance
// or received_at column), the new day's is what this build writes (version 2).
var (
	oldDay = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	newDay = time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
)

const (
	oldItem  = "github.com/eyelock/ynr#1"
	newItem  = "github.com/eyelock/ynr#2"
	oldTrace = traceA
	newTrace = traceB
)

// shipDay ships a day's records, each stamped as ynr stamps them on receipt: two runs and a step
// of the item in a trace, an event of the item, and a cost metric.
func shipDay(t *testing.T, r store.Reader, hour time.Time, trace, item, instance string) {
	t.Helper()
	stamp := func(s span) span {
		s.res["ynr.collector.id"] = "gha"
		s.res["ynr.collector.instance"] = instance
		s.res["ynr.received_at"] = hour.Add(10 * time.Minute).Format("2006-01-02T15:04:05.000Z")
		return s
	}
	at := hour.Add(5 * time.Minute)
	step := itemSpan(trace, "1111111111111111", item, at)
	run1 := runSpan(trace, "2222222222222222", "github.com/eyelock/ynr#lint", "converged", "claude-opus-5-5", 1.5, at.Add(time.Minute))
	run1.parent = "1111111111111111"
	run2 := runSpan(trace[:30]+"ff", "3333333333333333", "", "budget", "claude-haiku-4-5", 0.5, at.Add(2*time.Minute))
	ship(t, r, store.Traces, hour.Add(10*time.Minute), "local.a", traceLine(t, stamp(step), stamp(run1), stamp(run2)))
	ship(t, r, store.Logs, hour.Add(10*time.Minute), "local.a",
		eventLine(t, "ynf.step.started", at, map[string]any{"service.name": "ynf", "ynr.collector.instance": instance,
			"ynr.received_at": hour.Add(10 * time.Minute).Format("2006-01-02T15:04:05.000Z")}, map[string]any{"ynf.item.key": item}))
	ship(t, r, store.Metrics, hour.Add(10*time.Minute), "local.a",
		metricLine(t, "ynf.run.cost", pmetric.AggregationTemporalityDelta, 2, at))
}

// downgrade rewrites a Parquet file as v0.2.2 wrote it: without the columns added since, if it
// has them, and without any file metadata.
func downgrade(t *testing.T, r store.Reader, key string) {
	t.Helper()
	p, err := r.Local(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var drop []string
	for _, c := range []string{"collector_instance", "received_at"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM (DESCRIBE SELECT * FROM read_parquet(" + literal(p) + ")) WHERE column_name = " + literal(c)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			drop = append(drop, c)
		}
	}
	sel := "*"
	if len(drop) > 0 {
		sel = "* EXCLUDE (" + strings.Join(drop, ", ") + ")"
	}
	out := filepath.Join(t.TempDir(), "old.parquet")
	if _, err := db.Exec("COPY (SELECT " + sel + " FROM read_parquet(" + literal(p) + ")) TO " + literal(out) + " (FORMAT parquet, COMPRESSION zstd)"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Replace(context.Background(), key, b); err != nil {
		t.Fatal(err)
	}
}

// schemaOf is a Parquet file's version key ("" when it has none) and whether it has the
// received_at column.
func schemaOf(t *testing.T, r store.Reader, key string) (version string, hasReceived bool) {
	t.Helper()
	p, err := r.Local(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var v sql.NullString
	if err := db.QueryRow("SELECT max(decode(value)) FROM parquet_kv_metadata(" + literal(p) + ") WHERE decode(key) = " + literal(SchemaKey)).Scan(&v); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM (DESCRIBE SELECT * FROM read_parquet(" + literal(p) + ")) WHERE column_name = 'received_at'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return v.String, n > 0
}

// TestMixedSchemaStoreReadsEverywhere is what every store becomes on upgrading: parts, item
// index and rollups written by v0.2.2 (no version key, no collector_instance or received_at)
// beside the ones this build writes. Every named query returns the old records, and the new
// columns read NULL for them, from the direct read and from the hot tier; compacting an hour
// that has an old part writes a version 2 part.
func TestMixedSchemaStoreReadsEverywhere(t *testing.T) {
	r := openStore(t)
	ctx := context.Background()
	shipDay(t, r, oldDay, oldTrace, oldItem, "job-old")
	shipDay(t, r, newDay, newTrace, newItem, "job-new")
	c := Compaction{Grace: 10 * time.Minute, Keep: time.Hour, Lookback: 7 * 24 * time.Hour}
	if n, err := Compact(ctx, r, c, now); err != nil || n != 6 {
		t.Fatalf("compacted %d hours, %v", n, err)
	}
	// The old day's files are what v0.2.2 would have left: its parts, item index and rollups.
	var oldFiles, newFiles []string
	for _, prefix := range []string{"compacted/", "index/", "rollups/"} {
		for _, k := range keys(t, r, prefix) {
			if !strings.HasSuffix(k, ".parquet") {
				continue
			}
			if onDay(k, oldDay) {
				downgrade(t, r, k)
				oldFiles = append(oldFiles, k)
			} else if onDay(k, newDay) {
				newFiles = append(newFiles, k)
			}
		}
	}
	// A bucket reader keeps a part it has fetched, since a part never changes: read the rewritten
	// files through a reader with an empty cache.
	r = withFreshCache(t, r)
	for _, k := range oldFiles {
		if v, recv := schemaOf(t, r, k); v != "" || recv {
			t.Errorf("%s: version %q, received_at %v; want the v0.2.2 layout", k, v, recv)
		}
	}
	for _, k := range newFiles {
		if v, recv := schemaOf(t, r, k); v != "2" || (strings.HasPrefix(k, "compacted/") && !recv) {
			t.Errorf("%s: version %q, received_at %v; want version 2", k, v, recv)
		}
	}
	if len(oldFiles) != 6 || len(newFiles) != 6 { // a part each of traces, logs and metrics, the item index, two rollups
		t.Fatalf("%d old and %d new Parquet files", len(oldFiles), len(newFiles))
	}

	h := openHot(t, filepath.Join(t.TempDir(), "hot.duckdb"), r)
	if err := h.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if h.Failed != 0 {
		t.Fatalf("the hot tier skipped %d files", h.Failed)
	}
	direct := func(q *Query, p Params) *Result {
		res, err := Run(ctx, r, q, p)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	hot := func(h *Hot) func(*Query, Params) *Result {
		return func(q *Query, p Params) *Result {
			res, err := h.Run(ctx, q, p)
			if err != nil {
				t.Fatal(err)
			}
			return res
		}
	}
	t.Run("direct", func(t *testing.T) { mixedQueries(t, direct, 2) })
	t.Run("hot", func(t *testing.T) { mixedQueries(t, hot(h), 2) })

	// A late batch for the old day's hour: compacting it absorbs the old part into a version 2 one.
	ship(t, r, store.Traces, oldDay.Add(40*time.Minute), "local.b",
		traceLine(t, func() span {
			s := runSpan("0af7651916cd43dd8448eb211c80319e", "4444444444444444", "", "converged", "claude-opus-5-5", 1, oldDay.Add(30*time.Minute))
			s.res["ynr.collector.instance"] = "job-late"
			return s
		}()))
	if n, err := Compact(ctx, r, c, now); err != nil || n != 1 {
		t.Fatalf("recompacted %d hours, %v", n, err)
	}
	part := store.PartKey(store.Traces, oldDay, 2)
	if v, recv := schemaOf(t, r, part); v != "2" || !recv {
		t.Fatalf("%s: version %q, received_at %v; want a version 2 part", part, v, recv)
	}
	for _, k := range append(keys(t, r, "index/"), keys(t, r, "rollups/")...) { // the day was written again
		if v, _ := schemaOf(t, r, k); v != "2" {
			t.Errorf("%s: version %q after recompaction", k, v)
		}
	}
	// The running hot tier does not look back at an old hour for a late batch; a rebuilt one
	// reads the new part, with the old records in it.
	h2 := openHot(t, filepath.Join(t.TempDir(), "hot2.duckdb"), r)
	if err := h2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	t.Run("direct after recompaction", func(t *testing.T) { mixedQueries(t, direct, 3) })
	t.Run("rebuilt hot after recompaction", func(t *testing.T) { mixedQueries(t, hot(h2), 3) })
}

// withFreshCache opens the store again, with an empty cache if it is a bucket.
func withFreshCache(t *testing.T, r store.Reader) store.Reader {
	t.Helper()
	raw := r.URL()
	if u, err := url.Parse(raw); err == nil && u.Scheme == "s3" {
		q := u.Query()
		q.Set("cache", t.TempDir())
		u.RawQuery = q.Encode()
		raw = u.String()
	}
	fresh, err := store.OpenReader(raw)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}

// onDay reports whether a part, index or rollup key is of the day's records.
func onDay(key string, day time.Time) bool {
	return strings.Contains(key, day.Format("/2006/01/02/")) || strings.Contains(key, day.Format("/2006/01/02.")) ||
		strings.Contains(key, day.Format("/2006-01-02."))
}

// mixedQueries runs every named query over both days and checks the answers. runs is how many
// runs the old day holds with the late one, if it is there.
func mixedQueries(t *testing.T, readQuery func(*Query, Params) *Result, wantRuns int64) {
	t.Helper()
	read := func(name string, p Params) *Result {
		q, ok := Lookup(name)
		if !ok {
			t.Fatalf("no query %s", name)
		}
		return readQuery(q, p)
	}
	win := Params{Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Until: now.Add(time.Minute)}
	total := func(res *Result, column string) (n int64) {
		for _, row := range res.Rows {
			n += row[col(t, res, column)].(int64)
		}
		return n
	}
	// 2 runs on the new day, 2 on the old day, and a third on the old day once the late one is in.
	want := wantRuns + 2
	if got := total(read("runs", win), "runs"); got != want {
		t.Errorf("runs = %d, want %d", got, want)
	}
	if got := total(read("cost", win), "runs"); got != want {
		t.Errorf("cost runs = %d, want %d", got, want)
	}
	if res := read("recent", win); int64(len(res.Rows)) != want {
		t.Errorf("recent = %v", rows(res))
	}
	if res := read("items", win); len(res.Rows) != 2 {
		t.Errorf("items = %v", rows(res))
	}
	for _, item := range []string{oldItem, newItem} {
		res := read("item", Params{Arg: item, Since: win.Since, Until: win.Until})
		if len(res.Rows) != 2 { // the step and the event
			t.Errorf("item %s = %v", item, rows(res))
		}
	}
	for trace, spans := range map[string]int{oldTrace: 2, newTrace: 2} {
		if res := read("trace", Params{Arg: trace, Since: win.Since, Until: win.Until}); len(res.Rows) != spans {
			t.Errorf("trace %s = %v", trace, rows(res))
		}
	}

	// The long window is answered from the rollups, the old day's with no version key.
	long := read("runs", Params{Since: now.Add(-90 * 24 * time.Hour), Until: now.Add(time.Minute)})
	if got := total(long, "runs"); got != want {
		t.Errorf("runs over 90 days = %d, want %d: %v", got, want, rows(long))
	}
	if !long.FromRollups {
		t.Error("the long window was not answered from the rollups")
	}
	longCost := read("cost", Params{Since: now.Add(-90 * 24 * time.Hour), Until: now.Add(time.Minute)})
	if got := total(longCost, "runs"); got != want {
		t.Errorf("cost over 90 days = %d, want %d", got, want)
	}

	// The columns added in version 2 read NULL for the old records and their values for the new.
	for _, table := range []string{"spans", "logs", "metric_points"} {
		q := &Query{Name: "cols", Since: 7 * 24 * time.Hour, Signals: []string{map[string]string{"spans": store.Traces, "logs": store.Logs, "metric_points": store.Metrics}[table]},
			SQL: fmt.Sprintf(`SELECT date_trunc('day', time)::DATE::VARCHAR AS day, collector_instance, received_at IS NULL AS no_received
FROM %s WHERE time >= $since AND time < $until ORDER BY ALL`, table)}
		res := readQuery(q, win)
		for _, row := range res.Rows {
			switch row[0] {
			case "2026-10-02":
				if (row[1] == nil && row[2] != true) || (row[1] != nil && row[1] != "job-late") {
					t.Errorf("%s: old record = %v, want NULL collector_instance and received_at", table, row)
				}
			case "2026-10-04":
				if table != "metric_points" && (row[1] != "job-new" || row[2] != false) {
					t.Errorf("%s: new record = %v", table, row)
				}
			}
		}
		if len(res.Rows) == 0 {
			t.Errorf("%s: no records", table)
		}
	}
}
