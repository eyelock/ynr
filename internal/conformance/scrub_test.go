package conformance

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"

	"github.com/eyelock/ynr/spoolexporter"
)

// unscrubbedRelay is a relay with no scrubber, as ynr 0.2.0 was: it writes what it receives.
func unscrubbedRelay(dir string) (string, func() error, error) {
	w := spoolexporter.NewWriter(spoolexporter.Options{Dir: dir, Service: "unscrubbed"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	mux := http.NewServeMux()
	handle := func(conv func(body []byte) ([]byte, error)) http.HandlerFunc {
		return func(rw http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			line, err := conv(body)
			if err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteRequest(line, 1)
		}
	}
	mux.HandleFunc("/v1/traces", handle(func(b []byte) ([]byte, error) {
		req := ptraceotlp.NewExportRequest()
		if err := req.UnmarshalProto(b); err != nil {
			return nil, err
		}
		return (&ptrace.JSONMarshaler{}).MarshalTraces(req.Traces())
	}))
	mux.HandleFunc("/v1/metrics", handle(func(b []byte) ([]byte, error) {
		req := pmetricotlp.NewExportRequest()
		if err := req.UnmarshalProto(b); err != nil {
			return nil, err
		}
		return (&pmetric.JSONMarshaler{}).MarshalMetrics(req.Metrics())
	}))
	mux.HandleFunc("/v1/logs", handle(func(b []byte) ([]byte, error) {
		req := plogotlp.NewExportRequest()
		if err := req.UnmarshalProto(b); err != nil {
			return nil, err
		}
		return (&plog.JSONMarshaler{}).MarshalLogs(req.Logs())
	}))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), func() error {
		_ = srv.Close()
		w.Close()
		return nil
	}, nil
}

// TestScrubPassesTheRelay: the relay in this build keeps every scrubbed key out of the spool.
func TestScrubPassesTheRelay(t *testing.T) {
	res := runScrub(context.Background(), t.TempDir(), nil)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if len(res.leaks) > 0 || res.lines != 3 {
		t.Fatalf("lines = %d, leaks = %v; want 3 lines and none", res.lines, res.leaks)
	}
}

// TestScrubFailsAnUnscrubbedRelay: a relay with no scrubber fails every part of the check: each
// dropped key and each account id's raw value (ynr #155).
func TestScrubFailsAnUnscrubbedRelay(t *testing.T) {
	res := runScrub(context.Background(), t.TempDir(), unscrubbedRelay)
	if res.err != nil {
		t.Fatal(res.err)
	}
	all := strings.Join(res.leaks, "\n")
	for _, k := range append(append([]string{}, scrubDropped...), scrubAccountIDs...) {
		if !strings.Contains(all, k) {
			t.Errorf("a leak of %s was not reported:\n%s", k, all)
		}
	}
}

// TestScrubFailsAnEmptySpool: a relay that writes nothing must not pass by having nothing to leak.
func TestScrubFailsAnEmptySpool(t *testing.T) {
	nothing := func(dir string) (string, func() error, error) {
		srv := http.NewServeMux()
		s := &http.Server{Handler: srv, ReadHeaderTimeout: time.Second}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", nil, err
		}
		srv.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
		go func() { _ = s.Serve(ln) }()
		return "http://" + ln.Addr().String(), func() error { return s.Close() }, nil
	}
	res := runScrub(context.Background(), t.TempDir(), nothing)
	if res.err != nil || len(res.leaks) == 0 {
		t.Fatalf("err = %v, leaks = %v; want a reported leak", res.err, res.leaks)
	}
}

// TestScrubAgainstARunningRelay checks a relay the test did not start, such as an older pinned
// build: set YNR_SCRUB_ENDPOINT and YNR_SCRUB_SPOOL to its endpoint and spool folder. It reports
// the leaks and fails when there are any.
func TestScrubAgainstARunningRelay(t *testing.T) {
	endpoint, spool := os.Getenv("YNR_SCRUB_ENDPOINT"), os.Getenv("YNR_SCRUB_SPOOL")
	if endpoint == "" || spool == "" {
		t.Skip("YNR_SCRUB_ENDPOINT and YNR_SCRUB_SPOOL are not set")
	}
	leaks, err := ScrubAgainst(context.Background(), endpoint, spool)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaks) > 0 {
		t.Fatalf("%d leak(s):\n%s", len(leaks), strings.Join(leaks, "\n"))
	}
}
