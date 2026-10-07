# ADR-002: Telemetry model

Status: draft (2026-10-05)
Satisfies: FR-6, FR-7, NFR-6, NFR-12

## Context

Three tools in two languages describe the same work. Unless they use the same signals, the same
trace structure and the same attributes for the same things, one stream across them is only a
pile of records. The question an operator asks most, "what happened to this ticket", spans many
steps over days, not one trace.

## Decision

**Signals and what each is for:**

| Signal | Used for | Examples |
|---|---|---|
| Traces | the shape of a unit of work, and how long each part took | a ynf step with its claim, probe, decide and act; the `ynh agent run` it started |
| Events (log records with an event name) | discrete facts | `ynf.decision.made`, `ynh.run.started`, `ynf.intake.received` |
| Metrics | counts and totals | runs by outcome, tokens and cost by model, lease expiries |
| Logs | human-readable detail | the tools' existing structured logs, bridged into OpenTelemetry |

**Closed vocabularies, one prefix per tool.** `ynf.*`, `ynh.*`, `ynm.*` and `ynr.*` are each
owned by that tool alone, and declared in its own registry (ADR-007).

**Every unit of work announces its start.** Spans are written when they end, so a crash loses an
open span. Every unit of work therefore emits a `<tool>.<unit>.started` event when it begins
(`ynf.step.started`, `ynh.run.started`), and its span on completion. A start with no matching
span is how a crash shows up.

**One trace per step.** A ynf step is the root span and carries the item key, `step_id`, lane,
policy hash and lease epoch (ynf ADR-011). `ynh agent run` is its child, through `TRACEPARENT`.
The vendor CLI's spans are children of the run where the vendor honours `TRACEPARENT` (Claude
Code in headless mode is reported to; Codex is unverified, ADR-009). Where it does not, ynh hands
the vendor the run's id and trace id as resource attributes, so its records still join the run.
A call to ynm over HTTP carries W3C trace context. A run started by hand starts its own trace.

**An item's history is linked, not nested.** Receiving a CloudEvent is a short `ynf.intake` span
that carries the `ynf.intake.received` event (ADR-003). Each step span carries span links to the
item's previous step and to the `ynf.intake` span that triggered it; a link needs a span, so the
intake is one. ynf keeps the last step's trace and span ids on the item. Every span and event of a
step carries the item key, so "what happened to this ticket" is one query by item key, and the
links give its order.

**Factory and place.** Every record of factory work carries the factory it belongs to: `ynf.lane`
is the lane's id, where the lane is defined plus its name
(`github.com/example-org/factory-config#lint-paydown`, or `github.com/eyelock/ynh#docs-refresh` for a lane
a target repository defines itself). A lane may declare an explicit `id` that stays fixed if its
repository moves. `ynf.lane.harness` and `ynf.lane.focus` are what the run actually used, so a
target repository that overrides a lane shows up as a variant of the same factory. ynr stamps these
on receipt from ynf's run manifest, so a run cannot claim another factory (ADR-003). Where the work
happens is the repository in ynf's host-first form (`github.com/eyelock/ynh`,
`github.example.internal/example-org/x`), using OpenTelemetry's `vcs.*` attributes, so one factory across
many repositories and forges is one series broken down by repository. Staging and production are
told apart by `deployment.environment.name`.

**Who emitted.** Every tool sets `service.name`, `service.version` and `service.instance.id`. The
name and version also identify the tool's registry (ADR-007).

**Standard conventions first, pinned.** Where OpenTelemetry has a name, we use it: `gen_ai.*` for
model, effort and token usage, `vcs.*`, `cicd.*`, `cloudevents.*`. Several of these are still in
development, so each tool's registry pins the semantic-conventions version it follows, and names
may change when it moves.

**People appear by their handle,** never by name or email: the handle in the system where they
acted, qualified by its host (`github.com/octocat`, `example.atlassian.net/jdoe`, or the sign-in id
for ynm's audit events), in the standard `user.name` attribute, with the emitting tool's actor
kind attribute saying human, bot or ynf. Handles are personal data (NFR-16): retention bounds
them, the central dashboard requires sign-in, and an erasure list masks a handle in every query
at once and removes it at the next re-compaction (ADR-005). Joining one person's handles under a
single display handle is left for later.

**Cost** is what the runner reports. Neither ynr nor ynf prices tokens (ynf ADR-011).

**Logs flow without changing.** ynf bridges `log/slog` and ynm bridges its logger into
OpenTelemetry; stderr and `--log-file` output for people is unchanged.

**No content** (NFR-6). Prompts, completions, ticket text, code and memory bodies never appear.
Each tool redacts at the source before a record reaches the spool, and vendor CLIs are run with
their content options off. `ynr serve` runs a redaction processor as a second line, with its own
pattern list kept as data in ynr's configuration.

## Alternatives

- **One trace per item.** Not chosen: an item lives for days across instances, and a trace that
  long is unbounded and never complete.
- **A shared constants package per language.** Not chosen: a code dependency between siblings.
- **Redaction only in the collector.** Not chosen: an operator's own `OTEL_*` settings send data
  around the collector (NFR-3), and the vendor CLIs' names are not ours.

## Consequences

- Adding an event is a change to the emitting tool's registry, then a regeneration in that tool.
- Upgrading the pinned conventions is a deliberate change per tool, and dashboards read both
  versions through the registries ynr keeps (ADR-007).

## Open questions

- Which run events ynh emits beyond started and finished: each turn, each sensor verdict, each
  budget threshold?
- An operator-maintained map joining one person's handles across systems, for example under an
  LDAP id: when, and where it lives.

## History

- 2026-10-05: drafted.
