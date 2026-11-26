package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// markerKey is the resource attribute the checker itself plants through OTEL_RESOURCE_ATTRIBUTES
// (rule 2): it is not the tool's, so it is not replayed.
const markerKey = "conformance.marker"

// liveCheck is Weaver's live check (ADR-008, rule 7): it starts `weaver registry live-check` on
// the tool's registry, replays the records the tool wrote to it over OTLP, and returns what
// Weaver found wrong with them, one line each. Only the tool's own records are replayed (those
// whose service.name is the tool's), as rule 7's own name check does: a stub vendor's records
// are not the tool's to declare. It also returns how many records it replayed.
func liveCheck(ctx context.Context, weaver, registryDir, service string, lines [][]byte) (findings []string, sent int, err error) {
	grpcPort, err := freePort()
	if err != nil {
		return nil, 0, err
	}
	adminPort, err := freePort()
	if err != nil {
		return nil, 0, err
	}
	outDir, err := os.MkdirTemp("", "ynr-live-check-")
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = os.RemoveAll(outDir) }()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var log bytes.Buffer
	cmd := exec.CommandContext(ctx, weaver, "registry", "live-check",
		"-r", registryDir,
		// A tool need not list every standard attribute it uses: the conventions it depends on all count.
		"--include-unreferenced=true",
		"--input-source", "otlp",
		"--otlp-grpc-address", "127.0.0.1", "--otlp-grpc-port", strconv.Itoa(grpcPort),
		"--admin-port", strconv.Itoa(adminPort),
		"--format", "json", "-o", outDir)
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		cancel()
		select {
		case e := <-exited:
			_ = e
		case <-time.After(5 * time.Second):
		}
	}()

	admin := "http://127.0.0.1:" + strconv.Itoa(adminPort)
	if err := waitReady(ctx, admin+"/health", exited); err != nil {
		return nil, 0, fmt.Errorf("weaver live-check did not start: %w\n%s", err, excerpt(log.String()))
	}
	sent, err = replay(ctx, "127.0.0.1:"+strconv.Itoa(grpcPort), service, lines)
	if err != nil {
		return nil, sent, fmt.Errorf("sending the records to weaver live-check: %w", err)
	}
	if err := post(ctx, admin+"/stop"); err != nil {
		return nil, sent, fmt.Errorf("stopping weaver live-check: %w", err)
	}
	var waitErr error
	select {
	case waitErr = <-exited:
	case <-ctx.Done():
		return nil, sent, errors.New("weaver live-check did not finish")
	}
	b, readErr := os.ReadFile(filepath.Join(outDir, "live_check.json"))
	if readErr != nil {
		return nil, sent, fmt.Errorf("weaver live-check wrote no report (%w):\n%s", waitErr, excerpt(log.String()))
	}
	findings, err = violations(b)
	if err != nil {
		return nil, sent, err
	}
	if waitErr != nil && len(findings) == 0 {
		return nil, sent, fmt.Errorf("weaver live-check failed without naming a violation: %w\n%s", waitErr, excerpt(log.String()))
	}
	return findings, sent, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitReady polls url until it answers 200, weaver exits, or ctx ends. Weaver resolves the
// registry, and so downloads the semantic conventions, before it listens.
func waitReady(ctx context.Context, url string, exited <-chan error) error {
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case err := <-exited:
			return fmt.Errorf("weaver exited first (%w)", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func post(ctx context.Context, url string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return nil
}

// replay sends each spool line, keeping only the resources of service, to addr over OTLP gRPC,
// and returns how many resources it sent.
func replay(ctx context.Context, addr, service string, lines [][]byte) (int, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()
	traces, logs, metrics := ptraceotlp.NewGRPCClient(conn), plogotlp.NewGRPCClient(conn), pmetricotlp.NewGRPCClient(conn)
	sent := 0
	for _, line := range lines {
		var probe struct {
			Spans   json.RawMessage `json:"resourceSpans"`
			Logs    json.RawMessage `json:"resourceLogs"`
			Metrics json.RawMessage `json:"resourceMetrics"`
		}
		if json.Unmarshal(line, &probe) != nil {
			continue
		}
		switch {
		case probe.Spans != nil:
			td, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(line)
			if err != nil {
				continue
			}
			td.ResourceSpans().RemoveIf(func(r ptrace.ResourceSpans) bool { return !isService(r.Resource().Attributes().AsRaw(), service) })
			if td.ResourceSpans().Len() == 0 {
				continue
			}
			for i := 0; i < td.ResourceSpans().Len(); i++ {
				td.ResourceSpans().At(i).Resource().Attributes().Remove(markerKey)
			}
			sent += td.ResourceSpans().Len()
			_, err = traces.Export(ctx, ptraceotlp.NewExportRequestFromTraces(td))
			if err != nil {
				return sent, err
			}
		case probe.Logs != nil:
			ld, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(line)
			if err != nil {
				continue
			}
			ld.ResourceLogs().RemoveIf(func(r plog.ResourceLogs) bool { return !isService(r.Resource().Attributes().AsRaw(), service) })
			if ld.ResourceLogs().Len() == 0 {
				continue
			}
			for i := 0; i < ld.ResourceLogs().Len(); i++ {
				ld.ResourceLogs().At(i).Resource().Attributes().Remove(markerKey)
			}
			sent += ld.ResourceLogs().Len()
			if _, err = logs.Export(ctx, plogotlp.NewExportRequestFromLogs(ld)); err != nil {
				return sent, err
			}
		case probe.Metrics != nil:
			md, err := (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics(line)
			if err != nil {
				continue
			}
			md.ResourceMetrics().RemoveIf(func(r pmetric.ResourceMetrics) bool { return !isService(r.Resource().Attributes().AsRaw(), service) })
			if md.ResourceMetrics().Len() == 0 {
				continue
			}
			for i := 0; i < md.ResourceMetrics().Len(); i++ {
				md.ResourceMetrics().At(i).Resource().Attributes().Remove(markerKey)
			}
			sent += md.ResourceMetrics().Len()
			if _, err = metrics.Export(ctx, pmetricotlp.NewExportRequestFromMetrics(md)); err != nil {
				return sent, err
			}
		}
	}
	return sent, nil
}

// isService reports whether a resource is the tool's. A resource with no service.name is sent
// too: rule 2 reports it, and Weaver should see its attributes.
func isService(attrs map[string]any, service string) bool {
	v, ok := attrs["service.name"]
	return !ok || v == service
}

// violations lists the findings at violation level in Weaver's JSON report: every advice, wherever
// it sits in the samples, whose level is violation.
func violations(report []byte) ([]string, error) {
	var doc any
	if err := json.Unmarshal(report, &doc); err != nil {
		return nil, fmt.Errorf("weaver live-check's report is not JSON: %w", err)
	}
	var out []string
	seen := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if lvl, _ := t["level"].(string); lvl == "violation" {
				msg := fmt.Sprint(t["message"])
				if sig, _ := t["signal_type"].(string); sig != "" {
					msg = sig + ": " + msg
				}
				if id, _ := t["id"].(string); id != "" {
					msg += " (" + id + ")"
				}
				if !seen[msg] {
					seen[msg] = true
					out = append(out, msg)
				}
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(doc)
	return out, nil
}
