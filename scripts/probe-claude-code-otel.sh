#!/usr/bin/env bash
# Re-verify the Claude Code attribute list (internal/relay/claude-code-attributes.txt) against the
# installed Claude Code: run it once with OTEL_LOG_TOOL_DETAILS unset and once with it set to 1,
# each time with one custom subagent, and print the attribute NAMES it exported that are not on
# the list. Names only: values are never written to disk or printed.
#
# Usage: [CLAUDE_BIN=/path/to/claude] scripts/probe-claude-code-otel.sh [--keep-dir DIR]
#
# CLAUDE_BIN chooses the claude binary, for both --version and the probe runs; it defaults to the
# `claude` on PATH.
#
# It makes real (small) model calls with `claude -p`, so run it yourself, on the version you mean
# to pin, and read docs/how-to/re-verify-the-claude-code-attribute-list.md first.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
list="$root/internal/relay/claude-code-attributes.txt"
pin="$(sed -n 's/^const ClaudeCodePin = "\(.*\)"$/\1/p' "$root/internal/relay/vendor.go")"
claude_bin="${CLAUDE_BIN:-claude}"
version="$("$claude_bin" --version | awk '{print $1}')"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
if [ "${1:-}" = "--keep-dir" ]; then work="$2"; mkdir -p "$work"; trap - EXIT; fi

echo "pinned: $pin, installed: $version" >&2
[ "$pin" = "$version" ] || echo "note: the installed version is not the pin; to move the pin, update vendor.go and the list together" >&2

# A listener that takes OTLP/HTTP JSON and keeps only attribute names, one per line.
cat >"$work/listen.py" <<'PY'
import http.server, json, sys
out = open(sys.argv[2], "a", buffering=1)
def names(o):
    if isinstance(o, dict):
        if "key" in o and "value" in o:
            out.write(o["key"] + "\n")
        for k, v in o.items():
            if k in ("resource", "scope"):
                continue  # the relay checks record attributes, not resource or scope ones
            names(v)
    elif isinstance(o, list):
        for v in o:
            names(v)
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        try:
            names(json.loads(body))
        except Exception:
            pass
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(b"{}")
    def log_message(self, *a): pass
s = http.server.HTTPServer(("127.0.0.1", 0), H)
open(sys.argv[1], "w").write(str(s.server_port))
s.serve_forever()
PY

probe() { # $1 = label, $2 = OTEL_LOG_TOOL_DETAILS value or empty
  local label="$1" details="$2" names="$work/$1.names" portf="$work/$1.port"
  : >"$names"; rm -f "$portf"
  python3 "$work/listen.py" "$portf" "$names" & local lp=$!
  while [ ! -s "$portf" ]; do sleep 0.1; done
  local env=(CLAUDE_CODE_ENABLE_TELEMETRY=1 OTEL_METRICS_EXPORTER=otlp OTEL_LOGS_EXPORTER=otlp
    OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_PROTOCOL=http/json
    OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:$(cat "$portf")"
    CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1 OTEL_METRIC_EXPORT_INTERVAL=1000 OTEL_LOGS_EXPORT_INTERVAL=1000
    OTEL_TRACES_EXPORT_INTERVAL=1000 OTEL_RESOURCE_ATTRIBUTES=probe.label=$label)
  [ -n "$details" ] && env+=(OTEL_LOG_TOOL_DETAILS="$details")
  env "${env[@]}" "$claude_bin" -p \
    --agents '{"probe-agent":{"description":"Probe subagent","prompt":"Reply with the single word ok."}}' \
    --allowedTools "Bash(echo probe)" Agent \
    -- "Use the probe-agent subagent to say ok, then run the shell command 'echo probe' and reply done." \
    </dev/null >/dev/null 2>"$work/$label.err" || echo "claude exited non-zero for $label; see $work/$label.err" >&2
  sleep 3
  kill "$lp" 2>/dev/null || true
  wait "$lp" 2>/dev/null || true
}

probe default ""
probe details 1

grep -v '^#' "$list" | grep -v '^[[:space:]]*$' | sort -u >"$work/known"
cat "$work/default.names" "$work/details.names" | grep -vx 'probe.label' | sort -u >"$work/seen"
[ -s "$work/seen" ] || { echo "no attributes were exported: telemetry did not reach the listener" >&2; exit 2; }

echo "names exported by $version that are not on the list:"
comm -13 "$work/known" "$work/seen" | sed 's/^/  /'
echo "names on the list that $version did not export in this run (not necessarily wrong):"
comm -23 "$work/known" "$work/seen" | sed 's/^/  /'
