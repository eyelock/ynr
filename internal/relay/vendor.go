package relay

import (
	_ "embed"
	"sort"
	"strings"
	"sync"
)

// ClaudeCodePin is the Claude Code version whose attribute names ClaudeCodeAttributes lists. A
// strip list fails open: a name it has never seen passes through, so the relay counts the names
// Claude Code sends that are not on that list, and ynr doctor reports them, names only, never
// values. Moving the pin means re-running scripts/probe-claude-code-otel.sh against the new
// version and updating claude-code-attributes.txt and this constant together.
const ClaudeCodePin = "2.1.326"

// claudeCodeScope is the prefix of the instrumentation scope names Claude Code exports under.
// Only records under it are checked: another vendor's names are not on this list.
const claudeCodeScope = "com.anthropic.claude_code"

//go:embed claude-code-attributes.txt
var claudeCodeList string

// ClaudeCodeAttributes is the set of attribute names verified on ClaudeCodePin.
var ClaudeCodeAttributes = func() map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(claudeCodeList, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			m[l] = true
		}
	}
	return m
}()

// Limits on what the relay remembers about unknown names, so a hostile or buggy sender cannot
// grow the record or put content in it.
const (
	maxUnknownNames = 256
	maxNameLen      = 128
	// OtherName counts names past the limit, or too long or not made of name characters.
	OtherName = "(unusable name)"
)

// unknownSet counts the names Claude Code sent that are not on the list.
type unknownSet struct {
	mu     sync.Mutex
	counts map[string]int64
	dirty  bool
}

func (u *unknownSet) add(name string) {
	if !usableName(name) {
		name = OtherName
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.counts == nil {
		u.counts = map[string]int64{}
	}
	if _, ok := u.counts[name]; !ok && len(u.counts) >= maxUnknownNames {
		name = OtherName
	}
	u.counts[name]++
	u.dirty = true
}

// snapshot copies the counts, and reports whether they changed since the last snapshot taken
// with clear.
func (u *unknownSet) snapshot(clear bool) (map[string]int64, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	changed := u.dirty
	if clear {
		u.dirty = false
	}
	out := make(map[string]int64, len(u.counts))
	for k, v := range u.counts {
		out[k] = v
	}
	return out, changed
}

func usableName(s string) bool {
	if s == "" || len(s) > maxNameLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-', r == ':':
		default:
			return false
		}
	}
	return true
}

// UnknownNames returns the names in counts, sorted.
func UnknownNames(counts map[string]int64) []string {
	out := make([]string, 0, len(counts))
	for k := range counts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
