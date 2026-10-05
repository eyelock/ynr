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
	// Logf reports what goes wrong; the hot tier never stops ynr serve shipping.
	Logf func(format string, args ...any)
}
