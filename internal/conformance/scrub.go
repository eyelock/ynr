package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"

	"github.com/eyelock/ynr/internal/relay"
)

// The scrub check (ADR-006 rule 10, "no content"): vendor records carrying the attributes the
// relay must strip are sent through the relay under test, and none of them may reach the spool in
// raw form. It exists because a relay with no scrubber shipped in a sibling's agent image while
// every unit test stayed green (ynr #155).

// scrubDropped are the keys the relay drops outright: they must not appear in the spool at all.
var scrubDropped = []string{
	"cwd", "host.name", "user.email", "tool_input", "tool_parameters",
}

// scrubAccountIDs are the keys the relay replaces by a keyed hash (or drops): the key may stay,
// the raw value must not.
var scrubAccountIDs = []string{"user.id", "user.account_uuid", "user.account_id", "organization.id"}

// RelayStarter starts the relay under test writing into dir, and returns its OTLP/HTTP endpoint
// and a function that stops it after a final flush. The default is ynr's own relay.
type RelayStarter func(dir string) (endpoint string, stop func() error, err error)

// defaultRelay starts the relay compiled into this binary, with an account-id key, as a
// per-launch relay does.
func defaultRelay(dir string) (string, func() error, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", nil, err
	}
	keyDir, err := os.MkdirTemp("", "ynr-conformance-key-")
	if err != nil {
		return "", nil, err
	}
	keyFile := filepath.Join(keyDir, "key")
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		return "", nil, err
	}
	r, err := relay.New(relay.Config{Dir: dir, Service: "conformance-scrub", Drain: 2 * time.Second, KeyFile: keyFile})
	if err != nil {
		_ = os.RemoveAll(keyDir)
		return "", nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx) }()
	return r.Endpoint(), func() error {
		cancel()
		err := <-done
		_ = os.RemoveAll(keyDir)
		return err
	}, nil
}

// scrubFixture is the vendor records: a span, a metric and a log record, each carrying every
// scrubbed key at the resource, scope and record level, with a value unique to the run so a
// leak is found by value as well as by key. raw maps each account id key to its raw value.
type scrubFixture struct {
	traces  []byte
	metrics []byte
	logs    []byte
	raw     map[string]string
}

func newScrubFixture() (*scrubFixture, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	tag := fmt.Sprintf("%x", b)
	f := &scrubFixture{raw: map[string]string{}}
	for _, k := range scrubAccountIDs {
		f.raw[k] = "scrub-" + tag + "-" + strings.NewReplacer(".", "-", "_", "-").Replace(k)
	}
	leaky := func(m pcommon.Map) {
		m.PutStr("cwd", "/work/"+tag+"/project")
		m.PutStr("host.name", "host-"+tag)
		m.PutStr("user.email", "person-"+tag+"@example.com")
		m.PutStr("tool_input", `{"command":"cat /etc/`+tag+`"}`)
		m.PutStr("tool_parameters", `{"bash_command":"ls `+tag+`"}`)
		for _, k := range scrubAccountIDs {
			m.PutStr(k, f.raw[k])
		}
		m.PutStr("tool_name", "Bash") // not scrubbed: proves the fixture reached the spool
	}
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	leaky(rs.Resource().Attributes())
	rs.Resource().Attributes().PutStr("service.name", "vendor-fixture")
	ss := rs.ScopeSpans().AppendEmpty()
	sp := ss.Spans().AppendEmpty()
	sp.SetName("vendor.tool")
	sp.SetTraceID([16]byte{1})
	sp.SetSpanID([8]byte{2})
	leaky(sp.Attributes())
	leaky(sp.Events().AppendEmpty().Attributes())
	var err error
	if f.traces, err = ptraceotlp.NewExportRequestFromTraces(td).MarshalProto(); err != nil {
		return nil, err
	}
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	leaky(rm.Resource().Attributes())
	rm.Resource().Attributes().PutStr("service.name", "vendor-fixture")
	m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	m.SetName("vendor.tokens")
	leaky(m.SetEmptySum().DataPoints().AppendEmpty().Attributes())
	if f.metrics, err = pmetricotlp.NewExportRequestFromMetrics(md).MarshalProto(); err != nil {
		return nil, err
	}
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	leaky(rl.Resource().Attributes())
	rl.Resource().Attributes().PutStr("service.name", "vendor-fixture")
	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName("vendor.tool_result")
	leaky(lr.Attributes())
	if f.logs, err = plogotlp.NewExportRequestFromLogs(ld).MarshalProto(); err != nil {
		return nil, err
	}
	return f, nil
}

// send posts the fixture to a relay's endpoint.
func (f *scrubFixture) send(ctx context.Context, endpoint string) error {
	for path, body := range map[string][]byte{"/v1/traces": f.traces, "/v1/metrics": f.metrics, "/v1/logs": f.logs} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("the relay refused the fixture's %s: HTTP %d", path, resp.StatusCode)
		}
	}
	return nil
}

// leaks lists what the spool lines hold that the relay should have removed: a dropped key, or an
// account id's raw value. It also reports a fixture that never arrived, so an empty spool is not a pass.
func (f *scrubFixture) leaks(lines [][]byte) []string {
	var out []string
	arrived := 0
	for i, l := range lines {
		if bytes.Contains(l, []byte(`"Bash"`)) {
			arrived++
		}
		for _, k := range scrubDropped {
			if regexp.MustCompile(`"key"\s*:\s*"` + regexp.QuoteMeta(k) + `"`).Match(l) {
				out = append(out, fmt.Sprintf("line %d keeps the key %s", i+1, k))
			}
		}
		for _, k := range scrubAccountIDs {
			if bytes.Contains(l, []byte(f.raw[k])) {
				out = append(out, fmt.Sprintf("line %d keeps the raw value of %s", i+1, k))
			}
		}
	}
	if arrived < 3 {
		out = append(out, fmt.Sprintf("only %d of the fixture's 3 signals reached the spool: the relay wrote nothing to check", arrived))
	}
	sort.Strings(out)
	return out
}

// scrubResult is what the scrub check found, once per run.
type scrubResult struct {
	leaks []string
	err   error
	lines int
}

// runScrub sends the fixture through the relay under test and reads what it wrote.
func runScrub(ctx context.Context, root string, start RelayStarter) scrubResult {
	if start == nil {
		start = defaultRelay
	}
	f, err := newScrubFixture()
	if err != nil {
		return scrubResult{err: err}
	}
	spool := filepath.Join(root, "scrub")
	dir := filepath.Join(spool, "services", "scrub")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return scrubResult{err: err}
	}
	endpoint, stop, err := start(dir)
	if err != nil {
		return scrubResult{err: err}
	}
	sendErr := f.send(ctx, endpoint)
	if err := stop(); err != nil && sendErr == nil {
		sendErr = err
	}
	if sendErr != nil {
		return scrubResult{err: sendErr}
	}
	c, err := readCapture(spool)
	if err != nil {
		return scrubResult{err: err}
	}
	return scrubResult{leaks: f.leaks(c.Lines), lines: len(c.Lines)}
}

// ScrubAgainst runs the scrub check against a relay already running on endpoint, writing to
// spoolDir, and returns what leaked. It lets a pinned relay of any version be checked.
func ScrubAgainst(ctx context.Context, endpoint, spoolDir string) ([]string, error) {
	f, err := newScrubFixture()
	if err != nil {
		return nil, err
	}
	if err := f.send(ctx, endpoint); err != nil {
		return nil, err
	}
	time.Sleep(3 * time.Second) // the relay flushes its spool on an interval
	c, err := readCapture(spoolDir)
	if err != nil {
		return nil, err
	}
	return f.leaks(c.Lines), nil
}

// scrub is the check, reported under rule 10.
func (k *checker) scrub() {
	const name = "scrubbed keys stay out of the spool"
	switch {
	case k.scrubbed.err != nil:
		k.fail(10, name, "the scrub fixture could not be run: "+k.scrubbed.err.Error())
	case len(k.scrubbed.leaks) > 0:
		k.fail(10, name, fmt.Sprintf("%d scrubbed value(s) reached the spool through the relay", len(k.scrubbed.leaks)), k.scrubbed.leaks...)
	default:
		k.pass(10, name, fmt.Sprintf("a fixture carrying cwd, host.name, user.email, tool_input, tool_parameters and the four account ids went through the relay: %d spool lines, none holding them raw", k.scrubbed.lines))
	}
}
