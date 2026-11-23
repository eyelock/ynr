package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHotPathsKeepTheSocketShort(t *testing.T) {
	_, short := hotPaths("/home/u/.local/state/ynr/spool")
	if short != "/home/u/.local/state/ynr/spool/.ynr/serve.sock" {
		t.Fatalf("short root: %s", short)
	}
	long := "/" + strings.Repeat("x", 120)
	db, socket := hotPaths(long)
	if !strings.HasPrefix(db, long) || len(socket) > maxSocket || filepath.Dir(socket) != filepath.Join(os.TempDir(), "ynr-"+strconv.Itoa(os.Getuid())) {
		t.Fatalf("long root: %s %s", db, socket)
	}
	if _, again := hotPaths(long); again != socket {
		t.Fatal("the socket path is not stable")
	}
	if _, other := hotPaths(long + "y"); other == socket {
		t.Fatal("two roots share a socket")
	}
}

func TestPrivateDirRefusesAnOpenFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(dir); err == nil {
		t.Fatal("accepted a folder others can open")
	}
}

func TestServeSweepReachesTheRetentionServeApplies(t *testing.T) {
	week := 7 * 24 * time.Hour
	for _, tc := range []struct{ window, retain, want time.Duration }{
		{week, week, week},
		{week, 30 * 24 * time.Hour, 30 * 24 * time.Hour},
		{14 * 24 * time.Hour, week, 14 * 24 * time.Hour},
	} {
		if got := serveCompaction(tc.window, tc.retain).Retention; got != tc.want {
			t.Errorf("window %s, --retain %s: sweep reaches %s, want %s", tc.window, tc.retain, got, tc.want)
		}
	}
}
