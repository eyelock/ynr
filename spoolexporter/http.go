package spoolexporter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultHTTPTimeout bounds each HTTP export, and so the flush on shutdown.
const DefaultHTTPTimeout = 2 * time.Second

// HTTPOptions configures an HTTPSender. A signal with no endpoint is not
// sent: its exporter discards what it is given, without counting it.
type HTTPOptions struct {
	// TracesEndpoint, LogsEndpoint and MetricsEndpoint are the full URLs the
	// signals are posted to.
	TracesEndpoint  string
	LogsEndpoint    string
	MetricsEndpoint string
	// TracesHeaders, LogsHeaders and MetricsHeaders are sent with that
	// signal's requests. They often carry credentials: they appear in no
	// Stats, error or log.
	TracesHeaders  map[string]string
	LogsHeaders    map[string]string
	MetricsHeaders map[string]string
	// Timeout bounds each export. Zero takes DefaultHTTPTimeout.
	Timeout time.Duration
	// Client sends the requests. Nil takes a client that follows no
	// redirects, so headers never travel to an address the operator did not
	// name.
	Client *http.Client
}

// HTTPSender posts OTLP/JSON export requests to an operator's OTLP/HTTP
// endpoints. It is safe for concurrent use, so one HTTPSender serves the
// trace, log and metric exporters. Like the Writer it never fails: an export
// that cannot be sent is counted in Stats. Each export is bounded by the
// timeout, and there is no retry.
type HTTPSender struct {
	opts    HTTPOptions
	dropped atomic.Int64
	errors  atomic.Int64
}

// NewHTTPSender returns an HTTPSender for opts. It sends nothing until the
// first export.
func NewHTTPSender(opts HTTPOptions) *HTTPSender {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultHTTPTimeout
	}
	if opts.Client == nil {
		opts.Client = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &HTTPSender{opts: opts}
}

// Stats reports what the sender has dropped and failed so far. Dropped counts
// records (spans, log records or data points) not delivered; Errors counts
// exports that failed, timed out or were refused with a non-2xx status.
func (h *HTTPSender) Stats() Stats {
	return Stats{Dropped: h.dropped.Load(), Errors: h.errors.Load()}
}

func (h *HTTPSender) drop(records int) { h.dropped.Add(int64(records)) }

// sync does nothing: every export is sent before it returns.
func (h *HTTPSender) sync() {}

func (h *HTTPSender) target(sig signal) (string, map[string]string) {
	switch sig {
	case signalTraces:
		return h.opts.TracesEndpoint, h.opts.TracesHeaders
	case signalLogs:
		return h.opts.LogsEndpoint, h.opts.LogsHeaders
	default:
		return h.opts.MetricsEndpoint, h.opts.MetricsHeaders
	}
}

func (h *HTTPSender) send(ctx context.Context, sig signal, req any, records int) {
	endpoint, headers := h.target(sig)
	if endpoint == "" {
		return
	}
	body, err := json.Marshal(req)
	if err != nil {
		h.fail(records)
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, h.opts.Timeout)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		h.fail(records)
		return
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := h.opts.Client.Do(r)
	if err != nil {
		h.fail(records)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// Read a little of the body so the connection can be reused; the cap and
	// the deadline keep a hostile endpoint from holding us.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		h.fail(records)
	}
}

func (h *HTTPSender) fail(records int) {
	h.errors.Add(1)
	h.dropped.Add(int64(records))
}

// The environment variables the OpenTelemetry specification names, and which
// of them this package honours. The suffix is appended to
// OTEL_EXPORTER_OTLP_ or to OTEL_EXPORTER_OTLP_{TRACES,LOGS,METRICS}_.
const (
	envPrefix = "OTEL_EXPORTER_OTLP_"
	// jsonProtocol is the only protocol the sender speaks.
	jsonProtocol = "http/json"
)

// HTTPOptionsFromEnv reads the operator's OTEL_EXPORTER_OTLP_* settings
// through getenv (os.Getenv in a program).
//
// configured is false when none of the endpoint settings is set: the
// operator asked for nothing, and the caller exports nowhere extra.
//
// unsupported is not empty when a setting asks for something the sender
// cannot do: a protocol other than http/json, a custom CA or client
// certificate, or compression. It names the setting and never its value, in
// a form fit to print once, and the caller should then send nothing. When it
// is set the other results are unspecified.
//
// OTEL_EXPORTER_OTLP_ENDPOINT gets /v1/traces, /v1/logs and /v1/metrics
// appended; the per-signal endpoints are used as given. Headers are a
// comma-separated list of key=value pairs with percent-encoded values, the
// per-signal list overriding the general one by name. The timeout is in
// milliseconds. Entries of the header list that are not key=value are
// ignored.
func HTTPOptionsFromEnv(getenv func(string) string) (opts HTTPOptions, configured bool, unsupported string) {
	signals := []string{"TRACES", "LOGS", "METRICS"}
	// Settings with no support. Check the general name and each signal's.
	for _, suffix := range []string{"COMPRESSION", "CERTIFICATE", "CLIENT_CERTIFICATE", "CLIENT_KEY"} {
		for _, name := range append([]string{envPrefix + suffix}, perSignal(signals, suffix)...) {
			v := strings.TrimSpace(getenv(name))
			if v == "" || (suffix == "COMPRESSION" && strings.EqualFold(v, "none")) {
				continue
			}
			if suffix == "COMPRESSION" {
				return opts, false, name + " is set: compression is not supported"
			}
			return opts, false, name + " is set: custom certificates are not supported"
		}
	}
	for _, name := range append([]string{envPrefix + "PROTOCOL"}, perSignal(signals, "PROTOCOL")...) {
		if v := strings.TrimSpace(getenv(name)); v != "" && !strings.EqualFold(v, jsonProtocol) {
			return opts, false, name + " is not " + jsonProtocol + ": only OTLP/HTTP with JSON bodies is supported"
		}
	}

	ends := [3]*string{&opts.TracesEndpoint, &opts.LogsEndpoint, &opts.MetricsEndpoint}
	heads := [3]*map[string]string{&opts.TracesHeaders, &opts.LogsHeaders, &opts.MetricsHeaders}
	paths := [3]string{"v1/traces", "v1/logs", "v1/metrics"}
	base := strings.TrimSpace(getenv(envPrefix + "ENDPOINT"))
	general := parseHeaders(getenv(envPrefix + "HEADERS"))
	for i, sig := range signals {
		end := strings.TrimSpace(getenv(envPrefix + sig + "_ENDPOINT"))
		if end == "" && base != "" {
			end = strings.TrimRight(base, "/") + "/" + paths[i]
		}
		if end == "" {
			continue
		}
		if u, err := url.Parse(end); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			name := envPrefix + "ENDPOINT"
			if getenv(envPrefix+sig+"_ENDPOINT") != "" {
				name = envPrefix + sig + "_ENDPOINT"
			}
			return opts, false, name + " is not an http or https URL"
		}
		configured = true
		*ends[i] = end
		hs := map[string]string{}
		for k, v := range general {
			hs[k] = v
		}
		for k, v := range parseHeaders(getenv(envPrefix + sig + "_HEADERS")) {
			hs[k] = v
		}
		*heads[i] = hs
	}
	// The timeout is one setting for the sender: the longest asked for.
	for _, name := range append([]string{envPrefix + "TIMEOUT"}, perSignal(signals, "TIMEOUT")...) {
		if ms, err := strconv.Atoi(strings.TrimSpace(getenv(name))); err == nil && ms > 0 {
			if d := time.Duration(ms) * time.Millisecond; d > opts.Timeout {
				opts.Timeout = d
			}
		}
	}
	return opts, configured, ""
}

func perSignal(signals []string, suffix string) []string {
	out := make([]string, len(signals))
	for i, s := range signals {
		out[i] = envPrefix + s + "_" + suffix
	}
	return out
}

// parseHeaders reads a W3C baggage-style list: key=value pairs separated by
// commas, values percent-encoded. Pairs without a key or an "=" are skipped.
func parseHeaders(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if dec, err := url.PathUnescape(v); err == nil {
			v = dec
		}
		out[k] = v
	}
	return out
}
