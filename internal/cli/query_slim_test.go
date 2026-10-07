//go:build !full

package cli

import (
	"strings"
	"testing"

	"github.com/eyelock/ynr/internal/store"
)

func TestQueryNeedsTheFullBuild(t *testing.T) {
	code, _, errs := run("query", "runs", "--store", store.FolderURL(t.TempDir()))
	if code != ExitConfig || !strings.Contains(errs, "full build") {
		t.Fatalf("query in the slim build = %d %q", code, errs)
	}
}
