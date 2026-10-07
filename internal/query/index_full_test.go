//go:build full

package query

import (
	"context"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

func itemSpan(trace, id, item string, at time.Time) span {
	return span{trace: trace, id: id, name: "ynf.step", start: at, dur: time.Minute,
		res: map[string]any{"service.name": "ynf"}, attrs: map[string]any{"ynf.item.key": item}}
}

func itemRun(t *testing.T, r store.Reader, item string) *Result {
	t.Helper()
	q, _ := Lookup("item")
	res, err := Run(context.Background(), r, q, Params{Arg: item, Since: now.Add(-24 * time.Hour), Until: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestItemHistoryReadsOnlyIndexedHours: once hours are compacted and indexed, an item's history
// opens only the parts of the hours its index names. An hour changed since it was indexed, by a
// late batch, is read whole until the next compaction indexes it again.
func TestItemHistoryReadsOnlyIndexedHours(t *testing.T) {
	r := openStore(t)
	ctx := context.Background()
	base := now.Truncate(time.Hour).Add(-6 * time.Hour)
	ids := []string{"1111111111111111", "2222222222222222", "3333333333333333", "4444444444444444", "5555555555555555"}
	for i, id := range ids {
		hour := base.Add(time.Duration(i) * time.Hour)
		item := "github.com/eyelock/ynr#other"
		if i == 1 {
			item = "github.com/eyelock/ynr#12"
		}
		ship(t, r, store.Traces, hour.Add(10*time.Minute), "local.a", traceLine(t, itemSpan(traceA, id, item, hour.Add(5*time.Minute))))
	}
	if res := itemRun(t, r, "github.com/eyelock/ynr#12"); len(res.Rows) != 1 || res.Files != 5 {
		t.Fatalf("before indexing: %d rows from %d files", len(res.Rows), res.Files)
	}
	compact(t, r, now)
	if res := itemRun(t, r, "github.com/eyelock/ynr#12"); len(res.Rows) != 1 || res.Files != 1 {
		t.Fatalf("after indexing: %d rows from %d files, want 1 from 1", len(res.Rows), res.Files)
	}
	if res := itemRun(t, r, "github.com/eyelock/ynr#none"); len(res.Rows) != 0 || res.Files != 0 {
		t.Fatalf("an unknown item read %d files", res.Files)
	}

	// A late batch for the item lands in an indexed hour: that hour is read whole.
	late := base.Add(3 * time.Hour)
	key := store.BatchKey(store.Traces, late.Add(30*time.Minute), "offline", store.NewULID(now), "local.b", 0, 1)
	putLines(t, r, key, traceLine(t, itemSpan(traceB, "6666666666666666", "github.com/eyelock/ynr#12", late.Add(20*time.Minute))))
	if res := itemRun(t, r, "github.com/eyelock/ynr#12"); len(res.Rows) != 2 || res.Files != 3 {
		t.Fatalf("with a late batch: %d rows from %d files, want 2 from 3", len(res.Rows), res.Files)
	}
	compact(t, r, now.Add(time.Minute))
	if res := itemRun(t, r, "github.com/eyelock/ynr#12"); len(res.Rows) != 2 || res.Files != 2 {
		t.Fatalf("reindexed: %d rows from %d files, want 2 from 2", len(res.Rows), res.Files)
	}

	// An index lost in a crash is rebuilt by the next pass.
	if err := r.Delete(ctx, store.IndexKey(base.Truncate(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if res := itemRun(t, r, "github.com/eyelock/ynr#12"); res.Files != 5 {
		t.Fatalf("without an index, read %d files, want every part", res.Files)
	}
	compact(t, r, now.Add(2*time.Minute))
	if res := itemRun(t, r, "github.com/eyelock/ynr#12"); res.Files != 2 {
		t.Fatalf("after rebuilding the index, read %d files", res.Files)
	}
}
