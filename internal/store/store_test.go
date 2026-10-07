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
	t.Setenv("AWS_REGION", "us-east-1")
	if _, err := Open("s3://bucket/prefix?region=eu-west-2"); err != nil {
		t.Fatalf("s3: %v", err)
	}
	for _, bad := range []string{"", "/tmp/x", "file://host/tmp", "file:relative", "s3:///no-bucket", "s3://bucket/../x", "gs://bucket", "%"} {
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
	if got, err := r.Local(ctx, "logs/x.jsonl.gz"); err != nil || got != filepath.Join(dir, "logs", "x.jsonl.gz") {
		t.Fatalf("local = %s %v", got, err)
	}
}

func TestOnlyExactKeysParse(t *testing.T) {
	at := time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)
	good := BatchKey(Traces, at, "laptop", NewULID(at), "local.ynh-1-000001", 0, 10)
	b, err := ParseBatch(good)
	if err != nil || b.Signal != Traces || !b.Hour.Equal(at) || b.Collector != "laptop" || b.Source != "local.ynh-1-000001-0-10" {
		t.Fatalf("%s = %+v, %v", good, b, err)
	}
	for _, bad := range []string{
		"traces/2026/13/05/21/laptop/01M46ZGNYMGVGPGTJE69223MFB_x-0-1.jsonl.gz",           // month 13
		"traces/2026/10/05/21/a/b/01M46ZGNYMGVGPGTJE69223MFB_x-0-1.jsonl.gz",              // too deep
		"compacted/traces/2026/10/05/21/laptop/01M46ZGNYMGVGPGTJE69223MFB_x-0-1.jsonl.gz", // another prefix
		"traces/2026/10/05/21/Laptop/01M46ZGNYMGVGPGTJE69223MFB_x-0-1.jsonl.gz",           // collector id
		"traces/2026/10/05/21/laptop/notaulid_x-0-1.jsonl.gz",
	} {
		if _, err := ParseBatch(bad); err == nil {
			t.Errorf("parsed %s", bad)
		}
	}
	for key, want := range map[string]struct {
		n        int
		manifest bool
	}{PartKey(Logs, at, 3): {3, false}, ManifestKey(Logs, at, 12): {12, true}} {
		if n, m, err := ParseCompacted(key); err != nil || n != want.n || m != want.manifest {
			t.Errorf("%s = %d %v %v", key, n, m, err)
		}
	}
	for _, bad := range []string{"compacted/logs/2026/10/05/21/part-0.parquet", "compacted/logs/2026/10/05/21/part-1.json", "compacted/logs/2026/10/05/21/x/part-1.parquet"} {
		if _, _, err := ParseCompacted(bad); err == nil {
			t.Errorf("parsed %s", bad)
		}
	}
}

func TestFolderGetAndDelete(t *testing.T) {
	r, err := OpenReader(FolderURL(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := r.Put(ctx, "a/b", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if b, err := r.Get(ctx, "a/b"); err != nil || string(b) != "x" {
		t.Fatalf("get = %q %v", b, err)
	}
	if err := r.Delete(ctx, "a/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "a/b"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after delete: %v", err)
	}
	if err := r.Delete(ctx, "a/b"); err != nil {
		t.Fatalf("deleting twice: %v", err)
	}
}

func TestReplaceOverwritesWhatPutRefuses(t *testing.T) {
	r, err := OpenReader(FolderURL(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := IndexKey(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))
	if key != "index/items/2026/10/05.parquet" {
		t.Fatalf("key = %s", key)
	}
	if err := r.Replace(ctx, key, []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := r.Put(ctx, key, []byte("2")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("put over it = %v", err)
	}
	if err := r.Replace(ctx, key, []byte("3")); err != nil {
		t.Fatal(err)
	}
	if b, _ := r.Get(ctx, key); string(b) != "3" {
		t.Fatalf("got %q", b)
	}
}

func TestRegistryKeys(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	key := RegistryKey("gha-eyelock", "ynh", "1.2.3-rc.5", sum)
	if key != "registries/gha-eyelock/ynh/1.2.3-rc.5/"+sum+".json" || !ValidKey(key) {
		t.Fatalf("key %q", key)
	}
	r, err := ParseRegistry(key)
	if err != nil || r != (Registry{Collector: "gha-eyelock", Tool: "ynh", Version: "1.2.3-rc.5", SHA256: sum}) {
		t.Fatalf("%+v %v", r, err)
	}
	for _, bad := range []string{
		"registries/a/ynh/1/" + sum + ".json/x", "registries/a/ynh/../" + sum + ".json", "registries/a/ynh/1/short.json",
		"registries/A/ynh/1/" + sum + ".json", "registries/a/ynh/1/" + sum + ".txt", "traces/a/ynh/1/" + sum + ".json",
	} {
		if _, err := ParseRegistry(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
