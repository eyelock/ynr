package store

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"
)

// Signals, as they appear in keys.
const (
	Traces  = "traces"
	Logs    = "logs"
	Metrics = "metrics"
)

// BatchKey is where a collector's batch goes (ADR-005): hour received first, so a reader polls
// one hour's prefix, then the collector, then a name that sorts in shipping order and ends with
// the spool file and byte range the lines came from, so a re-shipped batch is recognisably the
// same data.
//
//	<signal>/<yyyy>/<mm>/<dd>/<hh>/<collector id>/<ship ulid>_<source>-<from>-<to>.jsonl.gz
func BatchKey(signal string, received time.Time, collector, ship, source string, from, to int64) string {
	t := received.UTC()
	return fmt.Sprintf("%s/%04d/%02d/%02d/%02d/%s/%s_%s-%d-%d.jsonl.gz",
		signal, t.Year(), int(t.Month()), t.Day(), t.Hour(), collector, ship, source, from, to)
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewULID returns a ULID for t: 48 bits of milliseconds then 80 random bits, in Crockford
// base32, so ULIDs sort by time.
func NewULID(t time.Time) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(t.UnixMilli())<<16)
	_, _ = rand.Read(b[6:])
	// 128 bits as 26 base32 characters, the first holding only the top 3 bits.
	var out [26]byte
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | (hi&31)<<59
		hi >>= 5
	}
	return string(out[:])
}
