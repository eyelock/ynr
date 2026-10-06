package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

func TestQueryListsTheNamedQueries(t *testing.T) {
	code, out, _ := run("query")
	if code != ExitOK {
		t.Fatalf("query = %d", code)
	}
	for _, name := range []string{"runs", "item <item key>", "trace <trace id>", "cost"} {
		if !strings.Contains(out, "  "+name) {
			t.Errorf("the list lacks %s:\n%s", name, out)
		}
	}
	if code, _, errs := run("query", "nope"); code != ExitUsage || !strings.Contains(errs, `no query "nope"`) {
		t.Errorf("unknown query = %d %q", code, errs)
	}
}

func TestQueryRefusesBadInput(t *testing.T) {
	s := store.FolderURL(t.TempDir())
	for _, args := range [][]string{
		{"item", "--store", s},           // needs an item key
		{"runs", "extra", "--store", s},  // takes no argument
		{"item", "a", "b", "--store", s}, // one argument at most
		{"runs", "--since", "yesterday", "--store", s},
		{"runs", "--since", "1h", "--until", "2h", "--store", s},
		{"runs", "--format", "xml", "--store", s},
	} {
		if code, _, errs := run(append([]string{"query"}, args...)...); code != ExitUsage {
			t.Errorf("%v = %d %q, want usage", args, code, errs)
		}
	}
}

func TestParseWhen(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"7d":                   at.Add(-7 * 24 * time.Hour),
		"90m":                  at.Add(-90 * time.Minute),
		"2026-10-01T00:00:00Z": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	} {
		if got, err := parseWhen(in, at); err != nil || !got.Equal(want) {
			t.Errorf("%s = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"-1h", "soon", "d"} {
		if _, err := parseWhen(bad, at); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
