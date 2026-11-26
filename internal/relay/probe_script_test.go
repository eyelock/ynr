package relay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The probe script must run the binary CLAUDE_BIN names, for --version and for the probe runs,
// not whichever claude is on PATH.
func TestProbeScriptHonoursClaudeBin(t *testing.T) {
	if testing.Short() {
		t.Skip("the probe script waits a few seconds per run")
	}
	for _, tool := range []string{"bash", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\necho \"$1\" >>" + calls + "\n[ \"$1\" = --version ] && echo '9.9.9 (Claude Code)'\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// A claude on PATH that must never run.
	decoy := filepath.Join(dir, "bin")
	if err := os.Mkdir(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "claude"), []byte("#!/bin/sh\necho decoy >>"+calls+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "probe-claude-code-otel.sh"))
	cmd.Env = append(os.Environ(), "CLAUDE_BIN="+fake, "PATH="+decoy+":"+os.Getenv("PATH"))
	out, _ := cmd.CombinedOutput() // exits 2: the fake exports nothing
	got, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("fake claude never ran: %v\n%s", err, out)
	}
	if strings.Contains(string(got), "decoy") {
		t.Errorf("the claude on PATH ran despite CLAUDE_BIN:\n%s", got)
	}
	if !strings.Contains(string(out), "installed: 9.9.9") {
		t.Errorf("--version did not come from CLAUDE_BIN:\n%s", out)
	}
	if !strings.Contains(string(got), "-p") {
		t.Errorf("the probe runs did not use CLAUDE_BIN:\n%s", got)
	}
}
