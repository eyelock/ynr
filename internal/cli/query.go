package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/eyelock/ynr/internal/query"
	"github.com/eyelock/ynr/internal/store"
)

// now is the clock ynr query measures windows from, replaced in tests.
var now = time.Now

// queryCmd runs a named query over the store (FR-13). With no running server to ask yet, it
// reads the store's batches directly (ADR-005).
func queryCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		listQueries(stdout)
		return ExitOK
	}
	name, rest := args[0], args[1:]
	q, ok := query.Lookup(name)
	if !ok {
		_, _ = fmt.Fprintf(stderr, "ynr: no query %q\n\n", name)
		listQueries(stderr)
		return ExitUsage
	}
	// The argument may come before or after the flags.
	var arg string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		arg, rest = rest[0], rest[1:]
	}
	fs := flags("query "+name, stderr)
	storeURL := fs.String("store", envOr("YNR_STORE", defaultStore()), "the store to read, such as file:///path (YNR_STORE)")
	since := fs.String("since", "", "start of the window: a duration back from now (24h, 7d) or a time (RFC 3339); default "+days(q.Since))
	until := fs.String("until", "", "end of the window, the same way; default now")
	lane := fs.String("lane", "", "only this lane's records, where the query takes one")
	format := fs.String("format", "text", "text or json")
	if err := fs.Parse(rest); err != nil {
		return ExitUsage
	}
	extra := fs.Args()
	if arg == "" && len(extra) > 0 {
		arg, extra = extra[0], extra[1:]
	}
	if len(extra) > 0 {
		_, _ = fmt.Fprintf(stderr, "ynr: query %s takes at most one argument\n", name)
		return ExitUsage
	}
	if *format != "text" && *format != "json" {
		_, _ = fmt.Fprintln(stderr, "ynr: --format must be text or json")
		return ExitUsage
	}
	t := now().UTC()
	p := query.Params{Arg: arg, Lane: *lane, Since: t.Add(-q.Since), Until: t}
	var err error
	if *since != "" {
		if p.Since, err = parseWhen(*since, t); err != nil {
			_, _ = fmt.Fprintf(stderr, "ynr: --since: %v\n", err)
			return ExitUsage
		}
	}
	if *until != "" {
		if p.Until, err = parseWhen(*until, t); err != nil {
			_, _ = fmt.Fprintf(stderr, "ynr: --until: %v\n", err)
			return ExitUsage
		}
	}
	if err := q.Check(p); err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitUsage
	}
	if *storeURL == "" {
		_, _ = fmt.Fprintln(stderr, "ynr: no store: set --store or YNR_STORE")
		return ExitConfig
	}
	r, err := store.OpenReader(*storeURL)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitConfig
	}
	res, err := query.Run(ctx, r, q, p)
	if errors.Is(err, query.ErrSlim) {
		_, _ = fmt.Fprintf(stderr, "ynr: %v; this is the slim build\n", err)
		return ExitConfig
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitAdapter
	}
	if *format == "json" {
		if err := writeJSON(stdout, q, p, res); err != nil {
			_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
			return ExitAdapter
		}
		return ExitOK
	}
	writeTable(stdout, res)
	return ExitOK
}

func listQueries(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Named queries (ynr query <name> [argument] [--since 7d] [--until <time>] [--lane <id>] [--format text|json]):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, n := range query.Names() {
		q, _ := query.Lookup(n)
		_, _ = fmt.Fprintf(tw, "  %s %s\t%s (default --since %s)\n", n, q.Arg, q.Help, days(q.Since))
	}
	_ = tw.Flush()
}

// days writes a whole number of days as 7d, and anything else as Go does.
func days(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
	return d.String()
}

// parseWhen reads a duration back from now, allowing days (7d), or an RFC 3339 time.
func parseWhen(s string, now time.Time) (time.Time, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		if v, err := strconv.Atoi(n); err == nil && v >= 0 {
			return now.Add(-time.Duration(v) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			return time.Time{}, errors.New("give a duration back from now, without a sign")
		}
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%q is neither a duration (24h, 7d) nor an RFC 3339 time", s)
}

// writeJSON writes the query, its window and its rows, each row an object in column order.
func writeJSON(w io.Writer, q *query.Query, p query.Params, res *query.Result) error {
	var b bytes.Buffer
	head, err := json.Marshal(struct {
		Query string    `json:"query"`
		Arg   string    `json:"arg,omitempty"`
		Lane  string    `json:"lane,omitempty"`
		Since time.Time `json:"since"`
		Until time.Time `json:"until"`
	}{q.Name, p.Arg, p.Lane, p.Since, p.Until})
	if err != nil {
		return err
	}
	b.Write(head[:len(head)-1])
	b.WriteString(`,"rows":[`)
	for i, row := range res.Rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('{')
		for j, c := range res.Columns {
			if j > 0 {
				b.WriteByte(',')
			}
			k, _ := json.Marshal(c)
			v, err := json.Marshal(row[j])
			if err != nil {
				return fmt.Errorf("column %s: %w", c, err)
			}
			b.Write(k)
			b.WriteByte(':')
			b.Write(v)
		}
		b.WriteByte('}')
	}
	b.WriteString("]}\n")
	_, err = w.Write(b.Bytes())
	return err
}

// writeTable writes rows as aligned columns. A depth column indents the name column instead of
// showing, so a trace reads as a tree.
func writeTable(w io.Writer, res *query.Result) {
	if len(res.Rows) == 0 {
		_, _ = fmt.Fprintln(w, "no records in this window")
		return
	}
	depth, name := -1, -1
	for i, c := range res.Columns {
		switch c {
		case "depth":
			depth = i
		case "name":
			name = i
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	var head []string
	for i, c := range res.Columns {
		if i != depth || name < 0 {
			head = append(head, c)
		}
	}
	_, _ = fmt.Fprintln(tw, strings.Join(head, "\t"))
	for _, row := range res.Rows {
		var cells []string
		for i, v := range row {
			if i == depth && name >= 0 {
				continue
			}
			cell := text(v)
			if i == name && depth >= 0 {
				if d, ok := row[depth].(int32); ok {
					cell = strings.Repeat("  ", int(d)) + cell
				} else if d, ok := row[depth].(int64); ok {
					cell = strings.Repeat("  ", int(d)) + cell
				}
			}
			cells = append(cells, cell)
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	_ = tw.Flush()
}

func text(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case time.Time:
		return x.UTC().Format("2006-01-02 15:04:05")
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	}
	return fmt.Sprint(v)
}
