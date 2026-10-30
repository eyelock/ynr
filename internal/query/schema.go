package query

// Every Parquet file ynr writes (parts, the item index, rollups) carries its schema version in
// its metadata under SchemaKey (ADR-005). A reader treats a file with no version, or version 1,
// as the layout v0.2.2 and earlier wrote, without the collector_instance and received_at
// columns, and refuses a file with a version newer than SchemaVersion. Version 2 is the layout
// with those columns. Readers read files of different versions together, matching columns by
// name (union_by_name), so a store may hold both; a column an older file lacks reads as NULL.
// Adding a column by name does not change the version; changing the meaning or type of an
// existing column does.
const (
	SchemaKey     = "ynr.schema_version"
	SchemaVersion = 2
)
