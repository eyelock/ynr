package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// Feed is the polling change feed (ADR-005): it tells the reader which batch files have landed
// since it last asked. It lists each signal's current and previous hour once for the collectors
// there, then keeps a listing position per collector and lists only the keys after it, so a
// poll reads what is new, not the whole hour. It holds only positions, which a restart rebuilds
// by listing from the start (NFR-19).
type Feed struct {
	r store.Reader

	mu  sync.Mutex
	pos map[string]string // "<signal>/<yyyy>/<mm>/<dd>/<hh>/<collector>/" -> the last key seen there
	// Ignored counts keys under the hours it polls that are not of the exact shape a reader
	// accepts (ADR-005).
	Ignored int
}

// NewFeed returns a feed over a store, with no positions yet: its first poll reads everything
// in the hours it covers.
func NewFeed(r store.Reader) *Feed { return &Feed{r: r, pos: map[string]string{}} }

// Positions is how many collector listing positions the feed holds.
func (f *Feed) Positions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pos)
}

// Poll returns, by signal, the batch keys that landed in the hour of now and the hour before
// since the last Poll. A batch uploaded after its hour and the next have passed is not seen
// here; it is read when its hour is next read whole, and by compaction.
func (f *Feed) Poll(ctx context.Context, now time.Time, signals []string) (map[string][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := now.UTC().Truncate(time.Hour)
	hours := []time.Time{cur.Add(-time.Hour), cur}
	oldest := HourPrefix("", hours[0])
	for k := range f.pos {
		if i := strings.IndexByte(k, '/'); i > 0 && len(k) >= i+15 && k[i:i+15] < oldest {
			delete(f.pos, k) // the hour left the feed
		}
	}
	out := map[string][]string{}
	var errs []error
	for _, sig := range signals {
		for _, hour := range hours {
			collectors, err := f.r.ListCollectors(ctx, sig, hour)
			if err != nil {
				errs = append(errs, fmt.Errorf("feed: %s %s: %w", sig, hour.Format("2006-01-02T15"), err))
				continue
			}
			for _, c := range collectors {
				prefix := HourPrefix(sig, hour) + c + "/"
				keys, err := f.r.ListAfter(ctx, prefix, f.pos[prefix])
				if err != nil {
					errs = append(errs, fmt.Errorf("feed: %s: %w", prefix, err))
					continue
				}
				last := f.pos[prefix]
				for _, k := range keys {
					b, err := store.ParseBatch(k)
					if err != nil || b.Signal != sig || !b.Hour.Equal(hour) || b.Collector != c {
						f.Ignored++
					} else {
						out[sig] = append(out[sig], k)
					}
					last = k
				}
				if last != "" {
					f.pos[prefix] = last
				}
			}
		}
	}
	return out, errors.Join(errs...)
}
