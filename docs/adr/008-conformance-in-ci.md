# ADR-008: Conformance in CI

Status: draft (2026-10-05)
Satisfies: FR-10, NFR-1, NFR-2, NFR-3, NFR-6, NFR-11, NFR-12

## Context

ADR-006 lists what every tool promises, and ADR-007 makes each tool the owner of its names. A
promise nobody checks drifts. At runtime ynr is deliberately lenient: it keeps whatever arrives.
The strictness has to live before a release.

## Decision

**Strict in CI, lenient at runtime.** Every tool runs one shared conformance check in CI, as a
required status check on its main branch. A failure blocks the merge; it never affects ynr in
production.

**`ynr conformance` is the checker.** It creates a spool, runs the tool's scenarios against it,
reads what was written, and checks how the tool behaved:

| Contract rule (ADR-006) | Check |
|---|---|
| 1 Where to write | run with `OTEL_EXPORTER_OTLP_*` set, with only a spool, and with neither: telemetry arrives at the right place, and with neither nothing is written |
| 2 Resource | every record has `service.name`, `service.version` and `service.instance.id`; values in `OTEL_RESOURCE_ATTRIBUTES` arrive |
| 3, 4 Trace context | given `TRACEPARENT`, the tool's root span is its child; a spawned process or HTTP call carries it on |
| 5 Started events | every unit-of-work span has a matching `started` event; a scenario killed mid-run leaves its `started` event and every batch written before the kill |
| 7 Names | `<tool> telemetry registry --format json` matches the tool's own registry, and Weaver's live check passes every record against it |
| 8 Cardinality | no metric attribute exceeds the limit its registry declares (a `ynr.cardinality` annotation on the attribute), and none looks like an id (ULIDs, UUIDs, hashes, item keys) |
| 10 No content | canary strings planted in the inputs (prompt, ticket text, file contents, memory bodies, a fake secret) never appear in any record |
| 11 Outcomes | each unit-of-work span ends with an outcome from the tool's own vocabulary and a matching status |
| 12 Never blocks | with the spool on a full filesystem, and with an operator endpoint that accepts connections and never answers, the exit code is unchanged and the tool exits within its normal time plus the flush limit |
| 13 Never into memory | with ynm in the test, ynm's store holds the same records before and after the scenario, apart from those the scenario writes on purpose, and contains no canary string |
| 14 Starts nothing | with `ynr` on the path and no configuration asking for it, no `ynr` process is started |

Rules 6 (spans at boundaries) and 9 (logger bridged, human output unchanged) need judgement and
stay in review; the check prints span counts and names to make that review quick.

**Each tool supplies its scenarios; ynr supplies the checks.** A tool keeps a conformance file in
its repository:

```yaml
# .ynr/conformance.yaml
service: ynh
registry: telemetry/registry            # the tool's own Weaver registry (ADR-007)
vendor: ynr-stub-vendor                 # deterministic; no model, no secrets
vendor_aliases: [claude]                # optional: other names the stub answers to on PATH
units: [ynh.run]                        # optional: span names that are units of work (rules 5, 11)
scenarios:
  - name: agent run, converged
    run: ynh agent run --harness testdata/harness --task @canary:prompt
    expect: { outcome: converged }
  - name: agent run, budget
    run: ynh agent run --harness testdata/harness --max-turns 1 --task @canary:prompt
    expect: { outcome: budget }
```

Each scenario's command runs from the repository root, the folder `ynr conformance` is run from, so
paths such as `testdata/harness` work as written; its spool and scratch files are kept elsewhere.
The stub vendor is first on `PATH` under its own name and each alias, so a tool that runs `claude`
gets the stub. Without `units`, a unit of work is a span that is a direct child of the
`TRACEPARENT` span, or one a `<unit>.started` event names.

**No real model in CI.** `ynr conformance` ships a stub vendor CLI that follows a script: it
answers turns, emits OTLP like a vendor CLI to the relay, honours `TRACEPARENT`, and exits with a
chosen result. ynh's scenarios drive it, so a public repository's CI needs no model, no secret
and no luck.

**The full chain is a required check too.** ynf's factory-image CI runs one scenario through the
whole chain: a ynf step, `ynh agent run` with the relay on, and the stub vendor, then checks they
form one trace with the factory attributes stamped (FR-6). A second, scheduled job runs the same
chain with the real Claude Code and Codex CLIs, so a vendor change shows up within a day without
gating merges. It plants a canary prompt and a repository settings file that tries to turn prompt
logging on, and fails if the canary reaches the spool.

**Distributed like the siblings.** `ynr` is released with GoReleaser to its GitHub releases and
published through `eyelock/homebrew-tap`, as ynh, ynf and ynm are. A tool's CI downloads a pinned
release (`gh release download`), puts `ynr` and the stub vendor on `PATH`, and runs `ynr conformance`
as an ordinary step; there is no separate action to maintain, and no token is needed.

## Alternatives

- **Each tool writes its own checks.** Not chosen: the checks would drift exactly as the
  instrumentation would.
- **Reject non-conforming telemetry at runtime instead.** Not chosen: it breaks NFR-1 and hides
  drift.
- **Weaver's live check alone.** Kept as one check, but it covers only names; the behavioural
  rules need a harness that drives the tool.

## Consequences

- Every tool's required merge check depends on a pinned `ynr` release. This is the one
  dependency on ynr a sibling takes, and it is in CI only (NFR-8).
- ynr must publish a release through the tap before any tool can make the check required.
- A tool can run `ynr conformance` locally with the same result as CI.

## Open questions

- None of its own.

## History

- 2026-10-05: drafted.
