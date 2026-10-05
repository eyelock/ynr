# ADR-001: Positioning: the observation plane

Status: draft (2026-10-05)
Satisfies: FR-1, FR-3, FR-11, FR-12, NFR-2, NFR-4, NFR-5, NFR-9, NFR-10

## Context

A factory run crosses three programs and often several hosts. ynf decides, ynh runs the agent,
ynm is consulted and written to. To know what is happening, or why something went wrong, an
operator needs one stream covering all of them, on a laptop and across every factory.

Two shortcuts are wrong. Writing logs into ynm makes memory a log store, and ynm's consolidation
merges and supersedes records, which would rewrite a step history into one that never happened
(ynf ADR-008). Feeding telemetry into ynf's intake would let anything that can emit telemetry,
including an agent, steer the factory (ADR-003).

## Decision

**ynr is the factory's observation plane, built on OpenTelemetry.** It reports what the tools
did. It does not remember (ynm) and it does not decide (ynf).

**One source, `ynr`,** is a distribution of the OpenTelemetry Collector, built with the Collector
Builder so it carries only the components ynr needs, wrapped in ynr's own commands:

```
ynr serve        [--ui <addr>]                 collector: read the spool, stamp, ship; local dashboard only with --ui
ynr central      [--ui <addr>]                 central: read the object store, hot tier, compaction; fleet dashboard only with --ui
ynr relay        --spool <folder>              loopback OTLP receiver for a vendor CLI, writing into a spool folder
ynr info         --format json                 version, build, capabilities, spool location
ynr tail         [--service …] [--item …]      the live stream, as text or JSON
ynr query        <name> [args] --format json   the dashboards' queries from the CLI
ynr conformance  --file .ynr/conformance.yaml  the instrumentation contract check (ADR-008)
ynr version | doctor
```

**Two builds from that one source:**

| Build | Has | Ships to |
|---|---|---|
| full | everything, including DuckDB (which needs cgo): the hot tier, dashboards, `query`, `central`, compaction | `eyelock/homebrew-tap`; laptops and central |
| slim | no cgo and no DuckDB: `relay`, `serve` as a shipper, `info`, `tail`, `conformance` | ynh's base image, so every run and factory job |

The slim build reports what it lacks in its capabilities, and refuses loudly when asked for
something it does not have, such as `--ui` (ynf ADR-012).

**Tools write files; ynr reads them.** Every sibling writes its telemetry into a spool folder
(ADR-004). `ynr serve` reads the spool and ships it to an object store, a local folder on a
laptop and S3 in the cloud (ADR-005). `ynr central` reads the object store and serves every
factory. The same code runs in both places; only the adapters differ (NFR-10).

**ynr ships its own dashboards.** Reporting is what ynr is for, and the questions are the
factory's own: what is each lane doing, what happened to this item, what is each model costing.
There are two flavours over one UI: the local dashboard in `ynr serve` and the fleet dashboard in
`ynr central`, both in the full build. Both are off by default and each is enabled on its own.

**What ynr is not:**
- Not memory. Telemetry is never written to ynm (NFR-4).
- Not control. Nothing ynf decides reads telemetry (NFR-5, ADR-003).
- Not required. Every sibling runs unchanged without it (NFR-2).
- Not self-starting. Nothing starts ynr unless configuration asks (NFR-11).

## Alternatives

- **Adopt an existing OpenTelemetry backend for storage and dashboards** (OpenObserve, Grafana's
  stack, SigNoz, ClickStack). OpenObserve is the closest fit: one binary, Parquet on local disk or
  S3. Not chosen as the default: it is a second server beside `ynr` on every laptop, and it is
  AGPL-licensed under the reporting product. It remains a candidate adapter behind ynr's store
  port (ADR-005).
- **Agent observability tools** (Langfuse, Phoenix, Opik, Laminar, OpenLIT). Not chosen: they
  model one application's model calls, not an outer loop of items and steps across many hosts,
  and most need several servers.
- **One build for everything.** Not chosen: DuckDB's cgo and size would land in every run image,
  where only the relay is needed.
- **A ynr library per language.** Not chosen: ynh and ynf are Go and ynm is TypeScript, and every
  tool would carry the fan-out to stores.
- **Logs into ynm memory.** Not chosen, see Context.

## Consequences

- The siblings depend on their language's OpenTelemetry SDK and the public spool exporter
  package, not on ynr (ADR-004, ADR-006).
- Building our own storage is bounded to a layout, compaction and named queries; DuckDB and the
  object store do the rest (ADR-005).
- The full build needs cgo cross-compilation for each platform it ships to.
- An operator can still send everything to any OpenTelemetry backend, through ynr or around it.

## Open questions

- None yet.

## History

- 2026-10-05: drafted.
