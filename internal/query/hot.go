package query

import (
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// HotConfig is how ynr serve runs its hot tier.
type HotConfig struct {
	// Path is the hot tier's database file; Socket the Unix socket the queries are answered on.
	Path, Socket string
	Store        store.Reader
	Window       time.Duration
	Poll         time.Duration
	// Compact, when set, is how this server compacts its store: a laptop's ynr serve compacts
	// its own folder (ADR-005).
	Compact *Compaction
	// Logf reports what goes wrong; the hot tier never stops ynr serve shipping.
	Logf func(format string, args ...any)
}

// Compaction is how a reader compacts its store (ADR-005).
type Compaction struct {
	// Grace is how long after an hour closes its first compaction waits for late uploads.
	Grace time.Duration
	// Keep is how long batches a manifest covers, and parts and manifests a newer manifest
	// supersedes, stay in the store, so a reader already holding them can finish.
	Keep time.Duration
	// Lookback is how far back hours are compacted.
	Lookback time.Duration
}

// DefaultCompaction is ADR-005's defaults, looking back as far as a laptop's hot tier holds.
var DefaultCompaction = Compaction{Grace: 10 * time.Minute, Keep: time.Hour, Lookback: 7 * 24 * time.Hour}
