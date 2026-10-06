package registry

import (
	"slices"
	"sort"
	"sync"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/eyelock/ynr/internal/names"
)

// MaxUnknown is the most distinct (service, version, name) triples counted. Past it, new names
// only add to Overflow, so a sender cannot make ynr's memory grow by inventing names.
const MaxUnknown = 1000

// maxName bounds the length of a name that is kept, for the same reason.
const maxName = 128

// names is what one registry declares, as sets, so the check is one lookup per name.
type nameSets struct {
	attrs, spans, events, metrics map[string]struct{}
}

func setsOf(r *Registry) *nameSets {
	s := &nameSets{attrs: map[string]struct{}{}, spans: map[string]struct{}{}, events: map[string]struct{}{}, metrics: map[string]struct{}{}}
	for _, a := range r.Attributes {
		s.attrs[a.ID] = struct{}{}
	}
	for _, a := range r.Standard {
		s.attrs[a] = struct{}{}
	}
	for _, x := range r.Spans {
		s.spans[x.Name] = struct{}{}
	}
	for _, x := range r.Events {
		s.events[x.Name] = struct{}{}
	}
	for _, x := range r.Metrics {
		s.metrics[x.Name] = struct{}{}
	}
	return s
}

// Unknown is how often one name was seen that its tool's registry does not declare.
type Unknown struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Name    string `json:"name"`
	Count   int64  `json:"count"`
}

type unknownKey struct{ service, version, name string }

// Checker checks records' names against the registries ynr learned (ADR-007). It honours and
// never rejects: it counts names a registry does not declare, marks records whose service and
// version have no learned registry, and changes nothing else.
type Checker struct {
	mu       sync.Mutex
	known    map[string]map[string]*nameSets // service.name, then service.version
	unknown  map[unknownKey]int64
	overflow int64
}

// NewChecker returns a checker that has learned nothing.
func NewChecker() *Checker {
	return &Checker{known: map[string]map[string]*nameSets{}, unknown: map[unknownKey]int64{}}
}

// Learn adds a registry. A second registry for the same tool and version replaces the first
// here; the store keeps both, and central reports the conflict.
func (c *Checker) Learn(r *Registry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.known[r.Tool] == nil {
		c.known[r.Tool] = map[string]*nameSets{}
	}
	c.known[r.Tool][r.Version] = setsOf(r)
}

// Tools returns the tool and version of every learned registry, sorted.
func (c *Checker) Tools() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for t, vs := range c.known {
		for v := range vs {
			out = append(out, t+" "+v)
		}
	}
	sort.Strings(out)
	return out
}

// Counts returns the unknown names counted so far, sorted, and how many were not counted
// because the cap was reached.
func (c *Checker) Counts() (counts []Unknown, overflow int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, n := range c.unknown {
		counts = append(counts, Unknown{Service: k.service, Version: k.version, Name: k.name, Count: n})
	}
	slices.SortFunc(counts, func(a, b Unknown) int {
		switch {
		case a.Service != b.Service:
			return cmpStr(a.Service, b.Service)
		case a.Version != b.Version:
			return cmpStr(a.Version, b.Version)
		}
		return cmpStr(a.Name, b.Name)
	})
	return counts, c.overflow
}

func cmpStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// str is an attribute as a string, empty when the record does not carry it.
func str(m pcommon.Map, k string) string {
	if v, ok := m.Get(k); ok {
		return v.AsString()
	}
	return ""
}

// scope is the registry that applies to one resource, or none.
type scope struct {
	c       *Checker
	service string
	version string
	sets    *nameSets
}

// resource finds the registry for a resource by its service.name and service.version, and marks
// the resource when there is none. The mark is applied after the stamp has removed every ynr.*
// attribute the sender set, so only ynr can say it.
func (c *Checker) resource(attrs pcommon.Map) scope {
	s := scope{c: c, service: str(attrs, names.AttrServiceName), version: str(attrs, names.AttrServiceVersion)}
	c.mu.Lock()
	s.sets = c.known[s.service][s.version]
	c.mu.Unlock()
	if s.sets == nil {
		attrs.PutStr(names.AttrRegistry, names.RegistryUnknown)
	}
	return s
}

func (s scope) count(name string) {
	if len(name) > maxName {
		name = name[:maxName]
	}
	k := unknownKey{s.service, s.version, name}
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if _, ok := s.c.unknown[k]; !ok && len(s.c.unknown) >= MaxUnknown {
		s.c.overflow++
		return
	}
	s.c.unknown[k]++
}

func (s scope) check(set map[string]struct{}, name string) {
	if name == "" {
		return
	}
	if _, ok := set[name]; !ok {
		s.count(name)
	}
}

func (s scope) attrs(m pcommon.Map) {
	m.Range(func(k string, _ pcommon.Value) bool {
		s.check(s.sets.attrs, k)
		return true
	})
}

// Traces marks and checks one ResourceSpans.
func (c *Checker) Traces(rs ptrace.ResourceSpans) {
	s := c.resource(rs.Resource().Attributes())
	if s.sets == nil {
		return
	}
	for i := 0; i < rs.ScopeSpans().Len(); i++ {
		spans := rs.ScopeSpans().At(i).Spans()
		for j := 0; j < spans.Len(); j++ {
			sp := spans.At(j)
			s.check(s.sets.spans, sp.Name())
			s.attrs(sp.Attributes())
			for k := 0; k < sp.Events().Len(); k++ {
				ev := sp.Events().At(k)
				s.check(s.sets.events, ev.Name())
				s.attrs(ev.Attributes())
			}
		}
	}
}

// Logs marks and checks one ResourceLogs. A log record with an event name is an event.
func (c *Checker) Logs(rl plog.ResourceLogs) {
	s := c.resource(rl.Resource().Attributes())
	if s.sets == nil {
		return
	}
	for i := 0; i < rl.ScopeLogs().Len(); i++ {
		recs := rl.ScopeLogs().At(i).LogRecords()
		for j := 0; j < recs.Len(); j++ {
			s.check(s.sets.events, recs.At(j).EventName())
			s.attrs(recs.At(j).Attributes())
		}
	}
}

// Metrics marks and checks one ResourceMetrics.
func (c *Checker) Metrics(rm pmetric.ResourceMetrics) {
	s := c.resource(rm.Resource().Attributes())
	if s.sets == nil {
		return
	}
	for i := 0; i < rm.ScopeMetrics().Len(); i++ {
		ms := rm.ScopeMetrics().At(i).Metrics()
		for j := 0; j < ms.Len(); j++ {
			m := ms.At(j)
			s.check(s.sets.metrics, m.Name())
			switch m.Type() {
			case pmetric.MetricTypeGauge:
				for k := 0; k < m.Gauge().DataPoints().Len(); k++ {
					s.attrs(m.Gauge().DataPoints().At(k).Attributes())
				}
			case pmetric.MetricTypeSum:
				for k := 0; k < m.Sum().DataPoints().Len(); k++ {
					s.attrs(m.Sum().DataPoints().At(k).Attributes())
				}
			case pmetric.MetricTypeHistogram:
				for k := 0; k < m.Histogram().DataPoints().Len(); k++ {
					s.attrs(m.Histogram().DataPoints().At(k).Attributes())
				}
			case pmetric.MetricTypeExponentialHistogram:
				for k := 0; k < m.ExponentialHistogram().DataPoints().Len(); k++ {
					s.attrs(m.ExponentialHistogram().DataPoints().At(k).Attributes())
				}
			case pmetric.MetricTypeSummary:
				for k := 0; k < m.Summary().DataPoints().Len(); k++ {
					s.attrs(m.Summary().DataPoints().At(k).Attributes())
				}
			default:
			}
		}
	}
}
