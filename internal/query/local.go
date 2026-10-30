package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/eyelock/ynr/internal/store"
)

// local returns paths on this machine for the keys, fetching them first from a bucket.
func local(ctx context.Context, r store.Reader, keys []string) ([]string, error) {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		p, err := localFile(ctx, r, k)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// localFile returns a path on this machine for one key. A Parquet file is checked for a schema
// version this build can read (ADR-005) before anything reads it.
func localFile(ctx context.Context, r store.Reader, key string) (string, error) {
	p, err := r.Local(ctx, key)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(key, ".parquet") {
		if err := checkSchema(ctx, p); err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
	}
	return p, nil
}
