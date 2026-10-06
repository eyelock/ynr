package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// AskTimeout is how long a tool has to print its registry. A tool that takes longer is skipped.
// It is a variable only so a test can shorten it.
var AskTimeout = 10 * time.Second

// maxRegistry bounds what one tool may print, so a misbehaving tool cannot fill memory.
const maxRegistry = 8 << 20

var (
	toolPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,63}$`)
)

// Learned is one registry a tool printed: the registry, and the bytes it was read from, whose
// SHA-256 names it in the store.
type Learned struct {
	Registry *Registry
	Raw      []byte
	SHA256   string
}

// Ask runs `<tool> telemetry registry --format json` and reads what it prints. The tool is found
// only as the bare name on the PATH the process has: a name with a path in it is refused, and a
// tool that is not on the PATH is an error for the caller to log, never to stop on. Nothing in a
// record reaches here; the names come from ynr's own configuration.
func Ask(ctx context.Context, tool string) (*Learned, error) {
	if !toolPattern.MatchString(tool) {
		return nil, fmt.Errorf("%q is not a bare tool name", tool)
	}
	path, err := exec.LookPath(tool)
	if err != nil {
		return nil, fmt.Errorf("%s is not on the PATH", tool)
	}
	ctx, cancel := context.WithTimeout(ctx, AskTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "telemetry", "registry", "--format", "json")
	var out, errOut bytes.Buffer
	cmd.Stdout = &limitWriter{w: &out, n: maxRegistry}
	cmd.Stderr = &limitWriter{w: &errOut, n: 4096}
	// If the tool leaves children holding its pipes, do not wait for them past the timeout.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s telemetry registry: no answer in %s", tool, AskTimeout)
		}
		return nil, fmt.Errorf("%s telemetry registry: %w: %s", tool, err, strings.TrimSpace(errOut.String()))
	}
	var r Registry
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		return nil, fmt.Errorf("%s telemetry registry: not a registry: %w", tool, err)
	}
	switch {
	case r.Tool != tool:
		return nil, fmt.Errorf("%s telemetry registry: it describes %q", tool, r.Tool)
	case !versionPattern.MatchString(r.Version):
		return nil, fmt.Errorf("%s telemetry registry: version %q cannot name a registry", tool, r.Version)
	case len(r.Attributes)+len(r.Standard)+len(r.Spans)+len(r.Events)+len(r.Metrics) == 0:
		// A different shape (the fields above are absent) or an empty registry: either way,
		// checking against it would call every name unknown.
		return nil, fmt.Errorf("%s telemetry registry: declares no names, or is not in the shape ynf prints", tool)
	}
	sum := sha256.Sum256(out.Bytes())
	return &Learned{Registry: &r, Raw: out.Bytes(), SHA256: hex.EncodeToString(sum[:])}, nil
}

// limitWriter fails once more than n bytes are written.
type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(p) > l.n {
		return 0, errors.New("output too large")
	}
	l.n -= len(p)
	return l.w.Write(p)
}

// Store writes a learned registry under the collector's own prefix (ADR-005). One already there
// is the same registry and is fine.
func Store(ctx context.Context, st store.Store, collector string, l *Learned) error {
	key := store.RegistryKey(collector, l.Registry.Tool, l.Registry.Version, l.SHA256)
	if err := st.Put(ctx, key, l.Raw); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("storing %s: %w", key, err)
	}
	return nil
}

// LearnTools asks each named tool for its registry, stores what it learns under the collector's
// prefix when st is not nil, and returns a checker holding them. It never fails: a tool that is
// missing, slow or wrong, or a store that refuses the write, is returned as a problem for the
// caller to log, and the rest carry on. With no tools it learns nothing and returns nil, so the
// caller does no checking at all.
func LearnTools(ctx context.Context, tools []string, st store.Store, collector string) (*Checker, []error) {
	if len(tools) == 0 {
		return nil, nil
	}
	c := NewChecker()
	var problems []error
	for _, tool := range tools {
		l, err := Ask(ctx, tool)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		c.Learn(l.Registry)
		if st != nil {
			if err := Store(ctx, st, collector, l); err != nil {
				problems = append(problems, err)
			}
		}
	}
	return c, problems
}
