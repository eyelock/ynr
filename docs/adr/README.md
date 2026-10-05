# Architecture Decision Records

These ADRs are **drafts and malleable** until ynr reaches a working product. They follow the same
lifecycle as ynm's and ynf's:

1. **Draft** (now): each file is edited in place as a decision changes. No supersession chain.
   Defaults chosen without strong evidence are marked `(default, <date>)` in the Decision.
2. **Build**: as the system is built, dated notes go into each ADR's **Addenda** section rather
   than rewriting the Decision, so the trail of what was learned stays visible.
3. **Consolidate**: close to a working product, each ADR's Decision is rewritten to absorb its
   addenda, and the status moves to `accepted`. From then on changes get a new ADR.

Each ADR has: Status, Context, Decision, Alternatives, Consequences, Open questions, History, and
the FR/NFR ids from ADR-000 it satisfies.

| ADR | Decision |
|---|---|
| [000](000-requirements.md) | Functional and non-functional requirements cited by the other ADRs; a factory is a ynf lane, identified by where it is defined and its name |
| [001](001-positioning.md) | ynr is the factory's observation plane on OpenTelemetry: one source in two builds (full with DuckDB, slim for images), with collector, central and relay roles and its own dashboards; never memory, never control |
| [002](002-telemetry-model.md) | Signals, one trace per step linked across an item's steps through intake spans, started events, lane ids and repository attributes, people by handle, no content |
| [003](003-control-and-observation-planes.md) | CloudEvents stay the control plane, mirrored one way; provenance from spool folders and ynf's run manifests; run folders are hostile input under environment quotas; central trusts collectors, which are pools or hosts |
| [004](004-spool-detection-and-wiring.md) | Tools write OTLP JSON lines to a spool through public exporter packages; `ynr serve` deletes only what is committed; detection picks where to write and configuration decides what runs; the relay for vendor CLIs |
| [005](005-storage-and-dashboards.md) | Collectors ship JSON batches, an hour-first layout, compaction into Parquet with manifests and an item index, DuckDB in a hot tier, no topic, server-rendered htmx dashboards off by default |
| [006](006-sibling-instrumentation-contract.md) | What every YN* tool does to take part, and one issue per tool |
| [007](007-tool-owned-registries.md) | Each tool owns its registry, identified by its name and version; ynr learns registries only from configured binaries, and never rejects |
| [008](008-conformance-in-ci.md) | Strict in CI, lenient at runtime: `ynr conformance` with a stub vendor, installed from the tap, plus a full-chain check in ynf's factory image |
| [009](009-walking-skeleton.md) | Four slices, starting with one trace through the chain on a laptop |
