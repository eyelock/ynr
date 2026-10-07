package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/eyelock/ynr"
	"github.com/eyelock/ynr/internal/names"
	"github.com/eyelock/ynr/internal/store"
)

func own(t *testing.T) *Registry {
	t.Helper()
	sub, err := fs.Sub(ynr.TelemetryRegistry, "telemetry/registry")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Load(sub, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestNamesMatchTheRegistry: internal/names/names.go is exactly what the registry generates, so
// a name added to the registry without `go generate ./...`, or a constant edited by hand, fails.
func TestNamesMatchTheRegistry(t *testing.T) {
	r, err := Load(os.DirFS("../../telemetry/registry"), "")
	if err != nil {
		t.Fatal(err)
	}
	want, err := Generate(r, "names", "ynr.")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../names/names.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("internal/names/names.go is not what telemetry/registry generates: run `go generate ./...`")
	}
}

func TestOwnRegistryDeclaresWhatYnrWrites(t *testing.T) {
	r := own(t)
	if r.Tool != "ynr" || r.Version != "1.2.3" || r.Semconv.Version != "1.40.0" {
		t.Errorf("%+v", r)
	}
	have := map[string]bool{}
	for _, a := range r.Attributes {
		have[a.ID] = true
	}
	for _, m := range r.Metrics {
		have[m.Name] = true
	}
	for _, n := range []string{"ynr.provenance", "ynr.provenance.warning", "ynr.collector.id", "ynr.collector.instance",
		"ynr.registry", "ynr.spool.evicted", "ynr.registry.unknown_names"} {
		if !have[n] {
			t.Errorf("ynr's registry lacks %s", n)
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Registry
	if err := json.Unmarshal(b, &back); err != nil || back.Tool != "ynr" {
		t.Errorf("round trip: %v", err)
	}
}

func TestLoadRefusals(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(os.DirFS(dir), ""); err == nil {
		t.Error("no manifest")
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.yaml", "name: t\n")
	write("g.yaml", "groups:\n  - {id: g, type: entity}\n")
	if _, err := Load(os.DirFS(dir), ""); err == nil {
		t.Error("unknown group type")
	}
}

// fakeTool puts a script called name first on the PATH. The script prints out when asked for
// `telemetry registry --format json` and exits 3 otherwise.
func fakeTool(t *testing.T, name, out string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out.json"), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1 $2 $3 $4\" != \"telemetry registry --format json\" ]; then exit 3; fi\ncat \"$(dirname \"$0\")/out.json\"\n"
	writeScript(t, dir, name, script)
}

func writeScript(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func regJSON(tool, version string) string {
	b, _ := json.Marshal(Registry{Tool: tool, Version: version, Spans: []Span{{Name: tool + ".run"}}})
	return string(b)
}

func TestAskRefusals(t *testing.T) {
	ctx := context.Background()
	// A name with a path in it is never run, even when it would work.
	fakeTool(t, "ynx", regJSON("ynx", "1.0.0"))
	if p, _ := exec.LookPath("ynx"); p == "" {
		t.Fatal("fake tool not on PATH")
	}
	for _, name := range []string{"/bin/ls", "./ynx", "../ynx", "", "a b", "YNX", "ynx;ls"} {
		if _, err := Ask(ctx, name); err == nil || !strings.Contains(err.Error(), "bare tool name") {
			t.Errorf("%q: %v", name, err)
		}
	}
	// Missing from the PATH.
	if _, err := Ask(ctx, "ynr-no-such-tool"); err == nil || !strings.Contains(err.Error(), "not on the PATH") {
		t.Errorf("missing: %v", err)
	}
	// Failing.
	dir := t.TempDir()
	writeScript(t, dir, "ynfail", "#!/bin/sh\necho boom >&2\nexit 1\n")
	if _, err := Ask(ctx, "ynfail"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("failing: %v", err)
	}
	// Not JSON, another tool's registry, an unusable version, no names, another shape.
	for name, out := range map[string]string{
		"ynjunk":  "not json",
		"ynother": regJSON("somethingelse", "1.0.0"),
		"ynbadv":  regJSON("ynbadv", "../1"),
		"ynempty": `{"tool":"ynempty","version":"1.0.0"}`,
		"ynshape": `{"tool":"ynshape","version":"1.0.0","registry":{"groups":[]}}`,
	} {
		fakeTool(t, name, out)
		if _, err := Ask(ctx, name); err == nil {
			t.Errorf("%s was learned", name)
		}
	}
}

func TestAskTimesOut(t *testing.T) {
	old := AskTimeout
	AskTimeout = 200 * time.Millisecond
	t.Cleanup(func() { AskTimeout = old })
	writeScript(t, t.TempDir(), "ynslow", "#!/bin/sh\nexec sleep 30\n")
	start := time.Now()
	if _, err := Ask(context.Background(), "ynslow"); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Errorf("slow: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

func TestLearnToolsStoresUnderTheCollectorAndSkipsTheRest(t *testing.T) {
	fakeTool(t, "ynh", regJSON("ynh", "1.0.0"))
	st, err := store.Open(store.FolderURL(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, problems := LearnTools(ctx, []string{"ynh", "ynr-no-such-tool"}, st, "gha-eyelock")
	if c == nil || len(problems) != 1 || strings.Join(c.Tools(), ",") != "ynh 1.0.0" {
		t.Fatalf("%v %v", c, problems)
	}
	l, err := Ask(ctx, "ynh")
	if err != nil {
		t.Fatal(err)
	}
	key := store.RegistryKey("gha-eyelock", "ynh", "1.0.0", l.SHA256)
	rd := st.(store.Reader)
	if b, err := rd.Get(ctx, key); err != nil || string(b) != regJSON("ynh", "1.0.0") {
		t.Fatalf("%s: %q %v", key, b, err)
	}
	// Learning again finds it there, which is fine.
	if err := Store(ctx, st, "gha-eyelock", l); err != nil {
		t.Errorf("second store: %v", err)
	}
	// A different registry for the same tool and version lands beside it.
	fakeTool(t, "ynh", strings.Replace(regJSON("ynh", "1.0.0"), "ynh.run", "ynh.other", 1))
	l2, _ := Ask(ctx, "ynh")
	if err := Store(ctx, st, "gha-eyelock", l2); err != nil || l2.SHA256 == l.SHA256 {
		t.Fatalf("conflicting store: %v", err)
	}
	if keys, _ := rd.List(ctx, "registries/gha-eyelock/ynh/1.0.0/"); len(keys) != 2 {
		t.Errorf("keys %v", keys)
	}
	// Nothing configured: nothing learned, nothing checked.
	if c, p := LearnTools(ctx, nil, st, "x"); c != nil || p != nil {
		t.Error("learned from nothing")
	}
}

func traces(service, version string, spans map[string]string) ptrace.ResourceSpans {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", service)
	if version != "" {
		rs.Resource().Attributes().PutStr("service.version", version)
	}
	for name, attr := range spans {
		sp := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		sp.SetName(name)
		if attr != "" {
			sp.Attributes().PutStr(attr, "x")
		}
	}
	return rs
}

func TestCheckerCountsUnknownNamesAndMarksUnknownRegistries(t *testing.T) {
	c := NewChecker()
	c.Learn(&Registry{Tool: "ynh", Version: "1", Attributes: []Attribute{{ID: "ynh.a"}}, Spans: []Span{{Name: "ynh.run"}}})
	rs := traces("ynh", "1", map[string]string{"ynh.run": "ynh.a", "ynh.nope": "ynh.b"})
	c.Traces(rs)
	if _, ok := rs.Resource().Attributes().Get(names.AttrRegistry); ok {
		t.Error("a record with a learned registry was marked")
	}
	counts, over := c.Counts()
	if len(counts) != 2 || over != 0 || counts[0].Name != "ynh.b" || counts[1].Name != "ynh.nope" {
		t.Errorf("%+v", counts)
	}
	// Another version, no version, and another tool are marked, and not counted.
	for _, rs := range []ptrace.ResourceSpans{traces("ynh", "2", map[string]string{"x": "y"}), traces("ynh", "", map[string]string{"x": "y"}), traces("other", "1", map[string]string{"x": "y"})} {
		c.Traces(rs)
		if v, _ := rs.Resource().Attributes().Get(names.AttrRegistry); v.Str() != names.RegistryUnknown {
			t.Errorf("not marked: %v", rs.Resource().Attributes().AsRaw())
		}
	}
	if counts, _ := c.Counts(); len(counts) != 2 {
		t.Errorf("counted names for unlearned registries: %+v", counts)
	}
}

func TestCheckerCapsDistinctNamesAndStillCountsKnownOnes(t *testing.T) {
	c := NewChecker()
	c.Learn(&Registry{Tool: "ynh", Version: "1"})
	for i := 0; i < MaxUnknown+50; i++ {
		c.Traces(traces("ynh", "1", map[string]string{fmt.Sprintf("n%d", i): ""}))
	}
	c.Traces(traces("ynh", "1", map[string]string{"n0": ""}))
	long := strings.Repeat("x", 10000)
	c.Traces(traces("ynh", "1", map[string]string{"n1": long}))
	counts, over := c.Counts()
	if len(counts) != MaxUnknown || over < 50 {
		t.Fatalf("distinct %d overflow %d", len(counts), over)
	}
	for _, u := range counts {
		if len(u.Name) > maxName {
			t.Fatalf("a %d byte name was kept", len(u.Name))
		}
		if u.Name == "n0" && u.Count != 2 {
			t.Errorf("n0 counted %d times, want 2: names seen before the cap keep counting", u.Count)
		}
	}
}
