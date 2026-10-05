# ADR-004: Spool, detection and wiring

Status: draft (2026-10-05)
Satisfies: FR-1, FR-2, FR-5, NFR-1, NFR-2, NFR-3, NFR-11, NFR-12, NFR-18, NFR-22

## Context

Telemetry held in memory or sent to an endpoint is lost when a process dies, and the data most
worth having, a job failing, is the data most likely to be lost that way. A docker-mode run cannot
reach a collector on the job's loopback, and every endpoint is one more hole in the run's egress
policy (ynf ADR-007). ynr has to be optional, need no configuration when present, and never start
anything nobody asked for.

## Decision

**Tools write to a spool; ynr reads it.** The spool is a folder of OpenTelemetry's JSON-lines
file format (OTLP JSON, one export request per line), with one subfolder per writer (ADR-003).
It is the only contract between a tool and ynr.

**Writing:**
- Each process writes its own files, `<service>-<instance id>-<seq>.jsonl`, so there is no
  locking. The active file ends `.open.jsonl` and is renamed when closed or rotated (8 MB,
  default).
- Records are written in batches at least every second. A batch written survives the process
  crashing (NFR-12). The file is flushed to disk at the end of each unit of work and on exit,
  which covers the host crashing too; each flush is bounded in time (2 seconds, default) and
  abandoned past it, so a slow network filesystem never blocks the tool (NFR-1).
- Each writer caps its own files (64 MB, default). Over the cap it drops new records and counts
  them, and never fails (NFR-1).
- `ynr serve` also caps the spool as a whole (1 GB on a laptop, default, and configurable for a
  job). If the object store is unreachable for long enough to reach the cap, it evicts the oldest
  closed files first, counting what it drops (`ynr.spool.evicted`), so an outage can never grow
  the spool without limit.
- **The spool exporter is shared and generic.** The Go and JavaScript SDKs do not ship an OTLP
  file exporter, and writing the spool correctly is subtle, so it is written once, beside the
  receiver that reads it: a Go module (`github.com/eyelock/ynr/spoolexporter`, released with tags
  `spoolexporter/v*`) and an npm package (`@eyelock/otel-spool-exporter`), both in the
  `spoolexporter/` folder of ynr's repository. Each has its own module, depends only on its
  language's OpenTelemetry SDK, with no network exporter or gRPC, and nothing else in ynr; they
  are ordinary OpenTelemetry exporters that know nothing of ynr's roles. The
  spool format and both ends of it live in one repository, and every tool uses them (NFR-8).

**Reading.** `ynr serve` reads every writer folder with ynr's own spool receiver, including open
files up to their last complete line, under the hostile-input rules in ADR-003. It ships what it
reads to the object store as compressed batches (ADR-005), each named by the spool file and byte
range it came from, and advances its recorded position in a file only once the batch holding those
lines has been committed to the store. Shipping is therefore at least once, and duplicates are
removed downstream (ADR-005). It deletes a closed file only when everything in it is committed, so
a crash between reading and shipping loses nothing. Whether the spool outlives the job is the
runner's choice: a persistent volume keeps it for the next `ynr serve`; an ephemeral one is swept
into ynf's run capture (below).

**How a tool chooses where to write, in order:**

1. The operator set `OTEL_EXPORTER_OTLP_*`: export there over the network (NFR-3).
2. `YNR_SPOOL` names a writer folder, or the laptop default (`$XDG_STATE_HOME/ynr/spool/local`)
   exists: write to the spool.
3. Otherwise: the SDK's no-op providers. Nothing is written and nothing fails (NFR-2).

A long-lived process that found no spool checks again once a minute, so a ynm server started
before ynr still finds the spool once it exists.

**`ynr serve`'s own settings** are flags with environment fallbacks (NFR-7): the spool root
(`--spool`, `YNR_SPOOL_ROOT`), the collector's identity (`--collector-id`, `YNR_COLLECTOR_ID`,
defaulting on a laptop to `local-<host>`), the job within its pool (`--collector-instance`,
`YNR_COLLECTOR_INSTANCE`) and where to ship (`--upstream`, `YNR_UPSTREAM`).

**Detection decides where to write; configuration decides what runs** (NFR-11). No tool starts
`ynr` because it found it. On a laptop, the person runs `ynr serve`, which creates the spool on
first start.

**`ynr` is a command, not a service.** Every role runs in the foreground: it logs to stderr, runs
until it is stopped (Ctrl-C or `SIGTERM`, after a bounded flush), and exits. ynr never daemonises
itself and ships no service installer. Keeping it running, under launchd, systemd, a container or
a terminal, is the choice of whoever starts it. Elsewhere, configuration starts it:

| What | Started when | Configured in |
|---|---|---|
| `ynr serve` in a factory job | ynf's factory configuration enables the collector | ynf's local configuration (ynf ADR-009) |
| `ynr relay` for a run | the run's telemetry relay setting is on | `ynh agent run --telemetry-relay`, `YNH_TELEMETRY_RELAY`, or ynh's configuration; a ynf lane sets the variable for its runs |

**`ynr info --format json`** reports the version, the build (`full` or `slim`, ADR-001), the
capabilities version, the spool root, and whether a `ynr serve` holds the spool, read from the
lock file `ynr serve` keeps in the spool root:

```json
{
  "version": "0.1.0",
  "build": "full",
  "capabilities": "0.1.0",
  "spool": "/home/me/.local/state/ynr/spool",
  "serving": { "pid": 4182, "since": "2026-10-05T09:14:02Z" }
}
```

`ynr doctor` warns when `OTEL_EXPORTER_OTLP_*` is set in the environment, since a tool then
exports there and the spool receives nothing; on a laptop with those variables set globally for
other tooling, that is otherwise a silent gap.

**In a factory job** with the collector enabled, ynf:
- starts `ynr serve` on the job's spool, and if the operator set `OTEL_EXPORTER_OTLP_*`, passes
  that endpoint to `ynr serve` as an upstream exporter, so the operator's choice is honoured at
  the edge;
- writes its own telemetry to `factory/`: when ynf's configuration enables the collector, the
  spool wins over the operator's `OTEL_EXPORTER_OTLP_*` for ynf itself, because the operator's
  endpoint is served by `ynr serve`'s upstream instead;
- for each run, creates `runs/<run id>/`, writes the run's manifest (ADR-003), and starts the run
  with `YNR_SPOOL` set to that folder and without `OTEL_EXPORTER_OTLP_*`, mounting only that
  folder in docker mode;
- at the end of the job, gives `ynr serve` a bounded time to ship what is left (30 seconds,
  default), and puts any spool file not yet committed into the run capture (ynf ADR-010).

Without the collector enabled, ynf still gives each run its folder when a spool is configured, and
otherwise its children inherit the environment unchanged.

**The relay** is for vendor CLIs, which export only over the network. `ynr relay --spool <folder>`
listens on a random loopback port, so parallel runs on one host never collide, writes what it
receives into that folder, and enforces a memory limit, a request size limit and a rate limit, since
the agent can reach it. It accepts OTLP/HTTP, protobuf or JSON, optionally gzipped, and prints its
endpoint as its first line of output (`--format json` adds its pid), which is how whoever started it
learns where to point the vendor. When the relay setting is on and `ynr` is present, `ynh agent run`
starts it for the length of the run, configures the vendor CLI, and stops it after the vendor exits
and the relay has flushed:

| Vendor | Configured with |
|---|---|
| Claude Code | `CLAUDE_CODE_ENABLE_TELEMETRY=1`, `OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER` and `OTEL_LOGS_EXPORTER` set to `otlp`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, `OTEL_EXPORTER_OTLP_ENDPOINT` set to the relay, `TRACEPARENT`, and `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`, without which it sends no spans. Verified with Claude Code 2.1.289: its `claude_code.interaction` span joins `TRACEPARENT`, and with prompt logging off a prompt's text arrives as `<REDACTED>`. Prompt logging is never turned on. |
| Codex | an `[otel]` block in a run-local copy of its `config.toml`, with the full signal path in the endpoint, since Codex is not configured through the environment. Whether it reads `TRACEPARENT` is unverified. |

**The relay's settings reach the agent's own subprocesses.** The vendor CLI passes its
environment on, so anything the agent starts that follows the order above, such as ynm launched
as an MCP server over stdio, sees `OTEL_EXPORTER_OTLP_ENDPOINT` and exports to the relay instead
of writing to the spool. That is expected: its records still land in the run's folder with `run`
provenance, but without the spool's crash safety.

The vendor settings in the table above are verified in the walking skeleton before anything else
relies on them (ADR-009), including whether a repository's own Claude Code settings file can
override the relay settings or turn prompt logging on. If it can, ynh passes its settings through
whatever Claude Code gives the highest precedence. The scheduled real-vendor job keeps a canary
check on it (ADR-008).

**What each tool emits** follows the contract in ADR-006:

| Tool | Emits |
|---|---|
| ynf | a trace per step, decisions, actions on trackers and forges, the CloudEvents mirror (ADR-003), metrics, its bridged logs |
| ynh | `ynh agent run` only: started and finished events, a span per run with its outcome, turns, tokens, cost and bound; no other command emits |
| ynm | server spans for MCP and HTTP requests, store and consolidation spans, and audit events through a new `otel` provider behind ynm ADR-017's audit seam; hosted, it writes to `services/ynm/` with its own `ynr serve` beside it |

**Images.** The slim build of `ynr` ships in ynh's base image, and so in ynf's factory image built
on it, so the relay and the collector are available wherever a run or a job runs. Its presence
turns nothing on.

## Alternatives

- **A separate public repository for the exporters.** Not chosen: the format would live apart
  from the receiver that reads it, and a change to it would span two repositories.
- **Export over the network to a local collector.** Not chosen as the local path: data in memory
  is lost when a process dies, docker-mode runs cannot reach the job's loopback, and each endpoint
  needs an egress exception. Kept only for vendor CLIs, through the relay, and for operators who
  set `OTEL_*`.
- **Start a collector when `ynr` is found.** Not chosen: a background process nobody asked for.
- **A per-user service install for laptops** (launchd, systemd). Not chosen: `ynr` is a command
  that does one thing while it runs; supervising it belongs to whoever starts it.
- **Instrument every ynh command.** Not chosen: ynh launches the vendor CLI and exits, so only
  `ynh agent run` lives long enough to report.

## Consequences

- Each sibling depends on the OpenTelemetry SDK and the spool exporter for its language.
- While ynr's repository is private, fetching the Go module needs a read-only token
  (`GOPRIVATE=github.com/eyelock/ynr`), in CI and for anyone building a sibling from source. For a
  public tool such as ynh, that means building from source outside its maintainers' machines and
  CI waits until ynr is made public; its released binaries are unaffected.
- A run that is killed leaves its started events and every batch already written.

## Open questions

- None yet.

## History

- 2026-10-05: drafted.
