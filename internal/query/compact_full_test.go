//go:build full

package query

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

func compact(t *testing.T, r store.Reader, at time.Time) int {
	t.Helper()
	n, err := Compact(context.Background(), r, Compaction{Grace: 10 * time.Minute, Keep: time.Hour, Lookback: 24 * time.Hour}, at)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func runsCount(t *testing.T, r store.Reader) int64 {
	t.Helper()
	res := run(t, r, "runs", Params{Since: now.Add(-24 * time.Hour), Until: now.Add(48 * time.Hour)})
	var total int64
	for _, row := range res.Rows {
		total += row[col(t, res, "runs")].(int64)
	}
	return total
}

func keys(t *testing.T, r store.Reader, prefix string) []string {
	t.Helper()
	k, err := r.List(context.Background(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestCompactionKeepsEveryRecordOnce walks an hour through its life: batches, a compaction, a
// late batch read before and after the next compaction, then pruning, with the same answer
// from every stage, and from a hot tier rebuilt at the end.
func TestCompactionKeepsEveryRecordOnce(t *testing.T) {
	r := openStore(t)
	hour := now.Truncate(time.Hour).Add(-3 * time.Hour)
	line := traceLine(t,
		runSpan(traceA, "1111111111111111", "", "converged", "m", 1, hour.Add(5*time.Minute)),
		runSpan(traceB, "2222222222222222", "", "converged", "m", 1, hour.Add(6*time.Minute)))
	ship(t, r, store.Traces, hour.Add(10*time.Minute), "local.a", line)
	ship(t, r, store.Traces, hour.Add(20*time.Minute), "local.a", line) // re-shipped
	if err := r.Put(context.Background(), HourPrefix(store.Traces, hour)+"laptop/junk.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := runsCount(t, r); got != 2 {
		t.Fatalf("before compaction: %d runs", got)
	}

	// Within the grace after the hour closes, nothing is compacted.
	if n := compact(t, r, hour.Add(time.Hour+5*time.Minute)); n != 0 {
		t.Fatalf("compacted %d hours within the grace", n)
	}
	if n := compact(t, r, hour.Add(time.Hour+11*time.Minute)); n != 1 {
		t.Fatalf("compacted %d hours, want 1", n)
	}
	h, err := ReadHour(context.Background(), r, store.Traces, hour)
	if err != nil {
		t.Fatal(err)
	}
	if h.Manifest == nil || h.Manifest.N != 1 || h.Manifest.Records != 2 || len(h.Manifest.Batches) != 2 || len(h.Batches) != 0 || h.Ignored != 1 {
		t.Fatalf("after the first compaction: %+v %+v", h, h.Manifest)
	}
	if got := runsCount(t, r); got != 2 {
		t.Fatalf("after compaction: %d runs", got)
	}

	// A batch that lands late, from a collector that was offline, is read before it is compacted.
	late := hour.Add(2 * time.Hour)
	key := store.BatchKey(store.Traces, hour.Add(30*time.Minute), "offline", store.NewULID(late), "local.b", 0, 1)
	putLines(t, r, key, traceLine(t,
		runSpan(traceA, "1111111111111111", "", "converged", "m", 1, hour.Add(5*time.Minute)), // also in the part
		runSpan("0af7651916cd43dd8448eb211c80319d", "3333333333333333", "", "budget", "m", 1, hour.Add(30*time.Minute))))
	if got := runsCount(t, r); got != 3 {
		t.Fatalf("with a late batch: %d runs", got)
	}
	if n := compact(t, r, late.Add(time.Minute)); n != 1 {
		t.Fatalf("the late batch was not compacted")
	}
	if got := runsCount(t, r); got != 3 {
		t.Fatalf("after recompaction: %d runs", got)
	}

	// Before Keep has passed, the covered batches and superseded part stay; after, they go.
	if len(keys(t, r, store.CompactedPrefix(store.Traces, hour))) != 4 {
		t.Fatalf("compacted = %v", keys(t, r, store.CompactedPrefix(store.Traces, hour)))
	}
	compact(t, r, late.Add(2*time.Hour))
	if got := keys(t, r, store.CompactedPrefix(store.Traces, hour)); len(got) != 2 ||
		got[0] != store.ManifestKey(store.Traces, hour, 2) || got[1] != store.PartKey(store.Traces, hour, 2) {
		t.Fatalf("after pruning, compacted = %v", got)
	}
	if got := keys(t, r, HourPrefix(store.Traces, hour)); len(got) != 1 { // only the junk is left
		t.Fatalf("after pruning, batches = %v", got)
	}
	if got := runsCount(t, r); got != 3 {
		t.Fatalf("after pruning: %d runs", got)
	}

	hot, err := OpenHot(context.Background(), filepath.Join(t.TempDir(), "hot.duckdb"), r, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hot.Close() }()
	hot.now = func() time.Time { return now }
	if err := hot.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := count(t, hot); c["spans"] != 3 {
		t.Fatalf("a hot tier rebuilt from the parts holds %v", c)
	}
}

// TestAManifestCannotPointOutsideItsHour: a manifest is checked before its parts are read.
func TestAManifestCannotPointOutsideItsHour(t *testing.T) {
	r := openStore(t)
	hour := now.Truncate(time.Hour)
	other := store.PartKey(store.Traces, hour.Add(-time.Hour), 1)
	b, _ := json.Marshal(Manifest{Signal: store.Traces, Hour: hour, N: 1, Parts: []string{other}})
	if err := r.Put(context.Background(), store.ManifestKey(store.Traces, hour, 1), b); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHour(context.Background(), r, store.Traces, hour); err == nil {
		t.Fatal("read a manifest naming another hour's part")
	}
}

func putLines(t *testing.T, r store.Reader, key string, lines ...[]byte) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(store.FolderURL(dir))
	if err != nil {
		t.Fatal(err)
	}
	ship(t, s, store.Traces, now, "x", lines...)
	got := keys(t, s.(store.Reader), "traces/")
	if len(got) != 1 {
		t.Fatal(errors.New("expected one batch"))
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(got[0])))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Put(context.Background(), key, data); err != nil {
		t.Fatal(err)
	}
}
