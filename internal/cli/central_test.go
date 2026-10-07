package cli

import (
	"strings"
	"testing"

	"github.com/eyelock/ynr"
	"github.com/eyelock/ynr/internal/store"
)

// TestCentralRefusals: central needs the full build, and --ui is refused unless sign-in is fully set up.
func TestCentralRefusals(t *testing.T) {
	st := store.FolderURL(t.TempDir())
	if ynr.Build != "full" {
		code, _, errs := run("central", "--store", st, "--state", t.TempDir())
		if code != ExitConfig || !strings.Contains(errs, "full build") {
			t.Fatalf("central in the slim build = %d %q", code, errs)
		}
		return
	}
	t.Setenv("YNR_OIDC_CLIENT_SECRET", "from-the-environment")
	t.Setenv("YNR_SESSION_SECRET", strings.Repeat("s", 40))
	// The client secret is never a flag, so it cannot show in ps.
	if code, _, errs := run("central", "--store", st, "--ui", "127.0.0.1:0", "--oidc-client-secret", "x"); code != ExitUsage || !strings.Contains(errs, "not defined") {
		t.Errorf("a client secret flag = %d %q", code, errs)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"ui without sign-in", []string{"--store", st, "--ui", "127.0.0.1:0"}, "rather than serve central's data unauthenticated"},
		{"ui without who may sign in", []string{"--store", st, "--ui", "127.0.0.1:0", "--oidc-issuer", "http://127.0.0.1:1",
			"--oidc-client-id", "c", "--oidc-redirect-url", "https://ynr.example.com/auth/callback"}, "who may sign in"},
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
