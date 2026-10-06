package store

import (
	"context"
	"errors"
	"sort"
	"time"
)

// Retention is how long a folder store keeps what it holds, and how big it may grow (ADR-005,
// NFR-15). Rollups are kept longer than records, so long-range views outlive the detail.
type Retention struct {
	// Records is how long batches, compacted parts and the item index are kept.
	Records time.Duration
	// Rollups is how long daily and monthly rollups are kept.
	Rollups time.Duration
	// MaxBytes caps the records' total size; the oldest are deleted first. Zero is no cap.
	MaxBytes int64
}

// LaptopRetention is NFR-15's laptop row, rolling 7 days capped at 1 GB, with ADR-005's
// 13 months of rollups.
var LaptopRetention = Retention{Records: 7 * 24 * time.Hour, Rollups: 396 * 24 * time.Hour, MaxBytes: 1 << 30}

// Retained is what one Retain pass deleted.
type Retained struct {
	ByAge, BySize int
	Bytes         int64
}

// Retain deletes what has aged out, then, while the records are over MaxBytes, the oldest
// records a whole span (an hour, or a day of the index) at a time, so an hour never keeps a
// manifest without its part. Keys of no shape it knows are left alone.
func Retain(ctx context.Context, r Reader, p Retention, now time.Time) (Retained, error) {
	var out Retained
	var errs []error
	type unit struct {
		end  time.Time
		keys []string
		size int64
	}
	units := map[time.Time]*unit{}
	var total int64
	for _, prefix := range []string{Traces + "/", Logs + "/", Metrics + "/", "compacted/", "index/", "rollups/"} {
		sizes, err := r.Sizes(ctx, prefix)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for key, size := range sizes {
			from, to, rollup, ok := Span(key)
			if !ok {
				continue
			}
			keep := p.Records
			if rollup {
				keep = p.Rollups
			}
			if keep > 0 && now.Sub(to) > keep {
				errs = append(errs, r.Delete(ctx, key))
				out.ByAge++
				out.Bytes += size
				continue
			}
			if rollup {
				continue
			}
			u := units[from]
			if u == nil {
				u = &unit{end: to}
				units[from] = u
			}
			u.keys = append(u.keys, key)
			u.size += size
			total += size
		}
	}
	if p.MaxBytes > 0 && total > p.MaxBytes {
		var starts []time.Time
		for t := range units {
			starts = append(starts, t)
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
		for _, t := range starts {
			if total <= p.MaxBytes {
				break
			}
			u := units[t]
			for _, k := range u.keys {
				errs = append(errs, r.Delete(ctx, k))
			}
			out.BySize += len(u.keys)
			out.Bytes += u.size
			total -= u.size
		}
	}
	return out, errors.Join(errs...)
}
