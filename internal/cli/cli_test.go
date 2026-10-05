package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynr/internal/spool"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsageAndVersion(t *testing.T) {
	if code, _, _ := run(); code != ExitUsage {
		t.Errorf("no args = %d", code)
	}
	if code, _, _ := run("nope"); code != ExitUsage {
		t.Errorf("unknown command = %d", code)
	}
	if code, out, _ := run("version"); code != ExitOK || !strings.Contains(out, "slim") {
		t.Errorf("version = %d %q", code, out)
	}
}

func TestInfoReportsTheServingProcess(t *testing.T) {
	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run("info", "--spool", root, "--format", "json")
	var rep infoReport
	if code != ExitOK || json.Unmarshal([]byte(out), &rep) != nil || rep.Serving != nil || rep.Build != "slim" {
		t.Fatalf("info = %d %s", code, out)
	}
	l, err := spool.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	_, out, _ = run("info", "--spool", root, "--format", "json")
	if json.Unmarshal([]byte(out), &rep) != nil || rep.Serving == nil || rep.Serving.PID == 0 {
		t.Fatalf("info while served = %s", out)
	}
}

func TestServeRefusals(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		args []string
		code int
		msg  string
	}{
		{"ui in slim", []string{"--spool", root, "--upstream", "http://x", "--ui", ":8080"}, ExitConfig, "full build"},
		{"no upstream", []string{"--spool", root}, ExitConfig, "--upstream"},
		{"bad collector id", []string{"--spool", root, "--upstream", "http://x", "--collector-id", "Bad Id"}, ExitConfig, "collector-id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("YNR_UPSTREAM", "")
			code, _, errOut := run(append([]string{"serve"}, c.args...)...)
			if code != c.code || !strings.Contains(errOut, c.msg) {
				t.Fatalf("code %d stderr %q", code, errOut)
			}
		})
	}
}

func TestServeRefusesASpoolAlreadyServed(t *testing.T) {
	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	l, err := spool.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	code, _, errOut := run("serve", "--spool", root, "--upstream", "http://127.0.0.1:1")
	if code != ExitAdapter || !strings.Contains(errOut, "already served") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestRelayRefusals(t *testing.T) {
	for name, args := range map[string][]string{
		"no folder":   {"relay", "--spool", ""},
		"public":      {"relay", "--spool", t.TempDir(), "--listen", "0.0.0.0:0"},
		"bad rate":    {"relay", "--spool", t.TempDir(), "--rate", "0"},
		"bad format":  {"relay", "--spool", t.TempDir(), "--format", "xml"},
		"missing dir": {"relay", "--spool", filepath.Join(t.TempDir(), "nope")},
	} {
		t.Setenv("YNR_SPOOL", "")
		var out, errb bytes.Buffer
		if code := Run(context.Background(), args, &out, &errb); code == ExitOK {
			t.Errorf("%s: exit 0", name)
		}
	}
}

// TestRelayPrintsItsEndpointThenWrites runs the command as ynh would: read the first line for
// the endpoint, send to it, stop the relay, and find the records in the folder.
func TestRelayPrintsItsEndpointThenWrites(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	var errb bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run(ctx, []string{"relay", "--spool", dir, "--format", "json"}, pw, &errb)
		_ = pw.Close()
	}()
	var ready struct {
		Endpoint string `json:"endpoint"`
		PID      int    `json:"pid"`
	}
	line, err := bufio.NewReader(pr).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	if err := json.Unmarshal(line, &ready); err != nil || ready.PID == 0 || !strings.HasPrefix(ready.Endpoint, "http://127.0.0.1:") {
		t.Fatalf("first line = %s (%v)", line, err)
	}
	body := `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"eventName":"claude_code.user_prompt"}]}]}]}`
	resp, err := http.Post(ready.Endpoint+"/v1/logs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	cancel()
	if c := <-code; c != ExitOK {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	if !strings.Contains(errb.String(), "1 accepted") {
		t.Errorf("summary = %q", errb.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".jsonl") || strings.HasSuffix(entries[0].Name(), ".open.jsonl") {
		t.Fatalf("files = %v", entries)
	}
}
