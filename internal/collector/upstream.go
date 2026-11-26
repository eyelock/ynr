package collector

import (
	"fmt"
	"net/url"
	"strings"
)

// ParseHeaders reads headers in the form of OTEL_EXPORTER_OTLP_HEADERS: comma-separated
// name=value pairs, where a value may be percent-encoded. Blank pairs are skipped.
func ParseHeaders(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("header %q is not name=value", pair)
		}
		dv, err := url.PathUnescape(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", k, err)
		}
		out[k] = dv
	}
	return out, nil
}

// UpstreamFromEnv fills the upstream settings the OTEL_EXPORTER_OTLP_* variables name, which the
// Collector's exporters do not read themselves: _HEADERS, _COMPRESSION (gzip or none),
// _CERTIFICATE, _CLIENT_CERTIFICATE and _CLIENT_KEY. The signal-specific variants are not read.
// Settings already set are kept, so a flag wins over the environment, header by header.
func UpstreamFromEnv(s *Settings, getenv func(string) string) error {
	if v := getenv("OTEL_EXPORTER_OTLP_HEADERS"); v != "" {
		h, err := ParseHeaders(v)
		if err != nil {
			return fmt.Errorf("OTEL_EXPORTER_OTLP_HEADERS: %w", err)
		}
		for k, val := range s.UpstreamHeaders {
			h[k] = val
		}
		s.UpstreamHeaders = h
	}
	if v := strings.ToLower(getenv("OTEL_EXPORTER_OTLP_COMPRESSION")); v != "" {
		if v != "gzip" && v != "none" {
			return fmt.Errorf("OTEL_EXPORTER_OTLP_COMPRESSION is %q, want gzip or none", v)
		}
		s.UpstreamCompression = v
	}
	for _, f := range []struct {
		name string
		to   *string
	}{
		{"OTEL_EXPORTER_OTLP_CERTIFICATE", &s.UpstreamCA},
		{"OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE", &s.UpstreamCert},
		{"OTEL_EXPORTER_OTLP_CLIENT_KEY", &s.UpstreamKey},
	} {
		if v := getenv(f.name); v != "" {
			*f.to = v
		}
	}
	return nil
}
