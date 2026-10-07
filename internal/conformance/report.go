package conformance

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Status is the result of one check.
type Status string

// The three results a check has: it passed, it failed with evidence, or it did not run, with a
// reason.
const (
	Pass    Status = "pass"
	Fail    Status = "fail"
	Skipped Status = "skipped"
)

// Check is one rule checked against one scenario.
type Check struct {
	Rule     int      `json:"rule"`
	Name     string   `json:"name"`
	Status   Status   `json:"status"`
	Detail   string   `json:"detail,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

// RunInfo says how one run of a scenario went.
type RunInfo struct {
	Run          string  `json:"run"`
	Exit         int     `json:"exit"`
	DurationMS   float64 `json:"duration_ms"`
	TimedOut     bool    `json:"timed_out,omitempty"`
	Killed       bool    `json:"killed,omitempty"`
	SpoolRecs    int     `json:"spool_records"`
	EndpointRecs int     `json:"endpoint_records"`
}

// ScenarioReport is everything found for one scenario.
type ScenarioReport struct {
	Name   string    `json:"name"`
	Runs   []RunInfo `json:"runs"`
	Checks []Check   `json:"checks"`
	// Review is what rules 6 and 9 need a person to look at: the spans written, by service.
	Review []string `json:"review,omitempty"`
}

// RuleSummary is a rule's result across every scenario.
type RuleSummary struct {
	Rule   int    `json:"rule"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Report is what ynr conformance prints, as text or, for CI, as JSON.
type Report struct {
	File      string           `json:"file"`
	Service   string           `json:"service"`
	Notes     []string         `json:"notes,omitempty"`
	Scenarios []ScenarioReport `json:"scenarios"`
	Rules     []RuleSummary    `json:"rules"`
	OK        bool             `json:"ok"`
}

// ruleTitles are the contract rules ynr conformance reports on (ADR-006).
var ruleTitles = map[int]string{
	1:  "where to write",
	2:  "resource",
	3:  "joins the trace it was given",
	4:  "passes the trace on",
	5:  "started events",
	6:  "spans at boundaries (review)",
	7:  "names",
	8:  "cardinality",
	9:  "logger bridged (review)",
	10: "no content",
	11: "outcomes",
	12: "never blocks",
	13: "never into memory",
	14: "starts nothing",
}

// summarise fills Rules and OK from the scenarios' checks: a rule fails if any check of it
// failed, passes if some check passed, and is skipped if none could run.
func (r *Report) summarise() {
	r.Rules = nil
	r.OK = true
	for rule := 1; rule <= 14; rule++ {
		var pass, fail, skip int
		var reasons []string
		seen := map[string]bool{}
		for _, s := range r.Scenarios {
			for _, c := range s.Checks {
				if c.Rule != rule {
					continue
				}
				switch c.Status {
				case Pass:
					pass++
				case Fail:
					fail++
				case Skipped:
					skip++
					if !seen[c.Detail] {
						seen[c.Detail] = true
						reasons = append(reasons, c.Detail)
					}
				}
			}
		}
		sum := RuleSummary{Rule: rule, Title: ruleTitles[rule]}
		switch {
		case fail > 0:
			sum.Status, sum.Detail = Fail, fmt.Sprintf("%d check(s) failed", fail)
			r.OK = false
		case pass > 0:
			sum.Status = Pass
		default:
			sum.Status = Skipped
			sum.Detail = strings.Join(reasons, "; ")
		}
		r.Rules = append(r.Rules, sum)
	}
}

// WriteText prints the report for a person.
func (r *Report) WriteText(w io.Writer) {
	_, _ = fmt.Fprintf(w, "ynr conformance: %s (service %s)\n", r.File, r.Service)
	for _, n := range r.Notes {
		_, _ = fmt.Fprintf(w, "  note: %s\n", n)
	}
	for _, s := range r.Scenarios {
		_, _ = fmt.Fprintf(w, "\nscenario: %s\n", s.Name)
		for _, ri := range s.Runs {
			extra := ""
			switch {
			case ri.TimedOut:
				extra = ", timed out"
			case ri.Killed:
				extra = ", killed"
			}
			_, _ = fmt.Fprintf(w, "  run %-17s exit %d, %s%s; records: spool %d, endpoint %d\n", ri.Run, ri.Exit,
				(time.Duration(ri.DurationMS * float64(time.Millisecond))).Round(time.Millisecond), extra, ri.SpoolRecs, ri.EndpointRecs)
		}
		for _, c := range s.Checks {
			_, _ = fmt.Fprintf(w, "  [%s] rule %-2d %s", strings.ToUpper(string(c.Status)), c.Rule, c.Name)
			if c.Detail != "" {
				_, _ = fmt.Fprintf(w, ": %s", c.Detail)
			}
			_, _ = fmt.Fprintln(w)
			for _, e := range c.Evidence {
				_, _ = fmt.Fprintf(w, "         %s\n", e)
			}
		}
		for _, l := range s.Review {
			_, _ = fmt.Fprintf(w, "  review (rules 6 and 9): %s\n", l)
		}
	}
	_, _ = fmt.Fprintln(w, "\nrules:")
	for _, s := range r.Rules {
		_, _ = fmt.Fprintf(w, "  %-2d %-32s %s", s.Rule, s.Title, s.Status)
		if s.Detail != "" {
			_, _ = fmt.Fprintf(w, " (%s)", s.Detail)
		}
		_, _ = fmt.Fprintln(w)
	}
	if r.OK {
		_, _ = fmt.Fprintln(w, "\nconformance: ok")
	} else {
		_, _ = fmt.Fprintln(w, "\nconformance: FAILED")
	}
}

// reviewLines summarises spans by service for rules 6 and 9.
func reviewLines(label string, c *Capture) []string {
	counts := map[string]map[string]int{}
	for _, r := range c.Recs {
		if r.Kind != "span" {
			continue
		}
		svc := r.Service()
		if svc == "" {
			svc = "(no service.name)"
		}
		if counts[svc] == nil {
			counts[svc] = map[string]int{}
		}
		counts[svc][r.Name]++
	}
	var out []string
	for _, svc := range sortedKeys(counts) {
		total := 0
		names := sortedKeys(counts[svc])
		parts := make([]string, 0, len(names))
		for _, n := range names {
			total += counts[svc][n]
			parts = append(parts, fmt.Sprintf("%s x%d", n, counts[svc][n]))
		}
		sort.Strings(parts)
		out = append(out, fmt.Sprintf("%s, %s: %d spans: %s", label, svc, total, strings.Join(parts, ", ")))
	}
	return out
}
