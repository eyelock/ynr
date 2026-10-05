# ADR-006: The sibling instrumentation contract

Status: draft (2026-10-05)
Satisfies: FR-1, FR-6, NFR-1, NFR-2, NFR-3, NFR-6, NFR-8, NFR-11, NFR-12

## Context

ynh, ynm and ynf each need an OpenTelemetry design. Designed separately, they would drift: three
setups, three ways to join a trace, three ideas of what is safe to export. Designed here in full,
ynr would be dictating each tool's internals.

So this ADR is the contract: what any YN* tool, present or future, does to take part. Each tool
records how it meets the contract in its own documents, and the asks are tracked as one issue per
tool.

## Decision

**Every participating tool:**

1. **Sets up once, at process start,** with its language's official OpenTelemetry SDK and the
   spool exporter package for its language (ADR-004), choosing where to write in ADR-004's
   order: the operator's `OTEL_EXPORTER_OTLP_*`, then the spool, then the no-op providers.
2. **Describes itself** with `service.name`, `service.version` and `service.instance.id`, and
   honours the standard `OTEL_RESOURCE_ATTRIBUTES`.
3. **Joins the trace it was given.** A process reads `TRACEPARENT` and `TRACESTATE` from its
   environment; a server reads W3C trace context from request headers, MCP included. With none,
   it starts a trace.
4. **Passes the trace on.** Every process it spawns gets `TRACEPARENT`, and every HTTP call
   carries the headers. A process it spawns inherits its spool folder, unless the tool is the one
   giving that process a folder of its own (ynf for runs, ADR-004).
5. **Announces each unit of work** with a `started` event when it begins (ADR-002).
6. **Spans its boundaries, not its functions:** one span per unit of the tool's own work, and a
   child span per call out to another system (git, a forge, a tracker, ynm, a store, a model).
7. **Owns and declares its names** (ADR-007): a registry in its own repository, under its own
   prefix, pinned to an OpenTelemetry semantic-conventions version, with constants generated from
   it and `<tool> telemetry registry --format json` printing it.
8. **Keeps metric attributes low-cardinality.** Lane, outcome, model and service are fine; item
   keys, run ids and trace ids never go on a metric. Limits are declared in its registry
   (ADR-008).
9. **Bridges its existing logger** into OpenTelemetry and keeps its human output unchanged.
10. **Exports no content** (NFR-6): no prompts, completions, ticket or comment text, code or
    memory bodies. Ids, enums, counts, durations and hashes only. People appear only by their
    host-qualified handle, never by name or email (ADR-002). It redacts secrets from string values
    at the source, before anything reaches the spool, and runs vendor CLIs with their content
    options off.
11. **Records outcomes, not just errors.** A unit of work's span ends with the tool's own outcome
    vocabulary as an attribute (ynf's `converged`, `budget`, `stuck`, …) and its status set from
    it.
12. **Never blocks and never fails because of telemetry** (NFR-1). Writes are batched, its spool
    files are capped, the flush on exit is bounded (2 seconds, default), errors are swallowed and
    counted, and telemetry never changes an exit code.
13. **Never writes telemetry into memory, and never acts on it** (NFR-4, NFR-5).
14. **Starts nothing because it found ynr** (NFR-11). It starts `ynr serve` or `ynr relay` only
    when its configuration says so.
15. **Proves it in CI** with `ynr conformance` (ADR-008).

**Each tool records its own design where its decisions live:**

| Tool | Where | Why there |
|---|---|---|
| ynm | a new ADR | ynm's ADRs were consolidated at v0.1.0, so changes get a new ADR |
| ynf | addenda to ADR-002, 007, 009, 010, 011 and 012 | ynf's ADRs are drafts in the build phase |
| ynh | a docs page | ynh records decisions in its docs |

**The asks, one issue per tool:**

| Tool | Issue | Scope |
|---|---|---|
| ynh | [eyelock/ynh#514](https://github.com/eyelock/ynh/issues/514) | `ynh agent run` only: spool output, joining ynf's trace, the relay setting and vendor configuration, the slim `ynr` in the base image, conformance against the stub vendor |
| ynf | [eyelock/ynf#86](https://github.com/eyelock/ynf/issues/86) | step traces linked through intake spans, the CloudEvents mirror, lane ids, run folders, quotas and manifests, starting `ynr serve` when configured, the full-chain conformance check |
| ynm | [eyelock/ynm#62](https://github.com/eyelock/ynm/issues/62) | an `otel` audit provider, server spans, trace context in, spool output, `services/ynm/` when hosted, a lazily loaded SDK |

## Alternatives

- **Each tool designs its own telemetry.** Not chosen: names, propagation and content rules would
  drift, and the dashboards would have to know each tool's dialect.
- **ynr writes the instrumentation in each repository.** Not chosen: it couples ynr to every
  tool's internals and breaks NFR-8.

## Consequences

- A new YN* tool joins by meeting this list; nothing in ynr changes.
- A tool that meets the contract also works with any OpenTelemetry backend, without ynr.

## Open questions

- None of its own.

## History

- 2026-10-05: drafted.
