package spoolexporter

import (
	"context"
	"encoding/hex"
	"math"
	"strconv"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// MetricExporter writes metrics to a Writer. It never returns an error.
type MetricExporter struct {
	w           *Writer
	temporality sdkmetric.TemporalitySelector
	aggregation sdkmetric.AggregationSelector
	stopped     atomic.Bool
}

var _ sdkmetric.Exporter = (*MetricExporter)(nil)

// MetricOption configures a MetricExporter.
type MetricOption func(*MetricExporter)

// WithTemporality chooses each instrument's temporality. The default is
// cumulative, as for the OTLP exporters.
func WithTemporality(s sdkmetric.TemporalitySelector) MetricOption {
	return func(e *MetricExporter) { e.temporality = s }
}

// WithAggregation chooses each instrument's aggregation.
func WithAggregation(s sdkmetric.AggregationSelector) MetricOption {
	return func(e *MetricExporter) { e.aggregation = s }
}

// NewMetricExporter returns a metric exporter writing to w. Register it with
// a periodic reader.
func NewMetricExporter(w *Writer, opts ...MetricOption) *MetricExporter {
	e := &MetricExporter{
		w:           w,
		temporality: sdkmetric.DefaultTemporalitySelector,
		aggregation: sdkmetric.DefaultAggregationSelector,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Temporality returns the temporality for an instrument kind.
func (e *MetricExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return e.temporality(k)
}

// Aggregation returns the aggregation for an instrument kind.
func (e *MetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return e.aggregation(k)
}

// Export writes rm as one ExportMetricsServiceRequest line.
func (e *MetricExporter) Export(_ context.Context, rm *metricdata.ResourceMetrics) error {
	if rm == nil {
		return nil
	}
	req, points := metricsRequest(rm)
	if points == 0 {
		return nil
	}
	if e.stopped.Load() {
		e.w.dropped.Add(int64(points))
		return nil
	}
	e.w.writeEncoded(req, points)
	return nil
}

// ForceFlush flushes the Writer's open file to disk, bounded by its
// SyncTimeout.
func (e *MetricExporter) ForceFlush(context.Context) error {
	e.w.Sync()
	return nil
}

// Shutdown stops the exporter. It leaves the Writer open; see
// TraceExporter.Shutdown.
func (e *MetricExporter) Shutdown(context.Context) error {
	e.stopped.Store(true)
	return nil
}

type jsonMetricsRequest struct {
	ResourceMetrics []jsonResourceMetrics `json:"resourceMetrics"`
}

type jsonResourceMetrics struct {
	Resource     jsonResource       `json:"resource"`
	ScopeMetrics []jsonScopeMetrics `json:"scopeMetrics"`
	SchemaURL    string             `json:"schemaUrl,omitempty"`
}

type jsonScopeMetrics struct {
	Scope     jsonScope    `json:"scope"`
	Metrics   []jsonMetric `json:"metrics"`
	SchemaURL string       `json:"schemaUrl,omitempty"`
}

type jsonMetric struct {
	Name                 string                    `json:"name"`
	Description          string                    `json:"description,omitempty"`
	Unit                 string                    `json:"unit,omitempty"`
	Gauge                *jsonGauge                `json:"gauge,omitempty"`
	Sum                  *jsonSum                  `json:"sum,omitempty"`
	Histogram            *jsonHistogram            `json:"histogram,omitempty"`
	ExponentialHistogram *jsonExponentialHistogram `json:"exponentialHistogram,omitempty"`
	Summary              *jsonSummary              `json:"summary,omitempty"`
}

type jsonGauge struct {
	DataPoints []jsonNumberPoint `json:"dataPoints"`
}

type jsonSum struct {
	DataPoints             []jsonNumberPoint `json:"dataPoints"`
	AggregationTemporality int               `json:"aggregationTemporality,omitempty"`
	IsMonotonic            bool              `json:"isMonotonic,omitempty"`
}

type jsonHistogram struct {
	DataPoints             []jsonHistogramPoint `json:"dataPoints"`
	AggregationTemporality int                  `json:"aggregationTemporality,omitempty"`
}

type jsonExponentialHistogram struct {
	DataPoints             []jsonExponentialPoint `json:"dataPoints"`
	AggregationTemporality int                    `json:"aggregationTemporality,omitempty"`
}

type jsonSummary struct {
	DataPoints []jsonSummaryPoint `json:"dataPoints"`
}

type jsonNumberPoint struct {
	Attributes        []jsonKeyValue `json:"attributes,omitempty"`
	StartTimeUnixNano string         `json:"startTimeUnixNano,omitempty"`
	TimeUnixNano      string         `json:"timeUnixNano,omitempty"`
	AsDouble          *jsonDouble    `json:"asDouble,omitempty"`
	AsInt             *string        `json:"asInt,omitempty"`
	Exemplars         []jsonExemplar `json:"exemplars,omitempty"`
}

type jsonHistogramPoint struct {
	Attributes        []jsonKeyValue `json:"attributes,omitempty"`
	StartTimeUnixNano string         `json:"startTimeUnixNano,omitempty"`
	TimeUnixNano      string         `json:"timeUnixNano,omitempty"`
	Count             string         `json:"count"`
	Sum               *jsonDouble    `json:"sum,omitempty"`
	BucketCounts      []string       `json:"bucketCounts,omitempty"`
	ExplicitBounds    []jsonDouble   `json:"explicitBounds,omitempty"`
	Exemplars         []jsonExemplar `json:"exemplars,omitempty"`
	Min               *jsonDouble    `json:"min,omitempty"`
	Max               *jsonDouble    `json:"max,omitempty"`
}

type jsonExponentialPoint struct {
	Attributes        []jsonKeyValue `json:"attributes,omitempty"`
	StartTimeUnixNano string         `json:"startTimeUnixNano,omitempty"`
	TimeUnixNano      string         `json:"timeUnixNano,omitempty"`
	Count             string         `json:"count"`
	Sum               *jsonDouble    `json:"sum,omitempty"`
	Scale             int32          `json:"scale,omitempty"`
	ZeroCount         string         `json:"zeroCount,omitempty"`
	Positive          *jsonBuckets   `json:"positive,omitempty"`
	Negative          *jsonBuckets   `json:"negative,omitempty"`
	Exemplars         []jsonExemplar `json:"exemplars,omitempty"`
	Min               *jsonDouble    `json:"min,omitempty"`
	Max               *jsonDouble    `json:"max,omitempty"`
	ZeroThreshold     *jsonDouble    `json:"zeroThreshold,omitempty"`
}

type jsonBuckets struct {
	Offset       int32    `json:"offset,omitempty"`
	BucketCounts []string `json:"bucketCounts,omitempty"`
}

type jsonSummaryPoint struct {
	Attributes        []jsonKeyValue `json:"attributes,omitempty"`
	StartTimeUnixNano string         `json:"startTimeUnixNano,omitempty"`
	TimeUnixNano      string         `json:"timeUnixNano,omitempty"`
	Count             string         `json:"count"`
	Sum               jsonDouble     `json:"sum"`
	QuantileValues    []jsonQuantile `json:"quantileValues,omitempty"`
}

type jsonQuantile struct {
	Quantile jsonDouble `json:"quantile"`
	Value    jsonDouble `json:"value"`
}

type jsonExemplar struct {
	FilteredAttributes []jsonKeyValue `json:"filteredAttributes,omitempty"`
	TimeUnixNano       string         `json:"timeUnixNano,omitempty"`
	AsDouble           *jsonDouble    `json:"asDouble,omitempty"`
	AsInt              *string        `json:"asInt,omitempty"`
	SpanID             string         `json:"spanId,omitempty"`
	TraceID            string         `json:"traceId,omitempty"`
}

// jsonDouble is a double field, written as the strings the protobuf JSON
// mapping uses for NaN and the infinities.
type jsonDouble float64

func (d jsonDouble) MarshalJSON() ([]byte, error) {
	f := float64(d)
	switch {
	case math.IsNaN(f):
		return []byte(`"NaN"`), nil
	case math.IsInf(f, 1):
		return []byte(`"Infinity"`), nil
	case math.IsInf(f, -1):
		return []byte(`"-Infinity"`), nil
	}
	return strconv.AppendFloat(nil, f, 'g', -1, 64), nil
}

func double(f float64) *jsonDouble {
	d := jsonDouble(f)
	return &d
}

func temporalityOf(t metricdata.Temporality) int {
	switch t {
	case metricdata.DeltaTemporality:
		return 1
	case metricdata.CumulativeTemporality:
		return 2
	}
	return 0
}

func uintString(n uint64) string { return strconv.FormatUint(n, 10) }

func uintStrings(ns []uint64) []string {
	if len(ns) == 0 {
		return nil
	}
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = uintString(n)
	}
	return out
}

func setAttrs(s attribute.Set) []jsonKeyValue { return encodeAttrs(s.ToSlice()) }

// number sets a point's value as asInt or asDouble, by its Go type.
func number[N int64 | float64](v N) (*jsonDouble, *string) {
	switch x := any(v).(type) {
	case int64:
		s := strconv.FormatInt(x, 10)
		return nil, &s
	case float64:
		return double(x), nil
	}
	return nil, nil
}

func extrema[N int64 | float64](e metricdata.Extrema[N]) *jsonDouble {
	if v, ok := e.Value(); ok {
		return double(float64(v))
	}
	return nil
}

func exemplars[N int64 | float64](es []metricdata.Exemplar[N]) []jsonExemplar {
	if len(es) == 0 {
		return nil
	}
	out := make([]jsonExemplar, len(es))
	for i, e := range es {
		d, n := number(e.Value)
		out[i] = jsonExemplar{
			FilteredAttributes: encodeAttrs(e.FilteredAttributes),
			TimeUnixNano:       unixNano(e.Time),
			AsDouble:           d,
			AsInt:              n,
			SpanID:             hex.EncodeToString(e.SpanID),
			TraceID:            hex.EncodeToString(e.TraceID),
		}
	}
	return out
}

func numberPoints[N int64 | float64](dps []metricdata.DataPoint[N]) []jsonNumberPoint {
	out := make([]jsonNumberPoint, len(dps))
	for i, p := range dps {
		d, n := number(p.Value)
		out[i] = jsonNumberPoint{
			Attributes:        setAttrs(p.Attributes),
			StartTimeUnixNano: unixNano(p.StartTime),
			TimeUnixNano:      unixNano(p.Time),
			AsDouble:          d,
			AsInt:             n,
			Exemplars:         exemplars(p.Exemplars),
		}
	}
	return out
}

func histogramPoints[N int64 | float64](dps []metricdata.HistogramDataPoint[N]) []jsonHistogramPoint {
	out := make([]jsonHistogramPoint, len(dps))
	for i, p := range dps {
		bounds := make([]jsonDouble, len(p.Bounds))
		for j, b := range p.Bounds {
			bounds[j] = jsonDouble(b)
		}
		out[i] = jsonHistogramPoint{
			Attributes:        setAttrs(p.Attributes),
			StartTimeUnixNano: unixNano(p.StartTime),
			TimeUnixNano:      unixNano(p.Time),
			Count:             uintString(p.Count),
			Sum:               double(float64(p.Sum)),
			BucketCounts:      uintStrings(p.BucketCounts),
			ExplicitBounds:    bounds,
			Exemplars:         exemplars(p.Exemplars),
			Min:               extrema(p.Min),
			Max:               extrema(p.Max),
		}
	}
	return out
}

func buckets(b metricdata.ExponentialBucket) *jsonBuckets {
	if b.Offset == 0 && len(b.Counts) == 0 {
		return nil
	}
	return &jsonBuckets{Offset: b.Offset, BucketCounts: uintStrings(b.Counts)}
}

func exponentialPoints[N int64 | float64](dps []metricdata.ExponentialHistogramDataPoint[N]) []jsonExponentialPoint {
	out := make([]jsonExponentialPoint, len(dps))
	for i, p := range dps {
		var zt *jsonDouble
		if p.ZeroThreshold != 0 {
			zt = double(p.ZeroThreshold)
		}
		var zc string
		if p.ZeroCount != 0 {
			zc = uintString(p.ZeroCount)
		}
		out[i] = jsonExponentialPoint{
			Attributes:        setAttrs(p.Attributes),
			StartTimeUnixNano: unixNano(p.StartTime),
			TimeUnixNano:      unixNano(p.Time),
			Count:             uintString(p.Count),
			Sum:               double(float64(p.Sum)),
			Scale:             p.Scale,
			ZeroCount:         zc,
			Positive:          buckets(p.PositiveBucket),
			Negative:          buckets(p.NegativeBucket),
			Exemplars:         exemplars(p.Exemplars),
			Min:               extrema(p.Min),
			Max:               extrema(p.Max),
			ZeroThreshold:     zt,
		}
	}
	return out
}

func summaryPoints(dps []metricdata.SummaryDataPoint) []jsonSummaryPoint {
	out := make([]jsonSummaryPoint, len(dps))
	for i, p := range dps {
		qs := make([]jsonQuantile, len(p.QuantileValues))
		for j, q := range p.QuantileValues {
			qs[j] = jsonQuantile{Quantile: jsonDouble(q.Quantile), Value: jsonDouble(q.Value)}
		}
		out[i] = jsonSummaryPoint{
			Attributes:        setAttrs(p.Attributes),
			StartTimeUnixNano: unixNano(p.StartTime),
			TimeUnixNano:      unixNano(p.Time),
			Count:             uintString(p.Count),
			Sum:               jsonDouble(p.Sum),
			QuantileValues:    qs,
		}
	}
	return out
}

// encodeMetric returns the metric and its number of data points; a metric
// of an aggregation this package does not know has none and is left out.
func encodeMetric(m metricdata.Metrics) (jsonMetric, int) {
	jm := jsonMetric{Name: m.Name, Description: m.Description, Unit: m.Unit}
	switch d := m.Data.(type) {
	case metricdata.Gauge[int64]:
		jm.Gauge = &jsonGauge{DataPoints: numberPoints(d.DataPoints)}
		return jm, len(d.DataPoints)
	case metricdata.Gauge[float64]:
		jm.Gauge = &jsonGauge{DataPoints: numberPoints(d.DataPoints)}
		return jm, len(d.DataPoints)
	case metricdata.Sum[int64]:
		jm.Sum = &jsonSum{DataPoints: numberPoints(d.DataPoints), AggregationTemporality: temporalityOf(d.Temporality), IsMonotonic: d.IsMonotonic}
		return jm, len(d.DataPoints)
	case metricdata.Sum[float64]:
		jm.Sum = &jsonSum{DataPoints: numberPoints(d.DataPoints), AggregationTemporality: temporalityOf(d.Temporality), IsMonotonic: d.IsMonotonic}
		return jm, len(d.DataPoints)
	case metricdata.Histogram[int64]:
		jm.Histogram = &jsonHistogram{DataPoints: histogramPoints(d.DataPoints), AggregationTemporality: temporalityOf(d.Temporality)}
		return jm, len(d.DataPoints)
	case metricdata.Histogram[float64]:
		jm.Histogram = &jsonHistogram{DataPoints: histogramPoints(d.DataPoints), AggregationTemporality: temporalityOf(d.Temporality)}
		return jm, len(d.DataPoints)
	case metricdata.ExponentialHistogram[int64]:
		jm.ExponentialHistogram = &jsonExponentialHistogram{DataPoints: exponentialPoints(d.DataPoints), AggregationTemporality: temporalityOf(d.Temporality)}
		return jm, len(d.DataPoints)
	case metricdata.ExponentialHistogram[float64]:
		jm.ExponentialHistogram = &jsonExponentialHistogram{DataPoints: exponentialPoints(d.DataPoints), AggregationTemporality: temporalityOf(d.Temporality)}
		return jm, len(d.DataPoints)
	case metricdata.Summary:
		jm.Summary = &jsonSummary{DataPoints: summaryPoints(d.DataPoints)}
		return jm, len(d.DataPoints)
	}
	return jm, 0
}

// metricsRequest encodes rm, leaving out metrics with no data points, and
// returns how many data points it holds.
func metricsRequest(rm *metricdata.ResourceMetrics) (jsonMetricsRequest, int) {
	rs := jsonResourceMetrics{Resource: encodeResource(rm.Resource), SchemaURL: schemaURL(rm.Resource)}
	points := 0
	for _, sm := range rm.ScopeMetrics {
		js := jsonScopeMetrics{Scope: encodeScope(sm.Scope), SchemaURL: sm.Scope.SchemaURL}
		for _, m := range sm.Metrics {
			jm, n := encodeMetric(m)
			if n == 0 {
				continue
			}
			js.Metrics = append(js.Metrics, jm)
			points += n
		}
		if len(js.Metrics) > 0 {
			rs.ScopeMetrics = append(rs.ScopeMetrics, js)
		}
	}
	if points == 0 {
		return jsonMetricsRequest{}, 0
	}
	return jsonMetricsRequest{ResourceMetrics: []jsonResourceMetrics{rs}}, points
}
