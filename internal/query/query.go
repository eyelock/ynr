// Package query holds the named queries (ADR-005): written once in DuckDB SQL over the store's
// records, and used by both ynr query and, later, the dashboard. The engine that runs them needs
// DuckDB, so only the full build has it (ADR-001); this file is shared by both builds.
package query

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// ErrSlim is returned by Run in the slim build, which has no DuckDB.
var ErrSlim = errors.New("queries need the full build of ynr, which includes DuckDB")

// Query is one named query.
type Query struct {
	Name string
	// Help is one line for ynr query's list.
	Help string
	// Arg names the query's one positional argument, such as an item key; empty for none.
	Arg string
	// Since is how far back the query looks when --since is not given.
	Since time.Duration
	// Signals are the signals the query reads; the others are not opened.
	Signals []string
	// Indexed queries read only the hours the item index names for the item in Arg.
	Indexed bool
	// SQL is DuckDB SQL over spans, logs and metric_points (batches.sql), with the
	// named parameters $since and $until (timestamps), $arg and $lane (text).
	SQL string
}

// Params are a query's inputs.
type Params struct {
	Arg          string
	Since, Until time.Time
	Lane         string
}

// Result is a query's rows, in its columns' order. Values are strings, numbers, booleans, times
// or nil.
type Result struct {
	Columns []string
	Rows    [][]any
	// Files is how many parts and batches a direct read opened, for tests and debugging.
	Files int
}

// Hours are the hours whose batches can hold records from since to until: a record is never
// received before it happens, and an hour either side covers clock skew between machines.
func Hours(since, until time.Time) []time.Time {
	var out []time.Time
	for h := since.UTC().Truncate(time.Hour).Add(-time.Hour); !h.After(until.UTC().Add(time.Hour)); h = h.Add(time.Hour) {
		out = append(out, h)
	}
	return out
}

// HourPrefix is where a signal's batches received in an hour are, as store.BatchKey lays them out.
func HourPrefix(signal string, h time.Time) string {
	h = h.UTC()
	return fmt.Sprintf("%s/%04d/%02d/%02d/%02d/", signal, h.Year(), int(h.Month()), h.Day(), h.Hour())
}

// Lookup finds a named query.
func Lookup(name string) (*Query, bool) {
	for i := range Catalogue {
		if Catalogue[i].Name == name {
			return &Catalogue[i], true
		}
	}
	return nil, false
}

// Names lists the named queries, sorted.
func Names() []string {
	var out []string
	for _, q := range Catalogue {
		out = append(out, q.Name)
	}
	sort.Strings(out)
	return out
}

// Check validates params against the query before it runs.
func (q *Query) Check(p Params) error {
	switch {
	case q.Arg != "" && strings.TrimSpace(p.Arg) == "":
		return fmt.Errorf("%s needs %s", q.Name, q.Arg)
	case q.Arg == "" && p.Arg != "":
		return fmt.Errorf("%s takes no argument", q.Name)
	case !p.Since.Before(p.Until):
		return errors.New("--since must be before --until")
	}
	return nil
}

// Catalogue is every named query. Runs are ynh.run spans, one per ynh agent run, whether a
// factory or a person started it; ynf stamps the lane on a factory's.
var Catalogue = []Query{
	{
		Name:    "runs",
		Help:    "runs by outcome for each lane (--lane to pick one)",
		Since:   24 * time.Hour,
		Signals: []string{store.Traces},
		SQL: `
SELECT coalesce(lane, '(by hand)') AS lane, coalesce(outcome, '(none)') AS outcome,
       count(*) AS runs, round(median(duration_ms) / 1000, 1) AS median_s,
       round(sum(cost_usd), 4) AS cost_usd, max(time) AS last
FROM spans
WHERE name = 'ynh.run' AND time >= $since AND time < $until AND ($lane = '' OR lane = $lane)
GROUP BY ALL
ORDER BY lane, runs DESC, outcome`,
	},
	{
		Name:    "item",
		Help:    "what happened to one work item, by its key: its steps, runs and events in order",
		Arg:     "<item key>",
		Indexed: true,
		Since:   7 * 24 * time.Hour,
		Signals: []string{store.Traces, store.Logs},
		SQL: `
SELECT time, 'span' AS record, name, service, outcome, round(duration_ms, 1) AS duration_ms,
       step_id, trace_id, span_id
FROM spans
WHERE item_key = $arg AND time >= $since AND time < $until
UNION ALL
SELECT time, 'event', event, service, outcome, NULL, step_id, trace_id, span_id
FROM logs
WHERE item_key = $arg AND event IS NOT NULL AND time >= $since AND time < $until
ORDER BY time, record DESC, name`,
	},
	{
		Name:    "trace",
		Help:    "one trace's spans as a tree, by trace id",
		Arg:     "<trace id>",
		Since:   7 * 24 * time.Hour,
		Signals: []string{store.Traces},
		SQL: `
WITH RECURSIVE t AS (
  SELECT * FROM spans WHERE trace_id = lower($arg) AND time >= $since AND time < $until
), tree AS (
  SELECT span_id, 0 AS depth, [time] AS path FROM t
  WHERE parent_span_id IS NULL OR parent_span_id NOT IN (SELECT span_id FROM t)
  UNION ALL
  SELECT t.span_id, tree.depth + 1, list_append(tree.path, t.time)
  FROM t JOIN tree ON t.parent_span_id = tree.span_id
  WHERE tree.depth < 64
)
SELECT t.time, tree.depth, t.name, t.service, t.kind, t.status, t.outcome,
       round(t.duration_ms, 1) AS duration_ms, t.span_id, t.parent_span_id
FROM t JOIN tree USING (span_id)
ORDER BY tree.path, t.span_id`,
	},
	{
		Name:    "cost",
		Help:    "what runs cost and the tokens they used, by model, as the runners reported it",
		Since:   7 * 24 * time.Hour,
		Signals: []string{store.Traces},
		SQL: `
SELECT coalesce(model, '(unknown)') AS model, count(*) AS runs,
       round(sum(cost_usd), 4) AS cost_usd,
       sum(TRY_CAST(attr(attributes, 'gen_ai.usage.input_tokens') AS BIGINT)) AS input_tokens,
       sum(TRY_CAST(attr(attributes, 'gen_ai.usage.output_tokens') AS BIGINT)) AS output_tokens
FROM spans
WHERE name = 'ynh.run' AND time >= $since AND time < $until AND ($lane = '' OR lane = $lane)
GROUP BY ALL
ORDER BY cost_usd DESC NULLS LAST, model`,
	},
}
