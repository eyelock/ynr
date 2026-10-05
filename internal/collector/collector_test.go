package collector

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"

	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/internal/stamp"
)

const traceLine = `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynh"}}]},` +
	`"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",` +
	`"name":"ynh.run","startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`

// TestShipsStampedSpansThenDeletes runs the real Collector against a fake OTLP/HTTP upstream.
func TestShipsStampedSpansThenDeletes(t *testing.T) {
	var mu sync.Mutex
	var provenance []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var src io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			src = zr
		}
		body, _ := io.ReadAll(src)
		req := ptraceotlp.NewExportRequest()
		if err := req.UnmarshalProto(body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		rs := req.Traces().ResourceSpans()
		mu.Lock()
		for i := 0; i < rs.Len(); i++ {
			v, _ := rs.At(i).Resource().Attributes().Get(stamp.Provenance)
			provenance = append(provenance, v.Str())
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "local", "ynh-1.jsonl")
	if err := os.WriteFile(file, []byte(traceLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Settings{
			SpoolRoot: root, PollInterval: 50 * time.Millisecond, MaxLine: spool.DefaultMaxLine,
			Identity: stamp.Identity{ID: "test"}, Upstream: upstream.URL,
		})
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, err := os.Stat(file)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the spool file was never shipped and deleted")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(provenance) != 1 || provenance[0] != "local" {
		t.Fatalf("upstream saw provenance %v, want [local]", provenance)
	}
}

func TestUpstreamIsRequired(t *testing.T) {
	if _, err := Config(Settings{SpoolRoot: "/x"}); err == nil {
		t.Fatal("want an error without an upstream")
	}
}
