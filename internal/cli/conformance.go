package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/eyelock/ynr/internal/conformance"
)

// ExitConformance is the exit code when a conformance check fails.
const ExitConformance = 1

// conformanceCmd is ynr conformance (ADR-008): runs a tool's scenarios and checks the contract.
func conformanceCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flags("conformance", stderr)
	file := fs.String("file", ".ynr/conformance.yaml", "the tool's conformance file")
	format := fs.String("format", "text", "report as text or json")
	timeout := fs.Duration("timeout", 60*time.Second, "longest one run of a scenario may take")
	flush := fs.Duration("flush-limit", 2*time.Second, "the tool's bound on its flush at exit (rule 12)")
	keep := fs.Bool("keep", false, "keep the temporary folders, for looking at what was written")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *format != "text" && *format != "json" {
		_, _ = fmt.Fprintln(stderr, "ynr: --format must be text or json")
		return ExitUsage
	}
	rep, err := conformance.Run(ctx, conformance.Options{File: *file, Timeout: *timeout, FlushLimit: *flush, Keep: *keep})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitConfig
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		rep.WriteText(stdout)
	}
	if !rep.OK {
		return ExitConformance
	}
	return ExitOK
}
