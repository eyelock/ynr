package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/internal/store"
)

func TestDoctorOTLPWarning(t *testing.T) {
	if c := checkOTLP([]string{"PATH=/bin", "OTEL_EXPORTER_OTLP_PROTOCOL=http/json"}); c.Status != statusOK {
		t.Errorf("a protocol alone warned: %+v", c)
	}
	c := checkOTLP([]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://x", "OTEL_EXPORTER_OTLP_ENDPOINT=http://y"})
	if c.Status != statusWarn || !strings.Contains(c.Detail, "OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") {
		t.Errorf("endpoints = %+v", c)
	}
}

func TestDoctorReportsAndExits(t *testing.T) {
	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	code, out, _ := run("doctor", "--spool", root, "--store", store.FolderURL(t.TempDir()), "--format", "json")
	var rep struct {
		Checks []check `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got := map[string]string{}
	for _, c := range rep.Checks {
		got[c.Name] = c.Status
	}
	// Nothing serves this spool, so doctor warns and exits non-zero.
	if code != ExitDoctor || got["spool"] != statusOK || got["serve"] != statusWarn || got["store"] != statusOK || got["backlog"] != statusOK {
		t.Fatalf("code %d checks %v", code, got)
	}
}

func TestDoctorBacklog(t *testing.T) {
	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	writeSpool(t, root, "local/a-1-000001.jsonl", "x\n")
	if c := checkBacklog(root, time.Now()); c.Status != statusOK {
		t.Errorf("a fresh closed file warned: %+v", c)
	}
	if c := checkBacklog(root, time.Now().Add(time.Hour)); c.Status != statusWarn {
		t.Errorf("an hour-old closed file = %+v", c)
	}
}

func writeSpool(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
