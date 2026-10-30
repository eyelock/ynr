package cli

import (
	"os"
	"path/filepath"
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

// A relative state folder, or the same one reached from another working directory, is the same
// socket for central and for ynr query.
func TestCentralSocketIsTheSameFromAnyDirectory(t *testing.T) {
	long := "/" + strings.Repeat("x", 120)
	t.Setenv("YNR_CENTRAL_STATE", long)
	t.Setenv("YNR_SPOOL_ROOT", "")
	got := querySockets("", "")
	if len(got) != 1 || got[0] != centralSocket(long) || len(got[0]) > maxSocket {
		t.Fatalf("querySockets = %v, want central's own fallback %s", got, centralSocket(long))
	}
	wd, _ := os.Getwd()
	if centralSocket("state") != centralSocket(filepath.Join(wd, "state")) {
		t.Fatal("a relative state folder is not made absolute")
	}
	if got := querySockets("", "/run/c.sock"); len(got) != 1 || got[0] != "/run/c.sock" {
		t.Fatalf("an explicit --socket is the only one asked: %v", got)
	}
}
