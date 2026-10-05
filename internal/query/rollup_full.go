//go:build full

package query

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// The rollups' columns, for an empty view when a window has none.
const (
	runRollupCols = `NULL::DATE AS day, NULL::VARCHAR AS lane, NULL::VARCHAR AS model, NULL::VARCHAR AS outcome,
  NULL::VARCHAR AS harness, NULL::VARCHAR AS repo, NULL::BIGINT AS runs, NULL::DOUBLE AS cost_usd,
  NULL::HUGEINT AS input_tokens, NULL::HUGEINT AS output_tokens, NULL::DOUBLE AS duration_ms`
	metricRollupCols = `NULL::DATE AS day, NULL::VARCHAR AS name, NULL::VARCHAR AS unit, NULL::VARCHAR AS type,
  NULL::VARCHAR AS temporality, NULL::VARCHAR AS service, NULL::STRUCT(k VARCHAR, v VARCHAR)[] AS attributes,
  NULL::DOUBLE AS total, NULL::DOUBLE AS count, NULL::DOUBLE AS min, NULL::DOUBLE AS max, NULL::BIGINT AS points`
)

// rollDay writes a day's rollups from the hours it was received in, so every record is counted
// on exactly one day: run totals from each run's span, and every metric by its attributes. A
// cumulative metric's total is its last value in the day; a delta metric's is the day's sum.
func rollDay(ctx context.Context, r store.Reader, day time.Time) error {
	var hours []time.Time
	for i := 0; i < 24; i++ {
		hours = append(hours, day.Add(time.Duration(i)*time.Hour))
	}
	files, err := ReadHours(ctx, r, []string{store.Traces, store.Metrics}, hours)
	if err != nil {
		return err
	}
	d := "DATE " + literal(day.Format("2006-01-02"))
	return writeRollups(ctx, r, day, store.Daily,
		macros+batchSQL(files.Batches)+combinedSQL(files.Parts),
		fmt.Sprintf(`SELECT %s AS day, lane, model, outcome, harness, repo, count(*) AS runs,
  sum(cost_usd) AS cost_usd,
  sum(TRY_CAST(attr(attributes, 'gen_ai.usage.input_tokens') AS BIGINT)) AS input_tokens,
  sum(TRY_CAST(attr(attributes, 'gen_ai.usage.output_tokens') AS BIGINT)) AS output_tokens,
  sum(duration_ms) AS duration_ms
FROM spans WHERE name = 'ynh.run' GROUP BY ALL`, d),
		fmt.Sprintf(`WITH series AS (
  SELECT name, unit, type, temporality, service, attributes, resource, start_time,
         arg_max(value, time) AS last, sum(value) AS sum, min(value) AS min, max(value) AS max,
         sum(count) AS count, count(*) AS points
  FROM metric_points GROUP BY name, unit, type, temporality, service, attributes, resource, start_time
)
SELECT %s AS day, name, unit, type, temporality, service, attributes,
  sum(CASE WHEN type = 'gauge' THEN NULL WHEN temporality = 'cumulative' THEN last ELSE sum END) AS total,
  sum(count) AS count, min(min) AS min, max(max) AS max, sum(points) AS points
FROM series GROUP BY ALL`, d))
}

// rollMonth writes a month's rollups from its days': run totals summed, and each metric's total
// summed for delta metrics and taken from the last day for cumulative ones.
func rollMonth(ctx context.Context, r store.Reader, month time.Time) error {
	var runs, metrics []string
	for d := month; d.Month() == month.Month(); d = d.AddDate(0, 0, 1) {
		for kind, list := range map[string]*[]string{store.RollupRuns: &runs, store.RollupMetrics: &metrics} {
			if ok, err := exists(ctx, r, store.RollupKey(kind, store.Daily, d)); err != nil {
				return err
			} else if ok {
				*list = append(*list, r.Location(store.RollupKey(kind, store.Daily, d)))
			}
		}
	}
	m := "DATE " + literal(month.Format("2006-01-02"))
	setup := fmt.Sprintf("CREATE VIEW run_days AS %s;\nCREATE VIEW metric_days AS %s;",
		rollupSource(runs, runRollupCols), rollupSource(metrics, metricRollupCols))
	return writeRollups(ctx, r, month, store.Monthly, setup,
		fmt.Sprintf(`SELECT %s AS day, lane, model, outcome, harness, repo, sum(runs) AS runs, sum(cost_usd) AS cost_usd,
  sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens, sum(duration_ms) AS duration_ms
FROM run_days GROUP BY ALL`, m),
		fmt.Sprintf(`SELECT %s AS day, name, unit, type, temporality, service, attributes,
  CASE WHEN any_value(temporality) = 'cumulative' THEN arg_max(total, day) ELSE sum(total) END AS total,
  sum(count) AS count, min(min) AS min, max(max) AS max, sum(points) AS points
FROM metric_days GROUP BY ALL`, m))
}

func writeRollups(ctx context.Context, r store.Reader, t time.Time, period, setup, runs, metrics string) error {
	tmp, err := os.MkdirTemp("", "ynr-rollup-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, setup); err != nil {
		return err
	}
	for kind, q := range map[string]string{store.RollupRuns: runs, store.RollupMetrics: metrics} {
		out := filepath.Join(tmp, kind+".parquet")
		if _, err := db.ExecContext(ctx, fmt.Sprintf("COPY (%s) TO %s (FORMAT parquet, COMPRESSION zstd)", q, literal(out))); err != nil {
			return fmt.Errorf("%s %s rollup for %s: %w", period, kind, t.Format("2006-01-02"), err)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			return err
		}
		if err := r.Replace(ctx, store.RollupKey(kind, period, t), b); err != nil {
			return err
		}
	}
	return nil
}

// rollupSource reads rollup files, or is empty with the rollup's columns.
func rollupSource(files []string, cols string) string {
	if len(files) == 0 {
		return "SELECT " + cols + " WHERE false"
	}
	return "SELECT * FROM " + strings.TrimSuffix(parquet(files), ")") + ", union_by_name = true)"
}

func exists(ctx context.Context, r store.Reader, key string) (bool, error) {
	keys, err := r.List(ctx, key[:strings.LastIndexByte(key, '/')+1])
	return slices.Contains(keys, key), err
}

// rollupFiles are the rollups covering a window, in whole days: a month's rollup where the
// window holds the whole month, and the days' rollups otherwise.
func rollupFiles(ctx context.Context, r store.Reader, kind string, since, until time.Time) ([]string, error) {
	var out []string
	from, to := since.UTC().Truncate(24*time.Hour), until.UTC()
	for d := from; d.Before(to); {
		month := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
		next := month.AddDate(0, 1, 0)
		if d.Equal(month) && !next.After(to) {
			key := store.RollupKey(kind, store.Monthly, month)
			ok, err := exists(ctx, r, key)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, r.Location(key))
				d = next
				continue
			}
		}
		key := store.RollupKey(kind, store.Daily, d)
		ok, err := exists(ctx, r, key)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, r.Location(key))
		}
		d = d.AddDate(0, 0, 1)
	}
	return out, nil
}

// runRollups answers a long-range query from the rollups alone.
func runRollups(ctx context.Context, r store.Reader, q *Query, p Params) (*Result, error) {
	runs, err := rollupFiles(ctx, r, store.RollupRuns, p.Since, p.Until)
	if err != nil {
		return nil, err
	}
	metrics, err := rollupFiles(ctx, r, store.RollupMetrics, p.Since, p.Until)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	setup := fmt.Sprintf("CREATE VIEW run_rollups AS %s;\nCREATE VIEW metric_rollups AS %s;",
		rollupSource(runs, runRollupCols), rollupSource(metrics, metricRollupCols))
	if _, err := db.ExecContext(ctx, setup); err != nil {
		return nil, err
	}
	rq := *q
	rq.SQL = q.RollupSQL
	res, err := runOn(ctx, db, &rq, p)
	if res != nil {
		res.Files, res.FromRollups = len(runs)+len(metrics), true
	}
	return res, err
}
