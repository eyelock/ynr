package stamp

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"

	"github.com/eyelock/ynr/internal/spool"
)

func attrs(kv map[string]string) pcommon.Map {
	m := pcommon.NewMap()
	for k, v := range kv {
		m.PutStr(k, v)
	}
	return m
}

func str(m pcommon.Map, k string) (string, bool) {
	v, ok := m.Get(k)
	if !ok {
		return "", false
	}
	return v.Str(), true
}

func TestRunWithManifestOverwritesClaims(t *testing.T) {
	a := attrs(map[string]string{
		Provenance:     "factory",            // a run claiming to be ynf
		"ynr.anything": "x",                  // any ynr.* the sender set
		Lane:           "github.com/x#other", // another factory
		"service.name": "ynh",
	})
	m, ok := ParseManifest([]byte(`{"run":"r1","lane":"github.com/example-org/cfg#lint","harness":"ynh-lint","focus":"tidy","item":"github.com/eyelock/ynh#77","step":"s3"}`), "r1")
	if !ok {
		t.Fatal("manifest should parse")
	}
	Resource(a, spool.Writer{Class: spool.Run, Name: "r1"}, Identity{ID: "gha-eyelock", Instance: "job-9"}, m, "")
	want := map[string]string{
		Provenance: "run", CollectorID: "gha-eyelock", CollectorInstance: "job-9",
		Lane: "github.com/example-org/cfg#lint", LaneHarness: "ynh-lint", LaneFocus: "tidy",
		ItemKey: "github.com/eyelock/ynh#77", StepID: "s3", RunID: "r1", "service.name": "ynh",
	}
	for k, v := range want {
		if got, _ := str(a, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if _, ok := a.Get("ynr.anything"); ok {
		t.Error("sender's ynr.* attribute survived")
	}
}

func TestRunWithoutManifestCannotClaimAFactory(t *testing.T) {
	a := attrs(map[string]string{Lane: "github.com/x#forged", ItemKey: "forged"})
	Resource(a, spool.Writer{Class: spool.Run, Name: "r1"}, Identity{ID: "c"}, nil, NoManifest)
	if _, ok := a.Get(Lane); ok {
		t.Error("forged lane survived")
	}
	if _, ok := a.Get(ItemKey); ok {
		t.Error("forged item survived")
	}
	if w, _ := str(a, ProvenanceWarning); w != NoManifest {
		t.Errorf("warning = %q", w)
	}
}

func TestFactoryKeepsItsOwnAttributes(t *testing.T) {
	a := attrs(map[string]string{Lane: "github.com/example-org/cfg#lint", Provenance: "run"})
	Resource(a, spool.Writer{Class: spool.Factory}, Identity{ID: "c"}, nil, "")
	if v, _ := str(a, Provenance); v != "factory" {
		t.Errorf("provenance = %q", v)
	}
	if v, _ := str(a, Lane); v != "github.com/example-org/cfg#lint" {
		t.Errorf("ynf's own lane attribute removed: %q", v)
	}
}

func TestManifestMustNameItsRun(t *testing.T) {
	if _, ok := ParseManifest([]byte(`{"run":"other","lane":"l"}`), "r1"); ok {
		t.Error("a manifest for another run was accepted")
	}
	if _, ok := ParseManifest([]byte(`not json`), "r1"); ok {
		t.Error("garbage accepted")
	}
}

func TestParseManifest_UID(t *testing.T) {
	m, ok := ParseManifest([]byte(`{"run":"r1","lane":"l","uid":1001}`), "r1")
	if !ok || m.UID == nil || *m.UID != 1001 {
		t.Fatalf("manifest = %+v, %v", m, ok)
	}
	m, ok = ParseManifest([]byte(`{"run":"r1","lane":"l"}`), "r1")
	if !ok || m.UID != nil {
		t.Fatalf("manifest without uid = %+v, %v", m, ok)
	}
	if _, ok := ParseManifest([]byte(`{"run":"r1","lane":"l","uid":-1}`), "r1"); ok {
		t.Fatal("accepted a negative uid")
	}
}
