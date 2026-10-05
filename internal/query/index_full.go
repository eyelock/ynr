//go:build full

package query

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// indexed are the signals whose records carry item keys.
var indexed = []string{store.Traces, store.Logs}

// buildIndex writes a day's item index from the day's compacted parts (ADR-005): each item key
// with the hours it appears in, each step's trace id, and its first and last times. Rows with no
// item key record which manifest of each hour the index was built from, so a reader knows when
// an hour has changed since and must be read whole.
func buildIndex(ctx context.Context, r store.Reader, day time.Time) error {
	var data, cover []string
	for _, sig := range indexed {
		for i := 0; i < 24; i++ {
			hour := day.Add(time.Duration(i) * time.Hour)
			h, err := ReadHour(ctx, r, sig, hour)
			if err != nil {
				return err
			}
			if h.Manifest == nil {
				continue
			}
			var locs []string
			for _, p := range h.Parts {
				locs = append(locs, r.Location(p))
			}
			at := "TIMESTAMP " + literal(hour.UTC().Format("2006-01-02 15:04:05"))
			data = append(data, fmt.Sprintf("SELECT item_key, step_id, trace_id, time, %s AS hour, %s AS signal FROM %s",
				at, literal(sig), parquet(locs)))
			cover = append(cover, fmt.Sprintf("(%s, %s, %d)", at, literal(sig), h.Manifest.N))
		}
	}
	if len(cover) == 0 {
		return nil
	}
	tmp, err := os.MkdirTemp("", "ynr-index-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	out := filepath.Join(tmp, "index.parquet")
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	stmt := fmt.Sprintf(`COPY (
  SELECT item_key, hour, signal, step_id, trace_id, min(time) AS first_seen, max(time) AS last_seen, NULL::INTEGER AS manifest
  FROM (%s) WHERE item_key IS NOT NULL GROUP BY ALL
  UNION ALL BY NAME
  SELECT NULL::VARCHAR AS item_key, hour, signal, manifest FROM (VALUES %s) AS c(hour, signal, manifest)
  ORDER BY item_key NULLS FIRST, hour
) TO %s (FORMAT parquet, COMPRESSION zstd)`, strings.Join(data, "\nUNION ALL "), strings.Join(cover, ", "), literal(out))
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("item index for %s: %w", day.Format("2006-01-02"), err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	return r.Replace(ctx, store.IndexKey(day), b)
}

// itemFiles are the files to read for one item's history: an hour the index covers, as it now
// stands, is read only if the index names the item in it; any other hour is read whole.
func itemFiles(ctx context.Context, r store.Reader, item string, hours []time.Time) (Files, error) {
	cover, hits, err := readIndex(ctx, r, item, hours)
	if err != nil {
		return Files{}, err
	}
	f := Files{Parts: map[string][]string{}, Batches: map[string][]string{}}
	for _, sig := range indexed {
		for _, t := range hours {
			h, err := ReadHour(ctx, r, sig, t)
			if err != nil {
				return f, err
			}
			k := sig + "@" + t.UTC().Format(time.RFC3339)
			if h.Manifest != nil && len(h.Batches) == 0 && cover[k] == h.Manifest.N && !hits[k] {
				continue
			}
			for _, p := range h.Parts {
				f.Parts[sig] = append(f.Parts[sig], r.Location(p))
			}
			for _, b := range h.Batches {
				f.Batches[sig] = append(f.Batches[sig], r.Location(b))
			}
		}
	}
	return f, nil
}

// readIndex reads the index files of the days the hours fall in: which manifest of each
// signal's hour each covers, and the hours the item appears in.
func readIndex(ctx context.Context, r store.Reader, item string, hours []time.Time) (cover map[string]int, hits map[string]bool, err error) {
	cover, hits = map[string]int{}, map[string]bool{}
	var locs []string
	seen := map[string]bool{}
	for _, t := range hours {
		key := store.IndexKey(t.Truncate(24 * time.Hour))
		if seen[key] {
			continue
		}
		seen[key] = true
		keys, err := r.List(ctx, key[:strings.LastIndexByte(key, '/')+1])
		if err != nil {
			return nil, nil, err
		}
		for _, k := range keys {
			if k == key {
				locs = append(locs, r.Location(k))
			}
		}
	}
	if len(locs) == 0 {
		return cover, hits, nil
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT signal, hour, manifest, item_key IS NOT NULL FROM `+parquet(locs)+
		` WHERE item_key IS NULL OR item_key = $item`, sql.Named("item", item))
	if err != nil {
		return nil, nil, fmt.Errorf("reading the item index: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sig string
		var hour time.Time
		var manifest sql.NullInt64
		var hit bool
		if err := rows.Scan(&sig, &hour, &manifest, &hit); err != nil {
			return nil, nil, err
		}
		k := sig + "@" + hour.UTC().Format(time.RFC3339)
		if hit {
			hits[k] = true
		} else {
			cover[k] = int(manifest.Int64)
		}
	}
	return cover, hits, rows.Err()
}
