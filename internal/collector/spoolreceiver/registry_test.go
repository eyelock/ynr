package spoolreceiver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/consumer/consumertest"

	"github.com/eyelock/ynr/internal/names"
	"github.com/eyelock/ynr/internal/registry"
	"github.com/eyelock/ynr/internal/store"
)

// ynhRegistry is what a fake ynh prints: one attribute, one span and one event.
var ynhRegistry = registry.Registry{
	Tool: "ynh", Version: "1.0.0",
	Attributes: []registry.Attribute{{ID: "ynh.thing"}},
	Standard:   []string{"gen_ai.request.model"},
	Spans:      []registry.Span{{Name: "ynh.run"}},
	Events:     []registry.Event{{Name: "ynh.run.started"}},
}

// fakeTool puts a script called name first on the PATH that prints out.
func fakeTool(t *testing.T, name, out string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out.json"), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1 $2 $3 $4\" != \"telemetry registry --format json\" ]; then exit 3; fi\ncat \"$(dirname \"$0\")/out.json\"\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func spansLine(service, version, span, attr string) string {
	return `{"resourceSpans":[{"resource":{"attributes":[` +
		`{"key":"service.name","value":{"stringValue":"` + service + `"}},` +
		`{"key":"service.version","value":{"stringValue":"` + version + `"}},` +
		`{"key":"ynr.registry","value":{"stringValue":"forged"}}]},` +
		`"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",` +
		`"name":"` + span + `","startTimeUnixNano":"1","endTimeUnixNano":"2",` +
		`"attributes":[{"key":"` + attr + `","value":{"stringValue":"x"}}]}]}]}]}`
}

func TestRegistriesAreLearnedStoredCheckedAndNothingIsDropped(t *testing.T) {
	raw, _ := json.Marshal(ynhRegistry)
	fakeTool(t, "ynh", string(raw))
	storeDir := t.TempDir()
	logLine := `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynh"}},` +
		`{"key":"service.version","value":{"stringValue":"1.0.0"}}]},` +
		`"scopeLogs":[{"logRecords":[{"eventName":"ynh.made.up","body":{"stringValue":"x"}}]}]}]}`
	root := spoolWith(t, map[string]string{"local/a.jsonl": strings.Join([]string{
		spansLine("ynh", "1.0.0", "ynh.run", "ynh.thing"),                 // all declared
		spansLine("ynh", "1.0.0", "ynh.surprise", "ynh.other"),            // two unknown
		spansLine("ynh", "1.0.0", "ynh.surprise", "gen_ai.request.model"), // one unknown again
		spansLine("ynh", "2.0.0", "ynh.run", "ynh.thing"),                 // version not learned
		spansLine("claude-code", "9", "claude.x", "claude.y"),             // tool not learned
		logLine,
	}, "\n") + "\n"})
	traces, logs := new(consumertest.TracesSink), new(consumertest.LogsSink)
	r := startWith(t, root, sinks{traces, logs, consumertest.NewNop()}, func(c *Config) {
		c.Store, c.RegistryTools = store.FolderURL(storeDir), []string{"ynh", "ynr-no-such-tool", "../ynf"}
	})
	if err := r.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The registry is stored under the collector's own prefix, named by its hash.
	var keys []string
	_ = filepath.WalkDir(filepath.Join(storeDir, "registries"), func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			rel, _ := filepath.Rel(storeDir, p)
			keys = append(keys, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(keys) != 1 {
		t.Fatalf("stored %v", keys)
	}
	k, err := store.ParseRegistry(keys[0])
	if err != nil || k.Collector != "gha-eyelock" || k.Tool != "ynh" || k.Version != "1.0.0" {
		t.Fatalf("key %q: %+v %v", keys[0], k, err)
	}
	if b, _ := os.ReadFile(filepath.Join(storeDir, keys[0])); string(b) != string(raw) {
		t.Errorf("stored %q", b)
	}

	// Every record was kept.
	all := traces.AllTraces()
	if len(all) != 5 || logs.LogRecordCount() != 1 {
		t.Fatalf("traces %d logs %d", len(all), logs.LogRecordCount())
	}
	marks := []string{"", "", "", names.RegistryUnknown, names.RegistryUnknown}
	for i, td := range all {
		if got := resourceAttr(td, names.AttrRegistry); got != marks[i] {
			t.Errorf("trace %d: ynr.registry = %q, want %q", i, got, marks[i])
		}
	}
	if v, ok := logs.AllLogs()[0].ResourceLogs().At(0).Resource().Attributes().Get(names.AttrRegistry); ok {
		t.Errorf("a log of a known tool was marked %q", v.Str())
	}

	// Unknown names are counted, by service, version and name; declared ones are not.
	counts, over := r.checker.Counts()
	got := map[string]int64{}
	for _, u := range counts {
		got[u.Service+" "+u.Version+" "+u.Name] = u.Count
	}
	want := map[string]int64{"ynh 1.0.0 ynh.surprise": 2, "ynh 1.0.0 ynh.other": 1, "ynh 1.0.0 ynh.made.up": 1}
	if len(got) != len(want) || over != 0 {
		t.Fatalf("counts %v overflow %d", got, over)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s = %d, want %d", k, got[k], n)
		}
	}
}

func TestNoRegistryToolsMeansNoChecking(t *testing.T) {
	root := spoolWith(t, map[string]string{"local/a.jsonl": spansLine("ynh", "1.0.0", "ynh.x", "ynh.y") + "\n"})
	traces := new(consumertest.TracesSink)
	r := start(t, root, sinks{traces, consumertest.NewNop(), consumertest.NewNop()})
	if err := r.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.checker != nil || len(traces.AllTraces()) != 1 {
		t.Fatal("checked, or dropped")
	}
	// The sender's forged ynr.registry is still removed by the stamp.
	if got := resourceAttr(traces.AllTraces()[0], names.AttrRegistry); got != "" {
		t.Errorf("forged ynr.registry survived: %q", got)
	}
}
