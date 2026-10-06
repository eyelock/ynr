package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConformanceRefusals(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(context.Background(), []string{"conformance", "--format", "xml"}, &out, &errb); code != ExitUsage {
		t.Errorf("bad format: exit %d", code)
	}
	if code := Run(context.Background(), []string{"conformance", "--file", filepath.Join(t.TempDir(), "none.yaml")}, &out, &errb); code != ExitConfig {
		t.Errorf("missing file: exit %d", code)
	}
}

// TestConformanceFailureExitsNonZero runs a tool that writes no telemetry at all: rule 1 fails,
// the exit code says so, and the JSON report is the full report.
func TestConformanceFailureExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "silent")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	file := filepath.Join(dir, "conformance.yaml")
	body := "service: silent\nscenarios:\n  - {name: nothing, run: silent, expect: {outcome: converged}}\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"conformance", "--file", file, "--format", "json", "--timeout", "10s"}, &out, &errb)
	if code != ExitConformance {
		t.Fatalf("exit %d, want %d\n%s\n%s", code, ExitConformance, out.String(), errb.String())
	}
	var rep struct {
		OK    bool `json:"ok"`
		Rules []struct {
			Rule   int    `json:"rule"`
			Status string `json:"status"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, out.String())
	}
	if rep.OK || rep.Rules[0].Rule != 1 || rep.Rules[0].Status != "fail" {
		t.Errorf("report: %+v", rep)
	}
	out.Reset()
	if code := Run(context.Background(), []string{"conformance", "--file", file, "--timeout", "10s"}, &out, &errb); code != ExitConformance || !bytes.Contains(out.Bytes(), []byte("conformance: FAILED")) {
		t.Errorf("text: exit %d\n%s", code, out.String())
	}
}
