package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// TestStoreContractCentral covers what central needs: version tags and conditional replace for
// leases, an hour's collectors, and listing only the keys after a position.
func TestStoreContractCentral(t *testing.T) {
	for name, r := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			hour := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
			lease := "leases/compaction/traces/2026/10/06/09.json"

			if _, _, err := r.GetWithTag(ctx, lease); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a missing tag get = %v", err)
			}
			if err := r.ReplaceIf(ctx, lease, []byte("x"), "nope"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("replacing a missing object = %v", err)
			}
			if err := r.Put(ctx, lease, []byte("a")); err != nil {
				t.Fatal(err)
			}
			data, tag, err := r.GetWithTag(ctx, lease)
			if err != nil || string(data) != "a" || tag == "" {
				t.Fatalf("get with tag = %q %q %v", data, tag, err)
			}
			if err := r.ReplaceIf(ctx, lease, []byte("b"), tag); err != nil {
				t.Fatal(err)
			}
			if err := r.ReplaceIf(ctx, lease, []byte("c"), tag); !errors.Is(err, ErrChanged) {
				t.Fatalf("replacing with a stale tag = %v, want ErrChanged", err)
			}
			if b, _ := r.Get(ctx, lease); string(b) != "b" {
				t.Fatalf("after a refused replace = %q", b)
			}

			// Of many writers that read the same version, exactly one wins.
			_, tag, _ = r.GetWithTag(ctx, lease)
			var wins atomic.Int32
			var wg sync.WaitGroup
			for i := range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if r.ReplaceIf(ctx, lease, []byte(fmt.Sprintf("w%d", i)), tag) == nil {
						wins.Add(1)
					}
				}()
			}
			wg.Wait()
			if wins.Load() != 1 {
				t.Fatalf("%d concurrent replaces won, want 1", wins.Load())
			}

			// Of many puts of one key, exactly one wins.
			wins.Store(0)
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if r.Put(ctx, "leases/compaction/traces/2026/10/06/10.json", []byte("p")) == nil {
						wins.Add(1)
					}
				}()
			}
			wg.Wait()
			if wins.Load() != 1 {
				t.Fatalf("%d concurrent puts won, want 1", wins.Load())
			}

			put := func(collector, ship string) string {
				k := BatchKey(Traces, hour.Add(5*time.Minute), collector, ship, "src-1", 0, 1)
				if err := r.Put(ctx, k, []byte("x")); err != nil {
					t.Fatal(err)
				}
				return k
			}
			a1, a2, a3 := put("alpha", "01AAAAAAAAAAAAAAAAAAAAAAAA"), put("alpha", "01BBBBBBBBBBBBBBBBBBBBBBBB"), put("alpha", "01CCCCCCCCCCCCCCCCCCCCCCCC")
			put("beta", "01AAAAAAAAAAAAAAAAAAAAAAAA")
			put("beta", "01AAAAAAAAAAAAAAAAAAAAAAAB")
			// Another hour and another signal are not this hour's collectors.
			if err := r.Put(ctx, BatchKey(Traces, hour.Add(time.Hour), "gamma", "01AAAAAAAAAAAAAAAAAAAAAAAA", "s-1", 0, 1), []byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := r.Put(ctx, BatchKey(Logs, hour, "delta", "01AAAAAAAAAAAAAAAAAAAAAAAA", "s-1", 0, 1), []byte("x")); err != nil {
				t.Fatal(err)
			}
			cs, err := r.ListCollectors(ctx, Traces, hour)
			sort.Strings(cs)
			if err != nil || strings.Join(cs, " ") != "alpha beta" {
				t.Fatalf("collectors = %v %v", cs, err)
			}
			if cs, err := r.ListCollectors(ctx, Metrics, hour); err != nil || len(cs) != 0 {
				t.Fatalf("collectors of an empty hour = %v %v", cs, err)
			}
			prefix := "traces/2026/10/06/09/alpha/"
			after, err := r.ListAfter(ctx, prefix, a1)
			if err != nil || strings.Join(after, " ") != a2+" "+a3 {
				t.Fatalf("after the first = %v %v", after, err)
			}
			if after, err := r.ListAfter(ctx, prefix, a3); err != nil || len(after) != 0 {
				t.Fatalf("after the last = %v %v", after, err)
			}
			if all, err := r.ListAfter(ctx, prefix, ""); err != nil || len(all) != 3 {
				t.Fatalf("after nothing = %v %v", all, err)
			}
		})
	}
}
