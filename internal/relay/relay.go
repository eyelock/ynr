// Package relay receives OTLP/HTTP from a vendor CLI and writes it into a spool folder (ADR-004).
// Vendor CLIs export only over the network; the relay turns that into spool files, so their
// telemetry gets the same reading, stamping and shipping as everything else. The agent can
// reach the relay, so it listens on loopback only and bounds request size, memory and rate.
package relay

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"golang.org/x/time/rate"

	"github.com/eyelock/ynr/spoolexporter"
)

// Defaults for Config.
const (
	DefaultListen     = "127.0.0.1:0"
	DefaultMaxRequest = 4 << 20  // bytes after decompression; the JSON line it becomes is limited too
	DefaultMaxMemory  = 64 << 20 // bytes of requests held at once
	DefaultRate       = 100      // requests a second, sustained
	DefaultBurst      = 200
	DefaultDrain      = 5 * time.Second
)

// Config configures a Relay. Zero values take the defaults.
type Config struct {
	// Dir is the writer folder the relay writes into: a run's folder in a factory job.
	Dir string
	// Listen is a loopback address; port 0 picks a free one, so parallel runs never collide.
	Listen string
	// Service and Instance name the relay's spool files.
	Service, Instance string
	MaxRequest        int64
	MaxMemory         int64
	Rate              float64
	Burst             int
	// Drain bounds how long a stopping relay waits for requests in flight and the final flush.
	Drain time.Duration
}

// Stats counts what the relay did with the requests it received.
type Stats struct {
	Accepted  int64 // requests written to the spool
	TooLarge  int64 // refused: over MaxRequest
	Busy      int64 // refused: over MaxMemory
	Limited   int64 // refused: over the rate limit
	Malformed int64 // refused: not decodable OTLP, or an unknown path or content type
	Spool     spoolexporter.Stats
}

// Relay is a running relay.
type Relay struct {
	cfg      Config
	w        *spoolexporter.Writer
	ln       net.Listener
	srv      *http.Server
	limiter  *rate.Limiter
	inflight atomic.Int64

	accepted, tooLarge, busy, limited, malformed atomic.Int64
}

// New validates cfg and starts listening. Serve then handles requests.
func New(cfg Config) (*Relay, error) {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Service == "" {
		cfg.Service = "ynr-relay"
	}
	if cfg.Instance == "" {
		cfg.Instance = strconv.Itoa(os.Getpid())
	}
	if cfg.MaxRequest <= 0 {
		cfg.MaxRequest = DefaultMaxRequest
	}
	if cfg.MaxMemory <= 0 {
		cfg.MaxMemory = DefaultMaxMemory
	}
	if cfg.MaxMemory < cfg.MaxRequest {
		return nil, fmt.Errorf("relay: memory limit %d is below the request limit %d", cfg.MaxMemory, cfg.MaxRequest)
	}
	if cfg.Rate <= 0 {
		cfg.Rate = DefaultRate
	}
	if cfg.Burst <= 0 {
		cfg.Burst = DefaultBurst
	}
	if cfg.Drain <= 0 {
		cfg.Drain = DefaultDrain
	}
	if err := loopback(cfg.Listen); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("relay: spool folder: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("relay: spool folder %s is not a directory", cfg.Dir)
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("relay: listening: %w", err)
	}
	r := &Relay{
		cfg: cfg,
		w: spoolexporter.NewWriter(spoolexporter.Options{
			Dir: cfg.Dir, Service: cfg.Service, InstanceID: cfg.Instance,
		}),
		ln:      ln,
		limiter: rate.NewLimiter(rate.Limit(cfg.Rate), cfg.Burst),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", r.handle(decodeTraces))
	mux.HandleFunc("/v1/metrics", r.handle(decodeMetrics))
	mux.HandleFunc("/v1/logs", r.handle(decodeLogs))
	r.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	return r, nil
}

// loopback refuses any address a run's neighbours could reach.
func loopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("relay: listen address %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("relay: listen address %q is not loopback", addr)
}

// Endpoint is the base URL a vendor CLI exports to, as OTEL_EXPORTER_OTLP_ENDPOINT.
func (r *Relay) Endpoint() string { return "http://" + r.ln.Addr().String() }

// Stats reports what the relay has done so far.
func (r *Relay) Stats() Stats {
	return Stats{
		Accepted: r.accepted.Load(), TooLarge: r.tooLarge.Load(), Busy: r.busy.Load(),
		Limited: r.limited.Load(), Malformed: r.malformed.Load(), Spool: r.w.Stats(),
	}
}

// Serve handles requests until ctx is done, then stops accepting, waits up to Drain for
// requests in flight, and flushes and closes the spool file.
func (r *Relay) Serve(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() { errc <- r.srv.Serve(r.ln) }()
	select {
	case err := <-errc:
		r.w.Close()
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), r.cfg.Drain)
	defer cancel()
	err := r.srv.Shutdown(sctx)
	r.w.Close()
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("relay: requests still in flight after %s", r.cfg.Drain)
	}
	return err
}

// decoder turns an OTLP request body into OTLP JSON and its record count.
type decoder func(body []byte, json bool) (out []byte, records int, err error)

func decodeTraces(b []byte, js bool) ([]byte, int, error) {
	req := ptraceotlp.NewExportRequest()
	if err := unmarshal(req.UnmarshalJSON, req.UnmarshalProto, b, js); err != nil {
		return nil, 0, err
	}
	out, err := req.MarshalJSON()
	return out, req.Traces().SpanCount(), err
}

func decodeMetrics(b []byte, js bool) ([]byte, int, error) {
	req := pmetricotlp.NewExportRequest()
	if err := unmarshal(req.UnmarshalJSON, req.UnmarshalProto, b, js); err != nil {
		return nil, 0, err
	}
	out, err := req.MarshalJSON()
	return out, req.Metrics().DataPointCount(), err
}

func decodeLogs(b []byte, js bool) ([]byte, int, error) {
	req := plogotlp.NewExportRequest()
	if err := unmarshal(req.UnmarshalJSON, req.UnmarshalProto, b, js); err != nil {
		return nil, 0, err
	}
	out, err := req.MarshalJSON()
	return out, req.Logs().LogRecordCount(), err
}

func unmarshal(fromJSON, fromProto func([]byte) error, b []byte, js bool) error {
	if js {
		return fromJSON(b)
	}
	return fromProto(b)
}

func (r *Relay) handle(decode decoder) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		js, ok := contentType(req.Header.Get("Content-Type"))
		if !ok {
			r.malformed.Add(1)
			http.Error(w, "content type must be application/x-protobuf or application/json", http.StatusUnsupportedMediaType)
			return
		}
		if !r.limiter.Allow() {
			r.limited.Add(1)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		if req.ContentLength > r.cfg.MaxRequest {
			r.tooLarge.Add(1)
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		// Reserve the most a request may hold once read, so the relay never holds more than
		// MaxMemory however many requests arrive at once.
		if r.inflight.Add(r.cfg.MaxRequest) > r.cfg.MaxMemory {
			r.inflight.Add(-r.cfg.MaxRequest)
			r.busy.Add(1)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		defer r.inflight.Add(-r.cfg.MaxRequest)

		body, status := r.read(req)
		if status != 0 {
			http.Error(w, http.StatusText(status), status)
			return
		}
		line, records, err := decode(body, js)
		if err != nil {
			r.malformed.Add(1)
			http.Error(w, "not an OTLP export request", http.StatusBadRequest)
			return
		}
		if records > 0 {
			r.w.WriteRequest(line, records)
		}
		r.accepted.Add(1)
		// An empty Export*ServiceResponse: full success, in the request's encoding.
		if js {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}
}

// read reads the body, decompressing gzip, and refuses anything over MaxRequest once
// decompressed. It returns a non-zero status on refusal.
func (r *Relay) read(req *http.Request) ([]byte, int) {
	var body io.Reader = http.MaxBytesReader(nil, req.Body, r.cfg.MaxRequest)
	switch req.Header.Get("Content-Encoding") {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(body)
		if err != nil {
			r.malformed.Add(1)
			return nil, http.StatusBadRequest
		}
		defer func() { _ = zr.Close() }()
		body = zr
	default:
		r.malformed.Add(1)
		return nil, http.StatusUnsupportedMediaType
	}
	b, err := io.ReadAll(io.LimitReader(body, r.cfg.MaxRequest+1))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig), int64(len(b)) > r.cfg.MaxRequest:
		r.tooLarge.Add(1)
		return nil, http.StatusRequestEntityTooLarge
	case err != nil:
		r.malformed.Add(1)
		return nil, http.StatusBadRequest
	}
	return b, 0
}

// contentType reports whether a request is JSON, and whether its type is one OTLP/HTTP uses.
func contentType(v string) (json, ok bool) {
	t, _, err := mime.ParseMediaType(v)
	if err != nil {
		return false, false
	}
	switch t {
	case "application/x-protobuf":
		return false, true
	case "application/json":
		return true, true
	}
	return false, false
}
