//go:build !full

package query

import "context"

// ServeHot is not available in the slim build, which has no DuckDB (ADR-001).
func ServeHot(context.Context, HotConfig) error { return ErrSlim }
