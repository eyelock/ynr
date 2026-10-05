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
	exporters, err := otelcol.MakeFactoryMap[exporter.Factory](otlphttpexporter.NewFactory(), debugexporter.NewFactory())
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
	if s.Upstream == "" {
		return "", errors.New("an upstream OTLP endpoint is required until the object store arrives (ADR-009, slice 2)")
	}
	exporters := []string{"otlp_http"}
	exp := map[string]any{
		"otlp_http": map[string]any{
			"endpoint": s.Upstream,
			// Synchronous: a line is committed only once the upstream has accepted it (ADR-004).
			"sending_queue":    map[string]any{"enabled": false},
			"retry_on_failure": map[string]any{"enabled": true, "max_elapsed_time": "30s"},
		},
	}
	if s.Debug {
		exporters = append(exporters, "debug")
		exp["debug"] = map[string]any{"verbosity": "basic"}
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
