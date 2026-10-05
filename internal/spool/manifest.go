package spool

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

// maxManifest bounds a run manifest's size.
const maxManifest = 64 << 10

// ReadManifest reads ynf's manifest for a run, from the manifests folder the run cannot reach
// (ADR-003), under the same rules as any spool file. It returns os.ErrNotExist when there is none.
func ReadManifest(root, runID string) ([]byte, error) {
	if !ValidName(runID) {
		return nil, fmt.Errorf("%w: run id %q", ErrRejected, runID)
	}
	dir := filepath.Join(root, ManifestsDir)
	owner, dev, err := dirOwnerAndDev(dir)
	if err != nil {
		return nil, err
	}
	f, _, size, err := openSafe(filepath.Join(dir, runID+".json"), []uint32{owner}, dev)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if size > maxManifest {
		return nil, fmt.Errorf("%w: manifest for %s is %d bytes", ErrRejected, runID, size)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxManifest))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errors.New("spool: empty manifest")
	}
	return b, nil
}
