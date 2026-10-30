package query

// Every Parquet file ynr writes (parts, the item index, rollups) carries its schema version in
// its metadata under SchemaKey (ADR-005). A reader treats a file with no version as version 1,
// and refuses a file with a version it does not know. Columns added by name, which readers
// tolerate by reading with union_by_name, do not change the version; one that changes the
// meaning or type of an existing column does.
const (
	SchemaKey     = "ynr.schema_version"
	SchemaVersion = 1
)
