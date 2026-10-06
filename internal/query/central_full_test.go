//go:build full

package query

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// counting wraps a reader and counts what the change feed reads from it.
type counting struct {
	store.Reader
	listed      atomic.Int64 // keys returned by ListAfter
	afterCalls  atomic.Int64
	wholeLists  atomic.Int64 // List calls, which return whole prefixes
	collectorOK atomic.Int64
}

func (c *counting) ListAfter(ctx context.Context, prefix, after string) ([]string, error) {
	c.afterCalls.Add(1)
	keys, err := c.Reader.ListAfter(ctx, prefix, after)
	c.listed.Add(int64(len(keys)))
	return keys, err
}

func (c *counting) List(ctx context.Context, prefix string) ([]string, error) {
	c.wholeLists.Add(1)
	return c.Reader.List(ctx, prefix)
}

func (c *counting) ListCollectors(ctx context.Context, signal string, hour time.Time) ([]string, error) {
	c.collectorOK.Add(1)
	return c.Reader.ListCollectors(ctx, signal, hour)
}

func (c *counting) reset() {
	c.listed.Store(0)
	c.afterCalls.Store(0)
	c.wholeLists.Store(0)
	c.collectorOK.Store(0)
}

// shipFrom is ship for a named collector, which returns the key.
func shipFrom(t *testing.T, s store.Store, signal, collector string, received time.Time, source string, lines ...[]byte) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, l := range lines {
		_, _ = zw.Write(append(l, '\n'))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	key := store.BatchKey(signal, received, collector, store.NewULID(received), source, 0, int64(buf.Len()))
	if err := s.Put(context.Background(), key, buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	return key
}

func spanID(i int) string { return fmt.Sprintf("%016x", i+1) }

// TestFeedReadsOnlyKeysAfterEachPosition is the exit check: with many collectors, a poll lists
// only the keys after each collector's position, never the whole hour again.
func TestFeedReadsOnlyKeysAfterEachPosition(t *testing.T) {
	const collectors, batches = 40, 3
	base := openStore(t)
	c := &counting{Reader: base}
	ctx := context.Background()
	hour := now.Truncate(time.Hour)
	line := func(i int) []byte {
		return traceLine(t, runSpan(traceA, spanID(i), "", "converged", "m", 1, now.Add(-time.Minute)))
	}
	n := 0
	for i := range collectors {
		for j := range batches {
			shipFrom(t, base, store.Traces, fmt.Sprintf("pool-%02d", i), hour.Add(time.Duration(j+1)*time.Minute), "src-1", line(n))
			n++
		}
	}
	// The previous hour is in the feed too.
	shipFrom(t, base, store.Traces, "pool-00", hour.Add(-30*time.Minute), "src-1", line(n))
	n++

	f := NewFeed(c)
	got, err := f.Poll(ctx, now, []string{store.Traces})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[store.Traces]) != n || f.Positions() != collectors+1 {
		t.Fatalf("first poll: %d keys, %d positions; want %d, %d", len(got[store.Traces]), f.Positions(), n, collectors+1)
	}

	// Nothing new: a call per collector, and no key returned.
	c.reset()
	got, err = f.Poll(ctx, now, []string{store.Traces})
	if err != nil || len(got[store.Traces]) != 0 || c.listed.Load() != 0 {
		t.Fatalf("an idle poll returned %d keys, listed %d, %v", len(got[store.Traces]), c.listed.Load(), err)
	}
	if c.afterCalls.Load() != collectors+1 {
		t.Fatalf("an idle poll made %d listings, want one per collector", c.afterCalls.Load())
	}

	// One new batch among 40 collectors: one key read.
	c.reset()
	k := shipFrom(t, base, store.Traces, "pool-07", hour.Add(10*time.Minute), "src-2", line(n))
	got, err = f.Poll(ctx, now.Add(time.Minute), []string{store.Traces})
	if err != nil || len(got[store.Traces]) != 1 || got[store.Traces][0] != k {
		t.Fatalf("poll after one batch = %v %v", got, err)
	}
	if c.listed.Load() != 1 || c.wholeLists.Load() != 0 {
		t.Fatalf("poll after one batch read %d keys with %d whole listings, want 1 and 0", c.listed.Load(), c.wholeLists.Load())
	}

	// A new collector appears; the hour rolls over, so the older hour leaves the feed.
	c.reset()
	shipFrom(t, base, store.Traces, "pool-new", hour.Add(time.Hour+time.Minute), "src-1", line(n+1))
	got, err = f.Poll(ctx, now.Add(time.Hour), []string{store.Traces})
	if err != nil || len(got[store.Traces]) != 1 {
		t.Fatalf("poll in the next hour = %v %v", got, err)
	}
	if f.Positions() != collectors+1 { // pool-00's old hour is gone; the new collector is in
		t.Fatalf("positions after the hour rolled = %d", f.Positions())
	}
}

// TestFeedIgnoresKeysOfTheWrongShape: a key under an hour's prefix that is not a batch is counted,
// not read.
func TestFeedIgnoresKeysOfTheWrongShape(t *testing.T) {
	r := openStore(t)
	hour := now.Truncate(time.Hour)
	shipFrom(t, r, store.Traces, "pool-a", hour.Add(time.Minute), "src-1", traceLine(t, runSpan(traceA, spanID(1), "", "converged", "m", 1, now)))
	if err := r.Put(context.Background(), HourPrefix(store.Traces, hour)+"pool-a/junk.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	f := NewFeed(r)
	got, err := f.Poll(context.Background(), now, []string{store.Traces})
	if err != nil || len(got[store.Traces]) != 1 || f.Ignored != 1 {
		t.Fatalf("poll = %v, ignored %d, %v", got, f.Ignored, err)
	}
}

func openFeedHot(t *testing.T, path string, r store.Reader) *Hot {
	t.Helper()
	h := openHot(t, path, r)
	h.UseFeed()
	return h
}

// TestCentralHotTierRestartLosesNothing: a hot tier fed by the change feed holds every record
// once; a restart (a new empty database over the same store) rebuilds the same, and records
// that land after it arrive through the feed, a poll reading only the new keys.
func TestCentralHotTierRestartLosesNothing(t *testing.T) {
	base := openStore(t)
	c := &counting{Reader: base}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hot.duckdb")
	hour := now.Truncate(time.Hour)
	span := func(i int, at time.Time) []byte {
		return traceLine(t, runSpan(traceA, spanID(i), "", "converged", "m", 1, at))
	}
	// Older hours, read whole on the first sync; the previous and current hour, from the feed.
	shipFrom(t, base, store.Traces, "pool-a", hour.Add(-4*time.Hour+time.Minute), "s-1", span(1, now.Add(-4*time.Hour)))
	shipFrom(t, base, store.Traces, "pool-b", hour.Add(-3*time.Hour+time.Minute), "s-1", span(2, now.Add(-3*time.Hour)))
	shipFrom(t, base, store.Traces, "pool-a", hour.Add(-time.Hour+time.Minute), "s-1", span(3, now.Add(-time.Hour)))
	shipFrom(t, base, store.Traces, "pool-b", hour.Add(time.Minute), "s-1", span(4, now.Add(-10*time.Minute)))
	shipFrom(t, base, store.Traces, "pool-a", hour.Add(2*time.Minute), "s-1", span(5, now.Add(-9*time.Minute)))
	shipFrom(t, base, store.Logs, "pool-a", hour.Add(2*time.Minute), "s-1",
		eventLine(t, "ynh.run.started", now.Add(-time.Hour), map[string]any{"service.name": "ynh"}, nil))

	h := openFeedHot(t, path, c)
	if err := h.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(t, h); got["spans"] != 5 || got["logs"] != 1 {
		t.Fatalf("after the first sync: %v", got)
	}
	// A poll reads only what is new.
	c.reset()
	shipFrom(t, base, store.Traces, "pool-b", hour.Add(5*time.Minute), "s-2", span(6, now.Add(-time.Minute)))
	if err := h.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(t, h); got["spans"] != 6 {
		t.Fatalf("after a new batch: %v", got)
	}
	if c.listed.Load() != 1 || c.wholeLists.Load() != 0 {
		t.Fatalf("the sync read %d keys with %d whole listings, want 1 and 0", c.listed.Load(), c.wholeLists.Load())
	}
	_ = h.Close()

	// Restart: new database, same store.
	h2 := openFeedHot(t, path, base)
	if err := h2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(t, h2); got["spans"] != 6 || got["logs"] != 1 {
		t.Fatalf("after a restart: %v", got)
	}
	shipFrom(t, base, store.Traces, "pool-c", hour.Add(8*time.Minute), "s-1", span(7, now.Add(-time.Minute)))
	if err := h2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(t, h2); got["spans"] != 7 {
		t.Fatalf("after a restart and a new collector: %v", got)
	}
}

// TestLeaseExcludesAndExpires: of many holders only one holds a lease at a time, an expired
// lease is taken over by exactly one, and a holder that lost its lease cannot renew it.
func TestLeaseExcludesAndExpires(t *testing.T) {
	r := openStore(t)
	ctx := context.Background()
	key := LeaseKey(store.Traces, now.Truncate(time.Hour))
	if key != "leases/compaction/traces/2026/10/05/21.json" {
		t.Fatalf("lease key = %s", key)
	}
	t0 := now
	a, err := acquireLease(ctx, r, key, "a", time.Minute, t0)
	if err != nil || a == nil {
		t.Fatalf("a acquires = %v %v", a, err)
	}
	if b, err := acquireLease(ctx, r, key, "b", time.Minute, t0.Add(30*time.Second)); err != nil || b != nil {
		t.Fatalf("b took a live lease: %v %v", b, err)
	}
	if err := a.renew(ctx, t0.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	if b, err := acquireLease(ctx, r, key, "b", time.Minute, t0.Add(90*time.Second)); err != nil || b != nil {
		t.Fatalf("b took a renewed lease: %v %v", b, err)
	}
	// Expired (renewed to t0+100s): many contenders, exactly one takes it over.
	var won atomic.Int32
	var wg sync.WaitGroup
	leases := make([]*lease, 6)
	for i := range leases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := acquireLease(ctx, r, key, fmt.Sprintf("c%d", i), time.Minute, t0.Add(3*time.Minute))
			if err != nil {
				t.Error(err)
			}
			if l != nil {
				won.Add(1)
				leases[i] = l
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d contenders took the expired lease, want 1", won.Load())
	}
	if err := a.renew(ctx, t0.Add(3*time.Minute)); !errors.Is(err, errLeaseLost) {
		t.Fatalf("the old holder renewed a lost lease: %v", err)
	}
	// A release lets the next holder in at once.
	for _, l := range leases {
		if l != nil {
			l.release(ctx, t0.Add(3*time.Minute))
		}
	}
	if d, err := acquireLease(ctx, r, key, "d", time.Minute, t0.Add(3*time.Minute)); err != nil || d == nil {
		t.Fatalf("d after a release = %v %v", d, err)
	}
}

// TestWithLeaseRunsOneAtATime: many holders run the same work under one lease; never two at once.
func TestWithLeaseRunsOneAtATime(t *testing.T) {
	r := openStore(t)
	ctx := context.Background()
	key := LeaseKey(store.Logs, now.Truncate(time.Hour))
	var active, peak, ran atomic.Int32
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 3 {
				held, err := withLease(ctx, r, key, fmt.Sprintf("h%d", i), time.Minute, time.Now, func(context.Context) error {
					n := active.Add(1)
					for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
					}
					time.Sleep(15 * time.Millisecond)
					active.Add(-1)
					ran.Add(1)
					return nil
				})
				if err != nil {
					t.Error(err)
				}
				_ = held
			}
		}()
	}
	wg.Wait()
	if peak.Load() != 1 || ran.Load() == 0 {
		t.Fatalf("peak %d concurrent holders, %d runs", peak.Load(), ran.Load())
	}
}

// TestWithLeaseStopsWorkWhenTheLeaseIsLost: a holder whose lease was taken has its work's context
// cancelled, and the loss is reported.
func TestWithLeaseStopsWorkWhenTheLeaseIsLost(t *testing.T) {
	r := openStore(t)
	ctx := context.Background()
	key := LeaseKey(store.Metrics, now.Truncate(time.Hour))
	held, err := withLease(ctx, r, key, "a", 90*time.Millisecond, time.Now, func(fctx context.Context) error {
		time.Sleep(10 * time.Millisecond)
		// Another central takes the lease out from under it.
		if err := r.Replace(ctx, key, encodeLease(leaseRecord{Holder: "thief", Expires: time.Now().Add(time.Hour)})); err != nil {
			return err
		}
		select {
		case <-fctx.Done():
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("the work was not stopped")
		}
	})
	if !held || err == nil || !errors.Is(err, errLeaseLost) {
		t.Fatalf("held %v, err %v; want the loss reported", held, err)
	}
}

// TestTwoCentralsNeverCompactAnHourTwice runs two central instances over one store, compacting
// together in rounds as late batches land. Each hour ends each round with exactly one new
// manifest, part numbers never repeat, and every record is in the latest part once.
func TestTwoCentralsNeverCompactAnHourTwice(t *testing.T) {
	r := openStore(t)
	ctx := context.Background()
	first := now.Truncate(time.Hour).Add(-4 * time.Hour)
	hours := []time.Time{first, first.Add(time.Hour), first.Add(2 * time.Hour)}
	n := 0
	land := func(round int) {
		for _, h := range hours {
			for _, sig := range []string{store.Traces, store.Logs} {
				at := h.Add(time.Duration(round+1) * time.Minute)
				if sig == store.Traces {
					shipFrom(t, r, sig, "pool-a", at, fmt.Sprintf("s-%d", round),
						traceLine(t, runSpan(traceA, spanID(n), "", "converged", "m", 1, at)))
					n++
				} else {
					shipFrom(t, r, sig, "pool-b", at, fmt.Sprintf("s-%d", round),
						eventLine(t, fmt.Sprintf("ynh.run.started.%d", round), at, map[string]any{"service.name": "ynh"}, nil))
				}
			}
		}
	}
	for round := 1; round <= 3; round++ {
		land(round)
		var wg sync.WaitGroup
		for _, who := range []string{"central-1", "central-2"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := Compaction{Grace: 10 * time.Minute, Keep: time.Hour, Lookback: 24 * time.Hour, Holder: who, LeaseTTL: time.Minute}
				if _, err := Compact(ctx, r, c, now); err != nil {
					t.Errorf("%s round %d: %v", who, round, err)
				}
			}()
		}
		wg.Wait()
		for _, h := range hours {
			for _, sig := range []string{store.Traces, store.Logs} {
				var manifests, parts int
				for _, k := range keys(t, r, store.CompactedPrefix(sig, h)) {
					num, isManifest, err := store.ParseCompacted(k)
					if err != nil {
						t.Fatal(err)
					}
					if isManifest {
						manifests++
						if num > round {
							t.Fatalf("%s: manifest %d in round %d", k, num, round)
						}
					} else {
						parts++
					}
				}
				if manifests != round || parts != round {
					t.Fatalf("%s %s after round %d: %d manifests and %d parts, want %d each (duplicated or missed compaction)",
						sig, h.Format("15h"), round, manifests, parts, round)
				}
				hr, err := ReadHour(ctx, r, sig, h)
				if err != nil {
					t.Fatal(err)
				}
				if hr.Manifest == nil || hr.Manifest.N != round || len(hr.Batches) != 0 || hr.Manifest.Records != int64(round) {
					t.Fatalf("%s %s after round %d: manifest %+v, uncovered %v", sig, h.Format("15h"), round, hr.Manifest, hr.Batches)
				}
			}
		}
	}
	for _, k := range keys(t, r, "leases/") {
		if !strings.HasPrefix(k, "leases/compaction/") {
			t.Fatalf("stray lease key %s", k)
		}
	}
}
