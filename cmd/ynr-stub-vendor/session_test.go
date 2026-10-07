package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

// event reads the next stdout line of a session.
func event(t *testing.T, sc *bufio.Scanner) map[string]any {
	t.Helper()
	if !sc.Scan() {
		t.Fatalf("session output ended: %v", sc.Err())
	}
	var m map[string]any
	if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
		t.Fatalf("not a JSON line: %q", sc.Text())
	}
	return m
}

// TestStreamJSONSession drives the stub the way ynh drives claude: the flags ynh passes, the two
// control requests, then user turns, reading events until each result.
func TestStreamJSONSession(t *testing.T) {
	s, srv := newSink(t)
	env := []string{"OTEL_EXPORTER_OTLP_ENDPOINT=" + srv.URL, "TRACEPARENT=00-" + traceID + "-" + spanID + "-01"}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var errb bytes.Buffer
	code := make(chan int, 1)
	args := []string{"--input-format", "stream-json", "--output-format", "stream-json", "--print", "--verbose",
		"--plugin-dir", "/x/.claude", "--add-dir", "/x", "--append-system-prompt", "be brief", "--model", "sonnet",
		"--effort", "high", "--permission-mode", "acceptEdits", "--settings", "{}", "--session-id", "abc",
		"--turns", "2", "--exit", "3"}
	go func() {
		code <- run(args, env, inR, outW, &errb)
		_ = outW.Close()
	}()
	sc := bufio.NewScanner(outR)

	hello := event(t, sc)
	if hello["type"] != "system" || hello["subtype"] != "init" || hello["session_id"] != "abc" ||
		hello["permissionMode"] != "acceptEdits" || hello["model"] != "stub-model" {
		t.Fatalf("init: %v", hello)
	}
	write := func(line string) {
		if _, err := io.WriteString(inW, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	// Both at once, as ynh sends them: the pipe is unbuffered, so one at a time would deadlock
	// against the stub's first answer.
	write(`{"type":"control_request","request_id":"ynh-get-settings","request":{"subtype":"get_settings"}}` + "\n" +
		`{"type":"control_request","request_id":"ynh-get-usage","request":{"subtype":"get_usage"}}`)
	settings := event(t, sc)
	resp := settings["response"].(map[string]any)
	applied := resp["response"].(map[string]any)["applied"].(map[string]any)
	if settings["type"] != "control_response" || resp["subtype"] != "success" || resp["request_id"] != "ynh-get-settings" || applied["effort"] != "high" {
		t.Fatalf("settings response: %v", settings)
	}
	usage := event(t, sc)["response"].(map[string]any)
	if usage["request_id"] != "ynh-get-usage" || usage["response"].(map[string]any)["session"].(map[string]any)["total_cost_usd"] != 0.0 {
		t.Fatalf("usage response: %v", usage)
	}

	for turn := 1; turn <= 2; turn++ {
		write(`{"type":"user","message":{"role":"user","content":"SECRET PROMPT"}}`)
		a := event(t, sc)
		msg, _ := a["message"].(map[string]any)
		if a["type"] != "assistant" || msg["model"] != "stub-model" || msg["role"] != "assistant" || msg["id"] == "" {
			t.Fatalf("turn %d assistant: %v", turn, a)
		}
		if c := msg["content"].([]any)[0].(map[string]any); c["type"] != "text" || c["text"] == "" {
			t.Fatalf("turn %d content: %v", turn, msg["content"])
		}
		if u := msg["usage"].(map[string]any); u["input_tokens"] != 100.0 || u["output_tokens"] != 20.0 {
			t.Fatalf("turn %d assistant usage: %v", turn, u)
		}
		r := event(t, sc)
		if r["type"] != "result" || r["subtype"] != "success" || r["is_error"] != false {
			t.Fatalf("turn %d result: %v", turn, r)
		}
		if u := r["usage"].(map[string]any); u["input_tokens"] != 100.0 || u["output_tokens"] != 20.0 || u["cache_read_input_tokens"] != 0.0 {
			t.Fatalf("turn %d result usage: %v", turn, u)
		}
		// The cost is a running total, as Claude Code's is.
		if got, want := r["total_cost_usd"].(float64), float64(turn)*turnCost; got < want-1e-9 || got > want+1e-9 {
			t.Fatalf("turn %d total_cost_usd %v, want %v", turn, got, want)
		}
	}
	select {
	case c := <-code:
		if c != 3 {
			t.Errorf("exit %d, want 3 (%s)", c, errb.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end after its scripted turns")
	}
	_ = inW.Close()

	req := ptraceotlp.NewExportRequest()
	if err := req.UnmarshalProto(s.body("/v1/traces")); err != nil {
		t.Fatalf("no OTLP traces arrived: %v", err)
	}
	spans := req.Traces().ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	sawRoot := false
	for i := 0; i < spans.Len(); i++ {
		if spans.At(i).TraceID().String() != traceID {
			t.Errorf("span %s left the trace", spans.At(i).Name())
		}
		sawRoot = sawRoot || spans.At(i).ParentSpanID().String() == spanID
	}
	if !sawRoot {
		t.Error("no span is a child of TRACEPARENT's span")
	}
	if s.body("/v1/metrics") == nil || s.body("/v1/logs") == nil {
		t.Error("metrics or logs did not arrive")
	}
	if bytes.Contains(s.body("/v1/logs"), []byte("SECRET")) {
		t.Error("the prompt reached the logs")
	}
}

// TestSessionEndsWhenStdinCloses: closing stdin, as ynh does on Close, ends the session with the
// scripted exit code, even before its turns are used.
func TestSessionEndsWhenStdinCloses(t *testing.T) {
	var out, errb bytes.Buffer
	in := strings.NewReader(`{"type":"user","message":{"role":"user","content":"hi"}}` + "\n")
	if code := run([]string{"--input-format", "stream-json", "--print", "--turns", "5", "--exit", "9", "--result", "budget"}, nil, in, &out, &errb); code != 9 {
		t.Errorf("exit %d, want 9 (%s)", code, errb.String())
	}
	if !strings.Contains(out.String(), `"subtype":"budget"`) || !strings.Contains(out.String(), `"is_error":true`) {
		t.Errorf("the scripted result is missing: %s", out.String())
	}
}

func TestUnknownInputFormatIsRefused(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--input-format", "bogus"}, nil, nilIn, &out, &errb); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}
