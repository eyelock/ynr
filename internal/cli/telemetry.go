package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/eyelock/ynr"
	"github.com/eyelock/ynr/internal/registry"
)

// telemetryCmd is `ynr telemetry registry`: the names ynr itself writes, printed as every YN tool
// prints its own, so one reader can learn them all (ADR-007). The registry is embedded in the
// binary; json is the shape `<tool> telemetry registry --format json` has for every tool.
func telemetryCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "registry" {
		_, _ = fmt.Fprintln(stderr, "ynr: telemetry: want `registry`")
		return ExitUsage
	}
	fl := flags("telemetry registry", stderr)
	format := fl.String("format", "text", "text or json")
	if err := fl.Parse(args[1:]); err != nil {
		return ExitUsage
	}
	if *format != "text" && *format != "json" {
		_, _ = fmt.Fprintln(stderr, "ynr: --format must be text or json")
		return ExitUsage
	}
	sub, err := fs.Sub(ynr.TelemetryRegistry, "telemetry/registry")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitAdapter
	}
	r, err := registry.Load(sub, ynr.Version)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ynr: %v\n", err)
		return ExitAdapter
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
		return ExitOK
	}
	_, _ = fmt.Fprint(stdout, registryText(r))
	return ExitOK
}

func registryText(r *registry.Registry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s, following %s %s\n\n", r.Tool, r.Version, r.Semconv.Name, r.Semconv.Version)
	b.WriteString("attributes\n")
	for _, at := range r.Attributes {
		fmt.Fprintf(&b, "  %s (%s)\n", at.ID, at.Type)
	}
	b.WriteString("spans\n")
	for _, s := range r.Spans {
		fmt.Fprintf(&b, "  %s\n", s.Name)
	}
	b.WriteString("events\n")
	for _, e := range r.Events {
		fmt.Fprintf(&b, "  %s\n", e.Name)
	}
	b.WriteString("metrics\n")
	for _, m := range r.Metrics {
		fmt.Fprintf(&b, "  %s (%s, %s)\n", m.Name, m.Instrument, m.Unit)
	}
	return b.String()
}
