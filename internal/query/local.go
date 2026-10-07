package query

import (
	"context"

	"github.com/eyelock/ynr/internal/store"
)

// local returns paths on this machine for the keys, fetching them first from a bucket.
func local(ctx context.Context, r store.Reader, keys []string) ([]string, error) {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		p, err := r.Local(ctx, k)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
