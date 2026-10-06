package query

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Erasure (ADR-005, NFR-16): handles named in the reader's erasure list are masked in every
// query at once, and compaction rewrites the hours holding them without them.
var (
	erasureMu sync.RWMutex
	erasure   []string
)

// Erased is what an erased handle reads as.
const Erased = "(erased)"

// SetErasure sets the handles to erase, for the whole process: ynr serve and ynr query each
// read one erasure list.
func SetErasure(handles []string) {
	erasureMu.Lock()
	defer erasureMu.Unlock()
	erasure = append([]string(nil), handles...)
}

// Erasure returns the handles being erased.
func Erasure() []string {
	erasureMu.RLock()
	defer erasureMu.RUnlock()
	return append([]string(nil), erasure...)
}

// LoadErasure reads an erasure list: one handle per line, as it appears in user.name
// (github.com/octocat); blank lines and lines starting with # are ignored.
func LoadErasure(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, " \t") {
			return nil, fmt.Errorf("%s:%d: a handle has no spaces: %q", path, n, line)
		}
		out = append(out, line)
	}
	return out, sc.Err()
}
