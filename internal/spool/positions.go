package spool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// positions records how far each spool file has been committed, by file identity, in the
// spool's state folder, which only ynr writes.
type positions struct {
	path  string
	Files map[string]int64 `json:"files"`
}

func loadPositions(root string) (*positions, error) {
	p := &positions{path: filepath.Join(root, StateDir, "positions.json"), Files: map[string]int64{}}
	b, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, p); err != nil {
		// A damaged state file means re-reading from the start: shipping is at least once.
		p.Files = map[string]int64{}
	}
	if p.Files == nil {
		p.Files = map[string]int64{}
	}
	return p, nil
}

// save writes the positions atomically: a temporary file renamed over the old one.
func (p *positions) save() error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

// prune forgets files that no longer exist.
func (p *positions) prune(seen map[string]bool) {
	for k := range p.Files {
		if !seen[k] {
			delete(p.Files, k)
		}
	}
}
