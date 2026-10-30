package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// memorySnapshot is ynm's records at one moment, as `ynm export` prints them: the raw JSONL log,
// history included, one record to a line.
type memorySnapshot struct {
	lines []string
}

// memoryHome is the folder every run of a scenario gets as YNM_HOME, so whatever the tool does
// with ynm lands in a scratch store and never in the operator's own (rule 13).
func (s *sandbox) memoryHome() string { return filepath.Join(s.root, "ynm-home") }

// snapshotMemory reads ynm's records from the scratch home, from the folder `ynr conformance` was
// invoked from, so a repository's project mount is read too. It returns an error when ynm cannot
// export.
func (s *sandbox) snapshotMemory(ctx context.Context, path string) (*memorySnapshot, error) {
	if err := os.MkdirAll(s.memoryHome(), 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "export", "--cwd", s.invoked)
	cmd.Dir = s.invoked
	cmd.Env = mergeEnv(map[string]string{"PATH": os.Getenv("PATH"), "YNM_HOME": s.memoryHome()})
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("`ynm export` failed: %v: %s", err, excerpt(stderr.String()))
	}
	snap := &memorySnapshot{}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			snap.lines = append(snap.lines, l)
		}
	}
	sort.Strings(snap.lines)
	return snap, nil
}

// memoryDiff is the records one snapshot has that the other lacks, each counted once.
func memoryDiff(have, lack *memorySnapshot) []string {
	seen := map[string]int{}
	for _, l := range lack.lines {
		seen[l]++
	}
	var out []string
	for _, l := range have.lines {
		if seen[l] > 0 {
			seen[l]--
			continue
		}
		out = append(out, l)
	}
	return out
}

// describeRecord names a ynm record by its metadata only. Its content is never shown, so a
// planted canary never reaches the report.
func describeRecord(line string) string {
	var r struct {
		ID, MemoryID, Op, Type, Level, Namespace string
	}
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		return "a line that is not a ynm record"
	}
	return fmt.Sprintf("%s %s record %s (memory %s, %s), namespace %q", r.Level, r.Type, r.ID, r.MemoryID, r.Op, r.Namespace)
}

// rule13: ynm's store holds the same records before and after the scenario, and none holds a
// canary. The scenario's runs all used a scratch YNM_HOME, so the strictest reading is the only
// one: a record added, changed or removed fails, because a conformance run has no reason to touch
// memory.
func (k *checker) rule13() {
	const name = "never into memory"
	if k.mem.path == "" {
		k.skip(13, name, "ynm is not on the PATH")
		return
	}
	if k.mem.err != nil {
		k.skip(13, name, "ynm is on the PATH, but its store could not be read: "+k.mem.err.Error())
		return
	}
	added := memoryDiff(k.mem.after, k.mem.before)
	removed := memoryDiff(k.mem.before, k.mem.after)
	var bad []string
	for _, l := range added {
		bad = append(bad, "added: "+describeRecord(l))
	}
	for _, l := range removed {
		bad = append(bad, "removed: "+describeRecord(l))
	}
	for _, kind := range sortedKeys(k.planted) {
		for _, l := range k.mem.after.lines {
			if strings.Contains(l, k.planted[kind]) {
				bad = append(bad, fmt.Sprintf("%s canary in %s", kind, describeRecord(l)))
			}
		}
	}
	if len(bad) > 0 {
		k.fail(13, name, fmt.Sprintf("ynm's store changed during the scenario: %d added, %d removed (%d problem(s) in all)", len(added), len(removed), len(bad)), bad...)
		return
	}
	k.pass(13, name, fmt.Sprintf("ynm's store held the same %d record(s) before and after, with no canary in them", len(k.mem.after.lines)))
}

// memoryState is what rule 13 needs: the ynm found on the PATH and the store before and after.
type memoryState struct {
	path          string
	before, after *memorySnapshot
	err           error
}
