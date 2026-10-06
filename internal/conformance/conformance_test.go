package conformance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// binDir holds the fake tool, named fake, and the stub vendor, built once for the tests.
var binDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ynr-conformance-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binDir = dir
	for out, pkg := range map[string]string{"fake": "./testdata/faketool", "ynr-stub-vendor": "../../cmd/ynr-stub-vendor"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, out), pkg)
		if b, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building %s: %v\n%s", pkg, err, b)
			_ = os.RemoveAll(dir)
			os.Exit(1)
		}
	}
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// conformanceFile writes a conformance file for the fake tool with the given scenarios.
func conformanceFile(t *testing.T, scenarios string) string {
	t.Helper()
	reg, err := filepath.Abs("testdata/registry")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "conformance.yaml")
	body := fmt.Sprintf("service: fake\nregistry: %s\nvendor: ynr-stub-vendor\nvendor_aliases: [claude]\nscenarios:\n%s", reg, scenarios)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const converged = `  - name: run, converged
    run: fake run --task @canary:prompt
    expect: { outcome: converged }
`

func runFake(t *testing.T, breaks string) *Report {
	t.Helper()
	return runScenarios(t, converged, map[string]string{"FAKE_BREAK": breaks})
}

func runScenarios(t *testing.T, scenarios string, env map[string]string) *Report {
	t.Helper()
	rep, err := Run(context.Background(), Options{
		File: conformanceFile(t, scenarios), Timeout: 20 * time.Second, KillWait: 4 * time.Second, Env: env,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func dump(rep *Report) string {
	var b bytes.Buffer
	rep.WriteText(&b)
	return b.String()
}

func rule(rep *Report, n int) RuleSummary {
	for _, r := range rep.Rules {
		if r.Rule == n {
			return r
		}
	}
	return RuleSummary{}
}

// TestConforming shows a tool that meets the contract passes every rule that can be checked.
func TestConforming(t *testing.T) {
	rep := runFake(t, "")
	if !rep.OK {
		t.Fatalf("a conforming tool failed:\n%s", dump(rep))
	}
	for _, n := range []int{1, 2, 3, 4, 5, 7, 8, 10, 11, 12, 14} {
		if got := rule(rep, n); got.Status != Pass {
			t.Errorf("rule %d: %s %s, want pass\n%s", n, got.Status, got.Detail, dump(rep))
		}
	}
	for _, n := range []int{6, 9, 13} {
		if got := rule(rep, n); got.Status != Skipped {
			t.Errorf("rule %d: %s, want skipped", n, got.Status)
		}
	}
	if len(rep.Scenarios[0].Review) == 0 {
		t.Error("no span counts were printed for review")
	}
}

// TestViolations is ADR-009's exit check: a deliberate violation of each rule fails its check.
func TestViolations(t *testing.T) {
	cases := []struct {
		name  string
		fault string
		rule  int
		// evidence is text the failing check shows.
		evidence string
	}{
		{"no service.name", "noservice", 2, "service.name"},
		{"no service.instance.id", "noinstance", 2, "service.instance.id"},
		{"ignores OTEL_RESOURCE_ATTRIBUTES", "noresattrs", 2, "conformance.marker"},
		{"writes with neither target set", "neither", 1, "telemetry-out"},
		{"writes to the spool when an endpoint is set", "bothtargets", 1, "also wrote"},
		{"ignores TRACEPARENT", "ignoretrace", 3, "not in the trace"},
		{"a span with no started event", "nostarted", 5, "has no fake.run.started"},
		{"started event only at the end", "latestarted", 5, "no started event"},
		{"names it does not declare", "unregistered", 7, "fake.mystery"},
		{"printed registry differs", "registrydrift", 7, "fake.git"},
		{"an id on a metric", "highcard", 8, "UUID"},
		{"a canary in a record", "canary", 10, "prompt canary"},
		{"no outcome", "nooutcome", 11, "no outcome"},
		{"no status", "nostatus", 11, "status is unset"},
		{"blocks on a hung endpoint", "hang", 12, "blocks on telemetry"},
		{"telemetry changes the exit code", "badexit", 12, "exit code"},
		{"starts ynr because it found it", "ynrstart", 14, "ynr serve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rep := runFake(t, tc.fault)
			if rep.OK {
				t.Fatalf("a violation passed:\n%s", dump(rep))
			}
			if got := rule(rep, tc.rule); got.Status != Fail {
				t.Fatalf("rule %d: %s, want fail\n%s", tc.rule, got.Status, dump(rep))
			}
			var found bool
			for _, c := range rep.Scenarios[0].Checks {
				if c.Rule != tc.rule || c.Status != Fail {
					continue
				}
				text := c.Detail + " " + strings.Join(c.Evidence, " ")
				if strings.Contains(text, tc.evidence) {
					found = true
				}
			}
			if !found {
				t.Errorf("rule %d failed, but without evidence containing %q:\n%s", tc.rule, tc.evidence, dump(rep))
			}
		})
	}
}

// TestCanaryNotInReport: the report names the kind of canary, and shows the record, but never
// the planted value itself, so a report is safe to keep.
func TestCanaryNotInReport(t *testing.T) {
	rep := runFake(t, "canary")
	if strings.Contains(dump(rep), "ynrcanary") {
		t.Errorf("the report holds a canary value:\n%s", dump(rep))
	}
}

// TestScenarioWithoutCanary: with nothing planted, rule 10 says it could not check.
func TestScenarioWithoutCanary(t *testing.T) {
	rep := runScenarios(t, `  - name: plain
    run: fake run --task hello
    expect: { outcome: converged }
`, nil)
	if got := rule(rep, 10); got.Status != Skipped {
		t.Errorf("rule 10: %s, want skipped", got.Status)
	}
}

// TestExpectedOutcome: a scenario that expects another outcome fails rule 11.
func TestExpectedOutcome(t *testing.T) {
	rep := runScenarios(t, `  - name: wrong
    run: fake run --task x
    expect: { outcome: budget }
`, nil)
	if got := rule(rep, 11); got.Status != Fail {
		t.Errorf("rule 11: %s, want fail\n%s", got.Status, dump(rep))
	}
}

// TestNonZeroExitIsNotAFailure: a scenario that exits non-zero on purpose, such as a budget
// stop, passes as long as telemetry does not change the code.
func TestNonZeroExitIsNotAFailure(t *testing.T) {
	rep := runScenarios(t, `  - name: budget
    run: fake run --task x
    expect: { outcome: budget }
`, map[string]string{"FAKE_EXIT": "3", "FAKE_OUTCOME": "budget"})
	if got := rule(rep, 12); got.Status != Pass {
		t.Errorf("rule 12: %s\n%s", got.Status, dump(rep))
	}
}

func TestLoadFileRefusals(t *testing.T) {
	cases := map[string]string{
		"no service":     "scenarios:\n  - {name: a, run: b}\n",
		"no scenarios":   "service: x\n",
		"no command":     "service: x\nscenarios:\n  - {name: a}\n",
		"unknown field":  "service: x\nsurprise: 1\nscenarios:\n  - {name: a, run: b}\n",
		"unknown canary": "service: x\nscenarios:\n  - {name: a, run: 'b @canary:wallet'}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFile(p); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestCanaries(t *testing.T) {
	a, b := newCanaries(), newCanaries()
	cmd := "x @canary:prompt y @canary:secret z @canary:prompt"
	out := a.substitute(cmd)
	if strings.Contains(out, "@canary") || strings.Count(out, a["prompt"]) != 2 || !strings.Contains(out, a["secret"]) {
		t.Errorf("substitute: %q", out)
	}
	if a["prompt"] == b["prompt"] || a["prompt"] == a["ticket"] {
		t.Error("canaries are not unique")
	}
	if got := a.planted(cmd); len(got) != 2 {
		t.Errorf("planted %v", got)
	}
}

func TestIDLike(t *testing.T) {
	for v, want := range map[string]bool{
		"3f2b8c1e-9a4d-4e7f-8b21-5c6d7e8f9a0b": true,
		"01K9Z3M7Q8V5X2N4R6T8W0Y1A3":           true,
		"9f2c4b7e1a2b3c4d":                     true,
		"ABC-123":                              true,
		"item/github.com/eyelock/ynh/issues/7": true,
		"converged":                            false,
		"claude-sonnet":                        false,
		"lint-paydown":                         false,
		"42":                                   false,
	} {
		if got := idLike(v) != ""; got != want {
			t.Errorf("idLike(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestLoadRegistry(t *testing.T) {
	r, err := LoadRegistry("testdata/registry")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Spans["fake.run"] || !r.Events["fake.run.started"] || !r.Metrics["fake.run.count"] {
		t.Errorf("registry: %+v", r)
	}
	if r.Attributes["fake.outcome"] != 5 || !r.declaresAttr("fake.run.outcome") {
		t.Errorf("attributes: %+v", r.Attributes)
	}
	if _, err := LoadRegistry(t.TempDir()); err == nil {
		t.Error("an empty folder loaded")
	}
}
