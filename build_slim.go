//go:build !full

package ynr

// Build is which of the two builds this binary is (ADR-001): slim has no cgo and no DuckDB.
const Build = "slim"
