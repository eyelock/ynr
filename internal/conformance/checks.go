package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/eyelock/ynr/internal/registry"
)

// checker runs the rule checks for one scenario over the runs it made.
type checker struct {
	f       *File
	sc      Scenario
	o       Options
	reg     *Registry
	regErr  error
	runs    map[string]*runOutcome
	planted map[string]string
	checks  []Check
}

func (k *checker) add(rule int, st Status, name, detail string, evidence ...string) {
	if len(evidence) > 6 {
		evidence = append(evidence[:6:6], fmt.Sprintf("... and %d more", len(evidence)-6))
	}
	k.checks = append(k.checks, Check{Rule: rule, Name: name, Status: st, Detail: detail, Evidence: evidence})
}

func (k *checker) pass(rule int, name, detail string) { k.add(rule, Pass, name, detail) }
func (k *checker) skip(rule int, name, why string)    { k.add(rule, Skipped, name, why) }
func (k *checker) fail(rule int, name, detail string, evidence ...string) {
	k.add(rule, Fail, name, detail, evidence...)
}

// own reports whether a record is the tool's: its service.name is the tool's, or missing, which
// rule 2 reports separately.
func (k *checker) own(r Rec) bool { return r.Service() == "" || r.Service() == k.f.Service }

func (k *checker) other(r Rec) bool { return r.Service() != "" && r.Service() != k.f.Service }

func describe(r Rec) string {
	s := fmt.Sprintf("%s %q", r.Kind, r.Name)
	if r.Writer != "" {
		s += " in " + r.Writer
	}
	if r.TraceID != "" {
		s += " trace " + r.TraceID
	}
	return s
}

// all runs every check the scenario's runs allow.
func (k *checker) all() []Check {
	k.rule1()
	k.rule2()
	k.rule3and4()
	k.rule5()
	k.rule7()
	k.rule8()
	k.rule10()
	k.rule11()
	k.rule12()
	k.rule13()
	k.rule14()
	return k.checks
}

// captures are where a run's records can be read, named for the report.
type captured struct {
	label string
	c     *Capture
	run   *runOutcome
}

// toolCaptures is the spool run's spool and the endpoint run's endpoint: the two places the
// tool's records arrive.
func (k *checker) toolCaptures() []captured {
	var out []captured
	if r := k.runs[runSpool]; r != nil {
		out = append(out, captured{"spool", r.Spool, r})
	}
	if r := k.runs[runEndpoint]; r != nil {
		out = append(out, captured{"operator endpoint", r.Endpoint, r})
	}
	return out
}

// rule1: telemetry goes where ADR-004's order says, and nowhere with neither.
func (k *checker) rule1() {
	const name = "where to write"
	if r := k.runs[runSpool]; r != nil {
		switch {
		case len(r.Spool.Recs) == 0:
			k.fail(1, name+": spool only", "nothing reached the spool with only YNR_SPOOL set",
				"exit "+fmt.Sprint(r.Exit)+", output: "+excerpt(r.Output))
		default:
			k.pass(1, name+": spool only", fmt.Sprintf("%d records reached the spool", len(r.Spool.Recs)))
		}
	}
	if r := k.runs[runEndpoint]; r != nil {
		got := 0
		for _, rec := range r.Endpoint.Recs {
			if rec.Service() != stubVendorService {
				got++
			}
		}
		switch {
		case got == 0:
			k.fail(1, name+": operator endpoint", "nothing from the tool reached the endpoint named by OTEL_EXPORTER_OTLP_ENDPOINT",
				"exit "+fmt.Sprint(r.Exit)+", output: "+excerpt(r.Output))
		case len(r.Spool.Recs) > 0:
			ev := []string{}
			for _, rec := range r.Spool.Recs[:min(3, len(r.Spool.Recs))] {
				ev = append(ev, describe(rec))
			}
			k.fail(1, name+": operator endpoint", fmt.Sprintf("the tool also wrote %d records to the spool; with OTEL_EXPORTER_OTLP_* set it exports there instead of to ynr", len(r.Spool.Recs)), ev...)
		default:
			k.pass(1, name+": operator endpoint", fmt.Sprintf("%d records reached the endpoint and none the spool", got))
		}
	}
	if r := k.runs[runNeither]; r != nil {
		if len(r.Stray) > 0 {
			k.fail(1, name+": neither set", "the tool wrote telemetry files with no spool and no endpoint configured", r.Stray...)
		} else {
			k.pass(1, name+": neither set", "nothing was written")
		}
	}
}

func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	if s == "" {
		return "(none)"
	}
	return s
}

// rule2: every record describes its source, and the operator's resource attributes arrive.
func (k *checker) rule2() {
	const name = "resource"
	required := []string{"service.name", "service.version", "service.instance.id"}
	for _, cp := range k.toolCaptures() {
		var missing []string
		seen := map[string]bool{}
		named, noMarker := 0, 0
		var markerEv []string
		for _, r := range cp.c.Recs {
			if k.other(r) {
				continue // a vendor's records carry its own identity
			}
			for _, key := range required {
				if r.Resource[key] == "" {
					id := key + "|" + r.Kind + "|" + r.Name
					if !seen[id] {
						seen[id] = true
						missing = append(missing, fmt.Sprintf("%s has no %s", describe(r), key))
					}
				}
			}
			if r.Service() == k.f.Service {
				named++
			}
			if r.Resource["conformance.marker"] != cp.run.Marker {
				noMarker++
				if len(markerEv) < 3 {
					markerEv = append(markerEv, fmt.Sprintf("%s lacks conformance.marker=%s from OTEL_RESOURCE_ATTRIBUTES", describe(r), cp.run.Marker))
				}
			}
		}
		label := name + " (" + cp.label + ")"
		total := 0
		for _, r := range cp.c.Recs {
			if !k.other(r) {
				total++
			}
		}
		switch {
		case total == 0:
			k.skip(2, label, "no records from the tool arrived there")
		case len(missing) > 0:
			k.fail(2, label, fmt.Sprintf("%d resource attribute(s) missing", len(missing)), missing...)
		case named == 0:
			k.fail(2, label, fmt.Sprintf("no record has service.name %q", k.f.Service))
		case noMarker > 0:
			k.fail(2, label, fmt.Sprintf("OTEL_RESOURCE_ATTRIBUTES did not arrive on %d record(s)", noMarker), markerEv...)
		default:
			k.pass(2, label, fmt.Sprintf("%d records carry service.name, service.version, service.instance.id and the operator's attributes", total))
		}
	}
}

// rule3and4: the tool's spans are in the trace it was given, and so are those of what it started.
func (k *checker) rule3and4() {
	for _, cp := range k.toolCaptures() {
		var own, others []Rec
		for _, r := range cp.c.kind("span") {
			if k.other(r) {
				others = append(others, r)
			} else {
				own = append(own, r)
			}
		}
		name3 := "joins TRACEPARENT (" + cp.label + ")"
		switch {
		case len(own) == 0:
			if cp.label == "spool" {
				k.fail(3, name3, "the tool wrote no span")
			} else {
				k.skip(3, name3, "no spans from the tool arrived there")
			}
		default:
			var foreign []string
			joined := false
			for _, s := range own {
				if s.TraceID != cp.run.TraceID {
					foreign = append(foreign, fmt.Sprintf("%s is in a trace other than TRACEPARENT's %s", describe(s), cp.run.TraceID))
				} else if s.ParentID == cp.run.SpanID {
					joined = true
				}
			}
			switch {
			case len(foreign) > 0:
				k.fail(3, name3, fmt.Sprintf("%d of %d spans are not in the trace given in TRACEPARENT", len(foreign), len(own)), foreign...)
			case !joined:
				k.fail(3, name3, "no span is a child of the span given in TRACEPARENT "+cp.run.SpanID)
			default:
				k.pass(3, name3, fmt.Sprintf("%d spans in trace %s, the root a child of TRACEPARENT's span", len(own), cp.run.TraceID))
			}
		}
		name4 := "passes the trace on (" + cp.label + ")"
		if len(others) == 0 {
			k.skip(4, name4, "no process the tool started wrote spans there; HTTP calls between tools are not checked")
			continue
		}
		var bad []string
		for _, s := range others {
			if s.TraceID != cp.run.TraceID {
				bad = append(bad, fmt.Sprintf("%s (service %s) is not in the tool's trace", describe(s), s.Service()))
			}
		}
		if len(bad) > 0 {
			k.fail(4, name4, fmt.Sprintf("%d spans of started processes are outside the trace", len(bad)), bad...)
		} else {
			k.pass(4, name4, fmt.Sprintf("%d spans of started processes are in the trace; HTTP calls between tools are not checked", len(others)))
		}
	}
}

// units are the tool's unit-of-work spans in a capture: those the conformance file names, or by
// default direct children of the TRACEPARENT span, and any span a started event announces.
func (k *checker) units(c *Capture, r *runOutcome) []Rec {
	named := map[string]bool{}
	for _, u := range k.f.Units {
		named[u] = true
	}
	started := map[string]bool{}
	for _, e := range c.kind("event") {
		if k.own(e) && strings.HasSuffix(e.Name, ".started") {
			started[strings.TrimSuffix(e.Name, ".started")] = true
		}
	}
	var out []Rec
	for _, s := range c.kind("span") {
		if !k.own(s) {
			continue
		}
		isUnit := started[s.Name]
		if len(named) > 0 {
			isUnit = isUnit || named[s.Name]
		} else {
			isUnit = isUnit || (s.ParentID == r.SpanID && s.TraceID == r.TraceID)
		}
		if isUnit {
			out = append(out, s)
		}
	}
	return out
}

// rule5: a unit of work announces its start, and a start survives a kill.
func (k *checker) rule5() {
	if r := k.runs[runSpool]; r != nil {
		const name = "started events"
		units := k.units(r.Spool, r)
		var starts []Rec
		for _, e := range r.Spool.kind("event") {
			if k.own(e) && strings.HasSuffix(e.Name, ".started") {
				starts = append(starts, e)
			}
		}
		if len(units) == 0 && len(starts) == 0 {
			k.skip(5, name, "no unit-of-work span found: none is a child of TRACEPARENT's span, and the conformance file names none in units")
		} else {
			var missing []string
			for _, u := range units {
				found := false
				for _, e := range starts {
					if e.Name == u.Name+".started" && e.TraceID == u.TraceID {
						found = true
					}
				}
				if !found {
					missing = append(missing, fmt.Sprintf("%s has no %s.started event", describe(u), u.Name))
				}
			}
			for _, e := range starts {
				found := false
				for _, s := range r.Spool.kind("span") {
					if s.Name == strings.TrimSuffix(e.Name, ".started") && s.TraceID == e.TraceID {
						found = true
					}
				}
				if !found {
					missing = append(missing, fmt.Sprintf("%s has no span: in a finished run that is a crash", describe(e)))
				}
			}
			if len(missing) > 0 {
				k.fail(5, name, fmt.Sprintf("%d unit(s) of work do not match their started events", len(missing)), missing...)
			} else {
				k.pass(5, name, fmt.Sprintf("%d unit(s) of work, each with its started event", len(units)))
			}
		}
	}
	if r := k.runs[runKill]; r != nil {
		const name = "started events survive a kill"
		var starts []Rec
		for _, e := range r.Spool.kind("event") {
			if k.own(e) && strings.HasSuffix(e.Name, ".started") {
				starts = append(starts, e)
			}
		}
		switch {
		case len(r.Torn()) > 0:
			k.fail(5, name, "a spool file ends in a partial line: a batch is not one write", r.Torn()...)
		case r.ExitedEarly && len(starts) == 0:
			k.skip(5, name, "the scenario finished before a started event was seen, so there was nothing to kill mid-run")
		case r.ExitedEarly:
			k.skip(5, name, "the scenario finished before it could be killed mid-run")
		case r.TimedOut && len(starts) == 0:
			k.fail(5, name, "no started event reached the spool before the limit")
		case len(starts) == 0:
			k.fail(5, name, "killed mid-run, the spool holds no started event")
		default:
			k.pass(5, name, fmt.Sprintf("killed mid-run, %d started event(s) and no partial line in the spool", len(starts)))
		}
	}
}

// Torn lists the files of the run's spool that end in a partial line.
func (r *runOutcome) Torn() []string { return r.Spool.Torn }

var (
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	ulidRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Za-hjkmnp-tv-z]{26}$`)
	hashRe = regexp.MustCompile(`^(?:0x)?[0-9a-fA-F]{16,}$`)
	itemRe = regexp.MustCompile(`^(?:item/\S+|[A-Z][A-Z0-9]+-\d+|\S+/\S+#\d+)$`)
)

// idLike names what a metric attribute value looks like if it is an identifier.
func idLike(v string) string {
	switch {
	case uuidRe.MatchString(v):
		return "a UUID"
	case ulidRe.MatchString(v) && strings.ToUpper(v) == v:
		return "a ULID"
	case hashRe.MatchString(v):
		return "a hash or trace id"
	case itemRe.MatchString(v):
		return "an item key"
	}
	return ""
}

// rule8: metric attributes stay inside the registry's limits and carry no ids.
func (k *checker) rule8() {
	const name = "cardinality"
	type key struct{ metric, attr string }
	distinct := map[key]map[string]bool{}
	var ids []string
	points := 0
	seenID := map[string]bool{}
	for _, cp := range k.toolCaptures() {
		for _, r := range cp.c.kind("metric") {
			if !k.own(r) {
				continue
			}
			points++
			for a, v := range r.Attrs {
				kk := key{r.Name, a}
				if distinct[kk] == nil {
					distinct[kk] = map[string]bool{}
				}
				distinct[kk][v] = true
				if what := idLike(v); what != "" && !seenID[kk.metric+"|"+kk.attr] {
					seenID[kk.metric+"|"+kk.attr] = true
					ids = append(ids, fmt.Sprintf("metric %q attribute %s has the value %q, which looks like %s", r.Name, a, v, what))
				}
			}
		}
	}
	if points == 0 {
		k.skip(8, name, "the tool wrote no metric points")
		return
	}
	var over []string
	limits := 0
	if k.reg != nil {
		for kk, vals := range distinct {
			if limit := k.reg.limit(kk.metric, kk.attr); limit > 0 {
				limits++
				if len(vals) > limit {
					over = append(over, fmt.Sprintf("metric %q attribute %s has %d distinct values, over its registry limit of %d", kk.metric, kk.attr, len(vals), limit))
				}
			}
		}
	}
	switch {
	case len(ids) > 0 || len(over) > 0:
		k.fail(8, name, "a metric attribute is unbounded", append(ids, over...)...)
	case k.reg == nil:
		k.pass(8, name, fmt.Sprintf("%d metric points carry no id-like value; no registry, so no declared limits were checked", points))
	default:
		k.pass(8, name, fmt.Sprintf("%d metric points carry no id-like value; %d attribute(s) checked against ynr.cardinality limits", points, limits))
	}
}

// rule10: no planted string appears in anything the tool or the vendor wrote.
func (k *checker) rule10() {
	const name = "no content"
	if len(k.planted) == 0 {
		k.skip(10, name, "the scenario plants no @canary:<kind> strings")
		return
	}
	var found []string
	scanned := 0
	for _, r := range k.runs {
		for label, c := range map[string]*Capture{"spool": r.Spool, "endpoint": r.Endpoint} {
			for i, line := range c.Lines {
				scanned++
				for kind, can := range k.planted {
					at := bytes.Index(line, []byte(can))
					if at < 0 {
						continue
					}
					lo, hi := max(0, at-50), min(len(line), at+len(can)+30)
					snippet := strings.ReplaceAll(string(line[lo:hi]), can, "<canary:"+kind+">")
					var what []string
					for _, rec := range c.Recs {
						if rec.Line == i && len(what) < 2 {
							what = append(what, describe(rec))
						}
					}
					found = append(found, fmt.Sprintf("%s canary in the %s of the %s run: %s; ...%s...", kind, label, r.Kind, strings.Join(what, ", "), snippet))
				}
			}
		}
	}
	if len(found) > 0 {
		k.fail(10, name, fmt.Sprintf("%d line(s) hold planted content", len(found)), found...)
	} else {
		kinds := sortedKeys(k.planted)
		k.pass(10, name, fmt.Sprintf("%d lines searched for the %s canar(ies): none found", scanned, strings.Join(kinds, ", ")))
	}
}

// rule11: each unit of work ends with an outcome and a status that agree.
func (k *checker) rule11() {
	const name = "outcomes"
	r := k.runs[runSpool]
	if r == nil {
		return
	}
	units := k.units(r.Spool, r)
	if len(units) == 0 {
		k.skip(11, name, "no unit-of-work span found, so no outcome to check")
		return
	}
	var bad []string
	statusOf := map[string]string{}
	var pairs []string
	for _, u := range units {
		outcome := ""
		for a, v := range u.Attrs {
			if strings.HasSuffix(a, ".outcome") && v != "" {
				outcome = v
			}
		}
		pair := fmt.Sprintf("%s: outcome=%s status=%s", u.Name, outcome, u.Status)
		pairs = append(pairs, pair)
		switch {
		case outcome == "":
			bad = append(bad, fmt.Sprintf("%s has no outcome attribute (a *.outcome)", describe(u)))
		case u.Status == "":
			bad = append(bad, fmt.Sprintf("%s ends with outcome %q but its status is unset", describe(u), outcome))
		}
		if outcome != "" && u.Status != "" {
			if prev, ok := statusOf[outcome]; ok && prev != u.Status {
				bad = append(bad, fmt.Sprintf("outcome %q ends with status %s on one span and %s on another", outcome, prev, u.Status))
			}
			statusOf[outcome] = u.Status
		}
		if want := k.sc.Expect.Outcome; want != "" && outcome != "" && outcome != want {
			bad = append(bad, fmt.Sprintf("%s ends with outcome %q, the scenario expects %q", describe(u), outcome, want))
		}
	}
	if len(bad) > 0 {
		k.fail(11, name, fmt.Sprintf("%d problem(s) with the outcomes of %d unit(s) of work", len(bad), len(units)), bad...)
		return
	}
	k.pass(11, name, strings.Join(pairs, "; "))
}

// rule12: a full spool and an endpoint that never answers change neither the exit code nor how
// long the tool takes by more than the flush limit.
func (k *checker) rule12() {
	base := k.runs[runSpool]
	for _, kind := range []string{runUnwritable, runHung} {
		r := k.runs[kind]
		if r == nil {
			continue
		}
		name := "never blocks (" + kind + ")"
		if base == nil {
			k.skip(12, name, "there is no baseline run to compare with")
			continue
		}
		allowed := base.Duration + k.o.FlushLimit + k.o.Slack
		switch {
		case r.TimedOut:
			k.fail(12, name, fmt.Sprintf("still running after %s, when the normal %s plus the %s flush limit allows %s: the tool blocks on telemetry",
				r.Duration.Round(time.Millisecond), base.Duration.Round(time.Millisecond), k.o.FlushLimit, allowed.Round(time.Millisecond)),
				"output: "+excerpt(r.Output))
		case r.Exit != base.Exit:
			k.fail(12, name, fmt.Sprintf("exit code %d, but %d when telemetry works: telemetry changed the exit code", r.Exit, base.Exit),
				"output: "+excerpt(r.Output))
		case r.Duration > allowed:
			k.fail(12, name, fmt.Sprintf("took %s, over the normal %s plus the %s flush limit",
				r.Duration.Round(time.Millisecond), base.Duration.Round(time.Millisecond), k.o.FlushLimit))
		default:
			k.pass(12, name, fmt.Sprintf("exit %d as normal, in %s (normal %s, limit %s)", r.Exit,
				r.Duration.Round(time.Millisecond), base.Duration.Round(time.Millisecond), allowed.Round(time.Millisecond)))
		}
	}
}

// rule13 needs ynm in the test.
func (k *checker) rule13() {
	if _, err := exec.LookPath("ynm"); err != nil {
		k.skip(13, "never into memory", "ynm is not on the PATH")
		return
	}
	k.skip(13, "never into memory", "ynm is on the PATH, but comparing its store before and after is not implemented yet")
}

// rule14: a ynr on the path, with nothing configured to start it, is never started.
func (k *checker) rule14() {
	const name = "starts nothing"
	var calls []string
	runs := 0
	for _, r := range k.runs {
		runs++
		for _, c := range r.ShimCalls {
			if c != "" {
				calls = append(calls, fmt.Sprintf("the %s run started: %s", r.Kind, c))
			}
		}
	}
	if len(calls) > 0 {
		k.fail(14, name, "the tool started ynr because it found it", calls...)
		return
	}
	k.pass(14, name, fmt.Sprintf("a ynr on the PATH was never started across %d runs", runs))
}

// rule7: the names the tool wrote are ones its registry declares, and the registry it prints is
// the one in its repository.
func (k *checker) rule7() {
	const name = "names"
	if k.f.Registry == "" {
		k.skip(7, name, "the conformance file names no registry")
		return
	}
	if k.regErr != nil {
		k.fail(7, name, k.regErr.Error())
		return
	}
	prefix := k.f.Service + "."
	undeclared := map[string]string{}
	checked := 0
	note := func(kind, n string, declared bool) {
		if !strings.HasPrefix(n, prefix) {
			return
		}
		checked++
		if !declared {
			undeclared[kind+" "+n] = kind + " " + fmt.Sprintf("%q", n) + " is not declared in the registry"
		}
	}
	for _, cp := range k.toolCaptures() {
		for _, r := range cp.c.Recs {
			if r.Service() != k.f.Service {
				continue
			}
			switch r.Kind {
			case "span":
				note("span", r.Name, k.reg.Spans[r.Name])
			case "event":
				note("event", r.Name, k.reg.Events[r.Name])
			case "metric":
				note("metric", r.Name, k.reg.Metrics[r.Name])
			}
			for a := range r.Attrs {
				note("attribute", a, k.reg.declaresAttr(a))
			}
			for a := range r.Resource {
				note("resource attribute", a, k.reg.declaresAttr(a))
			}
		}
	}
	if len(undeclared) > 0 {
		k.fail(7, name+": records", fmt.Sprintf("%d name(s) the tool wrote are not in its registry", len(undeclared)), sortedValues(undeclared)...)
	} else if checked == 0 {
		k.skip(7, name+": records", "the tool wrote no name under its "+prefix+" prefix; names outside it need Weaver and the semantic conventions")
	} else {
		k.pass(7, name+": records", fmt.Sprintf("%d names under %s are all declared; names outside the prefix are not checked without Weaver", checked, prefix))
	}

	k.registryPrinted()
	k.weaver()
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}

// registryPrinted compares `<tool> telemetry registry --format json` with the registry files.
func (k *checker) registryPrinted() {
	const name = "names: printed registry"
	path, err := exec.LookPath(k.f.Service)
	if err != nil {
		k.skip(7, name, k.f.Service+" is not on the PATH, so its printed registry was not compared")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "telemetry", "registry", "--format", "json")
	cmd.Env = os.Environ()
	for key, v := range k.o.Env {
		cmd.Env = append(cmd.Env, key+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		k.fail(7, name, fmt.Sprintf("`%s telemetry registry --format json` failed: %v", k.f.Service, err))
		return
	}
	var j registry.Registry
	if err := json.Unmarshal(out, &j); err != nil {
		k.fail(7, name, fmt.Sprintf("`%s telemetry registry --format json` printed no registry JSON: %v", k.f.Service, err))
		return
	}
	var diffs []string
	compare := func(kind string, printed []string, declared []string) {
		p, d := map[string]bool{}, map[string]bool{}
		for _, n := range printed {
			p[n] = true
		}
		for _, n := range declared {
			d[n] = true
		}
		for _, n := range sortedKeys(p) {
			if !d[n] {
				diffs = append(diffs, fmt.Sprintf("%s %q is printed but not in the registry files", kind, n))
			}
		}
		for _, n := range sortedKeys(d) {
			if !p[n] {
				diffs = append(diffs, fmt.Sprintf("%s %q is in the registry files but not printed", kind, n))
			}
		}
	}
	var attrs, spans, events, metrics []string
	for _, a := range j.Attributes {
		attrs = append(attrs, a.ID)
	}
	for _, s := range j.Spans {
		spans = append(spans, s.Name)
	}
	for _, e := range j.Events {
		events = append(events, e.Name)
	}
	for _, m := range j.Metrics {
		metrics = append(metrics, m.Name)
	}
	compare("attribute", attrs, sortedKeys(k.reg.own))
	compare("standard attribute", j.Standard, sortedKeys(k.reg.std))
	compare("span", spans, sortedKeys(k.reg.Spans))
	compare("event", events, sortedKeys(k.reg.Events))
	compare("metric", metrics, sortedKeys(k.reg.Metrics))
	if len(diffs) > 0 {
		k.fail(7, name, fmt.Sprintf("%d difference(s) between the printed registry and the files", len(diffs)), diffs...)
		return
	}
	k.pass(7, name, "the printed registry has the names the registry files declare")
}

// weaver runs Weaver's registry check when weaver is on the PATH.
func (k *checker) weaver() {
	const name = "names: Weaver"
	path, err := exec.LookPath("weaver")
	if err != nil {
		k.skip(7, name, "skipped (weaver not on PATH)")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "registry", "check", "-r", k.f.Registry).CombinedOutput()
	if err != nil {
		k.fail(7, name, "weaver registry check failed", excerpt(string(out)))
		return
	}
	k.skip(7, name, "weaver registry check passed; weaver's live check of the records is not run by this version, ynr's own name check above covers the tool's prefix")
}
