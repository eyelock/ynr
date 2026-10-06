package store

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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

// Batch is what a batch key says about its batch.
type Batch struct {
	Signal    string
	Hour      time.Time
	Collector string
	Source    string
}

var (
	batchPattern    = regexp.MustCompile(`^(traces|logs|metrics)/(\d{4})/(\d{2})/(\d{2})/(\d{2})/([a-z0-9][a-z0-9._-]{0,62})/[0-9A-HJKMNP-TV-Z]{26}_([A-Za-z0-9._~-]+-\d+-\d+)\.jsonl\.gz$`)
	compactPattern  = regexp.MustCompile(`^compacted/(traces|logs|metrics)/(\d{4})/(\d{2})/(\d{2})/(\d{2})/(part-|_manifest-)([1-9]\d{0,8})\.(parquet|json)$`)
	errNotThisShape = errors.New("not a key of this shape")
)

func hourOf(y, m, d, h string) (time.Time, error) {
	return time.Parse("2006/01/02/15", y+"/"+m+"/"+d+"/"+h)
}

// ParseBatch reads a batch key, accepting only the exact shape BatchKey writes (ADR-005): fixed
// depth, a real date and hour, a well-formed collector id. Anything else is not a batch.
func ParseBatch(key string) (Batch, error) {
	m := batchPattern.FindStringSubmatch(key)
	if m == nil {
		return Batch{}, errNotThisShape
	}
	h, err := hourOf(m[2], m[3], m[4], m[5])
	if err != nil {
		return Batch{}, err
	}
	return Batch{Signal: m[1], Hour: h, Collector: m[6], Source: m[7]}, nil
}

// CompactedPrefix is where an hour's compacted parts and manifests are.
func CompactedPrefix(signal string, hour time.Time) string {
	h := hour.UTC()
	return fmt.Sprintf("compacted/%s/%04d/%02d/%02d/%02d/", signal, h.Year(), int(h.Month()), h.Day(), h.Hour())
}

// PartKey and ManifestKey name an hour's n'th compaction: its Parquet part, and the manifest
// written after it that says which batches the part covers.
func PartKey(signal string, hour time.Time, n int) string {
	return fmt.Sprintf("%spart-%d.parquet", CompactedPrefix(signal, hour), n)
}

// ManifestKey names the manifest of an hour's n'th compaction; see PartKey.
func ManifestKey(signal string, hour time.Time, n int) string {
	return fmt.Sprintf("%s_manifest-%d.json", CompactedPrefix(signal, hour), n)
}

// ParseCompacted reads a part or manifest key of exactly the shape PartKey and ManifestKey
// write, returning its number and whether it is a manifest.
func ParseCompacted(key string) (n int, manifest bool, err error) {
	m := compactPattern.FindStringSubmatch(key)
	if m == nil || (m[6] == "part-") != (m[8] == "parquet") {
		return 0, false, errNotThisShape
	}
	if _, err := hourOf(m[2], m[3], m[4], m[5]); err != nil {
		return 0, false, err
	}
	n, err = strconv.Atoi(m[7])
	return n, m[6] == "_manifest-", err
}

// IndexKey is a day's item index (ADR-005): which items appear in which of the day's hours.
func IndexKey(day time.Time) string {
	d := day.UTC()
	return fmt.Sprintf("index/items/%04d/%02d/%02d.parquet", d.Year(), int(d.Month()), d.Day())
}

// Rollup kinds and periods (ADR-005): run totals and metric aggregates, by day and by month.
const (
	RollupRuns    = "runs"
	RollupMetrics = "metrics"
	Daily         = "daily"
	Monthly       = "monthly"
)

// RollupKey is where a kind's rollup for a day or a month is: rollups/<kind>/daily/<yyyy-mm-dd>
// or rollups/<kind>/monthly/<yyyy-mm>.
func RollupKey(kind, period string, t time.Time) string {
	t = t.UTC()
	if period == Monthly {
		return fmt.Sprintf("rollups/%s/monthly/%04d-%02d.parquet", kind, t.Year(), int(t.Month()))
	}
	return fmt.Sprintf("rollups/%s/daily/%04d-%02d-%02d.parquet", kind, t.Year(), int(t.Month()), t.Day())
}

var (
	indexPattern  = regexp.MustCompile(`^index/items/(\d{4})/(\d{2})/(\d{2})\.parquet$`)
	rollupPattern = regexp.MustCompile(`^rollups/(runs|metrics)/(daily/(\d{4})-(\d{2})-(\d{2})|monthly/(\d{4})-(\d{2}))\.parquet$`)
)

// Span is the time a key's records were received in, from its start to its end, for retention:
// an hour for batches and compacted parts, a day for the item index and daily rollups, a month
// for monthly rollups. ok is false for keys retention never touches, such as registries.
func Span(key string) (from, to time.Time, rollup, ok bool) {
	if b, err := ParseBatch(key); err == nil {
		return b.Hour, b.Hour.Add(time.Hour), false, true
	}
	if m := compactPattern.FindStringSubmatch(key); m != nil {
		if h, err := hourOf(m[2], m[3], m[4], m[5]); err == nil {
			return h, h.Add(time.Hour), false, true
		}
	}
	if m := indexPattern.FindStringSubmatch(key); m != nil {
		if d, err := time.Parse("2006/01/02", m[1]+"/"+m[2]+"/"+m[3]); err == nil {
			return d, d.Add(24 * time.Hour), false, true
		}
	}
	if m := rollupPattern.FindStringSubmatch(key); m != nil {
		if m[3] != "" {
			if d, err := time.Parse("2006-01-02", m[3]+"-"+m[4]+"-"+m[5]); err == nil {
				return d, d.Add(24 * time.Hour), true, true
			}
		} else if d, err := time.Parse("2006-01", m[6]+"-"+m[7]); err == nil {
			return d, d.AddDate(0, 1, 0), true, true
		}
	}
	return time.Time{}, time.Time{}, false, false
}

var registryPattern = regexp.MustCompile(`^registries/([a-z0-9][a-z0-9._-]{0,62})/([a-z0-9][a-z0-9_-]{0,31})/([A-Za-z0-9][A-Za-z0-9._~-]{0,63})/([0-9a-f]{64})\.json$`)

// RegistryKey is where a collector keeps a registry it learned from a tool (ADR-007), named by
// the tool, its version and the SHA-256 of the registry, so two different registries for one
// tool and version sit side by side:
//
//	registries/<collector id>/<tool>/<version>/<sha256>.json
func RegistryKey(collector, tool, version, sha256 string) string {
	return fmt.Sprintf("registries/%s/%s/%s/%s.json", collector, tool, version, sha256)
}

// Registry is what a registry key says about its object.
type Registry struct {
	Collector, Tool, Version, SHA256 string
}

// ParseRegistry reads a registry key, accepting only the exact shape RegistryKey writes.
func ParseRegistry(key string) (Registry, error) {
	m := registryPattern.FindStringSubmatch(key)
	if m == nil {
		return Registry{}, errNotThisShape
	}
	return Registry{Collector: m[1], Tool: m[2], Version: m[3], SHA256: m[4]}, nil
}
