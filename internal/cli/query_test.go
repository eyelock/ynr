package cli

import (
	"strings"
	"testing"

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
