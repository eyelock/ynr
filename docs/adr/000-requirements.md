# ADR-000: Requirements

Status: draft (2026-10-05)

## Context

ynr ("your named reporting") is the fourth sibling. ynh ("your named harness") guides agents and
runs a bounded agent loop with `ynh agent run`. ynm ("your named memory") manages what agents
remember. ynf ("your named factory") is the event-driven outer loop that decides what runs next.
Together they make a software factory with both model and deterministic steps, running as many
instances on many hosts.

A **factory** is a ynf lane: an intake, a ynh harness and a focus (its prompt and profile), run
by any number of ynf instances against any number of repositories on any number of forges. A
factory's id is where its lane is defined plus the lane's name, for example
`github.com/acme/factory-config#lint-paydown`.

Each sibling logs on its own terms: ynh with Go's standard logger, ynf with `log/slog` and planned
OpenTelemetry traces (ynf ADR-011), ynm through an audit seam with file, stdout and S3 sinks (ynm
ADR-017). Nobody can see, in one place, what every factory is doing. ynr is that place.

These are the requirements the other ADRs cite by id. They follow the same draft, addenda,
consolidate lifecycle as the ADRs.

## Functional requirements

### Collection
- FR-1 Tools write telemetry as OpenTelemetry JSON lines into a spool folder, using shared spool
  exporter packages; ynr reads the spool.
- FR-2 Relay a vendor CLI's network telemetry (Claude Code, Codex) into the spool.
- FR-3 Ship what it reads to an object store as compressed batches, compacted into Parquet by the
  reader of the store, and optionally forward it over OTLP to an operator's own backend.
- FR-4 Stamp on receipt, overwriting anything the sender set: provenance, the collector's
  identity, and the factory a run belongs to.
- FR-5 Report its version, capabilities and spool location with `ynr info --format json`.

### Correlation
- FR-6 One trace per ynf step, containing the `ynh agent run` it started and the vendor CLI
  inside that run.
- FR-7 Follow one item across its steps, over days, and back to the event that started it.
- FR-8 Mirror every CloudEvent ynf receives into the telemetry stream, one way.

### Registries
- FR-9 Learn each tool's registry of names from tool binaries named in ynr's configuration, and
  count names a tool emits that its registry does not declare.
- FR-10 Check a tool against the instrumentation contract in CI (`ynr conformance`).

### Reporting
- FR-11 A local dashboard over what one collector has read.
- FR-12 A central dashboard over every factory, fed from the object store.
- FR-13 The dashboards' queries from the CLI, with `--format json`, through the same JSON
  interface the dashboards use.

## Non-functional requirements

### Behaviour
- NFR-1 Never block or fail a producer. Losing telemetry is acceptable; stalling the factory is
  not.
- NFR-2 Optional: every sibling works unchanged when ynr is absent.
- NFR-3 An operator's standard `OTEL_*` settings are always honoured.
- NFR-4 Telemetry is never written to ynm memory. ynf writes curated learnings to ynm on purpose
  (ynf ADR-008); that is memory, not telemetry.
- NFR-5 Telemetry never drives a factory decision. A limit that needs fleet-wide numbers, such as
  a spend cap, is computed in the control plane, never read from ynr.
- NFR-6 No content in telemetry: no prompts, completions, ticket text, code or memory bodies.
  Redaction happens at the source, with the collector as a second line.
- NFR-7 Conventions mirror the siblings: stable `--format json`, meaningful exit codes, every
  environment variable a fallback for an explicit flag.
- NFR-8 No sibling has a build or runtime dependency on ynr's code. Siblings depend only on the
  spool exporter packages, which are generic OpenTelemetry exporters released from ynr's
  repository as their own Go module and npm package, depending on nothing else in it; their CI
  installs a pinned `ynr` release for the conformance check (ADR-008).
- NFR-9 Written in Go, as a distribution of the OpenTelemetry Collector, released as two builds
  from one source: a full build with DuckDB, and a slim build without cgo for images.
- NFR-10 Works fully on a laptop, offline, with nothing extra to run. The cloud is the same design
  with different adapters.
- NFR-11 Detection only decides where a tool writes. Nothing is started unless configuration
  asks for it.
- NFR-12 A record written to the spool survives the writing process crashing.
- NFR-21 A run cannot stall ynf by filling a disk: run folders have quotas the environment
  enforces, or, where it cannot, the spool sits on a filesystem of its own; ynr treats everything
  in a run folder as hostile input.
- NFR-22 Counts and costs are never doubled: shipping is at least once, and duplicates are
  removed before anything is counted.

### Targets

| | Laptop | Cloud |
|---|---|---|
| NFR-13 Volume | a few hundred runs a day | 10,000 runs a day across all factories, about 10 million records |
| NFR-14 Freshness | seconds | central typically 30 seconds behind, at most 60 |
| NFR-15 Retention | rolling 7 days, capped at 1 GB | traces and logs 30 days, events 90 days, hourly metrics 30 days, daily and monthly metric rollups 13 months; configurable |
| NFR-16 Personal data | people appear only by their handle in the system where they acted, never by name or email | the same; handles are personal data, bounded by retention, sign-in and an erasure list |
| NFR-17 Cost | disk | object storage and requests; nothing must run continuously for data to be kept |
| NFR-18 Authentication | file permissions on the spool; the relay listens on loopback only | each collector writes only its own object-store prefix; the central dashboard requires sign-in |
| NFR-19 Availability | not applicable | central holds no state that cannot be rebuilt from the object store |
| NFR-20 Sampling | none | none by default |

## History

- 2026-10-05: drafted.
