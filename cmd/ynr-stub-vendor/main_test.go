package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

const (
	traceID = "0af7651916cd43dd8448eb211c80319c"
	spanID  = "b7ad6b7169203331"
)

// sink records what the stub exports, by path.
type sink struct {
	mu     sync.Mutex
	bodies map[string][]byte
	types  map[string]string
}

func (s *sink) body(path string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[path]
}

func (s *sink) contentType(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.types[path]
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func newSink(t *testing.T) (*sink, *httptest.Server) {
	s := &sink{bodies: map[string][]byte{}, types: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies[r.URL.Path] = b
		s.types[r.URL.Path] = r.Header.Get("Content-Type")
		s.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func TestExportsAndJoinsTrace(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json", ""} {
		t.Run("protocol "+protocol, func(t *testing.T) {
			s, srv := newSink(t)
			env := []string{"OTEL_EXPORTER_OTLP_ENDPOINT=" + srv.URL, "TRACEPARENT=00-" + traceID + "-" + spanID + "-01",
				"OTEL_RESOURCE_ATTRIBUTES=ynh.run.id=r1,deployment.environment.name=ci"}
			if protocol != "" {
				env = append(env, "OTEL_EXPORTER_OTLP_PROTOCOL="+protocol)
			}
			var out, errb bytes.Buffer
			code := run([]string{"--turns", "2", "--exit", "7", "--result", "budget", "-p", "SECRET PROMPT"}, env, &out, &errb)
			if code != 7 {
				t.Fatalf("exit %d, want 7 (%s)", code, errb.String())
			}
			if !strings.Contains(out.String(), `"subtype":"budget"`) || strings.Contains(out.String(), "SECRET") {
				t.Errorf("output: %q", out.String())
			}
			wantType := "application/x-protobuf"
			js := protocol == "http/json"
			if js {
				wantType = "application/json"
			}
			for _, p := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
				if s.contentType(p) != wantType {
					t.Errorf("%s content type %q, want %q", p, s.contentType(p), wantType)
				}
			}
			req := ptraceotlp.NewExportRequest()
			var err error
			if js {
				err = req.UnmarshalJSON(s.body("/v1/traces"))
			} else {
				err = req.UnmarshalProto(s.body("/v1/traces"))
			}
			if err != nil {
				t.Fatal(err)
			}
			rs := req.Traces().ResourceSpans().At(0)
			if v, _ := rs.Resource().Attributes().Get("service.name"); v.AsString() != "claude-code" {
				t.Errorf("service.name %q", v.AsString())
			}
			if v, _ := rs.Resource().Attributes().Get("ynh.run.id"); v.AsString() != "r1" {
				t.Errorf("OTEL_RESOURCE_ATTRIBUTES did not arrive")
			}
			spans := rs.ScopeSpans().At(0).Spans()
			sawRoot := false
			for i := 0; i < spans.Len(); i++ {
				sp := spans.At(i)
				if sp.TraceID().String() != traceID {
					t.Errorf("span %s left the trace: %s", sp.Name(), sp.TraceID())
				}
				if sp.ParentSpanID().String() == spanID {
					sawRoot = true
				}
			}
			if !sawRoot {
				t.Error("no span is a child of TRACEPARENT's span")
			}
			// Metrics and logs decode too, and carry no prompt.
			mreq := pmetricotlp.NewExportRequest()
			lreq := plogotlp.NewExportRequest()
			if js {
				err = mreq.UnmarshalJSON(s.body("/v1/metrics"))
				if err == nil {
					err = lreq.UnmarshalJSON(s.body("/v1/logs"))
				}
			} else {
				err = mreq.UnmarshalProto(s.body("/v1/metrics"))
				if err == nil {
					err = lreq.UnmarshalProto(s.body("/v1/logs"))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if mreq.Metrics().MetricCount() == 0 || lreq.Logs().LogRecordCount() == 0 {
				t.Error("no metrics or log events")
			}
			if bytes.Contains(s.body("/v1/logs"), []byte("SECRET")) {
				t.Error("the prompt reached the logs")
			}
		})
	}
}

func TestNoEndpointExportsNothing(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--turns", "1"}, []string{"TRACEPARENT=00-" + traceID + "-" + spanID + "-01"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestUnsupportedProtocolAndNoneExporter(t *testing.T) {
	s, srv := newSink(t)
	var out, errb bytes.Buffer
	env := []string{"OTEL_EXPORTER_OTLP_ENDPOINT=" + srv.URL, "OTEL_EXPORTER_OTLP_PROTOCOL=grpc"}
	run([]string{"--turns", "1"}, env, &out, &errb)
	if s.count() != 0 {
		t.Errorf("grpc is not spoken, yet %d requests arrived", s.count())
	}
	env = []string{"OTEL_EXPORTER_OTLP_ENDPOINT=" + srv.URL, "OTEL_LOGS_EXPORTER=none", "OTEL_METRICS_EXPORTER=none"}
	run([]string{"--turns", "1"}, env, &out, &errb)
	if s.count() != 1 || s.body("/v1/traces") == nil {
		t.Errorf("only traces were wanted, got %d requests", s.count())
	}
}

func TestScriptFileAndEnvironment(t *testing.T) {
	p := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(p, []byte(`{"turns":3,"exit":4,"result":"stuck"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--script", p}, nil, &out, &errb); code != 4 {
		t.Errorf("exit %d, want 4", code)
	}
	if strings.Count(out.String(), "turn ") != 3 || !strings.Contains(out.String(), "stuck") {
		t.Errorf("output %q", out.String())
	}
	out.Reset()
	if code := run(nil, []string{"YNR_STUB_TURNS=2", "YNR_STUB_EXIT=5", "YNR_STUB_TURN_DELAY=1ms"}, &out, &errb); code != 5 || strings.Count(out.String(), "turn ") != 2 {
		t.Errorf("environment script: exit %d, output %q", code, out.String())
	}
	if code := run([]string{"--script", "/no/such/file"}, nil, &out, &errb); code != 2 {
		t.Errorf("missing script: exit %d", code)
	}
}

// TestHungEndpointCostsAboutASecond: a vendor never fails because its telemetry did, and the
// stub is no different.
func TestHungEndpointCostsAboutASecond(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	start := time.Now()
	var out, errb bytes.Buffer
	code := run([]string{"--turns", "1", "--exit", "3"}, []string{"OTEL_EXPORTER_OTLP_ENDPOINT=" + srv.URL}, &out, &errb)
	if code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Errorf("took %s", d)
	}
}
