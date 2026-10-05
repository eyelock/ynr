//go:build !full

package query

import (
	"context"

	"github.com/eyelock/ynr/internal/store"
)

// Run is not available in the slim build, which has no DuckDB (ADR-001).
func Run(context.Context, store.Reader, *Query, Params) (*Result, error) {
	return nil, ErrSlim
}
