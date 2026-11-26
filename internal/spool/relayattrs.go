package spool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// RelayAttrsName is the file a standing ynr relay keeps beside its lock, naming the vendor
// attributes it received that are not on the pinned list (ADR-006 rule 10), for ynr doctor. It
// holds names and counts only, never values. The reader skips it, like the lock.
const RelayAttrsName = ".ynr-relay-attrs.json"

// RelayAttrs is the content of RelayAttrsName.
type RelayAttrs struct {
	// Vendor and Version name the list the relay checked against, such as claude-code 2.1.316.
	Vendor  string `json:"vendor"`
	Version string `json:"version"`
	// Since is when the relay started: the counts cover its run only.
	Since time.Time `json:"since"`
	// Unknown counts, by attribute name, what was not on the list.
	Unknown map[string]int64 `json:"unknown"`
}

// WriteRelayAttrs replaces the file in dir, atomically, mode 0600.
func WriteRelayAttrs(dir string, a RelayAttrs) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, RelayAttrsName+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, RelayAttrsName)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ReadRelayAttrs reads the file in dir; it returns nil when there is none.
func ReadRelayAttrs(dir string) (*RelayAttrs, error) {
	b, err := os.ReadFile(filepath.Join(dir, RelayAttrsName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a RelayAttrs
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	return &a, nil
}
