# ADR-009: Walking skeleton and build order

Status: draft (2026-10-05)
Satisfies: FR-1, FR-2, FR-6, NFR-10, NFR-12, NFR-21

## Context

The scope is large: a collector distribution in two builds, spool exporters in two
languages, a relay, a storage layout with compaction and an item index, central, server-rendered
dashboards, Terraform, registries, a conformance check and three sibling integrations. Several
assumptions underneath it are untested, and if one fails the design above it changes:

- that Claude Code nests its spans under `TRACEPARENT` with the settings ADR-004 names, and
  whether a repository's own Claude Code settings can override them or turn prompt logging on
- whether Codex reads `TRACEPARENT`, and how its run-local configuration behaves
- that a docker-mode run with only its spool folder mounted, and a size-limited volume, writes
  everything it should and cannot affect anything else
- that ynr's own spool receiver can read open files, survive restarts, and delete only after the
  batch is committed to the object store
- that a batch written before `kill -9` survives, and its `started` event shows the crash
- that the slim build stays free of cgo, and the full build cross-compiles with DuckDB for each
  platform the tap ships
- that DuckDB over compressed JSON batches and Parquet in S3 keeps central within a minute

## Decision

**Build in four slices, each ending in something that works end to end.** A slice is done when
its exit checks pass, not when its parts exist. Findings go back into the ADRs they affect before
the next slice starts.

**Slice 1: one trace through the chain, on a laptop.**
- The Go and npm spool exporter packages, each checked against ynr's receiver in ynr's CI, so
  ynm can start writing to the spool before slice 4.
- `ynr serve`, slim build: ynr's own spool receiver, the hostile-input rules, provenance and
  factory-attribute stamping, and export over OTLP to a local Jaeger, which is only a viewer for
  this slice.
- ynh: spool output from `ynh agent run`, started and finished events, joining `TRACEPARENT`, the
  relay setting, and Claude Code's settings.
- ynf: the step span, the intake span, the run folder with its quota, the manifest, and
  `TRACEPARENT` into the run.
- Exit checks:
  - one trace in Jaeger from a ynf step through `ynh agent run` into Claude Code's spans
  - a `kill -9` mid-run leaves the started event and every earlier batch
  - a docker-mode run with only its folder mounted produces the same trace
  - an agent that fills its run folder hits the quota without affecting ynf, and `ynr serve`
    records the warning
  - a planted symlink or hard link to `manifests/`, `factory/` or a host file is ignored and
    counted, never read, shipped or deleted
  - an over-long or malformed line is skipped and counted
  - a repository settings file cannot turn Claude Code's prompt logging on
  - an MCP server the agent starts over stdio, such as ynm, inherits the relay endpoint and its
    records land in the run's folder with `run` provenance
  - on a hosted runner without per-run quotas, a run that fills the spool's own filesystem does
    not stall ynf
  - Codex's behaviour is recorded in ADR-004

**Slice 2: reporting on a laptop.**
- The full build with DuckDB; shipping batches to the folder adapter; the hot tier; compaction
  with manifests and the item index; the erasure list; the first named queries as JSON endpoints;
  `ynr query`, `ynr tail`; the dashboard pages for lanes, an item's history, a trace and the live
  tail.
- Exit checks: works offline with nothing else running; the local dashboard is seconds behind;
  restarting `ynr serve` loses nothing; a late batch is still read after its hour is compacted;
  an item's history touches only the hours its index names; a batch re-shipped after a crash,
  and spans a vendor retried through the relay, are each counted once; a spool over its cap
  evicts oldest first and counts the loss; a year of cost by model reads only the rollups.

**Slice 3: the same design in the cloud.**
- The S3 adapter, tested against MinIO; a role per collector with segment-scoped writes; path
  shape validation; `ynr central` with the polling change feed, hot tier, compaction and leases;
  the central dashboard with sign-in; `infra/aws`.
- Exit checks: central stays within 60 seconds with many collectors writing; a central restart
  rebuilds its hot tier and loses nothing; a collector cannot write, or appear to write, under
  another collector's segment, nor anywhere under `compacted/`, `rollups/` or `index/`; with
  many collectors, a poll reads only keys after each collector's listing position.

**Slice 4: the contract, enforced.**
- Registry learning from configured binaries; `ynr conformance` with the
  stub vendor; releases through `eyelock/homebrew-tap`; ynm's integration, including a hosted ynm
  under `services/ynm/`; the conformance check required in ynh, ynf and ynm; the full-chain check
  in ynf's factory image; the scheduled real-vendor job.
- Exit checks: all three siblings pass as a required check, and a deliberate violation in each
  fails it.

**Not in the skeleton:** generic registry views, cloud adapters beyond S3, the S3 notification
change feed, joining handles across systems, and any backend adapter such as OpenObserve.

## Alternatives

- **Build each layer completely before the next.** Not chosen: the riskiest assumptions sit at
  the bottom, in the vendor CLIs and the spool, and would be found last.
- **Start with storage and dashboards.** Not chosen: they are the most work and the least
  uncertain.

## Consequences

- Jaeger is used only as a viewer during slice 1; nothing depends on it afterwards.

## Open questions

- None yet.

## History

- 2026-10-05: drafted.
