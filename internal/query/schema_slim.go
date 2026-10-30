//go:build !full

package query

import "context"

// checkSchema does nothing in the slim build, which reads no Parquet (ADR-001).
func checkSchema(context.Context, string) error { return nil }
