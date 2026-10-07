package conformance

import (
	"fmt"
	"os"
	"sort"

	"github.com/eyelock/ynr/internal/registry"
)

// Registry is a tool's loaded registry with the lookups the checks need. The loading is
// internal/registry's, the same code `ynr serve` uses to check names at runtime.
type Registry struct {
	*registry.Registry
	own                    map[string]bool
	std                    map[string]bool
	Spans, Events, Metrics map[string]bool
	// limits is the ynr.cardinality limit a metric declares for each of its attributes.
	limits map[string]map[string]int
}

// LoadRegistry reads the registry in dir, a folder with a manifest.yaml and group files.
func LoadRegistry(dir string) (*Registry, error) {
	base, err := registry.Load(os.DirFS(dir), "")
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", dir, err)
	}
	if len(base.Attributes)+len(base.Spans)+len(base.Events)+len(base.Metrics) == 0 {
		return nil, fmt.Errorf("registry %s: no groups", dir)
	}
	r := &Registry{Registry: base, own: map[string]bool{}, std: map[string]bool{},
		Spans: map[string]bool{}, Events: map[string]bool{}, Metrics: map[string]bool{},
		limits: map[string]map[string]int{}}
	for _, a := range base.Attributes {
		r.own[a.ID] = true
	}
	for _, a := range base.Standard {
		r.std[a] = true
	}
	for _, s := range base.Spans {
		r.Spans[s.Name] = true
	}
	for _, e := range base.Events {
		r.Events[e.Name] = true
	}
	for _, m := range base.Metrics {
		r.Metrics[m.Name] = true
		for _, u := range m.Attributes {
			if u.Cardinality > 0 {
				if r.limits[m.Name] == nil {
					r.limits[m.Name] = map[string]int{}
				}
				r.limits[m.Name][u.Name] = u.Cardinality
			}
		}
	}
	return r, nil
}

// declaresAttr reports whether key is declared here or used by reference.
func (r *Registry) declaresAttr(key string) bool { return r.own[key] || r.std[key] }

// limit is the ynr.cardinality limit the registry gives a metric's attribute, zero for none.
func (r *Registry) limit(metric, attr string) int { return r.limits[metric][attr] }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
