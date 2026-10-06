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

//go:embed macros.sql
var macros string

//go:embed batches.sql
var batchViews string

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

// signal is a signal's OTLP column and type, and the table the named queries read it as.
type signal struct{ name, column, typ, table string }

var signals = []signal{
	{store.Traces, "resourceSpans", tracesType, "spans"},
	{store.Logs, "resourceLogs", logsType, "logs"},
	{store.Metrics, "resourceMetrics", metricsT, "metric_points"},
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

// Run runs a named query over the store's batches received in the hours its window can touch,
// reading them directly: what ynr query does when no ynr serve is answering.
func Run(ctx context.Context, r store.Reader, q *Query, p Params) (*Result, error) {
	if err := q.Check(p); err != nil {
		return nil, err
	}
	read := ReadHours
	if q.Indexed {
		read = func(ctx context.Context, r store.Reader, _ []string, hours []time.Time) (Files, error) {
			return itemFiles(ctx, r, p.Arg, hours)
		}
	}
	files, err := read(ctx, r, q.Signals, Hours(p.Since, p.Until))
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	// One connection, so the macros and views exist for the query that follows.
	db.SetMaxOpenConns(1)
	setup := macros + batchSQL(files.Batches) + combinedSQL(files.Parts)
	if _, err := db.ExecContext(ctx, setup); err != nil {
		return nil, fmt.Errorf("query: preparing the views: %w", err)
	}
	res, err := runOn(ctx, db, q, p)
	if res != nil {
		for _, fs := range []map[string][]string{files.Parts, files.Batches} {
			for _, f := range fs {
				res.Files += len(f)
			}
		}
	}
	return res, err
}

// batchSQL points the batch views at the given files of each signal.
func batchSQL(files map[string][]string) string {
	s := strings.ReplaceAll(batchViews, "@promoted@", promotedSQL())
	for _, sig := range signals {
		s = strings.ReplaceAll(s, "@"+sig.name+"@", relation(sig.column, sig.typ, files[sig.name]))
	}
	return s
}

// combinedSQL defines spans, logs and metric_points as the batch views together with the
// compacted parts, keeping one copy of each record: a batch re-shipped after its hour was
// compacted holds records the part already has.
func combinedSQL(parts map[string][]string) string {
	var b strings.Builder
	for _, sig := range signals {
		src := "SELECT * FROM batch_" + sig.table
		if ps := parts[sig.name]; len(ps) > 0 {
			src += " UNION ALL BY NAME SELECT * FROM " + parquet(ps)
		}
		fmt.Fprintf(&b, "\nCREATE OR REPLACE VIEW %s AS SELECT * FROM (%s)\nQUALIFY row_number() OVER (PARTITION BY record_id ORDER BY file) = 1;\n", sig.table, src)
	}
	return b.String()
}

// parquet reads compacted parts.
func parquet(files []string) string {
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = literal(f)
	}
	return "read_parquet([" + strings.Join(quoted, ", ") + "])"
}

// runOn runs a named query on a database that has spans, logs and metric_points.
func runOn(ctx context.Context, db *sql.DB, q *Query, p Params) (*Result, error) {
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
