package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(FolderURL(dir)); err != nil {
		t.Fatalf("folder: %v", err)
	}
	for _, bad := range []string{"", "/tmp/x", "file://host/tmp", "file:relative", "s3://bucket", "%"} {
		if _, err := Open(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}

func TestValidKey(t *testing.T) {
	for key, want := range map[string]bool{
		"traces/2026/10/05/19/laptop/01J_local.a-0-10.jsonl.gz": true,
		"a":       true,
		"":        false,
		"/a":      false,
		"a/":      false,
		"a//b":    false,
		"a/../b":  false,
		"./a":     false,
		"a b":     false,
		"a\\b":    false,
		"ünicode": false,
	} {
		if got := ValidKey(key); got != want {
			t.Errorf("ValidKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestFolderPut(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(FolderURL(dir))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.Put(ctx, "a/b/c.jsonl.gz", []byte("one")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "a", "b", "c.jsonl.gz"))
	if err != nil || string(b) != "one" {
		t.Fatalf("read back %q, %v", b, err)
	}
	if err := st.Put(ctx, "a/b/c.jsonl.gz", []byte("two")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrote an object: %v", err)
	}
	if err := st.Put(ctx, "../escape", []byte("x")); err == nil {
		t.Fatal("wrote outside the store")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "a", "b"))
	if len(entries) != 1 {
		t.Fatalf("left temporary files: %v", entries)
	}
}

func TestBatchKey(t *testing.T) {
	at := time.Date(2026, 10, 5, 9, 14, 2, 0, time.FixedZone("x", 3600))
	got := BatchKey(Traces, at, "laptop", "01ABC", "runs.r1.ynh-i-000001", 0, 120)
	want := "traces/2026/10/05/08/laptop/01ABC_runs.r1.ynh-i-000001-0-120.jsonl.gz"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !ValidKey(got) {
		t.Error("not a valid key")
	}
}

func TestNewULID(t *testing.T) {
	var ids []string
	base := time.Unix(1_800_000_000, 0)
	for i := range 50 {
		id := NewULID(base.Add(time.Duration(i) * time.Millisecond))
		if len(id) != 26 || strings.Trim(id, crockford) != "" {
			t.Fatalf("ULID %q", id)
		}
		ids = append(ids, id)
	}
	if !sort.StringsAreSorted(ids) {
		t.Error("ULIDs from later times do not sort later")
	}
	if a, b := NewULID(base), NewULID(base); a == b {
		t.Error("two ULIDs in the same millisecond are equal")
	}
}

func TestFolderListsWhatWasPut(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenReader(FolderURL(dir))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, k := range []string{"traces/2026/10/05/21/laptop/b.jsonl.gz", "traces/2026/10/05/20/laptop/a.jsonl.gz", "logs/x.jsonl.gz"} {
		if err := r.Put(ctx, k, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "traces", "2026", "10", "05", "21", "laptop", ".put-123"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	keys, err := r.List(ctx, "traces/")
	if err != nil {
		t.Fatal(err)
	}
	want := "traces/2026/10/05/20/laptop/a.jsonl.gz traces/2026/10/05/21/laptop/b.jsonl.gz"
	if strings.Join(keys, " ") != want {
		t.Fatalf("keys = %v", keys)
	}
	if keys, err := r.List(ctx, "metrics/2026/"); err != nil || len(keys) != 0 {
		t.Fatalf("an empty prefix = %v, %v", keys, err)
	}
	if _, err := r.List(ctx, "../"); err == nil {
		t.Fatal("listed outside the store")
	}
	if got := r.Location("logs/x.jsonl.gz"); got != filepath.Join(dir, "logs", "x.jsonl.gz") {
		t.Fatalf("location = %s", got)
	}
}
