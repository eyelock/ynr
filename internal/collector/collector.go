// Package collector runs ynr's OpenTelemetry Collector: the spool receiver feeding an OTLP
// exporter, built from settings rather than a configuration file (ADR-001).
package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/yamlprovider"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/debugexporter"
	"go.opentelemetry.io/collector/exporter/nopexporter"
	"go.opentelemetry.io/collector/exporter/otlphttpexporter"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/service/telemetry/otelconftelemetry"

	"github.com/eyelock/ynr"
	"github.com/eyelock/ynr/internal/collector/spoolreceiver"
	"github.com/eyelock/ynr/internal/stamp"
)

// Settings are what ynr serve runs with.
type Settings struct {
	SpoolRoot    string
	PollInterval time.Duration
	MaxLine      int
	Identity     stamp.Identity
	// Store is the object store's URL, such as file:///home/me/.local/share/ynr/store. With a
	// store, it is what commits the spool, and the upstream is a best-effort copy (ADR-005).
	Store string
	// ShipAge and ShipBytes say how often each spool file's new lines are shipped to the store.
	ShipAge   time.Duration
	ShipBytes int64
	// SpoolCap caps the spool as a whole; zero takes the receiver's default.
	SpoolCap int64
	// RegistryTools are the tools whose telemetry registries ynr learns at startup, by bare name on
	// the PATH (ADR-007). Empty learns nothing.
	RegistryTools []string
	// Upstream is an OTLP/HTTP endpoint, such as http://localhost:4318.
	Upstream string
	// Debug also prints a summary of everything shipped to stderr.
	Debug bool
}

// Factories are the components ynr's Collector carries.
func Factories() (otelcol.Factories, error) {
	receivers, err := otelcol.MakeFactoryMap[receiver.Factory](spoolreceiver.NewFactory())
	if err != nil {
		return otelcol.Factories{}, err
	}
	exporters, err := otelcol.MakeFactoryMap[exporter.Factory](otlphttpexporter.NewFactory(), debugexporter.NewFactory(), nopexporter.NewFactory())
	if err != nil {
		return otelcol.Factories{}, err
	}
	return otelcol.Factories{
		Receivers: receivers,
		Exporters: exporters,
		Telemetry: otelconftelemetry.NewFactory(),
	}, nil
}

// Config builds the Collector's configuration. It is JSON, which is also YAML.
func Config(s Settings) (string, error) {
	if s.Upstream == "" && s.Store == "" {
		return "", errors.New("nowhere to ship: set a store, an upstream OTLP endpoint, or both")
	}
	exporters := []string{}
	exp := map[string]any{}
	if s.Upstream != "" {
		exporters = append(exporters, "otlp_http")
		// The Collector requires initial_interval <= max_interval <= max_elapsed_time, and its
		// defaults (5s, 30s) do not fit under a short bound, so all three are set.
		retry := map[string]any{"enabled": true, "initial_interval": "5s", "max_interval": "30s", "max_elapsed_time": "30s"}
		if s.Store != "" {
			// A best-effort copy: a short retry, so a slow upstream delays shipping little.
			retry = map[string]any{"enabled": true, "initial_interval": "1s", "max_interval": "2s", "max_elapsed_time": "5s"}
		}
		exp["otlp_http"] = map[string]any{
			"endpoint": s.Upstream,
			// Synchronous: without a store, a line is committed only once the upstream has
			// accepted it (ADR-004).
			"sending_queue":    map[string]any{"enabled": false},
			"retry_on_failure": retry,
		}
	}
	if s.Debug {
		exporters = append(exporters, "debug")
		exp["debug"] = map[string]any{"verbosity": "basic"}
	}
	if len(exporters) == 0 {
		// The store is written by the receiver itself; a pipeline still needs an exporter.
		exporters = append(exporters, "nop")
		exp["nop"] = map[string]any{}
	}
	pipe := map[string]any{"receivers": []string{spoolreceiver.Type.String()}, "exporters": exporters}
	cfg := map[string]any{
		"receivers": map[string]any{
			spoolreceiver.Type.String(): map[string]any{
				"root":               s.SpoolRoot,
				"poll_interval":      s.PollInterval.String(),
				"max_line":           s.MaxLine,
				"collector_id":       s.Identity.ID,
				"collector_instance": s.Identity.Instance,
				"store":              s.Store,
				"ship_age":           shipAge(s).String(),
				"ship_bytes":         shipBytes(s),
				"spool_cap":          spoolCap(s),
				"registry_tools":     registryTools(s),
			},
		},
		"exporters": exp,
		"service": map[string]any{
			"telemetry": map[string]any{"metrics": map[string]any{"level": "none"}},
			"pipelines": map[string]any{"traces": pipe, "logs": pipe, "metrics": pipe},
		},
	}
	b, err := json.Marshal(cfg)
	return string(b), err
}

// Run runs the Collector until ctx is done.
func Run(ctx context.Context, s Settings) error {
	cfg, err := Config(s)
	if err != nil {
		return err
	}
	col, err := otelcol.NewCollector(otelcol.CollectorSettings{
		Factories: Factories,
		BuildInfo: component.BuildInfo{Command: "ynr", Description: "ynr serve", Version: ynr.Version},
		// ynr handles signals itself, through ctx.
		DisableGracefulShutdown: true,
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				URIs:              []string{"yaml:" + cfg},
				ProviderFactories: []confmap.ProviderFactory{yamlprovider.NewFactory()},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("collector: %w", err)
	}
	stop := context.AfterFunc(ctx, col.Shutdown)
	defer stop()
	return col.Run(ctx)
}

func shipAge(s Settings) time.Duration {
	if s.ShipAge > 0 {
		return s.ShipAge
	}
	return 15 * time.Second
}

func shipBytes(s Settings) int64 {
	if s.ShipBytes > 0 {
		return s.ShipBytes
	}
	return 16 << 20
}

func spoolCap(s Settings) int64 {
	if s.SpoolCap > 0 {
		return s.SpoolCap
	}
	return spoolreceiver.DefaultSpoolCap
}

// registryTools is the list as a non-nil slice, so the configuration always has a list.
func registryTools(s Settings) []string {
	if s.RegistryTools == nil {
		return []string{}
	}
	return s.RegistryTools
}
