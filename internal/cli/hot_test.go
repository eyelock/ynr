package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
