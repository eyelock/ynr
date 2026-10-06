package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/eyelock/ynr/internal/spool"
)

// Rec is one span, event, log record or metric data point read back from a spool, with the
// attributes the checks look at. Unlike ynr tail's records it is not stamped by ynr: it shows
// what the tool wrote.
type Rec struct {
	Kind     string // span, event, log or metric
	Name     string
	Writer   string // the writer folder, relative to the spool root
	Resource map[string]string
	Attrs    map[string]string
	TraceID  string
	SpanID   string
	ParentID string
	Status   string // "", ok or error, for spans
	Line     int    // index into Capture.Lines
}

// Service is the record's service.name.
func (r Rec) Service() string { return r.Resource["service.name"] }

// Capture is everything one spool, or one test endpoint's spool, received.
type Capture struct {
	Recs      []Rec
	Lines     [][]byte
	Malformed int
	// Files lists the spool files, relative to the root, and Torn those whose last line is not
	// complete: a tool killed mid-write leaves one, and a batch is meant to be one write call.
	Files []string
	Torn  []string
}

// Spans, Events and Metrics select records by kind.
func (c *Capture) kind(k string) []Rec {
	var out []Rec
	for _, r := range c.Recs {
		if r.Kind == k {
			out = append(out, r)
		}
	}
	return out
}

// readCapture reads every complete line under a spool root. A root that does not exist holds
// nothing.
func readCapture(root string) (*Capture, error) {
	c := &Capture{}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		c.Files = append(c.Files, rel)
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 && b[len(b)-1] != '\n' {
			c.Torn = append(c.Torn, rel)
		}
		return nil
	})
	f, err := spool.NewFollower(root, 0, true)
	if err != nil {
		return nil, err
	}
	err = f.Poll(context.Background(), func(w spool.Writer, line []byte) error {
		recs, err := decode(line, w.Rel, len(c.Lines))
		if err != nil {
			c.Malformed++
			return spool.ErrMalformed
		}
		c.Lines = append(c.Lines, append([]byte(nil), line...))
		c.Recs = append(c.Recs, recs...)
		return nil
	})
	return c, err
}

func attrMap(m pcommon.Map) map[string]string {
	out := make(map[string]string, m.Len())
	m.Range(func(k string, v pcommon.Value) bool {
		out[k] = v.AsString()
		return true
	})
	return out
}

// decode turns one OTLP JSON export request into records.
func decode(line []byte, writer string, idx int) ([]Rec, error) {
	var probe struct {
		Spans   json.RawMessage `json:"resourceSpans"`
		Logs    json.RawMessage `json:"resourceLogs"`
		Metrics json.RawMessage `json:"resourceMetrics"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return nil, err
	}
	var out []Rec
	switch {
	case probe.Spans != nil:
		td, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(line)
		if err != nil {
			return nil, err
		}
		for i := 0; i < td.ResourceSpans().Len(); i++ {
			rs := td.ResourceSpans().At(i)
			res := attrMap(rs.Resource().Attributes())
			for j := 0; j < rs.ScopeSpans().Len(); j++ {
				spans := rs.ScopeSpans().At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					s := spans.At(k)
					r := Rec{Kind: "span", Name: s.Name(), Writer: writer, Resource: res, Attrs: attrMap(s.Attributes()),
						TraceID: s.TraceID().String(), SpanID: s.SpanID().String(), Line: idx}
					if !s.ParentSpanID().IsEmpty() {
						r.ParentID = s.ParentSpanID().String()
					}
					switch s.Status().Code() {
					case ptrace.StatusCodeOk:
						r.Status = "ok"
					case ptrace.StatusCodeError:
						r.Status = "error"
					}
					out = append(out, r)
				}
			}
		}
	case probe.Logs != nil:
		ld, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(line)
		if err != nil {
			return nil, err
		}
		for i := 0; i < ld.ResourceLogs().Len(); i++ {
			rl := ld.ResourceLogs().At(i)
			res := attrMap(rl.Resource().Attributes())
			for j := 0; j < rl.ScopeLogs().Len(); j++ {
				lrs := rl.ScopeLogs().At(j).LogRecords()
				for k := 0; k < lrs.Len(); k++ {
					l := lrs.At(k)
					r := Rec{Kind: "log", Name: l.SeverityText(), Writer: writer, Resource: res, Attrs: attrMap(l.Attributes()), Line: idx}
					if name := l.EventName(); name != "" {
						r.Kind, r.Name = "event", name
					}
					if !l.TraceID().IsEmpty() {
						r.TraceID, r.SpanID = l.TraceID().String(), l.SpanID().String()
					}
					out = append(out, r)
				}
			}
		}
	case probe.Metrics != nil:
		md, err := (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics(line)
		if err != nil {
			return nil, err
		}
		for i := 0; i < md.ResourceMetrics().Len(); i++ {
			rm := md.ResourceMetrics().At(i)
			res := attrMap(rm.Resource().Attributes())
			for j := 0; j < rm.ScopeMetrics().Len(); j++ {
				ms := rm.ScopeMetrics().At(j).Metrics()
				for k := 0; k < ms.Len(); k++ {
					m := ms.At(k)
					for _, a := range pointAttrs(m) {
						out = append(out, Rec{Kind: "metric", Name: m.Name(), Writer: writer, Resource: res, Attrs: a, Line: idx})
					}
				}
			}
		}
	default:
		return nil, errors.New("not an OTLP export request")
	}
	return out, nil
}

// pointAttrs is the attributes of each data point of a metric.
func pointAttrs(m pmetric.Metric) []map[string]string {
	var out []map[string]string
	switch m.Type() {
	case pmetric.MetricTypeSum:
		for i := 0; i < m.Sum().DataPoints().Len(); i++ {
			out = append(out, attrMap(m.Sum().DataPoints().At(i).Attributes()))
		}
	case pmetric.MetricTypeGauge:
		for i := 0; i < m.Gauge().DataPoints().Len(); i++ {
			out = append(out, attrMap(m.Gauge().DataPoints().At(i).Attributes()))
		}
	case pmetric.MetricTypeHistogram:
		for i := 0; i < m.Histogram().DataPoints().Len(); i++ {
			out = append(out, attrMap(m.Histogram().DataPoints().At(i).Attributes()))
		}
	case pmetric.MetricTypeExponentialHistogram:
		for i := 0; i < m.ExponentialHistogram().DataPoints().Len(); i++ {
			out = append(out, attrMap(m.ExponentialHistogram().DataPoints().At(i).Attributes()))
		}
	case pmetric.MetricTypeSummary:
		for i := 0; i < m.Summary().DataPoints().Len(); i++ {
			out = append(out, attrMap(m.Summary().DataPoints().At(i).Attributes()))
		}
	}
	return out
}

// strayFiles lists every spool-format file under dir: what a tool wrote when it should have
// written nothing.
func strayFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}
