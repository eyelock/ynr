//go:build unix

package spool

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Evict keeps the spool within maxBytes (ADR-004). When the store has been unreachable long
// enough for the spool to reach its cap, it deletes the oldest closed files first, and counts
// each file and its bytes in Evicted and EvictedBytes, so an outage can never fill the disk and
// the loss is never silent. Open files are never evicted, since a writer is appending to them;
// each writer caps its own. Only regular files with one link are counted or deleted, so a link
// planted in a writer folder can never make ynr delete what it points to.
func (r *Reader) Evict(maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	writers, err := Writers(r.Root, r.Counters)
	if err != nil {
		return err
	}
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var closed []file
	var total int64
	for _, w := range writers {
		entries, err := os.ReadDir(w.Dir)
		if err != nil {
			continue // gone, or not readable: the reader counts what it refuses
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ClosedSuffix) {
				continue
			}
			p := filepath.Join(w.Dir, e.Name())
			fi, err := os.Lstat(p)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
				continue
			}
			total += fi.Size()
			if !strings.HasSuffix(e.Name(), OpenSuffix) {
				closed = append(closed, file{p, fi.Size(), fi.ModTime()})
			}
		}
	}
	if total <= maxBytes {
		return nil
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].mod.Before(closed[j].mod) })
	var errs []error
	for _, f := range closed {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		total -= f.size
		r.Counters.Evicted.Add(1)
		r.Counters.EvictedBytes.Add(f.size)
	}
	return errors.Join(errs...)
}
