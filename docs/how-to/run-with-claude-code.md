# Run ynr with Claude Code

Get Claude Code's own spans, metrics and events into the spool, in the same trace as the run that
started it. Claude Code exports only over the network, so it goes through `ynr relay`.

## What you need

- ynr on your `PATH` ([Install ynr](install.md)); either build works for the relay.
- `ynr serve` running on the spool, so what the relay writes is shipped
  ([ynr on a laptop](../tutorial/laptop/README.md) shows how).
- Claude Code, signed in.

## With ynh

If you start Claude Code with `ynh agent run`, turn the relay on and ynh does the rest: it starts
`ynr relay` on the run's spool folder, points Claude Code at it, and stops it when the run ends.
Any one of these turns it on, first match wins:

```bash
ynh agent run --telemetry-relay ...        # for this run
export YNH_TELEMETRY_RELAY=1               # for every run in this shell
```

or `"telemetry_relay": true` in `~/.ynh/config.json`. ynh starts the relay only when the backend is
`claude`, the run's telemetry goes to the spool (no `OTEL_EXPORTER_OTLP_*` set), `ynr` is on `PATH`
and the run is not under `--sandbox srt`. When one of these does not hold, ynh prints a note on
stderr and the run carries on without Claude Code's telemetry. ynh's
[telemetry page](https://github.com/eyelock/ynh/blob/main/docs/telemetry.md#vendor-telemetry-through-the-relay)
has the details.

## By hand

Without ynh, start the relay on the laptop's writer folder and ask it for its address as JSON:

```bash
ynr relay --spool "${XDG_STATE_HOME:-$HOME/.local/state}/ynr/spool/local" --format json
```

Expected, on its first line:

```text
{"endpoint":"http://127.0.0.1:54117","pid":58205}
```

In another terminal, give Claude Code the settings ADR-004 names, with the relay's endpoint:

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_TRACES_EXPORTER=otlp OTEL_METRICS_EXPORTER=otlp OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:54117
export CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1
claude -p "say hello"
```

`CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` is what makes Claude Code send spans at all. To make its
`claude_code.interaction` span a child of a trace you already have, also export that trace's
`TRACEPARENT` before running it.

Leave Claude Code's prompt logging off, which is its default: its prompt's text then reaches the
spool as `<REDACTED>`.

When it has finished, stop the relay with Ctrl-C. It flushes and prints how many requests it
accepted.

## Check that it arrived

Run `ynr tail` in another terminal while Claude Code runs; it prints Claude Code's spans as they are
written. About fifteen seconds after the run, they are in the store too: `ynr query recent` lists the runs that carry a `ynh.run` span, and
`ynr query trace <trace id>` prints a trace, Claude Code's spans included, as a tree.

## When nothing arrives

- `ynr doctor` says `warn otlp`: `OTEL_EXPORTER_OTLP_*ENDPOINT` is set in the shell that started the
  tool, so it exports there and the spool receives nothing.
- The relay's closing line counts refused requests: `too large`, `busy`, `rate limited` and
  `malformed`. Its limits are `--max-request`, `--max-memory` and `--rate`.
- Claude Code does not pass its `OTEL_*` settings to the processes it starts. An MCP server it
  launches over stdio, such as ynm, writes to the spool through `YNR_SPOOL`, which names the writer
  folder, not through the relay.
