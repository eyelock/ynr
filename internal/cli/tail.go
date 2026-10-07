package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/eyelock/ynr/internal/tail"
)

// tailCmd follows the spool live (ADR-001), in both builds.
func tailCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flags("tail", stderr)
	root := fs.String("spool", defaultRoot(), "spool root (YNR_SPOOL_ROOT)")
	service := fs.String("service", "", "only this service's records, such as ynh")
	item := fs.String("item", "", "only this work item's records, by its key")
	fromStart := fs.Bool("from-start", false, "begin with what the spool already holds, not only what comes next")
	poll := fs.Duration("poll", 500*time.Millisecond, "how often to look for new lines")
	format := fs.String("format", "text", "text or json (one object per line)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	switch {
	case fs.NArg() > 0:
		_, _ = fmt.Fprintln(stderr, "ynr: tail takes no arguments")
		return ExitUsage
	case *format != "text" && *format != "json":
		_, _ = fmt.Fprintln(stderr, "ynr: --format must be text or json")
		return ExitUsage
	case *poll <= 0:
		_, _ = fmt.Fprintln(stderr, "ynr: --poll must be positive")
		return ExitUsage
	case *root == "":
		_, _ = fmt.Fprintln(stderr, "ynr: no spool root: set --spool or YNR_SPOOL_ROOT")
		return ExitConfig
	}
	err := tail.Follow(ctx, tail.Options{Root: *root, FromStart: *fromStart, Poll: *poll,
		Filter: tail.Filter{Service: *service, Item: *item}, JSON: *format == "json"}, stdout)
	if err != nil && ctx.Err() == nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitAdapter
	}
	return ExitOK
}
