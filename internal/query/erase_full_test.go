//go:build full

package query

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

func actorSpan(id, actor string, at time.Time) span {
	return span{trace: traceA, id: id, name: "ynh.run", start: at, dur: time.Minute,
		res: map[string]any{"service.name": "ynh"}, attrs: map[string]any{"user.name": actor, "ynh.run.outcome": "converged"}}
}

func actors(t *testing.T, r store.Reader) []string {
	t.Helper()
	q := &Query{Name: "actors", Since: 24 * time.Hour, Signals: []string{store.Traces},
		SQL: `SELECT actor || ' ' || attr(attributes, 'user.name') FROM spans ORDER BY 1`}
	res, err := Run(context.Background(), r, q, Params{Since: now.Add(-24 * time.Hour), Until: now})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, row := range res.Rows {
		out = append(out, row[0].(string))
	}
	return out
}

// rawActors reads every Parquet part under the store directly, with no masking, so it sees
// exactly what is stored.
func rawActors(t *testing.T, dir string) int64 {
	t.Helper()
	var parts []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && filepath.Ext(p) == ".parquet" && filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(p)))))) == "traces" {
			parts = append(parts, p)
		}
		return nil
	})
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int64
	if err := db.QueryRow(`SELECT count(*) FROM ` + parquet(parts) + ` WHERE actor = 'github.com/alice'
  OR len(list_filter(attributes, a -> a.v = 'github.com/alice')) > 0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestErasureMasksAtOnceThenRemoves: an erased handle reads as (erased) in every query the
// moment it is listed, and the next compaction rewrites its hour without it, deleting at once
// the part and batches that held it.
func TestErasureMasksAtOnceThenRemoves(t *testing.T) {
	defer SetErasure(nil)
	dir := t.TempDir()
	r, err := store.OpenReader(store.FolderURL(dir))
	if err != nil {
		t.Fatal(err)
	}
	hour := now.Truncate(time.Hour).Add(-3 * time.Hour)
	ship(t, r, store.Traces, hour.Add(10*time.Minute), "local.a", traceLine(t,
		actorSpan("1111111111111111", "github.com/alice", hour.Add(time.Minute)),
		actorSpan("2222222222222222", "github.com/bob", hour.Add(2*time.Minute))))
	compact(t, r, now)
	if raw := rawActors(t, dir); raw != 1 {
		t.Fatalf("before erasure the part holds alice %d times", raw)
	}

	SetErasure([]string{"github.com/alice"})
	if got := actors(t, r); len(got) != 2 || got[0] != "(erased) (erased)" || got[1] != "github.com/bob github.com/bob" {
		t.Fatalf("masked at once: %v", got)
	}
	compact(t, r, now.Add(time.Minute))
	if raw := rawActors(t, dir); raw != 0 {
		t.Fatalf("after erasure the store still holds alice %d times", raw)
	}
	if got := keys(t, r, store.CompactedPrefix(store.Traces, hour)); len(got) != 2 || got[1] != store.PartKey(store.Traces, hour, 2) {
		t.Fatalf("the superseded part was not deleted at once: %v", got)
	}
	if got := keys(t, r, HourPrefix(store.Traces, hour)); len(got) != 0 {
		t.Fatalf("the batches holding alice were not deleted: %v", got)
	}
	if got := actors(t, r); len(got) != 2 || got[0] != "(erased) (erased)" {
		t.Fatalf("after recompaction: %v", got)
	}
	if n := compact(t, r, now.Add(2*time.Minute)); n != 0 {
		t.Fatalf("an hour already erased was compacted again")
	}

	hot := openHot(t, filepath.Join(t.TempDir(), "hot.duckdb"), r)
	if err := hot.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	var held int64
	if err := hot.db.QueryRow(`SELECT count(*) FROM spans WHERE actor = 'github.com/alice'`).Scan(&held); err != nil || held != 0 {
		t.Fatalf("the hot tier holds alice %d times (%v)", held, err)
	}
}

func TestLoadErasure(t *testing.T) {
	p := filepath.Join(t.TempDir(), "erase")
	_ = os.WriteFile(p, []byte("# people who asked\ngithub.com/alice\n\n  example.atlassian.net/jdoe  \n"), 0o600)
	got, err := LoadErasure(p)
	if err != nil || len(got) != 2 || got[1] != "example.atlassian.net/jdoe" {
		t.Fatalf("%v %v", got, err)
	}
	_ = os.WriteFile(p, []byte("Alice Smith\n"), 0o600)
	if _, err := LoadErasure(p); err == nil {
		t.Fatal("accepted a name with a space, which is not a handle")
	}
}
