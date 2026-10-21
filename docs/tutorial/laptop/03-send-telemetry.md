# 3. Send telemetry

Vendor CLIs such as Claude Code can only export over the network, so ynr has a relay: a small
receiver on a loopback port that writes what it gets into the spool. You will start one and post a
run to it with `curl`, as a vendor CLI would.

## Start the relay

Use terminal B. The relay writes into one writer folder. On a laptop that is `local`:

```bash
. ~/ynr-tutorial/env.sh
ynr relay --spool "$XDG_STATE_HOME/ynr/spool/local"
```

Expected:

```text
http://127.0.0.1:54349
```

The first line is the relay's address. The port is chosen freshly each time, so yours will differ.
Leave it running.

## Write the script that posts a run

In terminal C, create a script that sends one `ynh.run` span, with a child span, as OTLP/JSON. The
run belongs to the lane `github.com/example/factory#lint-paydown` and to the work item
`github.com/eyelock/ynr#12`, and it reports a model, a cost and token counts.

```bash
cat > ~/ynr-tutorial/post-run.sh <<'SCRIPT'
#!/bin/sh
# Usage: post-run.sh <trace id, 32 hex digits> <outcome> <cost in dollars>
# Sends one ynh.run span, with a child span, to the relay at $ENDPOINT.
set -eu
ENDPOINT=${ENDPOINT:?set ENDPOINT to the address the relay printed}
TRACE=$1
ROOT=$(printf %s "$TRACE" | cut -c1-16)
CHILD=$(printf %s "$TRACE" | cut -c17-32)
END=$(date +%s)000000000
START=$(( $(date +%s) - 42 ))000000000
curl -sS -X POST "$ENDPOINT/v1/traces" -H 'Content-Type: application/json' -d '{
  "resourceSpans": [{
    "resource": {"attributes": [
      {"key": "service.name", "value": {"stringValue": "ynh"}},
      {"key": "service.version", "value": {"stringValue": "0.10.0"}},
      {"key": "service.instance.id", "value": {"stringValue": "tutorial-1"}}
    ]},
    "scopeSpans": [{
      "scope": {"name": "tutorial"},
      "spans": [
        {"traceId": "'$TRACE'", "spanId": "'$ROOT'", "name": "ynh.run", "kind": 1,
         "startTimeUnixNano": "'$START'", "endTimeUnixNano": "'$END'", "status": {"code": 1},
         "attributes": [
           {"key": "ynf.lane", "value": {"stringValue": "github.com/example/factory#lint-paydown"}},
           {"key": "ynf.item.key", "value": {"stringValue": "github.com/eyelock/ynr#12"}},
           {"key": "ynh.run.outcome", "value": {"stringValue": "'$2'"}},
           {"key": "gen_ai.request.model", "value": {"stringValue": "claude-sonnet-5-5"}},
           {"key": "ynh.run.cost_usd", "value": {"doubleValue": '$3'}},
           {"key": "gen_ai.usage.input_tokens", "value": {"intValue": "15200"}},
           {"key": "gen_ai.usage.output_tokens", "value": {"intValue": "2100"}}
         ]},
        {"traceId": "'$TRACE'", "spanId": "'$CHILD'", "parentSpanId": "'$ROOT'",
         "name": "claude_code.interaction", "kind": 1,
         "startTimeUnixNano": "'$START'", "endTimeUnixNano": "'$END'", "status": {"code": 1},
         "attributes": [{"key": "ynf.item.key", "value": {"stringValue": "github.com/eyelock/ynr#12"}}]}
      ]
    }]
  }]
}'
echo
SCRIPT
chmod +x ~/ynr-tutorial/post-run.sh
```

## Post a run

Tell the script where the relay is, using the address terminal B printed, then post:

```bash
export ENDPOINT=http://127.0.0.1:54349     # the address your relay printed
~/ynr-tutorial/post-run.sh 5b8efff798038103d269b633813fc60c converged 0.0731
```

Expected:

```text
{}
```

`{}` is the relay's OTLP/JSON answer: the request was accepted. The relay has written the request
as one line into a file in the spool:

```bash
ls ~/ynr-tutorial/state/ynr/spool/local
```

Expected:

```text
ynr-relay-64344-000001.open.jsonl
```

The `.open.jsonl` ending means the relay still has the file open. A closed file ends in plain
`.jsonl`. `ynr serve` in terminal A reads open files up to their last complete line, so it can
already see the run.

Next: [4. Watch it arrive](04-watch-it-arrive.md).
