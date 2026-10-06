package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/eyelock/ynr/internal/query"
	"github.com/eyelock/ynr/internal/store"
)

// retainEvery is how often ynr serve applies retention to its folder store.
const retainEvery = 10 * time.Minute

// startRetention applies retention to a readable store now and every retainEvery, in both
// builds: deleting needs no DuckDB, so a slim ynr serve's folder never grows without bound.
func startRetention(ctx context.Context, storeURL string, p store.Retention, stderr io.Writer) (stop func()) {
	if storeURL == "" {
		return func() {}
	}
	r, err := store.OpenReader(storeURL)
	if err != nil {
		return func() {}
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(retainEvery)
		defer tick.Stop()
		for {
			if _, err := store.Retain(rctx, r, p, time.Now()); err != nil && rctx.Err() == nil {
				_, _ = fmt.Fprintf(stderr, "ynr: retention: %v\n", err)
			}
			select {
			case <-rctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// loadErasure reads the erasure list, if one is named, for every query this process runs.
func loadErasure(path string, stderr io.Writer) int {
	if path == "" {
		return ExitOK
	}
	handles, err := query.LoadErasure(path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: --erase: %v\n", err)
		return ExitConfig
	}
	query.SetErasure(handles)
	return ExitOK
}
