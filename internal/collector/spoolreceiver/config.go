// Package spoolreceiver is the Collector receiver that reads ynr's spool (ADR-004): it stamps
// each record's origin and hands it on synchronously, so a line is committed only once the
// pipeline has accepted it.
package spoolreceiver

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/component"
)

// Type is the receiver's name in configuration.
var Type = component.MustNewType("ynrspool")

// Config configures the receiver.
type Config struct {
	// Root is the spool root.
	Root string `mapstructure:"root"`
	// PollInterval is how often the spool is read.
	PollInterval time.Duration `mapstructure:"poll_interval"`
	// MaxLine bounds a line's length in bytes.
	MaxLine int `mapstructure:"max_line"`
	// CollectorID is this collector's identity: a runner pool or a host (ADR-003).
	CollectorID string `mapstructure:"collector_id"`
	// CollectorInstance is the job within the pool, recorded as data.
	CollectorInstance string `mapstructure:"collector_instance"`
	// Store is the object store's URL (ADR-005). When set, the receiver ships each spool file's
	// stamped lines there as compressed batches and commits a file's position only once its batch
	// is stored; the pipeline after it becomes a best-effort copy.
	Store string `mapstructure:"store"`
	// ShipAge and ShipBytes say how often a file's new lines are shipped: once they are this old
	// or this large, whichever comes first.
	ShipAge   time.Duration `mapstructure:"ship_age"`
	ShipBytes int64         `mapstructure:"ship_bytes"`
}

func createDefaultConfig() component.Config {
	return &Config{PollInterval: time.Second, ShipAge: 15 * time.Second, ShipBytes: 16 << 20}
}

// Validate checks the configuration.
func (c *Config) Validate() error {
	switch {
	case c.Root == "":
		return errors.New("root is required")
	case c.CollectorID == "":
		return errors.New("collector_id is required")
	case c.PollInterval <= 0:
		return errors.New("poll_interval must be positive")
	}
	return nil
}
