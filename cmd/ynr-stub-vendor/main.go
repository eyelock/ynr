// Command ynr-stub-vendor is a deterministic stand-in for a vendor CLI such as Claude Code
// (ADR-008). It uses no model and needs no secret: it follows a script, answers turns with fixed
// text, exports OTLP/HTTP to OTEL_EXPORTER_OTLP_ENDPOINT the way Claude Code does, joins the
// trace in TRACEPARENT, and exits with the scripted result. ynr conformance puts it on the PATH
// so a tool that runs a vendor CLI can be tested in CI with no model and no luck.
//
// It never echoes what it is asked: like Claude Code with prompt logging off, the prompt reaches
// telemetry as <REDACTED>.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

const (
	serviceName    = "claude-code"
	serviceVersion = "0.0.0-stub"
	// exportTimeout bounds a turn's exports, which run together, so a hung endpoint costs a
	// turn at most this.
	exportTimeout = time.Second
)

// script is what the stub does: how many turns, how long each takes, and how it ends.
type script struct {
	// Turns is the number of turns it answers.
	Turns int `json:"turns"`
	// TurnDelay is how long each turn takes, as a Go duration such as 200ms.
	TurnDelay string `json:"turn_delay"`
	// Exit is the exit code.
	Exit int `json:"exit"`
	// Result is printed as the last line of output, as {"type":"result","subtype":<Result>}.
	Result string `json:"result"`
	// Model, InputTokens and OutputTokens are what each turn reports.
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

func main() { os.Exit(run(os.Args[1:], os.Environ(), os.Stdout, os.Stderr)) }

func run(args, environ []string, stdout, stderr io.Writer) int {
	env := envMap(environ)
	s := script{Turns: 1, Result: "success", Model: "stub-model", InputTokens: 100, OutputTokens: 20}
	fs := flag.NewFlagSet("ynr-stub-vendor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("script", "", "a JSON script: turns, turn_delay, exit, result, model, input_tokens, output_tokens")
	turns := fs.Int("turns", -1, "turns to answer (YNR_STUB_TURNS)")
	delay := fs.String("turn-delay", "", "time each turn takes, such as 200ms (YNR_STUB_TURN_DELAY)")
	exit := fs.Int("exit", -1, "exit code (YNR_STUB_EXIT)")
	result := fs.String("result", "", "result named in the last line of output")
	// A vendor CLI is given a prompt and options; the stub accepts and ignores them.
	_ = fs.String("p", "", "ignored: the prompt")
	_ = fs.String("print", "", "ignored")
	_ = fs.String("output-format", "", "ignored")
	_ = fs.String("model", "", "ignored")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file != "" {
		b, err := os.ReadFile(*file)
		if err == nil {
			err = json.Unmarshal(b, &s)
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "ynr-stub-vendor: script %s: %v\n", *file, err)
			return 2
		}
	}
	if v, ok := env["YNR_STUB_TURNS"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			s.Turns = n
		}
	}
	if v, ok := env["YNR_STUB_EXIT"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			s.Exit = n
		}
	}
	if v := env["YNR_STUB_TURN_DELAY"]; v != "" {
		s.TurnDelay = v
	}
	if *turns >= 0 {
		s.Turns = *turns
	}
	if *delay != "" {
		s.TurnDelay = *delay
	}
	if *exit >= 0 {
		s.Exit = *exit
	}
	if *result != "" {
		s.Result = *result
	}
	var turnDelay time.Duration
	if s.TurnDelay != "" {
		d, err := time.ParseDuration(s.TurnDelay)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "ynr-stub-vendor: turn delay %q: %v\n", s.TurnDelay, err)
			return 2
		}
		turnDelay = d
	}

	x := newExporter(env, stderr)
	for i := 1; i <= s.Turns; i++ {
		time.Sleep(turnDelay)
		x.turn(i, s)
		_, _ = fmt.Fprintf(stdout, "turn %d: done\n", i)
	}
	_ = json.NewEncoder(stdout).Encode(map[string]string{"type": "result", "subtype": s.Result})
	return s.Exit
}

func envMap(environ []string) map[string]string {
	m := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// exporter sends one signal at a time, as the environment says. With no endpoint it does
// nothing, as a vendor CLI with telemetry off.
type exporter struct {
	env      map[string]string
	stderr   io.Writer
	client   *http.Client
	on       bool
	resource map[string]string
	traceID  pcommon.TraceID
	parent   pcommon.SpanID
	session  string
	logMu    sync.Mutex // the sends run together and share stderr
}

func newExporter(env map[string]string, stderr io.Writer) *exporter {
	x := &exporter{env: env, stderr: stderr, client: &http.Client{Timeout: exportTimeout}, session: randHex(8)}
	x.on = env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "" || env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] != ""
	x.resource = map[string]string{"service.name": serviceName, "service.version": serviceVersion}
	for _, kv := range strings.Split(env["OTEL_RESOURCE_ATTRIBUTES"], ",") {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.TrimSpace(k) != "" {
			x.resource[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if env["OTEL_SERVICE_NAME"] != "" {
		x.resource["service.name"] = env["OTEL_SERVICE_NAME"]
	}
	// The trace this process joins: with no TRACEPARENT it starts its own.
	x.traceID, x.parent = parseTraceparent(env["TRACEPARENT"])
	if x.traceID.IsEmpty() {
		copy(x.traceID[:], randBytes(16))
	}
	return x
}

// parseTraceparent reads a W3C traceparent header, zero values if it is absent or invalid.
func parseTraceparent(v string) (pcommon.TraceID, pcommon.SpanID) {
	var t pcommon.TraceID
	var s pcommon.SpanID
	parts := strings.Split(strings.TrimSpace(v), "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 {
		return t, s
	}
	tb, err1 := hex.DecodeString(parts[1])
	sb, err2 := hex.DecodeString(parts[2])
	if err1 != nil || err2 != nil {
		return t, s
	}
	copy(t[:], tb)
	copy(s[:], sb)
	return t, s
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func randHex(n int) string { return hex.EncodeToString(randBytes(n)) }

// protocol is http/protobuf unless the environment says http/json; anything else, such as grpc,
// the stub does not speak, and it exports nothing.
func (x *exporter) protocol(signal string) (asJSON, ok bool) {
	p := x.env["OTEL_EXPORTER_OTLP_"+strings.ToUpper(signal)+"_PROTOCOL"]
	if p == "" {
		p = x.env["OTEL_EXPORTER_OTLP_PROTOCOL"]
	}
	switch p {
	case "", "http/protobuf":
		return false, true
	case "http/json":
		return true, true
	}
	return false, false
}

// enabled reports whether the signal's exporter is on: off for OTEL_<SIGNAL>_EXPORTER=none.
func (x *exporter) enabled(signal string) bool {
	v := x.env["OTEL_"+strings.ToUpper(signal)+"_EXPORTER"]
	return x.on && v != "none"
}

// url is where a signal goes: a per-signal endpoint as given, or the base endpoint with the
// signal's path.
func (x *exporter) url(signal string) string {
	if e := x.env["OTEL_EXPORTER_OTLP_"+strings.ToUpper(signal)+"_ENDPOINT"]; e != "" {
		return e
	}
	return strings.TrimRight(x.env["OTEL_EXPORTER_OTLP_ENDPOINT"], "/") + "/v1/" + signal
}

type marshaler interface {
	MarshalProto() ([]byte, error)
	MarshalJSON() ([]byte, error)
}

// send posts a request and returns when it is answered or the turn's deadline passes.
func (x *exporter) send(ctx context.Context, signal string, req marshaler) {
	if !x.enabled(signal) {
		return
	}
	asJSON, ok := x.protocol(signal)
	if !ok {
		x.logMu.Lock()
		_, _ = fmt.Fprintf(x.stderr, "ynr-stub-vendor: protocol not supported, %s not exported\n", signal)
		x.logMu.Unlock()
		return
	}
	var body []byte
	var err error
	ctype := "application/x-protobuf"
	if asJSON {
		body, err = req.MarshalJSON()
		ctype = "application/json"
	} else {
		body, err = req.MarshalProto()
	}
	if err != nil {
		return
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, x.url(signal), bytes.NewReader(body))
	if err != nil {
		return
	}
	hr.Header.Set("Content-Type", ctype)
	for _, kv := range strings.Split(x.env["OTEL_EXPORTER_OTLP_HEADERS"], ",") {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.TrimSpace(k) != "" {
			hr.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	resp, err := x.client.Do(hr)
	if err != nil {
		return // a vendor never fails because its telemetry did
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func (x *exporter) setResource(r pcommon.Resource) {
	for k, v := range x.resource {
		r.Attributes().PutStr(k, v)
	}
	r.Attributes().PutStr("service.instance.id", x.session)
}

// turn exports one turn: an interaction span with an API request beneath it, the token and cost
// metrics, and the user prompt and API request events. Claude Code's names, none of its content.
func (x *exporter) turn(n int, s script) {
	if !x.on {
		return
	}
	var sends []func(context.Context)
	start := time.Now().Add(-time.Millisecond)
	end := time.Now()
	var spanID, reqID pcommon.SpanID
	copy(spanID[:], randBytes(8))
	copy(reqID[:], randBytes(8))

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	x.setResource(rs.Resource())
	spans := rs.ScopeSpans().AppendEmpty().Spans()
	in := spans.AppendEmpty()
	in.SetName("claude_code.interaction")
	in.SetKind(ptrace.SpanKindInternal)
	in.SetTraceID(x.traceID)
	in.SetSpanID(spanID)
	in.SetParentSpanID(x.parent)
	in.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
	in.SetEndTimestamp(pcommon.NewTimestampFromTime(end))
	in.Attributes().PutInt("interaction.sequence", int64(n))
	in.Attributes().PutStr("user_prompt", "<REDACTED>")
	in.Status().SetCode(ptrace.StatusCodeOk)
	rq := spans.AppendEmpty()
	rq.SetName("claude_code.llm_request")
	rq.SetKind(ptrace.SpanKindClient)
	rq.SetTraceID(x.traceID)
	rq.SetSpanID(reqID)
	rq.SetParentSpanID(spanID)
	rq.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
	rq.SetEndTimestamp(pcommon.NewTimestampFromTime(end))
	rq.Attributes().PutStr("model", s.Model)
	rq.Attributes().PutInt("input_tokens", s.InputTokens)
	rq.Attributes().PutInt("output_tokens", s.OutputTokens)
	sends = append(sends, func(ctx context.Context) { x.send(ctx, "traces", ptraceotlp.NewExportRequestFromTraces(td)) })

	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	x.setResource(rm.Resource())
	ms := rm.ScopeMetrics().AppendEmpty().Metrics()
	counter := func(name, unit string, v float64, attrs map[string]string) {
		m := ms.AppendEmpty()
		m.SetName(name)
		m.SetUnit(unit)
		sum := m.SetEmptySum()
		sum.SetIsMonotonic(true)
		sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
		dp := sum.DataPoints().AppendEmpty()
		dp.SetTimestamp(pcommon.NewTimestampFromTime(end))
		dp.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
		dp.SetDoubleValue(v)
		for k, a := range attrs {
			dp.Attributes().PutStr(k, a)
		}
	}
	counter("claude_code.token.usage", "tokens", float64(s.InputTokens), map[string]string{"type": "input", "model": s.Model})
	counter("claude_code.token.usage", "tokens", float64(s.OutputTokens), map[string]string{"type": "output", "model": s.Model})
	counter("claude_code.cost.usage", "USD", 0.001, map[string]string{"model": s.Model})
	sends = append(sends, func(ctx context.Context) { x.send(ctx, "metrics", pmetricotlp.NewExportRequestFromMetrics(md)) })

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	x.setResource(rl.Resource())
	lrs := rl.ScopeLogs().AppendEmpty().LogRecords()
	event := func(name string, attrs map[string]any) {
		l := lrs.AppendEmpty()
		l.SetTimestamp(pcommon.NewTimestampFromTime(end))
		l.SetEventName(name)
		l.SetSeverityText("INFO")
		l.Attributes().PutStr("event.name", name)
		l.Attributes().PutInt("event.sequence", int64(n))
		for k, v := range attrs {
			switch t := v.(type) {
			case string:
				l.Attributes().PutStr(k, t)
			case int64:
				l.Attributes().PutInt(k, t)
			}
		}
		l.SetTraceID(x.traceID)
		l.SetSpanID(spanID)
	}
	event("claude_code.user_prompt", map[string]any{"prompt": "<REDACTED>", "prompt_length": int64(0)})
	event("claude_code.api_request", map[string]any{"model": s.Model, "input_tokens": s.InputTokens, "output_tokens": s.OutputTokens})
	sends = append(sends, func(ctx context.Context) { x.send(ctx, "logs", plogotlp.NewExportRequestFromLogs(ld)) })

	ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, send := range sends {
		wg.Add(1)
		go func() {
			defer wg.Done()
			send(ctx)
		}()
	}
	wg.Wait()
}
