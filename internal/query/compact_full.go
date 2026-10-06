//go:build full

package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// Compact compacts every closed hour in the lookback that has batches its manifest does not
// cover, and deletes what newer manifests have made redundant once it has been kept long enough.
// A new part holds the hour's previous part and its new batches, so batches can be deleted.
func Compact(ctx context.Context, r store.Reader, c Compaction, now time.Time) (compacted int, err error) {
	now = now.UTC()
	last := now.Add(-time.Hour - c.Grace).Truncate(time.Hour) // the last hour closed for long enough
	var errs []error
	days := map[time.Time]bool{}   // days whose item index must be rewritten
	rolled := map[time.Time]bool{} // days whose rollups must be rewritten
	for _, sig := range signals {
		for t := now.Add(-c.Lookback).Truncate(time.Hour); !t.After(last); t = t.Add(time.Hour) {
			h, err := ReadHour(ctx, r, sig.name, t)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if len(h.Batches) > 0 {
				if err := compactHour(ctx, r, sig, t, h, now); err != nil {
					errs = append(errs, fmt.Errorf("compacting %s %s: %w", sig.name, t.Format("2006-01-02T15"), err))
					continue
				}
				compacted++
				if slices.Contains(indexed, sig.name) {
					days[t.Truncate(24*time.Hour)] = true
				}
				if sig.name != store.Logs {
					rolled[t.Truncate(24*time.Hour)] = true
				}
				if h, err = ReadHour(ctx, r, sig.name, t); err != nil {
					errs = append(errs, err)
					continue
				}
			}
			errs = append(errs, prune(ctx, r, h, c.Keep, now))
		}
	}
	// A day with compacted hours but no index, after a crash between the two, is indexed too.
	for t := now.Add(-c.Lookback).Truncate(24 * time.Hour); !t.After(last); t = t.Add(24 * time.Hour) {
		if !days[t] {
			missing, err := indexMissing(ctx, r, t)
			errs = append(errs, err)
			days[t] = missing
		}
	}
	for day, rebuild := range days {
		if rebuild {
			errs = append(errs, buildIndex(ctx, r, day))
		}
	}
	errs = append(errs, rollUp(ctx, r, c, now, rolled))
	return compacted, errors.Join(errs...)
}

// rollUp writes the rollups of each closed day in the lookback whose hours were compacted in
// this pass, or that has compacted hours but no rollup, then of each closed month holding one.
func rollUp(ctx context.Context, r store.Reader, c Compaction, now time.Time, touched map[time.Time]bool) error {
	var errs []error
	months := map[time.Time]bool{}
	for d := now.Add(-c.Lookback).Truncate(24 * time.Hour); !d.Add(24 * time.Hour).Add(time.Hour + c.Grace).After(now); d = d.AddDate(0, 0, 1) {
		need := touched[d]
		if !need {
			have, err := exists(ctx, r, store.RollupKey(store.RollupRuns, store.Daily, d))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !have {
				for _, sig := range []string{store.Traces, store.Metrics} {
					ks, err := r.List(ctx, "compacted/"+sig+"/"+d.Format("2006/01/02")+"/")
					errs = append(errs, err)
					need = need || len(ks) > 0
				}
			}
		}
		if need {
			if err := rollDay(ctx, r, d); err != nil {
				errs = append(errs, err)
				continue
			}
			months[time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)] = true
		}
	}
	for m := range months {
		if !m.AddDate(0, 1, 0).Add(time.Hour + c.Grace).After(now) {
			errs = append(errs, rollMonth(ctx, r, m))
		}
	}
	return errors.Join(errs...)
}

// indexMissing reports whether a day has compacted hours but no item index.
func indexMissing(ctx context.Context, r store.Reader, day time.Time) (bool, error) {
	key := store.IndexKey(day)
	keys, err := r.List(ctx, key[:strings.LastIndexByte(key, '/')+1])
	if err != nil || slices.Contains(keys, key) {
		return false, err
	}
	for _, sig := range indexed {
		ks, err := r.List(ctx, "compacted/"+sig+"/"+day.Format("2006/01/02")+"/")
		if err != nil {
			return false, err
		}
		if len(ks) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func compactHour(ctx context.Context, r store.Reader, sig signal, hour time.Time, h *Hour, now time.Time) error {
	n := 1
	var covered []string
	var parts []string
	if h.Manifest != nil {
		n = h.Manifest.N + 1
		covered = append(covered, h.Manifest.Batches...)
		for _, p := range h.Parts {
			parts = append(parts, r.Location(p))
		}
	}
	var batches []string
	for _, k := range h.Batches {
		batches = append(batches, r.Location(k))
	}
	partKey := store.PartKey(sig.name, hour, n)

	tmp, err := os.MkdirTemp("", "ynr-compact-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	out := filepath.Join(tmp, "part.parquet")

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	setup := macros + batchSQL(map[string][]string{sig.name: batches}) +
		combinedSQL(map[string][]string{sig.name: parts})
	if _, err := db.ExecContext(ctx, setup); err != nil {
		return err
	}
	// The part keeps each record once, ordered by time, and names itself as every record's file.
	copySQL := fmt.Sprintf("COPY (SELECT * REPLACE (%s AS file) FROM %s ORDER BY time, record_id) TO %s (FORMAT parquet, COMPRESSION zstd)",
		literal(partKey), sig.table, literal(out))
	if _, err := db.ExecContext(ctx, copySQL); err != nil {
		return err
	}
	var records int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+parquet([]string{out})).Scan(&records); err != nil {
		return err
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	if err := r.Put(ctx, partKey, data); err != nil {
		return err
	}
	m := Manifest{Signal: sig.name, Hour: hour.UTC(), N: n, Parts: []string{partKey},
		Batches: append(covered, h.Batches...), Records: records, Compacted: now}
	slices.Sort(m.Batches)
	m.Batches = slices.Compact(m.Batches)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// The manifest last: until it lands, readers read the batches, and the part is unused.
	return r.Put(ctx, store.ManifestKey(sig.name, hour, n), append(b, '\n'))
}

// prune deletes, once the latest manifest is older than keep, the batches it covers and the
// parts and manifests it supersedes.
func prune(ctx context.Context, r store.Reader, h *Hour, keep time.Duration, now time.Time) error {
	if h.Manifest == nil || now.Sub(h.Manifest.Compacted) < keep {
		return nil
	}
	latest := map[string]bool{store.ManifestKey(h.Manifest.Signal, h.Manifest.Hour, h.Manifest.N): true}
	for _, p := range h.Manifest.Parts {
		latest[p] = true
	}
	var errs []error
	for _, k := range h.Covered {
		errs = append(errs, r.Delete(ctx, k))
	}
	for _, k := range h.All {
		if !latest[k] {
			errs = append(errs, r.Delete(ctx, k))
		}
	}
	return errors.Join(errs...)
}
