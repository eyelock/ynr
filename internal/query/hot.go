package query

import (
	"net"
	"net/http"
	"sync"
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
	// CompactEvery is how often Compact runs; a minute when zero.
	CompactEvery time.Duration
	// Feed fills the hot tier from the polling change feed rather than by re-listing the recent
	// hours whole: what ynr central does, since its store has many collectors.
	Feed bool
	// UI, when set, serves the dashboard UIHandler builds over the hot tier (--ui).
	UI        net.Listener
	UIHandler func(Runner) http.Handler
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
	// Retention is how far back an erasure sweep reaches: when the erasure list changes, every
	// hour from this far back that holds an erased handle is compacted again without it, though
	// older than Lookback. Lookback when zero or shorter.
	Retention time.Duration
	// Swept, when set, remembers which erasure list the store was last swept for, so the sweep
	// runs once per change. Nil sweeps on every compaction while the list is not empty.
	Swept *SweepMark
	// Holder, when set, makes each hour's compaction take a lease in the store first, under this
	// identity, so two central instances never compact an hour at once (ADR-005). Empty for a
	// laptop's ynr serve, the only reader of its folder.
	Holder string
	// LeaseTTL is how long a lease lasts unrenewed; DefaultLeaseTTL when zero.
	LeaseTTL time.Duration
}

// DefaultCompaction is ADR-005's defaults, looking back as far as a laptop's hot tier holds.
var DefaultCompaction = Compaction{Grace: 10 * time.Minute, Keep: time.Hour, Lookback: 7 * 24 * time.Hour}

// DefaultErasureReach is how far back an erasure sweep reaches by default: an S3 bucket's lifecycle
// rules keep records for 30 days.
const DefaultErasureReach = 30 * 24 * time.Hour

// SweepMark remembers the erasure list a store was last swept for.
type SweepMark struct {
	mu  sync.Mutex
	sum string
}

// Is reports whether the store was last swept for this erasure list.
func (m *SweepMark) Is(sum string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sum == sum
}

// Set records that the store was swept for this erasure list.
func (m *SweepMark) Set(sum string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sum = sum
}
