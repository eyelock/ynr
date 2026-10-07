package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/eyelock/ynr/internal/query"
	"github.com/eyelock/ynr/internal/store"
)

// now is the clock ynr query measures windows from, replaced in tests.
var now = time.Now

// queryCmd runs a named query (FR-13): it asks the running ynr serve, and reads the store's
// batches directly when none answers (ADR-005).
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
	root := fs.String("spool", defaultRoot(), "the spool root of the ynr serve to ask (YNR_SPOOL_ROOT)")
	socketFlag := fs.String("socket", env("YNR_CENTRAL_SOCKET", ""), "the Unix socket of the ynr central to ask (YNR_CENTRAL_SOCKET)")
	storeURL := fs.String("store", envOr("YNR_STORE", defaultStore()), "read this store directly, such as file:///path, instead of asking ynr serve (YNR_STORE)")
	since := fs.String("since", "", "start of the window: a duration back from now (24h, 7d) or a time (RFC 3339); default "+days(q.Since))
	until := fs.String("until", "", "end of the window, the same way; default now")
	lane := fs.String("lane", "", "only this lane's records, where the query takes one")
	format := fs.String("format", "text", "text or json")
	erase := fs.String("erase", env("YNR_ERASE", ""), "a file of handles to mask, for a direct read (YNR_ERASE)")
	if err := fs.Parse(rest); err != nil {
		return ExitUsage
	}
	if code := loadErasure(*erase, stderr); code != ExitOK {
		return code
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
	raw := query.Raw{Arg: arg, Since: *since, Until: *until, Lane: *lane}
	p, err := q.Resolve(raw, now())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitUsage
	}
	direct := false
	fs.Visit(func(f *flag.Flag) { direct = direct || f.Name == "store" })

	// Ask the running ynr serve, whose hot tier answers in milliseconds; read the store
	// directly when none answers, or when --store names one.
	if !direct && (*root != "" || *socketFlag != "") {
		_, socket := hotPaths(*root)
		if *socketFlag != "" {
			socket = *socketFlag
		}
		doc, res, err := query.Ask(ctx, socket, name, raw)
		var se *query.ServerError
		switch {
		case err == nil:
			return write(stdout, stderr, *format, q, query.Params{Arg: doc.Arg, Lane: doc.Lane, Since: doc.Since, Until: doc.Until}, res)
		case errors.As(err, &se) && se.Status == http.StatusBadRequest:
			_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
			return ExitUsage
		case errors.As(err, &se):
			_, _ = fmt.Fprintf(stderr, "ynr: ynr serve: %v\n", err)
			return ExitAdapter
		case errors.Is(err, query.ErrNoServer) && *socketFlag != "":
			_, _ = fmt.Fprintf(stderr, "ynr: nothing is answering on %s; is ynr central running?\n", *socketFlag)
			return ExitAdapter
		case !errors.Is(err, query.ErrNoServer):
			_, _ = fmt.Fprintf(stderr, "ynr: asking ynr serve: %v\n", err)
			return ExitAdapter
		}
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
		_, _ = fmt.Fprintf(stderr, "ynr: %v; this is the slim build, and no full ynr serve is answering\n", err)
		return ExitConfig
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitAdapter
	}
	return write(stdout, stderr, *format, q, p, res)
}

func write(stdout, stderr io.Writer, format string, q *query.Query, p query.Params, res *query.Result) int {
	if format == "json" {
		if err := query.EncodeJSON(stdout, q, p, res); err != nil {
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
				cell = strings.Repeat("  ", indent(row[depth])) + cell
			}
			cells = append(cells, cell)
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	_ = tw.Flush()
}

// indent reads a depth, whether DuckDB or ynr serve's JSON gave it.
func indent(v any) int {
	switch d := v.(type) {
	case int32:
		return int(d)
	case int64:
		return int(d)
	case json.Number:
		n, _ := d.Int64()
		return int(n)
	}
	return 0
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
	case string:
		// Times come back from ynr serve as RFC 3339 text.
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil && strings.Contains(x, "T") {
			return text(t)
		}
	}
	return fmt.Sprint(v)
}
