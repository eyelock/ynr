package query

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// Manifest says which batches an hour's compacted part covers (ADR-005). It is written after
// the part, so a manifest never names a part that is not there.
type Manifest struct {
	Signal string    `json:"signal"`
	Hour   time.Time `json:"hour"`
	N      int       `json:"n"`
	Parts  []string  `json:"parts"`
	// Batches are the batch keys the parts hold every record of.
	Batches   []string  `json:"batches"`
	Records   int64     `json:"records"`
	Compacted time.Time `json:"compacted"`
}

// Hour is what a reader reads for one signal's hour: the latest manifest's parts, and every
// batch that manifest does not cover, so a batch that lands late is never invisible.
type Hour struct {
	Manifest *Manifest // nil before the hour's first compaction
	Parts    []string  // keys
	Batches  []string  // keys not covered by the manifest
	// All is every compacted key, including superseded parts and manifests.
	All []string
	// Covered are batch keys still in the store that the manifest covers.
	Covered []string
	// Ignored counts keys under the hour's prefixes that are not of the exact shape a reader
	// accepts (ADR-005).
	Ignored int
}

// ReadHour lists one signal's hour.
func ReadHour(ctx context.Context, r store.Reader, signal string, hour time.Time) (*Hour, error) {
	h := &Hour{}
	keys, err := r.List(ctx, store.CompactedPrefix(signal, hour))
	if err != nil {
		return nil, err
	}
	latest := 0
	for _, k := range keys {
		n, manifest, err := store.ParseCompacted(k)
		if err != nil {
			h.Ignored++
			continue
		}
		h.All = append(h.All, k)
		if manifest && n > latest {
			latest = n
		}
	}
	if latest > 0 {
		key := store.ManifestKey(signal, hour, latest)
		b, err := r.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		var m Manifest
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if m.Signal != signal || !m.Hour.Equal(hour.UTC()) || m.N != latest {
			return nil, fmt.Errorf("%s does not describe its own hour", key)
		}
		for _, p := range m.Parts {
			if n, manifest, err := store.ParseCompacted(p); err != nil || manifest || n > latest ||
				p != store.PartKey(signal, hour, n) {
				return nil, fmt.Errorf("%s names a part outside its hour: %s", key, p)
			}
		}
		h.Manifest, h.Parts = &m, m.Parts
	}
	keys, err = r.List(ctx, HourPrefix(signal, hour))
	if err != nil {
		return nil, err
	}
	covered := map[string]bool{}
	if h.Manifest != nil {
		for _, b := range h.Manifest.Batches {
			covered[b] = true
		}
	}
	for _, k := range keys {
		b, err := store.ParseBatch(k)
		if err != nil || b.Signal != signal || !b.Hour.Equal(hour.UTC()) {
			h.Ignored++
			continue
		}
		if covered[k] {
			h.Covered = append(h.Covered, k)
		} else {
			h.Batches = append(h.Batches, k)
		}
	}
	return h, nil
}

// Files are the locations to read for some hours of each signal.
type Files struct {
	Parts, Batches map[string][]string
}

// ReadHours resolves the given hours of each signal into the locations to read.
func ReadHours(ctx context.Context, r store.Reader, signals []string, hours []time.Time) (Files, error) {
	f := Files{Parts: map[string][]string{}, Batches: map[string][]string{}}
	for _, s := range signals {
		for _, t := range hours {
			h, err := ReadHour(ctx, r, s, t)
			if err != nil {
				return f, err
			}
			parts, err := local(ctx, r, h.Parts)
			if err != nil {
				return f, err
			}
			batches, err := local(ctx, r, h.Batches)
			if err != nil {
				return f, err
			}
			f.Parts[s] = append(f.Parts[s], parts...)
			f.Batches[s] = append(f.Batches[s], batches...)
		}
	}
	return f, nil
}
