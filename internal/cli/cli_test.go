package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynr"
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
	if code, out, _ := run("version"); code != ExitOK || !strings.Contains(out, ynr.Build) {
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
	if code != ExitOK || json.Unmarshal([]byte(out), &rep) != nil || rep.Serving != nil || rep.Build != ynr.Build {
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
	uiRefusal := "full build" // the slim build has no dashboard; the full one binds only loopback
	if ynr.Build == "full" {
		uiRefusal = "not a loopback address"
	}
	cases := []struct {
		name string
		args []string
		code int
		msg  string
	}{
		{"ui", []string{"--spool", root, "--upstream", "http://x", "--ui", ":8080"}, ExitConfig, uiRefusal},
		{"nowhere to ship", []string{"--spool", root, "--store", ""}, ExitConfig, "nowhere to ship"},
		{"bad store", []string{"--spool", root, "--store", "gs://bucket"}, ExitConfig, "--store"},
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

// TestRelayStopsWhenStdinCloses: with --exit-on-stdin-eof the relay ends with whoever started it,
// even one killed outright, since its end of the pipe closes.
func TestRelayStopsWhenStdinCloses(t *testing.T) {
	in, closeIn := io.Pipe()
	old := stdin
	stdin = in
	t.Cleanup(func() { stdin = old })
	out, outW := io.Pipe()
	var errb bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run(context.Background(), []string{"relay", "--spool", t.TempDir(), "--exit-on-stdin-eof"}, outW, &errb)
		_ = outW.Close()
	}()
	if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()
	_ = closeIn.Close()
	select {
	case c := <-code:
		if c != ExitOK {
			t.Fatalf("exit %d: %s", c, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the relay kept running after stdin closed")
	}
}

func TestTelemetryRegistryPrintsYnrsNamesInTheShapeEveryToolUses(t *testing.T) {
	code, out, _ := run("telemetry", "registry", "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	var r struct {
		Tool       string                   `json:"tool"`
		Version    string                   `json:"version"`
		Semconv    struct{ Version string } `json:"semantic_conventions"`
		Attributes []struct{ ID string }    `json:"attributes"`
		Standard   []string                 `json:"standard_attributes"`
		Metrics    []struct{ Name string }  `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.Tool != "ynr" || r.Version != ynr.Version || r.Semconv.Version == "" || len(r.Attributes) == 0 || len(r.Metrics) == 0 {
		t.Errorf("%+v", r)
	}
	if code, out, _ := run("telemetry", "registry"); code != ExitOK || !strings.Contains(out, "ynr.registry.unknown_names") {
		t.Errorf("text: %d %q", code, out)
	}
	for _, args := range [][]string{{"telemetry"}, {"telemetry", "nope"}, {"telemetry", "registry", "--format", "yaml"}} {
		if code, _, _ := run(args...); code != ExitUsage {
			t.Errorf("%v = %d", args, code)
		}
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" ynh, ynf,,ynm ,ynh")
	if strings.Join(got, "|") != "ynh|ynf|ynm" || splitList("") != nil {
		t.Errorf("%q", got)
	}
}

// The usage text names every flag the commands really take, so it cannot drift from the code.
func TestUsageNamesEveryFlag(t *testing.T) {
	_, usage, _ := run("help")
	flagLine := regexp.MustCompile(`(?m)^\s+-([a-z][a-z-]*)`)
	for _, args := range [][]string{
		{"info"}, {"doctor"}, {"serve"}, {"central"}, {"relay"}, {"conformance"}, {"tail"}, {"query", "runs"},
	} {
		_, _, help := run(append(args, "-h")...)
		found := flagLine.FindAllStringSubmatch(help, -1)
		if len(found) == 0 {
			t.Errorf("%v -h listed no flags:\n%s", args, help)
		}
		for _, m := range found {
			if !strings.Contains(usage, "--"+m[1]) {
				t.Errorf("ynr %s takes --%s, which the usage does not mention", strings.Join(args, " "), m[1])
			}
		}
	}
}

// The ynr query usage line names its own flags, not just any flag the other commands share (--spool
// is also tail's), and matches docs/reference/cli.md.
func TestQueryUsageNamesItsFlags(t *testing.T) {
	_, usage, _ := run("help")
	i := strings.Index(usage, "  ynr query ")
	if i < 0 {
		t.Fatal("usage has no ynr query line")
	}
	block := usage[i:]
	if j := strings.Index(block, "\n\n"); j >= 0 {
		block = block[:j]
	}
	_, _, help := run("query", "runs", "-h")
	for _, m := range regexp.MustCompile(`(?m)^\s+-([a-z][a-z-]*)`).FindAllStringSubmatch(help, -1) {
		if !strings.Contains(block, "--"+m[1]) {
			t.Errorf("the ynr query usage does not mention --%s", m[1])
		}
	}
}

// `ynr query` and `ynr query --help` list every flag the query command takes, so the listing
// cannot drift from the flags the code defines (ynr #123).
func TestQueryHelpListsEveryFlag(t *testing.T) {
	_, _, flagHelp := run("query", "runs", "-h")
	flagLine := regexp.MustCompile(`(?m)^\s+-([a-z][a-z-]*)`)
	found := flagLine.FindAllStringSubmatch(flagHelp, -1)
	if len(found) == 0 {
		t.Fatalf("ynr query runs -h listed no flags:\n%s", flagHelp)
	}
	for _, args := range [][]string{{"query"}, {"query", "--help"}, {"query", "-h"}} {
		_, out, _ := run(args...)
		for _, m := range found {
			if !strings.Contains(out, "--"+m[1]) {
				t.Errorf("ynr %s does not list --%s", strings.Join(args, " "), m[1])
			}
		}
	}
}

// TestCollectorIDForAnyHost: every hostname gives an id the collector accepts, and two long ones
// sharing a start stay apart. A CI runner's hostname is often longer than an id may be.
func TestCollectorIDForAnyHost(t *testing.T) {
	long := "iad20-fj917-5597187e-256d-4af3-8659-f326c957451c-12685478a4c2"
	for _, host := range []string{"mac", "Davids-MacBook-Pro.local", long, long + "x", strings.Repeat("a", 300), "--", "UPPER_case.example.com"} {
		if id := collectorIDFor(host); !idPattern.MatchString(id) {
			t.Errorf("host %q gives %q, which the collector refuses", host, id)
		}
	}
	if collectorIDFor(long) == collectorIDFor(long+"x") {
		t.Error("two long hostnames sharing a start give the same id")
	}
	if got := collectorIDFor("Davids-MacBook-Pro.local"); got != "local-davids-macbook-pro" {
		t.Errorf("short hostname = %q, want it unchanged", got)
	}
}

// TestInfoAndDoctorSeeTheStandingRelay: a relay on a fixed port writing into the spool's local
// folder, as a Wheelhouse runs one, shows in ynr info and ynr doctor while it runs, and a second
// one on that port is refused plainly.
func TestInfoAndDoctorSeeTheStandingRelay(t *testing.T) {
	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(root, spool.LocalDir)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	_, out, _ := run("info", "--spool", root)
	if !strings.Contains(out, "relay         no") {
		t.Errorf("info before = %s", out)
	}

	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	var errb bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run(ctx, []string{"relay", "--spool", local, "--listen", addr}, pw, &errb)
		_ = pw.Close()
	}()
	if _, err := bufio.NewReader(pr).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, pr) }()

	_, out, _ = run("info", "--spool", root, "--format", "json")
	var rep infoReport
	if json.Unmarshal([]byte(out), &rep) != nil || rep.Relay == nil || rep.Relay.Endpoint != "http://"+addr {
		t.Errorf("info while relaying = %s", out)
	}
	c := checkRelay(root)
	if c.Status != statusOK || !strings.Contains(c.Detail, "http://"+addr) {
		t.Errorf("doctor relay = %+v", c)
	}
	if code, _, errOut := run("relay", "--spool", t.TempDir(), "--listen", addr); code != ExitConfig || !strings.Contains(errOut, "already in use") {
		t.Errorf("second relay on the port = %d %q", code, errOut)
	}

	cancel()
	if c := <-code; c != ExitOK {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	if c := checkRelay(root); c.Status != statusOK || !strings.Contains(c.Detail, "no standing ynr relay") {
		t.Errorf("doctor relay after stop = %+v", c)
	}
}

// fakeYnh puts a ynh on the PATH that prints a registry declaring the given attributes.
func fakeYnh(t *testing.T, attrs ...string) {
	t.Helper()
	var as []string
	for _, a := range attrs {
		as = append(as, `{"id":"`+a+`","type":"string"}`)
	}
	reg := `{"tool":"ynh","version":"1.0.0","attributes":[` + strings.Join(as, ",") + `]}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reg.json"), []byte(reg), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat \"$(dirname \"$0\")/reg.json\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ynh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRelayResourceAttrRefusals: a key no sibling registry declares, a malformed pair, and a
// registry that cannot be read are all refused at start-up, before anything listens.
func TestRelayResourceAttrRefusals(t *testing.T) {
	fakeYnh(t, "ynh.session.id")
	cases := map[string][]string{
		"undeclared":  {"relay", "--spool", t.TempDir(), "--resource-attr", "ynh.other=x"},
		"standard":    {"relay", "--spool", t.TempDir(), "--resource-attr", "service.name=x"},
		"no tool":     {"relay", "--spool", t.TempDir(), "--resource-attr", "ynh.session.id=x", "--registry-tools", "ynr-no-such-tool"},
		"not a pair":  {"relay", "--spool", t.TempDir(), "--resource-attr", "ynh.session.id"},
		"empty value": {"relay", "--spool", t.TempDir(), "--resource-attr", "ynh.session.id="},
		"two values":  {"relay", "--spool", t.TempDir(), "--resource-attr", "ynh.session.id=a", "--resource-attr", "ynh.session.id=b"},
	}
	for name, args := range cases {
		var out, errb bytes.Buffer
		if code := Run(context.Background(), args, &out, &errb); code == ExitOK {
			t.Errorf("%s: exit 0, stderr %q", name, errb.String())
		}
		if out.Len() != 0 {
			t.Errorf("%s: printed an endpoint: %q", name, out.String())
		}
	}
}

// TestRelayStampsTheDeclaredResourceAttr runs the command with a declared key and finds it on a
// record whose vendor sent another value.
func TestRelayStampsTheDeclaredResourceAttr(t *testing.T) {
	fakeYnh(t, "ynh.session.id")
	dir := t.TempDir()
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var errb bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run(ctx, []string{"relay", "--spool", dir, "--format", "json", "--resource-attr", "ynh.session.id=sess-9"}, pw, &errb)
	}()
	var ready struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(pr).Decode(&ready); err != nil {
		t.Fatalf("ready line: %v (%s)", err, errb.String())
	}
	body := `{"resourceLogs":[{"resource":{"attributes":[{"key":"ynh.session.id","value":{"stringValue":"vendor"}}]},"scopeLogs":[{"logRecords":[{"eventName":"x"}]}]}]}`
	resp, err := http.Post(ready.Endpoint+"/v1/logs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cancel()
	if c := <-code; c != ExitOK {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	entries, _ := os.ReadDir(dir)
	var all string
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		all += string(b)
	}
	if !strings.Contains(all, "sess-9") || strings.Contains(all, "vendor") {
		t.Errorf("spool = %s", all)
	}
}

// TestRelayRefusesMisdeclaredAndStrippedKeys: a registry that declares host.name or user.email
// makes the key refused (it is off-prefix and on the strip list), and the message says why.
func TestRelayRefusesMisdeclaredAndStrippedKeys(t *testing.T) {
	for _, key := range []string{"host.name", "user.email"} {
		fakeYnh(t, "ynh.session.id", key)
		var out, errb bytes.Buffer
		code := Run(context.Background(), []string{"relay", "--spool", t.TempDir(), "--resource-attr", key + "=x"}, &out, &errb)
		if code != ExitConfig || out.Len() != 0 {
			t.Errorf("%s: exit %d, stdout %q", key, code, out.String())
		}
		if !strings.Contains(errb.String(), key) || !strings.Contains(errb.String(), "own prefix") {
			t.Errorf("%s: unclear message %q", key, errb.String())
		}
	}
}

// TestCommandsRefuseStrayArguments: a positional argument no command takes is refused with exit 2
// and a message naming it, before the command does anything (ynr #127). The case that bit was a
// relay started with `resource-attr k=v`, the dashes forgotten, which ran without the stamp.
func TestCommandsRefuseStrayArguments(t *testing.T) {
	spool := t.TempDir()
	for name, args := range map[string][]string{
		"version":            {"version", "extra"},
		"--version":          {"--version", "extra"},
		"info":               {"info", "--spool", spool, "extra"},
		"doctor":             {"doctor", "extra"},
		"relay":              {"relay", "--spool", spool, "resource-attr", "k=v"},
		"serve":              {"serve", "extra"},
		"central":            {"central", "extra"},
		"conformance":        {"conformance", "extra"},
		"telemetry registry": {"telemetry", "registry", "extra"},
	} {
		var out, errb bytes.Buffer
		code := Run(context.Background(), args, &out, &errb)
		if code != ExitUsage {
			t.Errorf("%s: exit %d, want %d (stderr %q)", name, code, ExitUsage, errb.String())
		}
		if !strings.Contains(errb.String(), "takes no arguments") || !strings.Contains(errb.String(), `"extra"`) && name != "relay" {
			t.Errorf("%s: stderr %q does not name the argument", name, errb.String())
		}
		if name == "relay" && !strings.Contains(errb.String(), `"resource-attr"`) {
			t.Errorf("relay: stderr %q does not name resource-attr", errb.String())
		}
		if out.Len() != 0 {
			t.Errorf("%s: printed %q", name, out.String())
		}
	}
}
