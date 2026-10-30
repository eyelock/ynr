//go:build full

package query

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

func writeParquet(t *testing.T, kv string) string {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	out := filepath.Join(t.TempDir(), "f.parquet")
	if _, err := db.Exec("COPY (SELECT 1 AS a) TO " + literal(out) + " (FORMAT parquet" + kv + ")"); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSchemaVersionIsChecked: no version reads as version 1, a newer or unreadable one fails
// loudly (ADR-005).
func TestSchemaVersionIsChecked(t *testing.T) {
	ctx := context.Background()
	if err := checkSchema(ctx, writeParquet(t, "")); err != nil {
		t.Errorf("an unversioned file: %v", err)
	}
	if err := checkSchema(ctx, writeParquet(t, ", "+schemaKV)); err != nil {
		t.Errorf("the current version: %v", err)
	}
	for _, v := range []string{"2", "99", "x", "0"} {
		err := checkSchema(ctx, writeParquet(t, ", KV_METADATA {'"+SchemaKey+"': '"+v+"'}"))
		if err == nil {
			t.Errorf("version %q was accepted", v)
		}
	}
}

// TestCompactedPartsAreVersioned: compaction stamps the version, and a reader refuses a part from
// a newer ynr instead of misreading it.
func TestCompactedPartsAreVersioned(t *testing.T) {
	r := openStore(t)
	hour := now.Truncate(time.Hour).Add(-3 * time.Hour)
	ship(t, r, store.Traces, hour.Add(10*time.Minute), "local.a",
		traceLine(t, runSpan(traceA, "1111111111111111", "", "converged", "m", 1, hour.Add(5*time.Minute))))
	compact(t, r, hour.Add(time.Hour+11*time.Minute))
	var parquets []string
	for _, k := range keys(t, r, "") {
		if strings.HasSuffix(k, ".parquet") {
			parquets = append(parquets, k)
		}
	}
	if len(parquets) == 0 {
		t.Fatal("no Parquet written")
	}
	for _, k := range parquets {
		p, err := r.Local(context.Background(), k)
		if err != nil {
			t.Fatal(err)
		}
		db, _ := sql.Open("duckdb", "")
		var v string
		err = db.QueryRow("SELECT decode(value) FROM parquet_kv_metadata(" + literal(p) + ") WHERE decode(key) = " + literal(SchemaKey)).Scan(&v)
		_ = db.Close()
		if err != nil || v != "1" {
			t.Errorf("%s: version %q, %v", k, v, err)
		}
	}
	// A part rewritten by a newer ynr is refused.
	part := parquets[0]
	if !strings.Contains(part, "part-") {
		t.Skip("no part")
	}
	p, _ := r.Local(context.Background(), part)
	db, _ := sql.Open("duckdb", "")
	newer := filepath.Join(t.TempDir(), "n.parquet")
	if _, err := db.Exec("COPY (SELECT * FROM read_parquet(" + literal(p) + ")) TO " + literal(newer) + " (FORMAT parquet, KV_METADATA {'" + SchemaKey + "': '2'})"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := checkSchema(context.Background(), newer); err == nil {
		t.Error("a newer part was accepted")
	}
}
