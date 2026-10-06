package conformance

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Registry is the names a tool's Weaver registry declares, read from its YAML and nothing
// more. It is deliberately small: it knows the groups format ynf and ynr use (attribute groups,
// spans, events and metrics), not Weaver's resolution of references into semantic conventions.
// TODO: replace it with ynr's registry loader (internal/registry) once that is on develop.
type Registry struct {
	// Attributes are the attributes the registry declares itself, with their ynr.cardinality
	// limit, zero for none.
	Attributes map[string]int
	// Refs are attributes it uses from elsewhere, such as semantic conventions.
	Refs                   map[string]bool
	Spans, Events, Metrics map[string]bool
}

type groupFile struct {
	Groups []struct {
		ID         string `yaml:"id"`
		Type       string `yaml:"type"`
		Name       string `yaml:"name"`
		MetricName string `yaml:"metric_name"`
		Attributes []struct {
			ID          string         `yaml:"id"`
			Ref         string         `yaml:"ref"`
			Annotations map[string]any `yaml:"annotations"`
		} `yaml:"attributes"`
	} `yaml:"groups"`
}

// LoadRegistry reads every YAML file under dir except the manifest.
func LoadRegistry(dir string) (*Registry, error) {
	r := &Registry{Attributes: map[string]int{}, Refs: map[string]bool{},
		Spans: map[string]bool{}, Events: map[string]bool{}, Metrics: map[string]bool{}}
	found := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		ext := filepath.Ext(p)
		if d.IsDir() || (ext != ".yaml" && ext != ".yml") || strings.HasPrefix(d.Name(), "manifest") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var gf groupFile
		if err := yaml.Unmarshal(b, &gf); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		found = true
		for _, g := range gf.Groups {
			switch g.Type {
			case "span":
				r.Spans[strings.TrimPrefix(g.ID, "span.")] = true
			case "event":
				n := g.Name
				if n == "" {
					n = strings.TrimPrefix(g.ID, "event.")
				}
				r.Events[n] = true
			case "metric":
				n := g.MetricName
				if n == "" {
					n = strings.TrimPrefix(g.ID, "metric.")
				}
				r.Metrics[n] = true
			}
			for _, a := range g.Attributes {
				switch {
				case a.ID != "":
					limit := 0
					switch v := a.Annotations["ynr.cardinality"].(type) {
					case int:
						limit = v
					case float64:
						limit = int(v)
					}
					r.Attributes[a.ID] = limit
				case a.Ref != "":
					r.Refs[a.Ref] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", dir, err)
	}
	if !found {
		return nil, fmt.Errorf("registry %s: no YAML files with groups", dir)
	}
	// A reference to an attribute the registry declares itself is not a use of another
	// registry's.
	for id := range r.Attributes {
		delete(r.Refs, id)
	}
	return r, nil
}

// declaresAttr reports whether key is declared here or used by reference.
func (r *Registry) declaresAttr(key string) bool {
	_, own := r.Attributes[key]
	return own || r.Refs[key]
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
