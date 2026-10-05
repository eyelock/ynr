package spool

import "sync/atomic"

// Counters count what the reader skipped or did, so losses are visible rather than silent.
type Counters struct {
	// Ignored counts entries that are not writer folders or spool files.
	Ignored atomic.Int64
	// Rejected counts files refused by the hostile-input rules: links, other owners, other devices.
	Rejected atomic.Int64
	// Oversized counts lines longer than the reader's limit, skipped.
	Oversized atomic.Int64
	// Malformed counts lines the handler could not parse, skipped.
	Malformed atomic.Int64
	// Lines counts lines handed on successfully.
	Lines atomic.Int64
	// Deleted counts closed files removed after everything in them was committed.
	Deleted atomic.Int64
}

// Snapshot is a point-in-time copy of Counters.
type Snapshot struct {
	Ignored, Rejected, Oversized, Malformed, Lines, Deleted int64
}

// Snapshot copies the counters.
func (c *Counters) Snapshot() Snapshot {
	return Snapshot{
		Ignored: c.Ignored.Load(), Rejected: c.Rejected.Load(), Oversized: c.Oversized.Load(),
		Malformed: c.Malformed.Load(), Lines: c.Lines.Load(), Deleted: c.Deleted.Load(),
	}
}
