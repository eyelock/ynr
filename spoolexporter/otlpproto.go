package spoolexporter

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"

	"google.golang.org/protobuf/encoding/protowire"
)

// This file writes the OTLP protobuf encoding of the same request structs
// otlpjson.go writes as JSON, for OTLP/HTTP with an application/x-protobuf
// body. It uses only protowire, so it links no gRPC and no generated
// message types. The field numbers are those of the opentelemetry-proto
// .proto files (common, resource, trace, logs, metrics); the tests decode
// every encoding with the official types to hold them to it.
//
// The structs carry ids as hex, times and 64-bit integers as decimal
// strings and bytes as base64, as OTLP/JSON writes them, so the encoder
// reads them back; a malformed string is an error, never a silent zero.
// As in proto3, a field at its default is omitted, except a oneof member or
// an optional field that is set.

// encodeProtobuf returns the OTLP protobuf encoding of an export request
// built by traceRequest, logsRequest or metricsRequest.
func encodeProtobuf(req any) ([]byte, error) {
	switch r := req.(type) {
	case jsonTraceRequest:
		return pbRepeated(nil, 1, r.ResourceSpans, pbResourceSpans)
	case jsonLogsRequest:
		return pbRepeated(nil, 1, r.ResourceLogs, pbResourceLogs)
	case jsonMetricsRequest:
		return pbRepeated(nil, 1, r.ResourceMetrics, pbResourceMetrics)
	}
	return nil, fmt.Errorf("spoolexporter: cannot encode %T as protobuf", req)
}

// pbMsg appends a length-delimited submessage field built by f.
func pbMsg(b []byte, num protowire.Number, f func([]byte) ([]byte, error)) ([]byte, error) {
	sub, err := f(nil)
	if err != nil {
		return nil, err
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, sub), nil
}

// pbRepeated appends one submessage field per item.
func pbRepeated[T any](b []byte, num protowire.Number, items []T, f func([]byte, T) ([]byte, error)) ([]byte, error) {
	var err error
	for _, it := range items {
		b, err = pbMsg(b, num, func(sub []byte) ([]byte, error) { return f(sub, it) })
		if err != nil {
			return nil, err
		}
	}
	return b, nil
}

func pbString(b []byte, num protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func pbVarint(b []byte, num protowire.Number, n uint64) []byte {
	if n == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, n)
}

func pbBool(b []byte, num protowire.Number, v bool) []byte {
	if !v {
		return b
	}
	return pbVarint(b, num, 1)
}

func pbSint32(b []byte, num protowire.Number, n int32) []byte {
	if n == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, protowire.EncodeZigZag(int64(n)))
}

func pbFixed32(b []byte, num protowire.Number, n uint32) []byte {
	if n == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.Fixed32Type)
	return protowire.AppendFixed32(b, n)
}

func pbFixed64(b []byte, num protowire.Number, n uint64) []byte {
	if n == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.Fixed64Type)
	return protowire.AppendFixed64(b, n)
}

// pbDouble writes a double, present even at zero: it is for oneof members
// and optional fields, which the caller has checked are set.
func pbDouble(b []byte, num protowire.Number, f float64) []byte {
	b = protowire.AppendTag(b, num, protowire.Fixed64Type)
	return protowire.AppendFixed64(b, math.Float64bits(f))
}

// pbDoubleDefault writes a non-optional double, omitted at zero.
func pbDoubleDefault(b []byte, num protowire.Number, f float64) []byte {
	if math.Float64bits(f) == 0 {
		return b
	}
	return pbDouble(b, num, f)
}

func pbOptDouble(b []byte, num protowire.Number, d *jsonDouble) []byte {
	if d == nil {
		return b
	}
	return pbDouble(b, num, float64(*d))
}

// pbTime writes a decimal-string nanosecond field as a fixed64.
func pbTime(b []byte, num protowire.Number, s string) ([]byte, error) {
	n, err := parseU64(s)
	if err != nil {
		return nil, err
	}
	return pbFixed64(b, num, n), nil
}

func pbHexBytes(b []byte, num protowire.Number, s string) ([]byte, error) {
	if s == "" {
		return b, nil
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("spoolexporter: bad hex id: %w", err)
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, raw), nil
}

func parseU64(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("spoolexporter: bad unsigned integer %q", s)
	}
	return n, nil
}

func parseI64(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("spoolexporter: bad integer %q", s)
	}
	return n, nil
}

// pbSfixed64 writes an asInt oneof member, present even at zero.
func pbAsInt(b []byte, num protowire.Number, s *string) ([]byte, error) {
	if s == nil {
		return b, nil
	}
	n, err := parseI64(*s)
	if err != nil {
		return nil, err
	}
	b = protowire.AppendTag(b, num, protowire.Fixed64Type)
	return protowire.AppendFixed64(b, uint64(n)), nil
}

// pbPackedFixed64 and pbPackedDouble write packed repeated fields.
func pbPackedU64Strings(b []byte, num protowire.Number, ss []string, fixed bool) ([]byte, error) {
	if len(ss) == 0 {
		return b, nil
	}
	var packed []byte
	for _, s := range ss {
		n, err := parseU64(s)
		if err != nil {
			return nil, err
		}
		if fixed {
			packed = protowire.AppendFixed64(packed, n)
		} else {
			packed = protowire.AppendVarint(packed, n)
		}
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, packed), nil
}

func pbPackedDoubles(b []byte, num protowire.Number, ds []jsonDouble) []byte {
	if len(ds) == 0 {
		return b
	}
	var packed []byte
	for _, d := range ds {
		packed = protowire.AppendFixed64(packed, math.Float64bits(float64(d)))
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, packed)
}

// Common messages.

func pbKeyValue(b []byte, kv jsonKeyValue) ([]byte, error) {
	b = pbString(b, 1, kv.Key)
	return pbMsg(b, 2, func(sub []byte) ([]byte, error) { return pbAnyValue(sub, kv.Value) })
}

func pbAttrs(b []byte, num protowire.Number, kvs []jsonKeyValue) ([]byte, error) {
	return pbRepeated(b, num, kvs, pbKeyValue)
}

func pbAnyValue(b []byte, v jsonValue) ([]byte, error) {
	switch {
	case v.StringValue != nil:
		// A oneof member is written even when empty.
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		return protowire.AppendString(b, *v.StringValue), nil
	case v.BoolValue != nil:
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		return protowire.AppendVarint(b, protowire.EncodeBool(*v.BoolValue)), nil
	case v.IntValue != nil:
		n, err := parseI64(*v.IntValue)
		if err != nil {
			return nil, err
		}
		b = protowire.AppendTag(b, 3, protowire.VarintType)
		return protowire.AppendVarint(b, uint64(n)), nil
	case v.DoubleValue != nil:
		f, err := doubleOf(v.DoubleValue)
		if err != nil {
			return nil, err
		}
		return pbDouble(b, 4, f), nil
	case v.ArrayValue != nil:
		return pbMsg(b, 5, func(sub []byte) ([]byte, error) {
			return pbRepeated(sub, 1, v.ArrayValue.Values, pbAnyValue)
		})
	case v.KvlistValue != nil:
		return pbMsg(b, 6, func(sub []byte) ([]byte, error) {
			return pbAttrs(sub, 1, v.KvlistValue.Values)
		})
	case v.BytesValue != nil:
		raw, err := base64.StdEncoding.DecodeString(*v.BytesValue)
		if err != nil {
			return nil, fmt.Errorf("spoolexporter: bad base64 bytes value: %w", err)
		}
		b = protowire.AppendTag(b, 7, protowire.BytesType)
		return protowire.AppendBytes(b, raw), nil
	}
	return b, nil
}

// doubleOf reads a jsonValue's DoubleValue: a *float64, or the string the
// JSON mapping uses for NaN and the infinities.
func doubleOf(v any) (float64, error) {
	switch d := v.(type) {
	case *float64:
		return *d, nil
	case float64:
		return d, nil
	case string:
		switch d {
		case "NaN":
			return math.NaN(), nil
		case "Infinity":
			return math.Inf(1), nil
		case "-Infinity":
			return math.Inf(-1), nil
		}
	}
	return 0, fmt.Errorf("spoolexporter: bad double value %v", v)
}

func pbResource(b []byte, r jsonResource) ([]byte, error) { return pbAttrs(b, 1, r.Attributes) }

func pbScope(b []byte, s jsonScope) ([]byte, error) {
	b = pbString(b, 1, s.Name)
	b = pbString(b, 2, s.Version)
	return pbAttrs(b, 3, s.Attributes)
}

// pbResourceAndScope writes the resource (field 1) every Resource* message
// starts with. A resource is written even when empty, as the official
// encoders do.
func pbResourceField(b []byte, r jsonResource) ([]byte, error) {
	return pbMsg(b, 1, func(sub []byte) ([]byte, error) { return pbResource(sub, r) })
}

func pbScopeField(b []byte, s jsonScope) ([]byte, error) {
	return pbMsg(b, 1, func(sub []byte) ([]byte, error) { return pbScope(sub, s) })
}

// Traces.

func pbResourceSpans(b []byte, rs jsonResourceSpans) ([]byte, error) {
	b, err := pbResourceField(b, rs.Resource)
	if err != nil {
		return nil, err
	}
	b, err = pbRepeated(b, 2, rs.ScopeSpans, func(b []byte, ss jsonScopeSpans) ([]byte, error) {
		b, err := pbScopeField(b, ss.Scope)
		if err != nil {
			return nil, err
		}
		b, err = pbRepeated(b, 2, ss.Spans, pbSpan)
		if err != nil {
			return nil, err
		}
		return pbString(b, 3, ss.SchemaURL), nil
	})
	if err != nil {
		return nil, err
	}
	return pbString(b, 3, rs.SchemaURL), nil
}

func pbSpan(b []byte, s jsonSpan) ([]byte, error) {
	var err error
	if b, err = pbHexBytes(b, 1, s.TraceID); err != nil {
		return nil, err
	}
	if b, err = pbHexBytes(b, 2, s.SpanID); err != nil {
		return nil, err
	}
	b = pbString(b, 3, s.TraceState)
	if b, err = pbHexBytes(b, 4, s.ParentSpanID); err != nil {
		return nil, err
	}
	b = pbString(b, 5, s.Name)
	b = pbVarint(b, 6, uint64(s.Kind))
	if b, err = pbTime(b, 7, s.StartTimeUnixNano); err != nil {
		return nil, err
	}
	if b, err = pbTime(b, 8, s.EndTimeUnixNano); err != nil {
		return nil, err
	}
	if b, err = pbAttrs(b, 9, s.Attributes); err != nil {
		return nil, err
	}
	b = pbVarint(b, 10, uint64(s.DroppedAttributesCount))
	if b, err = pbRepeated(b, 11, s.Events, pbEvent); err != nil {
		return nil, err
	}
	b = pbVarint(b, 12, uint64(s.DroppedEventsCount))
	if b, err = pbRepeated(b, 13, s.Links, pbLink); err != nil {
		return nil, err
	}
	b = pbVarint(b, 14, uint64(s.DroppedLinksCount))
	if s.Status != nil {
		b, err = pbMsg(b, 15, func(sub []byte) ([]byte, error) {
			sub = pbString(sub, 2, s.Status.Message)
			return pbVarint(sub, 3, uint64(s.Status.Code)), nil
		})
		if err != nil {
			return nil, err
		}
	}
	return pbFixed32(b, 16, s.Flags), nil
}

func pbEvent(b []byte, e jsonEvent) ([]byte, error) {
	b, err := pbTime(b, 1, e.TimeUnixNano)
	if err != nil {
		return nil, err
	}
	b = pbString(b, 2, e.Name)
	if b, err = pbAttrs(b, 3, e.Attributes); err != nil {
		return nil, err
	}
	return pbVarint(b, 4, uint64(e.DroppedAttributesCount)), nil
}

func pbLink(b []byte, l jsonLink) ([]byte, error) {
	b, err := pbHexBytes(b, 1, l.TraceID)
	if err != nil {
		return nil, err
	}
	if b, err = pbHexBytes(b, 2, l.SpanID); err != nil {
		return nil, err
	}
	b = pbString(b, 3, l.TraceState)
	if b, err = pbAttrs(b, 4, l.Attributes); err != nil {
		return nil, err
	}
	b = pbVarint(b, 5, uint64(l.DroppedAttributesCount))
	return pbFixed32(b, 6, l.Flags), nil
}

// Logs.

func pbResourceLogs(b []byte, rl jsonResourceLogs) ([]byte, error) {
	b, err := pbResourceField(b, rl.Resource)
	if err != nil {
		return nil, err
	}
	b, err = pbRepeated(b, 2, rl.ScopeLogs, func(b []byte, sl jsonScopeLogs) ([]byte, error) {
		b, err := pbScopeField(b, sl.Scope)
		if err != nil {
			return nil, err
		}
		b, err = pbRepeated(b, 2, sl.LogRecords, pbLogRecord)
		if err != nil {
			return nil, err
		}
		return pbString(b, 3, sl.SchemaURL), nil
	})
	if err != nil {
		return nil, err
	}
	return pbString(b, 3, rl.SchemaURL), nil
}

func pbLogRecord(b []byte, r jsonLogRecord) ([]byte, error) {
	b, err := pbTime(b, 1, r.TimeUnixNano)
	if err != nil {
		return nil, err
	}
	b = pbVarint(b, 2, uint64(r.SeverityNumber))
	b = pbString(b, 3, r.SeverityText)
	if r.Body != nil {
		body := *r.Body
		if b, err = pbMsg(b, 5, func(sub []byte) ([]byte, error) { return pbAnyValue(sub, body) }); err != nil {
			return nil, err
		}
	}
	if b, err = pbAttrs(b, 6, r.Attributes); err != nil {
		return nil, err
	}
	b = pbVarint(b, 7, uint64(r.DroppedAttributesCount))
	b = pbFixed32(b, 8, r.Flags)
	if b, err = pbHexBytes(b, 9, r.TraceID); err != nil {
		return nil, err
	}
	if b, err = pbHexBytes(b, 10, r.SpanID); err != nil {
		return nil, err
	}
	if b, err = pbTime(b, 11, r.ObservedTimeUnixNano); err != nil {
		return nil, err
	}
	return pbString(b, 12, r.EventName), nil
}

// Metrics.

func pbResourceMetrics(b []byte, rm jsonResourceMetrics) ([]byte, error) {
	b, err := pbResourceField(b, rm.Resource)
	if err != nil {
		return nil, err
	}
	b, err = pbRepeated(b, 2, rm.ScopeMetrics, func(b []byte, sm jsonScopeMetrics) ([]byte, error) {
		b, err := pbScopeField(b, sm.Scope)
		if err != nil {
			return nil, err
		}
		b, err = pbRepeated(b, 2, sm.Metrics, pbMetric)
		if err != nil {
			return nil, err
		}
		return pbString(b, 3, sm.SchemaURL), nil
	})
	if err != nil {
		return nil, err
	}
	return pbString(b, 3, rm.SchemaURL), nil
}

func pbMetric(b []byte, m jsonMetric) ([]byte, error) {
	b = pbString(b, 1, m.Name)
	b = pbString(b, 2, m.Description)
	b = pbString(b, 3, m.Unit)
	var err error
	switch {
	case m.Gauge != nil:
		b, err = pbMsg(b, 5, func(sub []byte) ([]byte, error) {
			return pbRepeated(sub, 1, m.Gauge.DataPoints, pbNumberPoint)
		})
	case m.Sum != nil:
		b, err = pbMsg(b, 7, func(sub []byte) ([]byte, error) {
			sub, err := pbRepeated(sub, 1, m.Sum.DataPoints, pbNumberPoint)
			if err != nil {
				return nil, err
			}
			sub = pbVarint(sub, 2, uint64(m.Sum.AggregationTemporality))
			return pbBool(sub, 3, m.Sum.IsMonotonic), nil
		})
	case m.Histogram != nil:
		b, err = pbMsg(b, 9, func(sub []byte) ([]byte, error) {
			sub, err := pbRepeated(sub, 1, m.Histogram.DataPoints, pbHistogramPoint)
			if err != nil {
				return nil, err
			}
			return pbVarint(sub, 2, uint64(m.Histogram.AggregationTemporality)), nil
		})
	case m.ExponentialHistogram != nil:
		b, err = pbMsg(b, 10, func(sub []byte) ([]byte, error) {
			sub, err := pbRepeated(sub, 1, m.ExponentialHistogram.DataPoints, pbExponentialPoint)
			if err != nil {
				return nil, err
			}
			return pbVarint(sub, 2, uint64(m.ExponentialHistogram.AggregationTemporality)), nil
		})
	case m.Summary != nil:
		b, err = pbMsg(b, 11, func(sub []byte) ([]byte, error) {
			return pbRepeated(sub, 1, m.Summary.DataPoints, pbSummaryPoint)
		})
	}
	return b, err
}

func pbExemplar(b []byte, e jsonExemplar) ([]byte, error) {
	b, err := pbAttrs(b, 7, e.FilteredAttributes)
	if err != nil {
		return nil, err
	}
	if b, err = pbTime(b, 2, e.TimeUnixNano); err != nil {
		return nil, err
	}
	b = pbOptDouble(b, 3, e.AsDouble)
	if b, err = pbHexBytes(b, 4, e.SpanID); err != nil {
		return nil, err
	}
	if b, err = pbHexBytes(b, 5, e.TraceID); err != nil {
		return nil, err
	}
	return pbAsInt(b, 6, e.AsInt)
}

func pbNumberPoint(b []byte, p jsonNumberPoint) ([]byte, error) {
	b, err := pbTime(b, 2, p.StartTimeUnixNano)
	if err != nil {
		return nil, err
	}
	if b, err = pbTime(b, 3, p.TimeUnixNano); err != nil {
		return nil, err
	}
	b = pbOptDouble(b, 4, p.AsDouble)
	if b, err = pbRepeated(b, 5, p.Exemplars, pbExemplar); err != nil {
		return nil, err
	}
	if b, err = pbAsInt(b, 6, p.AsInt); err != nil {
		return nil, err
	}
	return pbAttrs(b, 7, p.Attributes)
}

func pbHistogramPoint(b []byte, p jsonHistogramPoint) ([]byte, error) {
	b, err := pbTime(b, 2, p.StartTimeUnixNano)
	if err != nil {
		return nil, err
	}
	if b, err = pbTime(b, 3, p.TimeUnixNano); err != nil {
		return nil, err
	}
	n, err := parseU64(p.Count)
	if err != nil {
		return nil, err
	}
	b = pbFixed64(b, 4, n)
	b = pbOptDouble(b, 5, p.Sum)
	if b, err = pbPackedU64Strings(b, 6, p.BucketCounts, true); err != nil {
		return nil, err
	}
	b = pbPackedDoubles(b, 7, p.ExplicitBounds)
	if b, err = pbRepeated(b, 8, p.Exemplars, pbExemplar); err != nil {
		return nil, err
	}
	if b, err = pbAttrs(b, 9, p.Attributes); err != nil {
		return nil, err
	}
	b = pbOptDouble(b, 11, p.Min)
	return pbOptDouble(b, 12, p.Max), nil
}

func pbBuckets(b []byte, bk *jsonBuckets) ([]byte, error) {
	b = pbSint32(b, 1, bk.Offset)
	return pbPackedU64Strings(b, 2, bk.BucketCounts, false)
}

func pbExponentialPoint(b []byte, p jsonExponentialPoint) ([]byte, error) {
	b, err := pbAttrs(b, 1, p.Attributes)
	if err != nil {
		return nil, err
	}
	if b, err = pbTime(b, 2, p.StartTimeUnixNano); err != nil {
		return nil, err
	}
	if b, err = pbTime(b, 3, p.TimeUnixNano); err != nil {
		return nil, err
	}
	n, err := parseU64(p.Count)
	if err != nil {
		return nil, err
	}
	b = pbFixed64(b, 4, n)
	b = pbOptDouble(b, 5, p.Sum)
	b = pbSint32(b, 6, p.Scale)
	if b, err = pbTime(b, 7, p.ZeroCount); err != nil {
		return nil, err
	}
	if p.Positive != nil {
		if b, err = pbMsg(b, 8, func(sub []byte) ([]byte, error) { return pbBuckets(sub, p.Positive) }); err != nil {
			return nil, err
		}
	}
	if p.Negative != nil {
		if b, err = pbMsg(b, 9, func(sub []byte) ([]byte, error) { return pbBuckets(sub, p.Negative) }); err != nil {
			return nil, err
		}
	}
	if b, err = pbRepeated(b, 11, p.Exemplars, pbExemplar); err != nil {
		return nil, err
	}
	b = pbOptDouble(b, 12, p.Min)
	b = pbOptDouble(b, 13, p.Max)
	return pbOptDouble(b, 14, p.ZeroThreshold), nil
}

func pbSummaryPoint(b []byte, p jsonSummaryPoint) ([]byte, error) {
	b, err := pbTime(b, 2, p.StartTimeUnixNano)
	if err != nil {
		return nil, err
	}
	if b, err = pbTime(b, 3, p.TimeUnixNano); err != nil {
		return nil, err
	}
	n, err := parseU64(p.Count)
	if err != nil {
		return nil, err
	}
	b = pbFixed64(b, 4, n)
	b = pbDoubleDefault(b, 5, float64(p.Sum))
	b, err = pbRepeated(b, 6, p.QuantileValues, func(b []byte, q jsonQuantile) ([]byte, error) {
		b = pbDoubleDefault(b, 1, float64(q.Quantile))
		return pbDoubleDefault(b, 2, float64(q.Value)), nil
	})
	if err != nil {
		return nil, err
	}
	return pbAttrs(b, 7, p.Attributes)
}
