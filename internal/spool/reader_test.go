package spool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type got struct {
	rel, line string
}

func setup(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func poll(t *testing.T, r *Reader) []got {
	t.Helper()
	var out []got
	err := r.Poll(context.Background(), func(w Writer, line []byte) error {
		out = append(out, got{w.Rel, string(line)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func newReader(t *testing.T, root string, maxLine int) *Reader {
	t.Helper()
	r, err := NewReader(root, maxLine)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWritersAndClasses(t *testing.T) {
	root := setup(t)
	for _, d := range []string{"factory", "services/ynm", "runs/run-1", "runs/.hidden", "stray"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, "runs", "linked")); err != nil {
		t.Fatal(err)
	}
	c := &Counters{}
	ws, err := Writers(root, c)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, w := range ws {
		rels = append(rels, string(w.Class)+":"+w.Rel)
	}
	want := "factory:factory local:local run:runs/run-1 service:services/ynm"
	if strings.Join(rels, " ") != want {
		t.Fatalf("writers = %v, want %s", rels, want)
	}
	if c.Ignored.Load() != 3 { // stray, .hidden, the linked run folder
		t.Fatalf("ignored = %d, want 3", c.Ignored.Load())
	}
}

func TestLinesInOrderAndNoRedeliveryAfterRestart(t *testing.T) {
	root := setup(t)
	f := filepath.Join(root, "local", "ynh-1-0001"+OpenSuffix)
	writeFile(t, f, "a\nb\npart")
	r := newReader(t, root, 0)
	if g := poll(t, r); len(g) != 2 || g[0].line != "a" || g[1].line != "b" {
		t.Fatalf("first poll = %v", g)
	}
	appendFile(t, f, "ial\nc\n")
	r2 := newReader(t, root, 0) // a restart: positions come from the state folder
	g := poll(t, r2)
	if len(g) != 2 || g[0].line != "partial" || g[1].line != "c" {
		t.Fatalf("after restart = %v", g)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("an open file must not be deleted: %v", err)
	}
}

func TestRenameOnCloseKeepsPositionThenDeletes(t *testing.T) {
	root := setup(t)
	open := filepath.Join(root, "local", "ynh-1-0001"+OpenSuffix)
	writeFile(t, open, "a\n")
	r := newReader(t, root, 0)
	poll(t, r)
	appendFile(t, open, "b\n")
	closed := strings.TrimSuffix(open, OpenSuffix) + ClosedSuffix
	if err := os.Rename(open, closed); err != nil {
		t.Fatal(err)
	}
	if g := poll(t, r); len(g) != 1 || g[0].line != "b" {
		t.Fatalf("after close = %v, want only b", g)
	}
	if _, err := os.Stat(closed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed file should be deleted once committed, stat err = %v", err)
	}
	if r.Counters.Deleted.Load() != 1 {
		t.Fatalf("deleted = %d", r.Counters.Deleted.Load())
	}
}

func TestFailedHandOffIsReadAgain(t *testing.T) {
	root := setup(t)
	f := filepath.Join(root, "local", "x"+ClosedSuffix)
	writeFile(t, f, "a\nb\nc\n")
	r := newReader(t, root, 0)
	var seen []string
	err := r.Poll(context.Background(), func(_ Writer, line []byte) error {
		if string(line) == "b" {
			return errors.New("export failed")
		}
		seen = append(seen, string(line))
		return nil
	})
	if err == nil {
		t.Fatal("want the hand-off error")
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("file must survive a failed hand-off: %v", err)
	}
	g := poll(t, r)
	if strings.Join(seen, ",") != "a" || len(g) != 2 || g[0].line != "b" || g[1].line != "c" {
		t.Fatalf("seen %v then %v", seen, g)
	}
}

func TestMalformedAndOversizedAreSkippedAndCounted(t *testing.T) {
	root := setup(t)
	long := strings.Repeat("x", 200<<10) // longer than the 64 KiB read buffer
	writeFile(t, filepath.Join(root, "local", "x"+ClosedSuffix), "bad\n"+long+"\nok\n\n")
	r := newReader(t, root, 1024)
	var lines []string
	err := r.Poll(context.Background(), func(_ Writer, line []byte) error {
		if string(line) == "bad" {
			return ErrMalformed
		}
		lines = append(lines, string(line))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Counters.Snapshot()
	if strings.Join(lines, ",") != "ok" || s.Malformed != 1 || s.Oversized != 1 || s.Lines != 1 {
		t.Fatalf("lines %v counters %+v", lines, s)
	}
}

func TestClosedFileWithoutFinalNewline(t *testing.T) {
	root := setup(t)
	f := filepath.Join(root, "local", "x"+ClosedSuffix)
	writeFile(t, f, "a\ntrailing")
	r := newReader(t, root, 0)
	if g := poll(t, r); len(g) != 1 {
		t.Fatalf("got %v", g)
	}
	if r.Counters.Malformed.Load() != 1 {
		t.Fatalf("malformed = %d", r.Counters.Malformed.Load())
	}
	if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("closed file should be deleted")
	}
}

func TestLinksAreRefusedAndTheirTargetsKept(t *testing.T) {
	root := setup(t)
	manifest := filepath.Join(root, ManifestsDir, "run-1.json")
	writeFile(t, manifest, `{"lane":"secret"}`+"\n")
	host := filepath.Join(t.TempDir(), "host"+ClosedSuffix)
	writeFile(t, host, "host\n")
	run := filepath.Join(root, RunsDir, "run-1")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(manifest, filepath.Join(run, "m"+ClosedSuffix)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(host, filepath.Join(run, "h"+ClosedSuffix)); err != nil {
		t.Fatal(err)
	}
	factory := filepath.Join(root, FactoryDir, "f"+ClosedSuffix)
	writeFile(t, factory, "factory\n")
	if err := os.Link(factory, filepath.Join(run, "hard"+ClosedSuffix)); err != nil {
		t.Fatal(err)
	}
	r := newReader(t, root, 0)
	var lines []string
	if err := r.Poll(context.Background(), func(w Writer, line []byte) error {
		lines = append(lines, w.Rel+":"+string(line))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The factory file has two links while the planted hard link exists, so it is refused too.
	if len(lines) != 0 {
		t.Fatalf("read through a link: %v", lines)
	}
	if r.Counters.Rejected.Load() != 4 {
		t.Fatalf("rejected = %d, want 4", r.Counters.Rejected.Load())
	}
	for _, p := range []string{manifest, host, factory} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s must not be deleted: %v", p, err)
		}
	}
}

func TestLock(t *testing.T) {
	root := setup(t)
	if info, err := Holder(root); err != nil || info != nil {
		t.Fatalf("holder before = %v, %v", info, err)
	}
	l, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(root); !errors.Is(err, ErrServing) {
		t.Fatalf("second acquire = %v, want ErrServing", err)
	}
	info, err := Holder(root)
	if err != nil || info == nil || info.PID != os.Getpid() {
		t.Fatalf("holder = %+v, %v", info, err)
	}
	l.Release()
	if info, _ := Holder(root); info != nil {
		t.Fatalf("holder after release = %+v", info)
	}
}
