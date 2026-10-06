//go:build full

package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// DefaultLeaseTTL is how long a compaction lease lasts without being renewed: the time one
// hour's compaction takes, with margin. A holder renews it every third of this while it works.
const DefaultLeaseTTL = 2 * time.Minute

// LeaseKey is the lease for compacting one signal's hour (ADR-005):
// leases/compaction/<signal>/<yyyy>/<mm>/<dd>/<hh>.json
func LeaseKey(signal string, hour time.Time) string {
	h := hour.UTC()
	return fmt.Sprintf("leases/compaction/%s/%04d/%02d/%02d/%02d.json", signal, h.Year(), int(h.Month()), h.Day(), h.Hour())
}

// leaseRecord is what a lease object holds.
type leaseRecord struct {
	Holder   string    `json:"holder"`
	Acquired time.Time `json:"acquired"`
	Expires  time.Time `json:"expires"`
}

// lease is a held lease: the holder and the version of the object that says so.
type lease struct {
	r      store.Reader
	key    string
	holder string
	ttl    time.Duration
	tag    string
}

func encodeLease(rec leaseRecord) []byte {
	b, _ := json.Marshal(rec)
	return append(b, '\n')
}

// acquireLease takes a lease by conditional put, so of two holders only one succeeds. It takes
// over a lease that has expired, or one this holder already holds, by conditional replace on the
// version it read, so of two that find it expired only one succeeds. It returns nil when someone
// else holds it. Expiry is a time on the holder's clock, so central instances' clocks should
// agree to well within the TTL.
func acquireLease(ctx context.Context, r store.Reader, key, holder string, ttl time.Duration, now time.Time) (*lease, error) {
	rec := encodeLease(leaseRecord{Holder: holder, Acquired: now.UTC(), Expires: now.Add(ttl).UTC()})
	for attempt := 0; attempt < 3; attempt++ {
		err := r.Put(ctx, key, rec)
		if err == nil {
			return confirm(ctx, r, key, holder, ttl)
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		data, tag, err := r.GetWithTag(ctx, key)
		if errors.Is(err, os.ErrNotExist) {
			continue // released by deletion between the put and the get
		}
		if err != nil {
			return nil, err
		}
		var cur leaseRecord
		// An unreadable lease is no one's, and is taken like an expired one.
		if json.Unmarshal(data, &cur) == nil && cur.Holder != holder && now.Before(cur.Expires) {
			return nil, nil
		}
		err = r.ReplaceIf(ctx, key, rec, tag)
		if errors.Is(err, store.ErrChanged) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return confirm(ctx, r, key, holder, ttl)
	}
	return nil, nil
}

// confirm reads back the lease just written for the version tag renewals need.
func confirm(ctx context.Context, r store.Reader, key, holder string, ttl time.Duration) (*lease, error) {
	data, tag, err := r.GetWithTag(ctx, key)
	if err != nil {
		return nil, err
	}
	var cur leaseRecord
	if json.Unmarshal(data, &cur) != nil || cur.Holder != holder {
		return nil, nil
	}
	return &lease{r: r, key: key, holder: holder, ttl: ttl, tag: tag}, nil
}

// errLeaseLost is returned when a lease was taken over, because it was not renewed in time.
var errLeaseLost = errors.New("the compaction lease was taken over")

// renew extends the lease to ttl from now, if it is still ours.
func (l *lease) renew(ctx context.Context, now time.Time) error {
	rec := encodeLease(leaseRecord{Holder: l.holder, Acquired: now.UTC(), Expires: now.Add(l.ttl).UTC()})
	if err := l.r.ReplaceIf(ctx, l.key, rec, l.tag); err != nil {
		if errors.Is(err, store.ErrChanged) {
			return errLeaseLost
		}
		return err
	}
	c, err := confirm(ctx, l.r, l.key, l.holder, l.ttl)
	if err != nil {
		return err
	}
	if c == nil {
		return errLeaseLost
	}
	l.tag = c.tag
	return nil
}

// release expires the lease now, if it is still ours, so the next holder need not wait for the
// TTL. The object stays; it is replaced, never deleted, so a release cannot remove a lease
// another holder has since taken.
func (l *lease) release(ctx context.Context, now time.Time) {
	rec := encodeLease(leaseRecord{Holder: l.holder, Acquired: now.UTC(), Expires: now.UTC()})
	_ = l.r.ReplaceIf(ctx, l.key, rec, l.tag)
}

// withLease runs fn holding the lease on key, renewing it while fn runs. fn's context ends if
// the lease is lost. held is false, and fn not run, when another holder has the lease. An empty
// holder means no leases, for a laptop's folder that only one process reads: fn just runs.
func withLease(ctx context.Context, r store.Reader, key, holder string, ttl time.Duration, now func() time.Time, fn func(context.Context) error) (held bool, err error) {
	if holder == "" {
		return true, fn(ctx)
	}
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	l, err := acquireLease(ctx, r, key, holder, ttl, now())
	if err != nil || l == nil {
		return false, err
	}
	fctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	done := make(chan struct{})
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		tick := time.NewTicker(ttl / 3)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if err := l.renew(fctx, now()); err != nil && fctx.Err() == nil {
					cancel(err)
					return
				}
			}
		}
	}()
	err = fn(fctx)
	close(done)
	<-renewed
	if cause := context.Cause(fctx); cause != nil && ctx.Err() == nil {
		err = errors.Join(err, fmt.Errorf("lease %s: %w", key, cause))
	}
	// Release with a fresh context: ctx may be what ended the work.
	rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer rcancel()
	l.release(rctx, now())
	return true, err
}
