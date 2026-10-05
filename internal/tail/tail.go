// Package tail is ynr tail (ADR-001): the live stream of what tools are writing to the spool,
// one line per span, event, log record or metric point, as text or JSON. It reads the spool
// with a follower, so it needs no DuckDB and runs in the slim build, and never disturbs ynr serve.
package tail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/internal/stamp"
)

// Record is one line of the stream.
type Record struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"` // span, event, log or metric
	Service    string    `json:"service,omitempty"`
	Name       string    `json:"name"`
	Lane       string    `json:"lane,omitempty"`
	Item       string    `json:"item,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	Status     string    `json:"status,omitempty"`
	DurationMS float64   `json:"duration_ms,omitempty"`
	Value      *float64  `json:"value,omitempty"`
	Body       string    `json:"body,omitempty"`
	TraceID    string    `json:"trace_id,omitempty"`
	SpanID     string    `json:"span_id,omitempty"`
	Writer     string    `json:"writer"`
}

// Filter keeps records of one service and one item; empty keeps all.
type Filter struct {
	Service, Item string
}

func (f Filter) keep(r Record) bool {
	return (f.Service == "" || r.Service == f.Service) && (f.Item == "" || r.Item == f.Item)
}

// Options are how to follow.
type Options struct {
	Root      string
	FromStart bool
	Poll      time.Duration
	Filter    Filter
	JSON      bool
}

// Follow writes the stream to w until ctx ends.
func Follow(ctx context.Context, o Options, w io.Writer) error {
	f, err := spool.NewFollower(o.Root, 0, o.FromStart)
	if err != nil {
		return err
	}
	tick := time.NewTicker(o.Poll)
	defer tick.Stop()
	for {
		manifests := map[string]*stamp.Manifest{}
		err := f.Poll(ctx, func(wr spool.Writer, line []byte) error {
			recs, err := Decode(line, wr, manifest(o.Root, wr, manifests))
			if err != nil {
				return spool.ErrMalformed
			}
			for _, r := range recs {
				if o.Filter.keep(r) {
					if err := Write(w, r, o.JSON); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// manifest is a run's manifest, read once per poll, so a run's records show their lane and item.
func manifest(root string, w spool.Writer, cache map[string]*stamp.Manifest) *stamp.Manifest {
	if w.Class != spool.Run {
		return nil
	}
	if m, ok := cache[w.Name]; ok {
		return m
	}
	var m *stamp.Manifest
	if b, err := spool.ReadManifest(root, w.Name); err == nil {
		m, _ = stamp.ParseManifest(b, w.Name)
	}
	cache[w.Name] = m
	return m
}

// Decode turns one spool line, an OTLP JSON export request, into records, stamped as ynr serve
// stamps them so a run's records carry its lane and item.
func Decode(line []byte, w spool.Writer, m *stamp.Manifest) ([]Record, error) {
	var probe struct {
		Spans   json.RawMessage `json:"resourceSpans"`
		Logs    json.RawMessage `json:"resourceLogs"`
		Metrics json.RawMessage `json:"resourceMetrics"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return nil, err
	}
	id := stamp.Identity{ID: "tail"}
	var out []Record
	switch {
	case probe.Spans != nil:
		td, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(line)
		if err != nil {
			return nil, err
		}
		for i := 0; i < td.ResourceSpans().Len(); i++ {
			rs := td.ResourceSpans().At(i)
			stamp.Resource(rs.Resource().Attributes(), w, id, m, "")
			for j := 0; j < rs.ScopeSpans().Len(); j++ {
				spans := rs.ScopeSpans().At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					s := spans.At(k)
					r := base(rs.Resource().Attributes(), s.Attributes(), w)
					r.Time, r.Kind, r.Name = s.StartTimestamp().AsTime().UTC(), "span", s.Name()
					r.DurationMS = float64(s.EndTimestamp()-s.StartTimestamp()) / 1e6
					r.TraceID, r.SpanID = s.TraceID().String(), s.SpanID().String()
					if s.Status().Code() != ptrace.StatusCodeUnset {
						r.Status = strings.ToLower(s.Status().Code().String())
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
			stamp.Resource(rl.Resource().Attributes(), w, id, m, "")
			for j := 0; j < rl.ScopeLogs().Len(); j++ {
				lrs := rl.ScopeLogs().At(j).LogRecords()
				for k := 0; k < lrs.Len(); k++ {
					l := lrs.At(k)
					r := base(rl.Resource().Attributes(), l.Attributes(), w)
					r.Time = l.Timestamp().AsTime().UTC()
					if l.Timestamp() == 0 {
						r.Time = l.ObservedTimestamp().AsTime().UTC()
					}
					r.Kind, r.Name = "log", l.SeverityText()
					if name := l.EventName(); name != "" {
						r.Kind, r.Name = "event", name
					}
					r.Body = l.Body().AsString()
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
			stamp.Resource(rm.Resource().Attributes(), w, id, m, "")
			for j := 0; j < rm.ScopeMetrics().Len(); j++ {
				ms := rm.ScopeMetrics().At(j).Metrics()
				for k := 0; k < ms.Len(); k++ {
					out = append(out, points(rm.Resource().Attributes(), ms.At(k), w)...)
				}
			}
		}
	default:
		return nil, errors.New("not an OTLP export request")
	}
	return out, nil
}

func points(res pcommon.Map, m pmetric.Metric, w spool.Writer) []Record {
	var out []Record
	add := func(attrs pcommon.Map, at pcommon.Timestamp, v float64) {
		r := base(res, attrs, w)
		r.Time, r.Kind, r.Name, r.Value = at.AsTime().UTC(), "metric", m.Name(), &v
		out = append(out, r)
	}
	number := func(dps pmetric.NumberDataPointSlice) {
		for i := 0; i < dps.Len(); i++ {
			dp := dps.At(i)
			v := dp.DoubleValue()
			if dp.ValueType() == pmetric.NumberDataPointValueTypeInt {
				v = float64(dp.IntValue())
			}
			add(dp.Attributes(), dp.Timestamp(), v)
		}
	}
	switch m.Type() {
	case pmetric.MetricTypeSum:
		number(m.Sum().DataPoints())
	case pmetric.MetricTypeGauge:
		number(m.Gauge().DataPoints())
	case pmetric.MetricTypeHistogram:
		for i := 0; i < m.Histogram().DataPoints().Len(); i++ {
			dp := m.Histogram().DataPoints().At(i)
			add(dp.Attributes(), dp.Timestamp(), dp.Sum())
		}
	case pmetric.MetricTypeExponentialHistogram:
		for i := 0; i < m.ExponentialHistogram().DataPoints().Len(); i++ {
			dp := m.ExponentialHistogram().DataPoints().At(i)
			add(dp.Attributes(), dp.Timestamp(), dp.Sum())
		}
	}
	return out
}

// base fills what every record shows from its resource and attributes, the resource first, as
// that is where ynr stamps what a run's manifest says.
func base(res, attrs pcommon.Map, w spool.Writer) Record {
	pick := func(keys ...string) string {
		for _, k := range keys {
			for _, m := range []pcommon.Map{res, attrs} {
				if v, ok := m.Get(k); ok && v.AsString() != "" {
					return v.AsString()
				}
			}
		}
		return ""
	}
	return Record{
		Service: pick("service.name"),
		Lane:    pick(stamp.Lane),
		Item:    pick(stamp.ItemKey),
		Outcome: pick("ynf.outcome", "ynh.run.outcome", "ynm.outcome"),
		Writer:  w.Rel,
	}
}

// Write writes one record: a JSON object per line, or aligned text.
func Write(w io.Writer, r Record, asJSON bool) error {
	if asJSON {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = w.Write(append(b, '\n'))
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %-6s %-10s %s", r.Time.Format("15:04:05.000"), r.Kind, r.Service, r.Name)
	for _, kv := range [][2]string{{"lane", r.Lane}, {"item", r.Item}, {"outcome", r.Outcome}, {"status", r.Status}} {
		if kv[1] != "" {
			fmt.Fprintf(&b, "  %s=%s", kv[0], kv[1])
		}
	}
	if r.Kind == "span" {
		fmt.Fprintf(&b, "  %s", (time.Duration(r.DurationMS * float64(time.Millisecond))).Round(time.Millisecond))
	}
	if r.Value != nil {
		b.WriteString("  " + strconv.FormatFloat(*r.Value, 'f', -1, 64))
	}
	if r.Body != "" {
		body := r.Body
		if rs := []rune(body); len(rs) > 120 {
			body = string(rs[:117]) + "..."
		}
		b.WriteString("  " + strconv.Quote(body))
	}
	if r.TraceID != "" {
		b.WriteString("  trace=" + r.TraceID)
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}
