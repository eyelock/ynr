package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
