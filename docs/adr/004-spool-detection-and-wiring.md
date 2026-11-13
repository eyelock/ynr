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
  language's OpenTelemetry SDK, and nothing else in ynr; they are ordinary OpenTelemetry
  exporters that know nothing of ynr's roles. Neither has gRPC or protobuf. (Amended
  2026-11-01: the Go module also has an OTLP/HTTP JSON exporter, using only `net/http`, so a tool
  can post the same OTLP/JSON to an operator's `OTEL_EXPORTER_OTLP_*` endpoint beside the spool.
  The npm package does not, yet.) (Amended
  2026-11-13: the Go module's exporter also sends `http/protobuf`, chosen by
  `OTEL_EXPORTER_OTLP_PROTOCOL`, with an encoder built on `protowire` from
  `google.golang.org/protobuf`; it still has no gRPC, and a consumer links only that one
  module. The design is bead ynw-egnq, built in ynw-20p5.) The
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

1. The operator set `OTEL_EXPORTER_OTLP_*` explicitly: export to that collector over the
   network, instead of to ynr (NFR-3). No YN tool sets these for itself, so this happens only
   when someone chooses it.
2. `YNR_SPOOL` names a writer folder, or the laptop default (`$XDG_STATE_HOME/ynr/spool/local`)
   exists: write to ynr's spool.
3. Otherwise: the SDK's no-op providers. Nothing is written and nothing fails (NFR-2).

A long-lived process that found no spool checks again once a minute, so a ynm server started
before ynr still finds the spool once it exists.

**`ynr serve`'s own settings** are flags with environment fallbacks (NFR-7): the spool root
(`--spool`, `YNR_SPOOL_ROOT`), the collector's identity (`--collector-id`, `YNR_COLLECTOR_ID`,
defaulting on a laptop to `local-<host>`), the job within its pool (`--collector-instance`,
`YNR_COLLECTOR_INSTANCE`) the object store it ships to (`--store`, `YNR_STORE`, defaulting on a
laptop to the folder `$XDG_DATA_HOME/ynr/store`), and an optional OTLP endpoint it also forwards to
(`--upstream`, `YNR_UPSTREAM`). With a store, the store is what commits the spool, and the upstream
is a best-effort copy that never holds the spool back; without one, the upstream commits it.

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
| `ynr relay` for a run or a launch | the telemetry relay setting is on | `ynh agent run --telemetry-relay` and `ynh run`, `YNH_TELEMETRY_RELAY`, or ynh's configuration; a ynf lane sets the variable for its runs |
| `ynr relay` for a Wheelhouse | the Wheelhouse's own start script, and its doctor checks it | the Wheelhouse (a plugin script), on a fixed loopback port it chooses |

**`ynr info --format json`** reports the version, the build (`full` or `slim`, ADR-001), the
capabilities version, the spool root, whether a `ynr serve` holds the spool, read from the
lock file `ynr serve` keeps in the spool root, and whether a standing `ynr relay` writes into it,
read from the lock file the relay keeps there. `relay` is `null` when none does, otherwise the
relay's `pid`, `since` and `endpoint`; a Wheelhouse's doctor reads it:

```json
{
  "version": "0.1.0",
  "build": "full",
  "capabilities": "0.1.0",
  "spool": "/home/me/.local/state/ynr/spool",
  "serving": { "pid": 4182, "since": "2026-10-05T09:14:02Z" },
  "relay": { "pid": 5107, "since": "2026-11-07T08:00:00Z", "endpoint": "http://127.0.0.1:4318" }
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
the agent can reach it. Before writing, it strips the path and host attributes ADR-006 rule 10
names, since a vendor cannot always be told not to send them. It accepts OTLP/HTTP, protobuf or
JSON, optionally gzipped, and prints its
endpoint as its first line of output (`--format json` adds its pid), which is how whoever started it
learns where to point the vendor. With `--exit-on-stdin-eof` it also stops when its standard input
closes, so it ends with the process that started it even if that process is killed outright. When
the relay setting is on and `ynr` is present, `ynh agent run` and `ynh run` each start one for the
length of the run or launch, configure the vendor CLI, and stop it after the vendor exits and the
relay has flushed (amended 2026-11-07: `ynh run` too, see "`ynh run` records every launch" below):

| Vendor | Configured with |
|---|---|
| Claude Code | `CLAUDE_CODE_ENABLE_TELEMETRY=1`, `OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER` and `OTEL_LOGS_EXPORTER` set to `otlp`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, `OTEL_EXPORTER_OTLP_ENDPOINT` set to the relay, `TRACEPARENT`, and `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`, without which it sends no spans. Verified with Claude Code 2.1.289: its `claude_code.interaction` span joins `TRACEPARENT`, and with prompt logging off a prompt's text arrives as `<REDACTED>`. Prompt logging is never turned on. |
| Codex | an `[otel]` block in a run-local copy of its `config.toml`, with the full signal path in the endpoint, since Codex is not configured through the environment. Whether it reads `TRACEPARENT` is unverified. |

**A Wheelhouse may have a standing relay.** Amended 2026-11-07. Seats start through `ynh run`, and
the desk's delegates are sessions of their own (below), so a per-launch relay serves one seat and
ends with it. A Wheelhouse that wants one endpoint for every seat, started now or later, can run
`ynr relay` as a standing relay instead: the same command with the same limits, long-lived, on a
fixed loopback port instead of a random one, with limits sized for many seats at once. It is the
alternative to the per-launch relay, no longer the only way and not specific to Claude Code (see
"`ynh run` records every launch"). ynr still never daemonises: whoever runs the Wheelhouse starts
the standing relay and keeps it running, in the foreground like any other role, and the
Wheelhouse's doctor checks that it is up (`ynr doctor` also sees it).

**A Wheelhouse's standing relay writes to the `local/` folder of the laptop spool** (the default
`$XDG_STATE_HOME/ynr/spool/local`), the one a laptop's `ynr serve` already reads. It is not a run,
so it gets no `runs/<run id>/` folder, and not a factory or a hosted service, so ADR-003 gives its
records `ynr.provenance` of `local`, stamped by `ynr serve` from the folder and overwriting
anything the record says, as for every writer. That is the honest label: a record from a seat's
vendor process, relayed over loopback, is no stronger a claim than "something on this laptop wrote
it". Several Wheelhouses on one laptop share that folder and are told apart by the run label
`ynh.label.wheelhouse`, never by provenance and never by a folder per Wheelhouse. A run label
`wheelhouse:<name>`, which every harness in a Wheelhouse carries, becomes that resource attribute
(decided 2026-11-02: general ynh labels, one attribute per key; the label rule is ADR-006 rule 10).
`ynh run` appends it to the vendor's `OTEL_RESOURCE_ATTRIBUTES` and passes it to child processes
through `YNH_LABELS`, so the desk's subagents carry it too, and
`ynr query --label wheelhouse:<name>` selects one Wheelhouse. This ADR states the design; the build is ynw-17g.14 to ynw-17g.19.

**The Wheelhouse's standing relay holds the HMAC key.** ADR-006 rule 10 hashes account identifiers
(`user.id`, `user.account_uuid`, `user.account_id`, `organization.id`) with a key, so the same
account hashes alike across records without the id being recoverable. The Wheelhouse's standing
relay is the one that holds that key. A per-run or per-launch relay, such as that of `ynh agent
run` or `ynh run`, has none, so it drops those ids instead of hashing them.

**`ynh run` records every launch, for every vendor.** Amended 2026-11-07, superseding the
2026-11-01 decision that `ynh run` emits nothing of its own and only wires Claude Code. That
decision rested on `ynh run` ending in an exec of the vendor, which left ynh no process to own a
span. `ynh run` now stays alive as the parent of every vendor CLI (ynh #774), so it can, and the
principle is that telemetry works through ynh for any vendor (claude, codex, cursor, copilot,
opencode) and a vendor's own telemetry only enriches it. In three parts:

1. **Layer 1, ynh's own record.** For every launch and every vendor, `ynh run` writes a
   `ynh.session` span with `ynh.session.started` and `ynh.session.finished` events and its own
   outcome vocabulary (proposed: `exited`, `failed`, `signalled`, `refused`). It shares
   `ynh.harness.*`, the vendor, the focus, the profile and `ynh.label.<key>` with `ynh agent run`'s
   `ynh.run` span, which stays `ynh agent run`'s alone. It carries no tokens or cost, since an
   interactive vendor reports none to ynh; those come from the vendor's own records. It follows
   the destination order above (an operator's `OTEL_EXPORTER_OTLP_*`, the spool, nothing) and is
   written whenever a destination exists. Under `--sandbox srt` the relay is skipped with a note,
   but `ynh.session` is still written, since ynh runs outside the sandbox.
2. **Enrichment, the vendor's own telemetry.** Where the vendor can export telemetry, `ynh run`
   points it at a relay, wired as in the table above (for Claude Code, the same environment,
   including `OTEL_LOG_TOOL_DETAILS=1`, without which it does not name its subagents; prompt
   logging is never turned on). The relay is, by default, a per-launch `ynr relay` that `ynh run`
   starts and stops with the launch, switched like `ynh agent run`'s (`--telemetry-relay`,
   `YNH_TELEMETRY_RELAY`, `telemetry_relay` in ynh's configuration). When a standing relay
   endpoint is configured (a flag, an environment variable or ynh's configuration; loopback only,
   ADR-006 rule 14), `ynh run` points the vendor at it instead and starts none. The operator's
   `OTEL_EXPORTER_OTLP_*` wins over both. A vendor that exports no telemetry still has its layer 1
   record.
3. **Delegate sessions.** Each `delegates_to` entry runs as its own ynh session behind a ynh MCP
   server (decided 2026-11-03), not as a native subagent of the parent, so each leaves its own
   `ynh.session` record.

**A vendor's records join `ynh.session` in three layers.** Interactive Claude Code ignores an
inbound `TRACEPARENT`, so no single mechanism joins every vendor:

1. `ynh run` always sets `TRACEPARENT` naming the `ynh.session` span. Codex honours it; interactive
   Claude Code does not; Copilot, OpenCode and Cursor are unchecked.
2. `ynh run` always appends `ynh.session.id` to the vendor's `OTEL_RESOURCE_ATTRIBUTES`, beside the
   labels. This works for any vendor that honours the standard variable. As a resource attribute it
   reaches the vendor's metrics, which ADR-006 rule 8 allows as its one named exception: one value
   per launch, dropped or rolled up by ynr before rollups.
3. A per-launch relay stamps `ynh.session.id` on every record it receives (ynw-17g.28, to be
   built), so a vendor that ignores the first two is still joined. The stamp goes on metrics too, under the
   same rule 8 exception. A standing relay serves many seats and cannot know which launch a record
   belongs to, so only layers 1 and 2 apply to it.

The `ynh run` wiring, the environment list, the space behaviour (`OTEL_RESOURCE_ATTRIBUTES` is
percent-encoded and without spaces, since one space makes Claude Code drop every key) and the
subagent names were verified with Claude Code 2.1.316, as the per-run table above is for 2.1.289.
A seat that `ynh run` launched is named by those resource attributes and by `ynh.session`; a
subagent of the desk is named by Claude Code's own `agent.name` and subagent type, which
`OTEL_LOG_TOOL_DETAILS=1` provides. `OTEL_LOG_TOOL_DETAILS=1` also puts tool inputs on the
records, so it is only safe behind the relay's strip list and hashing of account identifiers
(ADR-006 rule 10), which is why the endpoint must be a relay, never an arbitrary collector.

**The relay's settings stay with the vendor.** Claude Code does not pass its `OTEL_*` settings to
the processes it starts, so an MCP server the agent launches over stdio, such as ynm, does not see
the relay. It finds `YNR_SPOOL`, which ynh passes to the run, and writes to the run's folder itself,
with `run` provenance and the spool's crash safety.

**A vendor propagates the trace by its own rules.** With tracing on, Claude Code sends `traceparent`
to its own API and to HTTP MCP servers, and has no setting to stop it. ADR-006's rule that our tools
send trace context only within the factory binds our tools, not a vendor's; we accept this, because
withholding `TRACEPARENT` from the vendor would break the trace from a step into the agent. The
headers carry trace and span ids only, never content.

The vendor settings in the table above are verified in the walking skeleton before anything else
relies on them (ADR-009), including whether a repository's own Claude Code settings file can
override the relay settings or turn prompt logging on. If it can, ynh passes its settings through
whatever Claude Code gives the highest precedence. The scheduled real-vendor job keeps a canary
check on it (ADR-008).

**What each tool emits** follows the contract in ADR-006:

| Tool | Emits |
|---|---|
| ynf | a trace per step, decisions, actions on trackers and forges, the CloudEvents mirror (ADR-003), metrics, its bridged logs |
| ynh | `ynh agent run`: started and finished events, a `ynh.run` span per run with its outcome, turns, tokens, cost and bound. `ynh run`, for every launch and vendor: a `ynh.session` span with started and finished events and its outcome, and the vendor's environment set so that the vendor reports itself and joins it |
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
- **Start the Wheelhouse relay from `ynh run`.** Not chosen: a process the launcher starts is
  per launch, ends with it, and means every seat starts its own. A relay that belongs to the
  Wheelhouse and outlives any one seat is started by the Wheelhouse. (Amended 2026-11-07: the
  per-launch relay is now ynh run's default, and a configured standing relay the alternative. What
  stays not chosen is `ynh run` starting a relay that outlives the launch.)
- **A per-user service install for laptops** (launchd, systemd). Not chosen: `ynr` is a command
  that does one thing while it runs; supervising it belongs to whoever starts it.
- **Instrument every ynh command.** Not chosen for the commands that do not run work, such as
  `ynh install` or `ynh ls`. For `ynh run` it was rejected until 2026-11-07, on the ground that
  `ynh run` replaced its own process with the vendor CLI and left ynh nothing to own a span, and it
  is now chosen: `ynh run` supervises every vendor (ynh #774), so it writes `ynh.session`.
- **Claude Code's own telemetry as the only Wheelhouse source** (ynw-8kl decision A1, recorded by
  [ynr #95](https://github.com/eyelock/ynr/pull/95)). Superseded 2026-11-07: it covered one vendor
  and left a launch of any other vendor with no record, against the principle that telemetry works
  through ynh for any vendor.

## Consequences

- Each sibling depends on the OpenTelemetry SDK and the spool exporter for its language.
- A run that is killed leaves its started events and every batch already written.
- A launch through `ynh run` leaves a `ynh.session` record whichever vendor it starts, so a
  vendor with no telemetry of its own is still counted.

## Open questions

- None yet.

## History

- 2026-10-05: drafted.
- 2026-11-01: a standing relay per Wheelhouse, and `ynh run` wiring the vendor's own telemetry.
- 2026-11-02: Wheelhouses sharing `local/` are told apart by the `ynh.label.wheelhouse` run label;
  the `ynh run` wiring is verified on Claude Code 2.1.316; the standing relay holds the HMAC key and
  a per-run relay drops the account ids.
- 2026-11-07: `ynr info --format json` reports the standing relay writing into the spool as `relay`
  (`null`, or `pid`, `since` and `endpoint`).
- 2026-11-07: `ynh run` writes its own `ynh.session` record for every launch and every vendor, and the
  vendor's telemetry only enriches it, joined by `TRACEPARENT`, `ynh.session.id` and the relay's
  stamp; the relay is per launch by default, with a configured standing relay as the alternative and
  no longer Claude Code only; each delegate is a session of its own. Supersedes ynw-8kl decision A1
  as [ynr #95](https://github.com/eyelock/ynr/pull/95) recorded it ("no `ynh run` span", wiring
  "when the vendor is Claude Code"). Design: ynw-17g.20 (decisions 6 to 8) and ynw-17g.23; this
  amendment is ynw-17g.27.
- 2026-11-09: `ynh.session.id` and the relay's stamp reach vendor metrics, under the one named
  exception to ADR-006 rule 8 (one value per launch, dropped or rolled up by ynr before rollups;
  ynw-j4a7, option B; ynw-8w20). Layers 2 and 3 say so.
