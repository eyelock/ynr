package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
)

// stores are the adapters the contract runs against: the folder always, and S3 when
// YNR_TEST_S3 names a bucket, such as
// s3://ynr-test?region=us-east-1&endpoint=http://127.0.0.1:9000&path_style=true for a local
// MinIO, with its credentials in AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY.
func stores(t *testing.T) map[string]Reader {
	t.Helper()
	out := map[string]Reader{}
	f, err := OpenReader(FolderURL(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	out["folder"] = f
	raw := os.Getenv("YNR_TEST_S3")
	if raw == "" {
		return out
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	u.Path = "/" + strings.Trim(u.Path, "/") + "/" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")) + "-" + hex.EncodeToString(b[:])
	q := u.Query()
	q.Set("cache", t.TempDir())
	u.RawQuery = q.Encode()
	s, err := OpenReader(u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.(*S3).EnsureBucket(context.Background()); err != nil {
		t.Fatalf("creating the test bucket: %v", err)
	}
	out["s3"] = s
	return out
}

func TestStoreContract(t *testing.T) {
	for name, r := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			key := "traces/2026/10/06/09/laptop/a.jsonl.gz"
			if err := r.Put(ctx, key, []byte("one")); err != nil {
				t.Fatal(err)
			}
			if err := r.Put(ctx, key, []byte("two")); !errors.Is(err, os.ErrExist) {
				t.Fatalf("a second put = %v, want ErrExist", err)
			}
			if b, err := r.Get(ctx, key); err != nil || string(b) != "one" {
				t.Fatalf("get = %q %v", b, err)
			}
			if _, err := r.Get(ctx, "traces/none"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a missing get = %v", err)
			}
			if err := r.Put(ctx, "traces/2026/10/06/08/laptop/b.jsonl.gz", []byte("bb")); err != nil {
				t.Fatal(err)
			}
			if err := r.Put(ctx, "logs/2026/10/06/08/laptop/c.jsonl.gz", []byte("ccc")); err != nil {
				t.Fatal(err)
			}
			keys, err := r.List(ctx, "traces/")
			if err != nil || strings.Join(keys, " ") != "traces/2026/10/06/08/laptop/b.jsonl.gz "+key {
				t.Fatalf("list = %v %v", keys, err)
			}
			if ks, err := r.List(ctx, "metrics/"); err != nil || len(ks) != 0 {
				t.Fatalf("an empty prefix = %v %v", ks, err)
			}
			if _, err := r.List(ctx, "../"); err == nil {
				t.Fatal("listed an invalid prefix")
			}
			sizes, err := r.Sizes(ctx, "")
			if err != nil || len(sizes) != 3 || sizes["logs/2026/10/06/08/laptop/c.jsonl.gz"] != 3 {
				t.Fatalf("sizes = %v %v", sizes, err)
			}
			p, err := r.Local(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(p); string(b) != "one" {
				t.Fatalf("local copy = %q", b)
			}
			idx := "index/items/2026/10/06.parquet"
			if err := r.Replace(ctx, idx, []byte("v1")); err != nil {
				t.Fatal(err)
			}
			if p, err := r.Local(ctx, idx); err != nil || readFile(p) != "v1" {
				t.Fatalf("local index = %v", err)
			}
			if err := r.Replace(ctx, idx, []byte("v2")); err != nil {
				t.Fatal(err)
			}
			if p, err := r.Local(ctx, idx); err != nil || readFile(p) != "v2" {
				t.Fatalf("a replaced index was not fetched again: %v", err)
			}
			if err := r.Delete(ctx, key); err != nil {
				t.Fatal(err)
			}
			if err := r.Delete(ctx, key); err != nil {
				t.Fatalf("deleting twice = %v", err)
			}
			if _, err := r.Get(ctx, key); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("after delete = %v", err)
			}
		})
	}
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}
