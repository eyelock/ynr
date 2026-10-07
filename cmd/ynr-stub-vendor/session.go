package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// sessionOptions are the flags a session reports back.
type sessionOptions struct {
	id             string
	permissionMode string
	effort         string
}

// usage is a message's token usage, in Claude Code's stream-json field names.
type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

// turnCost is what each turn costs, matching the cost metric the stub exports.
const turnCost = 0.001

// runSession speaks Claude Code's stream-json session as ynh drives it (--print
// --input-format stream-json --output-format stream-json --verbose): it announces itself with a
// system init event, then reads one JSON object per line from in. A control_request is answered
// with a control_response. A user message is a turn: the stub exports its telemetry, then writes an
// assistant event and a result event carrying that turn's usage and the running cost. After the
// scripted number of turns, or when in closes, it exits with the scripted code.
func runSession(in io.Reader, out io.Writer, x *exporter, s script, turnDelay time.Duration, o sessionOptions) int {
	enc := json.NewEncoder(out)
	emit := func(v map[string]any) { _ = enc.Encode(v) }
	hello := map[string]any{"type": "system", "subtype": "init", "session_id": o.id, "model": s.Model, "tools": []string{}}
	if o.permissionMode != "" {
		// Reported as asked: ynh stops a session that started in another mode.
		hello["permissionMode"] = o.permissionMode
	}
	emit(hello)

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	var cost float64
	answered := 0
	for answered < s.Turns && sc.Scan() {
		var m struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "control_request":
			emit(controlResponse(m.RequestID, m.Request.Subtype, o, cost))
		case "user":
			answered++
			time.Sleep(turnDelay)
			x.turn(answered, s)
			cost += turnCost
			u := usage{InputTokens: s.InputTokens, OutputTokens: s.OutputTokens}
			emit(map[string]any{"type": "assistant", "session_id": o.id, "message": map[string]any{
				"id": fmt.Sprintf("msg_stub_%d", answered), "type": "message", "model": s.Model, "role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("turn %d: done", answered)}},
				"usage":   u,
			}})
			emit(map[string]any{
				"type": "result", "subtype": s.Result, "is_error": s.Result != "success",
				"num_turns": 1, "result": fmt.Sprintf("turn %d: done", answered), "stop_reason": "end_turn",
				"session_id": o.id, "usage": u, "total_cost_usd": cost,
			})
		}
	}
	return s.Exit
}

// controlResponse answers a control request. get_settings reports the applied effort and get_usage
// the session's running cost, the two ynh asks for; any other request is a success with no body.
func controlResponse(id, subtype string, o sessionOptions, cost float64) map[string]any {
	body := map[string]any{}
	switch subtype {
	case "get_settings":
		var effort any
		if o.effort != "" {
			effort = o.effort
		}
		body["effective"] = map[string]any{}
		body["sources"] = []any{}
		body["applied"] = map[string]any{"effort": effort, "model": nil}
	case "get_usage":
		body["session"] = map[string]any{"total_cost_usd": cost}
	}
	return map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": id, "response": body,
	}}
}
