// Package stamp applies what ynr knows about a record's origin, overwriting anything the sender
// claimed: provenance from the spool folder, the collector's identity from its configuration, and
// a run's factory from ynf's manifest (ADR-003).
package stamp

import (
	"encoding/json"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"

	"github.com/eyelock/ynr/internal/names"
	"github.com/eyelock/ynr/internal/spool"
)

// Attributes ynr stamps. ynr.* are ynr's own (ADR-007); the ynf.* names belong to ynf, and ynr
// applies them on ynf's behalf from its run manifest.
const (
	Provenance        = names.AttrProvenance
	ProvenanceWarning = names.AttrProvenanceWarning
	CollectorID       = names.AttrCollectorID
	CollectorInstance = names.AttrCollectorInstance

	Lane        = "ynf.lane"
	LaneHarness = "ynf.lane.harness"
	LaneFocus   = "ynf.lane.focus"
	ItemKey     = "ynf.item.key"
	StepID      = "ynf.step.id"
	RunID       = "ynf.run.id"
)

// factoryKeys are the names only a manifest may set on a run's records.
var factoryKeys = []string{Lane, LaneHarness, LaneFocus, ItemKey, StepID, RunID}

// Identity is the collector's identity: a runner pool or a host, and optionally the job within
// it, which is data rather than identity (ADR-003).
type Identity struct {
	ID       string
	Instance string
}

// Manifest is what ynf writes for each run, out of the run's reach (ADR-003).
type Manifest struct {
	Run     string `json:"run"`
	Lane    string `json:"lane"`
	Harness string `json:"harness"`
	Focus   string `json:"focus"`
	Item    string `json:"item"`
	Step    string `json:"step"`
	// UID is the user the run writes as, when that is not the run folder's owner: a run in an
	// image writes as the image's user. ynr then accepts the run's files from that user too.
	UID *uint32 `json:"uid,omitempty"`
}

// ParseManifest parses a manifest and checks it names the run whose folder it is for.
func ParseManifest(b []byte, runID string) (*Manifest, bool) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil || m.Run != runID || m.Lane == "" {
		return nil, false
	}
	return &m, true
}

// Warnings recorded in ynr.provenance.warning.
const (
	NoManifest  = names.ProvenanceWarningNoManifest
	BadManifest = names.ProvenanceWarningBadManifest
)

// Resource stamps one resource's attributes. Every ynr.* attribute the sender set is removed
// first. For a run, the factory attributes are removed too and set only from the manifest; m is
// nil when there is none, and warning says why.
func Resource(attrs pcommon.Map, w spool.Writer, id Identity, m *Manifest, warning string) {
	attrs.RemoveIf(func(k string, _ pcommon.Value) bool { return strings.HasPrefix(k, "ynr.") })
	attrs.PutStr(Provenance, string(w.Class))
	attrs.PutStr(CollectorID, id.ID)
	if id.Instance != "" {
		attrs.PutStr(CollectorInstance, id.Instance)
	}
	if w.Class != spool.Run {
		return
	}
	for _, k := range factoryKeys {
		attrs.Remove(k)
	}
	if m == nil {
		if warning != "" {
			attrs.PutStr(ProvenanceWarning, warning)
		}
		return
	}
	attrs.PutStr(RunID, m.Run)
	put := func(k, v string) {
		if v != "" {
			attrs.PutStr(k, v)
		}
	}
	put(Lane, m.Lane)
	put(LaneHarness, m.Harness)
	put(LaneFocus, m.Focus)
	put(ItemKey, m.Item)
	put(StepID, m.Step)
}
