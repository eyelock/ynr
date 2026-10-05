//go:build full

package query

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"math/big"
	"strings"
	"time"

	_ "github.com/duckdb/duckdb-go/v2" // registers the duckdb driver

	"github.com/eyelock/ynr/internal/store"
)

//go:embed views.sql
var views string

// The OTLP JSON shapes DuckDB reads each signal's lines as. Only the fields the views use are
// named; read_json ignores the rest. Numbers that OTLP JSON may write as strings are JSON, and
// the views convert them.
const (
	kv         = `STRUCT(key VARCHAR, value JSON)[]`
	resource   = `resource STRUCT(attributes ` + kv + `)`
	scope      = `scope STRUCT(name VARCHAR, version VARCHAR)`
	tracesType = `STRUCT(` + resource + `, scopeSpans STRUCT(` + scope + `, spans STRUCT(` +
		`traceId VARCHAR, spanId VARCHAR, parentSpanId VARCHAR, name VARCHAR, kind JSON, ` +
		`startTimeUnixNano JSON, endTimeUnixNano JSON, attributes ` + kv + `, ` +
		`status STRUCT(code JSON, message VARCHAR))[])[])[]`
	logsType = `STRUCT(` + resource + `, scopeLogs STRUCT(` + scope + `, logRecords STRUCT(` +
		`timeUnixNano JSON, observedTimeUnixNano JSON, severityNumber JSON, severityText VARCHAR, ` +
		`eventName VARCHAR, body JSON, attributes ` + kv + `, traceId VARCHAR, spanId VARCHAR)[])[])[]`
	point    = `STRUCT(attributes ` + kv + `, startTimeUnixNano JSON, timeUnixNano JSON, asDouble JSON, asInt JSON)`
	hpoint   = `STRUCT(attributes ` + kv + `, startTimeUnixNano JSON, timeUnixNano JSON, count JSON, sum JSON)`
	metricsT = `STRUCT(` + resource + `, scopeMetrics STRUCT(` + scope + `, metrics STRUCT(` +
		`name VARCHAR, unit VARCHAR, gauge STRUCT(dataPoints ` + point + `[]), ` +
		`sum STRUCT(dataPoints ` + point + `[], aggregationTemporality JSON, isMonotonic BOOLEAN), ` +
		`histogram STRUCT(dataPoints ` + hpoint + `[], aggregationTemporality JSON), ` +
		`exponentialHistogram STRUCT(dataPoints ` + hpoint + `[], aggregationTemporality JSON))[])[])[]`
)

var signals = []struct{ name, column, typ string }{
	{store.Traces, "resourceSpans", tracesType},
	{store.Logs, "resourceLogs", logsType},
	{store.Metrics, "resourceMetrics", metricsT},
}

// The columns every view promotes from attributes, for queries and dashboards to filter on
// (ADR-005). Where a tool has its own name for the same thing, the first present wins.
var promoted = []struct {
	column string
	keys   []string
}{
	{"service", []string{"service.name"}},
	{"item_key", []string{"ynf.item.key"}},
	{"step_id", []string{"ynf.step.id"}},
	{"run_id", []string{"ynf.run.id"}},
	{"lane", []string{"ynf.lane"}},
	{"harness", []string{"ynf.lane.harness", "ynh.harness.name"}},
	{"focus", []string{"ynf.lane.focus", "ynh.run.focus"}},
	{"repo", []string{"ynf.repo", "vcs.repository.url.full"}},
	{"outcome", []string{"ynf.outcome", "ynh.run.outcome", "ynm.outcome"}},
	{"actor", []string{"user.name"}},
	{"model", []string{"gen_ai.response.model", "gen_ai.request.model"}},
	{"provenance", []string{"ynr.provenance"}},
	{"collector_id", []string{"ynr.collector.id"}},
}

func promotedSQL() string {
	var cols []string
	for _, p := range promoted {
		var picks []string
		for _, k := range p.keys {
			picks = append(picks, "pick(resource, attributes, "+literal(k)+")")
		}
		cols = append(cols, "coalesce("+strings.Join(picks, ", ")+") AS "+p.column)
	}
	return strings.Join(cols, ",\n       ")
}

// Run runs a named query over the store's batches received in the hours its window can touch.
func Run(ctx context.Context, r store.Reader, q *Query, p Params) (*Result, error) {
	if err := q.Check(p); err != nil {
		return nil, err
	}
	setup := strings.ReplaceAll(views, "@promoted@", promotedSQL())
	for _, s := range signals {
		files, err := batches(ctx, r, q, s.name, p)
		if err != nil {
			return nil, err
		}
		setup = strings.ReplaceAll(setup, "@"+s.name+"@", relation(s.column, s.typ, files))
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	// One connection, so the macros and views exist for the query that follows.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, setup); err != nil {
		return nil, fmt.Errorf("query: preparing the views: %w", err)
	}
	rows, err := db.QueryContext(ctx, q.SQL,
		sql.Named("since", p.Since.UTC()), sql.Named("until", p.Until.UTC()),
		sql.Named("arg", p.Arg), sql.Named("lane", p.Lane))
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", q.Name, err)
	}
	defer func() { _ = rows.Close() }()
	res := &Result{}
	if res.Columns, err = rows.Columns(); err != nil {
		return nil, err
	}
	for rows.Next() {
		vals := make([]any, len(res.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			vals[i] = plain(v)
		}
		res.Rows = append(res.Rows, vals)
	}
	return res, rows.Err()
}

// batches lists the signal's batch files in the hours the window can touch, or none when the
// query does not read the signal.
func batches(ctx context.Context, r store.Reader, q *Query, signal string, p Params) ([]string, error) {
	var reads bool
	for _, s := range q.Signals {
		reads = reads || s == signal
	}
	if !reads {
		return nil, nil
	}
	var files []string
	for _, h := range Hours(p.Since, p.Until) {
		keys, err := r.List(ctx, HourPrefix(signal, h))
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if strings.HasSuffix(k, ".jsonl.gz") {
				files = append(files, r.Location(k))
			}
		}
	}
	return files, nil
}

// relation reads the files as newline-delimited OTLP JSON, or is empty with the same columns
// when there are none, since read_json refuses an empty list.
func relation(column, typ string, files []string) string {
	if len(files) == 0 {
		return fmt.Sprintf("(SELECT NULL::%s AS %s, NULL::VARCHAR AS filename WHERE false)", typ, column)
	}
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = literal(f)
	}
	return fmt.Sprintf("read_json([%s], format = 'newline_delimited', compression = 'gzip', "+
		"filename = true, ignore_errors = true, maximum_object_size = 16777216, columns = {%s: '%s'})",
		strings.Join(quoted, ", "), column, typ)
}

// literal quotes a string for SQL.
func literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// plain turns what the driver returns into values that print and encode simply.
func plain(v any) any {
	switch x := v.(type) {
	case time.Time:
		return x.UTC()
	case *big.Int:
		if x.IsInt64() {
			return x.Int64()
		}
		return x.String()
	case []byte:
		return string(x)
	}
	return v
}
