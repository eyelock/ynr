//go:build full

package query

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
)

// schemaKV is the metadata clause every Parquet write carries.
var schemaKV = fmt.Sprintf("KV_METADATA {'%s': '%d'}", SchemaKey, SchemaVersion)

var checked sync.Map // paths already found readable

// checkSchema fails when a Parquet file's schema version is newer than this build reads, or is
// not a number. A file with no version is version 1 (ADR-005).
func checkSchema(ctx context.Context, path string) error {
	if _, ok := checked.Load(path); ok {
		return nil
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var v sql.NullString
	err = db.QueryRowContext(ctx, "SELECT max(decode(value)) FROM parquet_kv_metadata("+literal(path)+") WHERE decode(key) = "+literal(SchemaKey)).Scan(&v)
	if err != nil {
		return fmt.Errorf("reading the schema version: %w", err)
	}
	if v.Valid {
		n, err := strconv.Atoi(v.String)
		if err != nil || n < 1 {
			return fmt.Errorf("unreadable schema version %q: written by a ynr this one does not understand", v.String)
		}
		if n > SchemaVersion {
			return fmt.Errorf("schema version %d is newer than this ynr reads (%d): upgrade ynr", n, SchemaVersion)
		}
	}
	checked.Store(path, true)
	return nil
}
