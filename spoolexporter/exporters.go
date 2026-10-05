package spoolexporter

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// The exporters are OpenTelemetry's own OTLP/HTTP exporters, so the SDK's data is translated to
// OTLP exactly as it would be for a network collector. Their HTTP client never reaches a network:
// its transport writes each request body into the spool as an OTLP JSON line and answers it.

// endpoint is a placeholder host; the transport never dials it.
const endpoint = "spool.invalid"

// SpanExporter returns a span exporter writing to the spool.
func (s *Spool) SpanExporter(ctx context.Context) (*otlptrace.Exporter, error) {
	return otlptracehttp.New(ctx,
		otlptracehttp.WithHTTPClient(s.client()),
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
		otlptracehttp.WithCompression(otlptracehttp.NoCompression),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
	)
}

// MetricExporter returns a metric exporter writing to the spool. opts may choose temporality and
// aggregation; options naming an endpoint, client, compression or retry are overridden.
func (s *Spool) MetricExporter(ctx context.Context, opts ...otlpmetrichttp.Option) (sdkmetric.Exporter, error) {
	return otlpmetrichttp.New(ctx, append(opts,
		otlpmetrichttp.WithHTTPClient(s.client()),
		otlpmetrichttp.WithEndpoint(endpoint),
		otlpmetrichttp.WithInsecure(),
		otlpmetrichttp.WithCompression(otlpmetrichttp.NoCompression),
		otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: false}),
	)...)
}

// LogExporter returns a log exporter writing to the spool.
func (s *Spool) LogExporter(ctx context.Context) (sdklog.Exporter, error) {
	return otlploghttp.New(ctx,
		otlploghttp.WithHTTPClient(s.client()),
		otlploghttp.WithEndpoint(endpoint),
		otlploghttp.WithInsecure(),
		otlploghttp.WithCompression(otlploghttp.NoCompression),
		otlploghttp.WithRetry(otlploghttp.RetryConfig{Enabled: false}),
	)
}

func (s *Spool) client() *http.Client { return &http.Client{Transport: transport{s}} }

type transport struct{ s *Spool }

// RoundTrip writes the request into the spool and always answers success, so the exporter never
// retries or reports an error; what could not be written is counted instead.
func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.s.export(r)
	return &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": {"application/x-protobuf"}},
		Body:       http.NoBody,
		Request:    r,
	}, nil
}

func (s *Spool) export(r *http.Request) {
	if r.Body == nil {
		s.malformed.Add(1)
		return
	}
	defer func() { _ = r.Body.Close() }()
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			s.malformed.Add(1)
			return
		}
		body = zr
	}
	// Read one byte past the line limit: anything that large is dropped without decoding.
	b, err := io.ReadAll(io.LimitReader(body, int64(s.opts.maxLine)+1))
	if err != nil {
		s.malformed.Add(1)
		return
	}
	if len(b) > s.opts.maxLine {
		s.oversized.Add(1)
		return
	}
	line, err := toJSON(r.URL.Path, b)
	if err != nil {
		s.malformed.Add(1)
		return
	}
	s.write(bytes.TrimSpace(line))
}

// toJSON re-encodes an OTLP protobuf export request as OTLP JSON, with the collector's own
// encoder, so ids are hex and enums are numbers as the OTLP JSON encoding requires.
func toJSON(path string, b []byte) ([]byte, error) {
	switch path {
	case "/v1/traces":
		req := ptraceotlp.NewExportRequest()
		if err := req.UnmarshalProto(b); err != nil {
			return nil, err
		}
		return req.MarshalJSON()
	case "/v1/metrics":
		req := pmetricotlp.NewExportRequest()
		if err := req.UnmarshalProto(b); err != nil {
			return nil, err
		}
		return req.MarshalJSON()
	case "/v1/logs":
		req := plogotlp.NewExportRequest()
		if err := req.UnmarshalProto(b); err != nil {
			return nil, err
		}
		return req.MarshalJSON()
	}
	return nil, errUnknownSignal
}
