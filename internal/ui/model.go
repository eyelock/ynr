package ui

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/eyelock/ynr/internal/query"
)

// Table is a query's result ready to render: its columns, and each row's cells as text with an
// optional link.
type Table struct {
	Columns []string
	Rows    [][]Cell
}

// Cell is one value as shown.
type Cell struct {
	Text, Href, Class string
}

// table renders any result: times in UTC, numbers plainly, trace ids and item keys as links.
func table(res *query.Result) Table {
	t := Table{Columns: res.Columns}
	for _, row := range res.Rows {
		var cells []Cell
		for i, v := range row {
			c := Cell{Text: text(v)}
			switch res.Columns[i] {
			case "trace_id":
				if c.Text != "-" {
					c.Href, c.Class = "/trace/"+url.PathEscape(c.Text), "mono"
					c.Text = c.Text[:min(len(c.Text), 12)] + "…"
				}
			case "item":
				if c.Text != "-" {
					c.Href = "/item?key=" + url.QueryEscape(c.Text)
				}
			case "outcome", "last_outcome":
				c.Class = "outcome " + outcomeClass(c.Text)
			}
			cells = append(cells, c)
		}
		t.Rows = append(t.Rows, cells)
	}
	return t
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
	case *big.Int:
		return x.String()
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

func number(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case int32:
		return float64(x)
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f
	}
	return 0
}

// outcomeClass colours an outcome: what ended well, what ran out, what failed.
func outcomeClass(o string) string {
	switch o {
	case "converged", "completed", "ok", "accepted", "claimed":
		return "good"
	case "budget", "stuck", "held", "deduplicated", "aborted":
		return "warn"
	case "failed", "error", "tamper", "operator_error", "lost", "rejected":
		return "bad"
	}
	return "none"
}

func column(res *query.Result, name string) int {
	for i, c := range res.Columns {
		if c == name {
			return i
		}
	}
	return -1
}

// Lanes is the lanes page: each lane's runs, yield and cost, the outcomes as a chart, cost by
// model, and the latest runs.
type Lanes struct {
	Since     string
	Lanes     []Lane
	RunsChart string
	CostChart string
	Cost      Table
	Recent    Table
}

// Lane is one lane's row.
type Lane struct {
	Name                string
	Runs, Converged     int64
	Yield, Cost, Median string
	Last                string
}

func buildLanes(since string, runs, cost, recent *query.Result) Lanes {
	p := Lanes{Since: since, Cost: table(cost), Recent: table(recent)}
	li, oi, ri, ci, mi, la := column(runs, "lane"), column(runs, "outcome"), column(runs, "runs"),
		column(runs, "cost_usd"), column(runs, "median_s"), column(runs, "last")
	byLane := map[string]*Lane{}
	counts := map[string]map[string]float64{}
	outcomes := map[string]bool{}
	var costs = map[string]float64{}
	for _, row := range runs.Rows {
		name, outcome := text(row[li]), text(row[oi])
		l := byLane[name]
		if l == nil {
			l = &Lane{Name: name}
			byLane[name] = l
			counts[name] = map[string]float64{}
		}
		n := int64(number(row[ri]))
		l.Runs += n
		if outcome == "converged" {
			l.Converged += n
		}
		costs[name] += number(row[ci])
		counts[name][outcome] += float64(n)
		outcomes[outcome] = true
		if t := text(row[la]); t > l.Last {
			l.Last = t
		}
		if l.Median == "" && mi >= 0 && row[mi] != nil {
			l.Median = text(row[mi]) + "s"
		}
	}
	var names []string
	for n, l := range byLane {
		names = append(names, n)
		l.Yield = "-"
		if l.Runs > 0 {
			l.Yield = fmt.Sprintf("%.0f%%", 100*float64(l.Converged)/float64(l.Runs))
		}
		l.Cost = fmt.Sprintf("$%.2f", costs[n])
		if l.Median == "" {
			l.Median = "-"
		}
	}
	sort.Strings(names)
	for _, n := range names {
		p.Lanes = append(p.Lanes, *byLane[n])
	}
	var outs []string
	for o := range outcomes {
		outs = append(outs, o)
	}
	sort.Strings(outs)
	// Each outcome its own colour: the good ones green, running out amber, stalling violet,
	// failures red.
	colours := map[string]string{"converged": "#2e9e6b", "completed": "#2e9e6b", "budget": "#d99a2b",
		"stuck": "#8e6bd8", "aborted": "#8a8f98", "error": "#d1495b", "failed": "#d1495b",
		"tamper": "#9b1d2f", "operator_error": "#e07aa0", "lost": "#b5643c"}
	var series []map[string]any
	for _, o := range outs {
		var data []float64
		for _, n := range names {
			data = append(data, counts[n][o])
		}
		series = append(series, map[string]any{"name": o, "type": "bar", "stack": "runs", "data": data,
			"itemStyle": map[string]any{"color": colourOr(colours[o], "#8a8f98")}})
	}
	p.RunsChart = chart(map[string]any{
		"tooltip": map[string]any{"trigger": "axis"}, "legend": map[string]any{"top": 0},
		"grid":   map[string]any{"left": 8, "right": 8, "bottom": 8, "top": 32, "containLabel": true},
		"xAxis":  map[string]any{"type": "value"},
		"yAxis":  map[string]any{"type": "category", "data": names},
		"series": series,
	})
	var models []string
	var spend []float64
	mo, co := column(cost, "model"), column(cost, "cost_usd")
	for _, row := range cost.Rows {
		models = append(models, text(row[mo]))
		spend = append(spend, number(row[co]))
	}
	p.CostChart = chart(map[string]any{
		"tooltip": map[string]any{"trigger": "item"},
		"color":   palette,
		"series":  []map[string]any{{"type": "pie", "radius": []string{"45%", "70%"}, "data": pieData(models, spend)}},
	})
	return p
}

// palette reads on light and dark backgrounds alike.
var palette = []string{"#4c6fff", "#2eb8a6", "#e0a030", "#c062d1", "#e5654f", "#5aa9e6", "#9bbf3a", "#d6588e"}

func colourOr(c, def string) string {
	if c == "" {
		return def
	}
	return c
}

func pieData(names []string, values []float64) []map[string]any {
	var out []map[string]any
	for i := range names {
		out = append(out, map[string]any{"name": names[i], "value": values[i]})
	}
	return out
}

func chart(option map[string]any) string {
	b, _ := json.Marshal(option)
	return string(b)
}

// Trace is the waterfall: each span placed on the trace's time line.
type Trace struct {
	ID       string
	Start    string
	Duration string
	Spans    []Span
}

// Span is one row of the waterfall.
type Span struct {
	Depth                          int
	Name, Service, Status, Outcome string
	Duration                       string
	Left, Width                    float64 // percent of the trace's time line
	ID, Parent                     string
	Title                          string
}

func buildTrace(id string, res *query.Result) Trace {
	tr := Trace{ID: id}
	ti, di, ni, si, st, oi, du, sp, pa := column(res, "time"), column(res, "depth"), column(res, "name"),
		column(res, "service"), column(res, "status"), column(res, "outcome"), column(res, "duration_ms"),
		column(res, "span_id"), column(res, "parent_span_id")
	var start, end time.Time
	for _, row := range res.Rows {
		t, _ := row[ti].(time.Time)
		e := t.Add(time.Duration(number(row[du]) * float64(time.Millisecond)))
		if start.IsZero() || t.Before(start) {
			start = t
		}
		if e.After(end) {
			end = e
		}
	}
	total := end.Sub(start)
	if total <= 0 {
		total = time.Millisecond
	}
	tr.Start, tr.Duration = start.UTC().Format("2006-01-02 15:04:05.000"), total.Round(time.Millisecond).String()
	for _, row := range res.Rows {
		t, _ := row[ti].(time.Time)
		d := time.Duration(number(row[du]) * float64(time.Millisecond))
		s := Span{Depth: int(number(row[di])), Name: text(row[ni]), Service: text(row[si]),
			Status: text(row[st]), Outcome: text(row[oi]), Duration: d.Round(time.Millisecond).String(),
			Left:  100 * float64(t.Sub(start)) / float64(total),
			Width: max(0.3, 100*float64(d)/float64(total)),
			ID:    text(row[sp]), Parent: text(row[pa])}
		s.Width = min(s.Width, 100-s.Left)
		s.Title = fmt.Sprintf("%s · %s · starts +%s · %s", s.Name, s.Service, t.Sub(start).Round(time.Millisecond), s.Duration)
		tr.Spans = append(tr.Spans, s)
	}
	return tr
}
