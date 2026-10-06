package cli

import (
	"strings"
	"testing"

	"github.com/eyelock/ynr"
	"github.com/eyelock/ynr/internal/store"
)

// TestCentralRefusals: central needs the full build, and --ui is refused until sign-in exists.
func TestCentralRefusals(t *testing.T) {
	st := store.FolderURL(t.TempDir())
	if ynr.Build != "full" {
		code, _, errs := run("central", "--store", st, "--state", t.TempDir())
		if code != ExitConfig || !strings.Contains(errs, "full build") {
			t.Fatalf("central in the slim build = %d %q", code, errs)
		}
		return
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"ui", []string{"--store", st, "--ui", "127.0.0.1:0"}, "sign-in not built yet"},
		{"no store", []string{"--store", ""}, "no store"},
		{"bad window", []string{"--store", st, "--hot-window", "0s"}, "must be positive"},
		{"bad scheme", []string{"--store", "gs://bucket"}, "no adapter"},
	} {
		code, _, errs := run(append([]string{"central"}, tc.args...)...)
		if code != ExitConfig || !strings.Contains(errs, tc.want) {
			t.Errorf("%s: %d %q, want %q", tc.name, code, errs, tc.want)
		}
	}
}

func TestCentralSocketStaysShort(t *testing.T) {
	if got := centralSocket("/home/u/.local/state/ynr/central"); got != "/home/u/.local/state/ynr/central/central.sock" {
		t.Fatalf("short state: %s", got)
	}
	if got := centralSocket("/" + strings.Repeat("x", 120)); len(got) > maxSocket {
		t.Fatalf("long state: %s", got)
	}
}
