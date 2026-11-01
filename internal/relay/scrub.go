package relay

import (
	"path/filepath"
	"regexp"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Stripped names the attributes the relay always drops, wherever they appear: a person's email
// address and full name, the working directory, and the host and process attributes that name
// the machine or the user on it. A vendor
// CLI sends them and not every vendor can be told not to (Codex sends cwd and host.name), so the
// relay enforces ADR-006's "no content" rule for them at one point, for every vendor.
var Stripped = []string{
	"user.email",
	"user.full_name",
	"cwd",
	"host.name",
	"host.id",
	"host.ip",
	"host.mac",
	"process.owner",
	"process.command",
	"process.command_line",
	"process.command_args",
	"process.executable.path",
}

// email matches a value that is an email address and nothing else. Only a whole value counts, so a
// sentence or a URL that mentions one is left alone.
var email = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}$`)

// scrubber drops the Stripped attributes, any attribute whose string value is an email address,
// and any whose string value is the user's home folder or an absolute path under it, from resources, scopes, spans, span events and links,
// metric data points and exemplars, and log records. A map value is scrubbed key by key; a list
// holding such a path is dropped whole.
type scrubber struct {
	names map[string]bool
	home  string // cleaned, without a trailing separator; empty when unknown or the root
}

func newScrubber(home string) *scrubber {
	s := &scrubber{names: make(map[string]bool, len(Stripped))}
	for _, n := range Stripped {
		s.names[n] = true
	}
	if home != "" && filepath.IsAbs(home) {
		if h := filepath.Clean(home); h != string(filepath.Separator) {
			s.home = h
		}
	}
	return s
}

// underHome reports whether v is the home folder or an absolute path under it.
func (s *scrubber) underHome(v string) bool {
	if s.home == "" {
		return false
	}
	return v == s.home || strings.HasPrefix(v, s.home+string(filepath.Separator))
}

// attrs scrubs m in place and returns how many attributes it dropped.
func (s *scrubber) attrs(m pcommon.Map) int {
	n := 0
	m.RemoveIf(func(k string, v pcommon.Value) bool {
		if s.names[k] || s.drop(v, &n) {
			n++
			return true
		}
		return false
	})
	return n
}

// drop reports whether v must go, scrubbing a map value in place and counting what it drops.
func (s *scrubber) drop(v pcommon.Value, n *int) bool {
	switch v.Type() {
	case pcommon.ValueTypeStr:
		return s.underHome(v.Str()) || email.MatchString(v.Str())
	case pcommon.ValueTypeMap:
		*n += s.attrs(v.Map())
	case pcommon.ValueTypeSlice:
		sl := v.Slice()
		for i := range sl.Len() {
			if e := sl.At(i); e.Type() == pcommon.ValueTypeMap {
				*n += s.attrs(e.Map())
			} else if s.drop(e, n) {
				return true
			}
		}
	}
	return false
}

func (s *scrubber) traces(td ptrace.Traces) int {
	n := 0
	for _, rs := range td.ResourceSpans().All() {
		n += s.attrs(rs.Resource().Attributes())
		for _, ss := range rs.ScopeSpans().All() {
			n += s.attrs(ss.Scope().Attributes())
			for _, sp := range ss.Spans().All() {
				n += s.attrs(sp.Attributes())
				for _, ev := range sp.Events().All() {
					n += s.attrs(ev.Attributes())
				}
				for _, l := range sp.Links().All() {
					n += s.attrs(l.Attributes())
				}
			}
		}
	}
	return n
}

func (s *scrubber) logs(ld plog.Logs) int {
	n := 0
	for _, rl := range ld.ResourceLogs().All() {
		n += s.attrs(rl.Resource().Attributes())
		for _, sl := range rl.ScopeLogs().All() {
			n += s.attrs(sl.Scope().Attributes())
			for _, lr := range sl.LogRecords().All() {
				n += s.attrs(lr.Attributes())
			}
		}
	}
	return n
}

func (s *scrubber) metrics(md pmetric.Metrics) int {
	n := 0
	for _, rm := range md.ResourceMetrics().All() {
		n += s.attrs(rm.Resource().Attributes())
		for _, sm := range rm.ScopeMetrics().All() {
			n += s.attrs(sm.Scope().Attributes())
			for _, m := range sm.Metrics().All() {
				n += s.metric(m)
			}
		}
	}
	return n
}

func (s *scrubber) metric(m pmetric.Metric) int {
	n := 0
	exemplars := func(es pmetric.ExemplarSlice) {
		for _, e := range es.All() {
			n += s.attrs(e.FilteredAttributes())
		}
	}
	switch m.Type() {
	case pmetric.MetricTypeGauge:
		for _, dp := range m.Gauge().DataPoints().All() {
			n += s.attrs(dp.Attributes())
			exemplars(dp.Exemplars())
		}
	case pmetric.MetricTypeSum:
		for _, dp := range m.Sum().DataPoints().All() {
			n += s.attrs(dp.Attributes())
			exemplars(dp.Exemplars())
		}
	case pmetric.MetricTypeHistogram:
		for _, dp := range m.Histogram().DataPoints().All() {
			n += s.attrs(dp.Attributes())
			exemplars(dp.Exemplars())
		}
	case pmetric.MetricTypeExponentialHistogram:
		for _, dp := range m.ExponentialHistogram().DataPoints().All() {
			n += s.attrs(dp.Attributes())
			exemplars(dp.Exemplars())
		}
	case pmetric.MetricTypeSummary:
		for _, dp := range m.Summary().DataPoints().All() {
			n += s.attrs(dp.Attributes())
		}
	}
	n += s.attrs(m.Metadata())
	return n
}
