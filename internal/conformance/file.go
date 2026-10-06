// Package conformance is ynr conformance (ADR-008): the one shared check every YN* tool runs in
// CI. It runs a tool's scenarios against a temporary spool and a local test endpoint, reads what
// was written, and checks the mechanical rules of the instrumentation contract (ADR-006). It is
// strict where ynr's runtime is lenient: a failure blocks a merge and never affects production.
package conformance

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// File is a tool's conformance file, .ynr/conformance.yaml in its repository (ADR-008).
type File struct {
	// Service is the tool's service.name, and its prefix: ynh records are named ynh.*.
	Service string `yaml:"service"`
	// Registry is the path to the tool's own Weaver registry (ADR-007), relative to the folder
	// ynr conformance runs in.
	Registry string `yaml:"registry"`
	// Vendor is the stub vendor's command name, found by bare name on the PATH.
	Vendor string `yaml:"vendor"`
	// VendorAliases are further names the stub vendor answers to in the scenarios' PATH, such as
	// claude, for a tool that starts the vendor CLI by its own name.
	VendorAliases []string `yaml:"vendor_aliases"`
	// Units names the spans that are units of work, for rules 5 and 11. When empty, a unit of
	// work is a span of the tool that is a direct child of the TRACEPARENT the check gives it,
	// or the span of any started event.
	Units     []string   `yaml:"units"`
	Scenarios []Scenario `yaml:"scenarios"`
}

// Scenario is one command to run and what it should end in.
type Scenario struct {
	Name string `yaml:"name"`
	// Run is run through sh -c in a temporary folder. @canary:<kind> becomes a unique random
	// string per run, for kind prompt, ticket, file, memory or secret (rule 10).
	Run    string `yaml:"run"`
	Expect Expect `yaml:"expect"`
}

// Expect is what a scenario's unit of work should record.
type Expect struct {
	// Outcome is the outcome the unit-of-work span ends with (rule 11).
	Outcome string `yaml:"outcome"`
}

// CanaryKinds are the kinds of planted content a scenario may name (ADR-008 rule 10).
var CanaryKinds = []string{"prompt", "ticket", "file", "memory", "secret"}

// LoadFile reads and validates a conformance file.
func LoadFile(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("conformance file: %w", err)
	}
	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("conformance file %s: %w", path, err)
	}
	if f.Service == "" {
		return nil, fmt.Errorf("conformance file %s: service is required", path)
	}
	if len(f.Scenarios) == 0 {
		return nil, fmt.Errorf("conformance file %s: no scenarios", path)
	}
	for i, s := range f.Scenarios {
		if s.Name == "" || strings.TrimSpace(s.Run) == "" {
			return nil, fmt.Errorf("conformance file %s: scenario %d needs a name and a run command", path, i+1)
		}
		for _, kind := range canaryRefs(s.Run) {
			if !validKind(kind) {
				return nil, fmt.Errorf("conformance file %s: scenario %q: unknown canary kind %q (want one of %s)",
					path, s.Name, kind, strings.Join(CanaryKinds, ", "))
			}
		}
	}
	return &f, nil
}

func validKind(k string) bool {
	for _, c := range CanaryKinds {
		if c == k {
			return true
		}
	}
	return false
}
