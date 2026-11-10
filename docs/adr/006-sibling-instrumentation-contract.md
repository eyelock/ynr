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
   order: another collector only when the operator has set `OTEL_EXPORTER_OTLP_*` explicitly,
   otherwise ynr's spool, otherwise the no-op providers.
2. **Describes itself** with `service.name`, `service.version` and `service.instance.id`, and
   honours the standard `OTEL_RESOURCE_ATTRIBUTES`.
3. **Joins the trace it was given.** A process reads `TRACEPARENT` and `TRACESTATE` from its
   environment; a server reads W3C trace context from request headers, MCP included. With none,
   it starts a trace. A vendor CLI that ignores an inbound `TRACEPARENT` is joined by other means
   (ADR-004: `ynh.session.id` in its resource attributes, and the relay stamping it).
4. **Passes the trace on, within the factory.** Every process it spawns gets `TRACEPARENT`, and
   every call to another of our tools (ynh, ynm, ynf, ynr) carries the W3C headers. Calls to
   third parties, such as a model API, an identity provider, a forge or a tracker, carry none:
   they never report to ynr, so the headers would only let an outside service correlate our
   requests. A process it spawns inherits its spool folder, unless the tool is the one giving
   that process a folder of its own (ynf for runs, ADR-004).
5. **Announces each unit of work** with a `started` event when it begins (ADR-002).
6. **Spans its boundaries, not its functions:** one span per unit of the tool's own work, and a
   child span per call out to another system (git, a forge, a tracker, ynm, a store, a model).
7. **Owns and declares its names** (ADR-007): a registry in its own repository, under its own
   prefix, pinned to an OpenTelemetry semantic-conventions version, with constants generated from
   it and `<tool> telemetry registry --format json` printing it.
8. **Keeps metric attributes low-cardinality.** Lane, outcome, model and service are fine; item
   keys, run ids and trace ids never go on a metric. Limits are declared in its registry
   (ADR-008). One named exception: `ynh.session.id` (rule 10) may reach a vendor's metrics as a
   resource attribute, since `ynh run` appends it to the vendor's resource attributes and a
   per-launch relay stamps it on every record, metrics included. It has one value per launch, and
   ynr drops it or rolls it up before rollups, so it never becomes a rollup dimension.
9. **Bridges its existing logger** into OpenTelemetry and keeps its human output unchanged.
10. **Exports no content** (NFR-6): no prompts, completions, ticket or comment text, code or
    memory bodies. Ids, enums, counts, durations and hashes only. People appear only by their
    host-qualified handle, never by name or email (ADR-002). It redacts secrets from string values
    at the source, before anything reaches the spool, and runs vendor CLIs with their content
    options off (the one exception is the tool-details option, below). No paths and no host names either: a tool never exports its working directory,
    the machine's name, or a path under the user's home, which names their account. Vendor CLIs
    cannot always be told this (Codex sends `cwd` and `host.name` and has no setting to stop it,
    and its logs carry `user.email`), so `ynr relay` enforces it for every vendor: before anything
    reaches the spool, it drops `user.email`, `user.full_name`, `cwd`, `host.name`, `host.id`, `host.ip`, `host.mac`, `process.owner`, `process.command`,
    `process.command_line`, `process.command_args` and `process.executable.path`, any attribute
    whose whole value is an email address, and any whose value is the user's home folder or an
    absolute path under it, from traces,
    metrics and logs (resource, scope, span, span event and link, data point and exemplar, and log
    record attributes). They are dropped, not hashed, with one exception: account ids (below).

    - **Tool details only behind the strip list.** A vendor's tool-detail option (Claude Code's
      `OTEL_LOG_TOOL_DETAILS`) may be on only when its telemetry reaches the spool through
      `ynr relay`, never straight to a spool or a backend. It logs what a tool was asked to do, so
      the relay strips `tool_input` and `tool_parameters` whole, wherever they appear, and keeps
      the names the option adds that identify a seat and no content: `agent.name`, `agent_type`
      and `subagent_type`. With the relay not in the path, the option stays off.
    - **Account ids are kept only as a keyed one-way hash.** `user.id`, `user.account_uuid`,
      `user.account_id` and `organization.id` are replaced by their HMAC-SHA-256 under
      one fixed secret key per Wheelhouse, held by the Wheelhouse's standing relay, so the same
      account groups together across records and cannot be read back. The value is the hex HMAC
      together with a short public key id derived from the key, so a reader can see which key
      produced it without learning the key. The key is a key file only, never the key's value in
      an environment variable: the relay is given the file by flag, or by an environment variable
      that names the file. The file must have mode 0600 and must not lie under the spool root,
      and the relay refuses one that does or that group or others can read. The key is never
      written to the spool, a log, a span or a metric, and never exported. With no key
      configured (a per-run or per-launch relay, such as that of `ynh agent run` or `ynh run` in
      ADR-004, has none) the relay drops these four attributes rather than keep them raw or hash
      them unkeyed (a plain hash of a guessable id can be reversed by trying ids). Losing the key
      means the relay can no longer reproduce the hashes: ids already hashed stay as they were
      and cannot be matched to new ones. Changing the key starts a new set of hashes the same
      way: records of one account before and after do not join, nothing in the spool can be
      re-hashed, and a report across the change counts that account twice. Neither exposes an
      id, so a key change is a deliberate, recorded act, not a repair.
    - **Run labels are ids the user chose, never content.** A tool that lets a user attach labels
      to a run exports each as the resource attribute `<tool prefix>.label.<key>` (ynh's are
      `ynh.label.<key>`), and only when it meets these limits:
      - a key matches `^[a-z][a-z0-9_.-]{0,31}$`;
      - a value matches `^[A-Za-z0-9._/#-]{1,64}$`, so it has no spaces and no `@`, and it neither
        starts with `/` or `~` nor contains the user's home folder;
      - a run has at most 16 labels;
      - a label never holds a person's name or email, a host name, a secret, or a run or item id.
        It is a short id for a Wheelhouse, a project or a batch: the key `wheelhouse` with the
        value `ynw`, for instance.

      The tool refuses an invalid label at the source, before anything reaches the spool; it does
      not export a sanitised version. This holds for every vendor the tool runs, since the label
      belongs to the run and not to a vendor's own telemetry. Resource attributes reach metrics,
      so rule 8's cardinality limit applies: a tool declares the cap on distinct label values in
      its registry (ADR-008).
    - **`ynh.session.id` is an id, not content.** The id `ynh run` gives a launch, set on its
      `ynh.session` span and appended to the vendor's resource attributes, and stamped by a
      per-launch relay on what it receives, is generated by ynh and holds no name, path or host.
      It joins records, and it is the one named exception to rule 8: it may sit on a vendor's
      metrics (one value per launch), and ynr drops or rolls it up before rollups.
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
| ynh | [eyelock/ynh#514](https://github.com/eyelock/ynh/issues/514) | `ynh agent run`: spool output, joining ynf's trace, the relay setting and vendor configuration, the slim `ynr` in the base image, conformance against the stub vendor; and `ynh run`'s own `ynh.session` record for every launch and vendor, with the vendor's telemetry wired to a relay and joined to it (ADR-004) |
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

- Whether the relay should keep a hashed form of a host or path attribute instead of dropping it,
  so runs on one machine can still be grouped. Until that is decided, it drops them.
- Whether the relay drops a vendor's `user.name`. ADR-002 puts a person's host-qualified handle
  in `user.name`, so a handle there is allowed, but a vendor may put a display name there. The
  relay keeps it for now.

## History

- 2026-10-05: drafted.
- 2026-10-31: no paths or host names in exported attributes, enforced for vendor CLIs by the relay
  (decided in the review of [ynh #649](https://github.com/eyelock/ynh/pull/649)).
- 2026-10-31: the relay also drops `user.email`, `user.full_name` and any value that is an email
  address, enforcing rule 10's ban on email addresses and names for vendor CLIs.
- 2026-11-01: rule 10 allows a vendor's tool details only behind the relay's strip list
  (`tool_input` and `tool_parameters` dropped whole), and keeps account ids (`user.id`,
  `user.account_uuid`, `user.account_id`, `organization.id`) only as a keyed HMAC-SHA-256, the key
  held outside the spool and the ids dropped when there is none. This settles the account-id
  question from [ynr #87](https://github.com/eyelock/ynr/pull/87).
- 2026-11-03: rule 10 says what a run label may be (a user-chosen id, with key and value patterns, at
  most 16 per run, refused at the source, rule 8's cardinality limit applying), for any vendor.
- 2026-11-07: any-vendor telemetry through ynh. The ynh row names `ynh run`'s own `ynh.session`
  record for every launch and vendor, the vendor's telemetry only enriching it; rule 3 notes that a
  vendor ignoring `TRACEPARENT` is joined by `ynh.session.id` and the relay's stamp; rule 10 says
  `ynh.session.id` is an id, not content, and that a per-launch relay holds no HMAC key. This
  supersedes ynw-8kl decision A1 as [ynr #95](https://github.com/eyelock/ynr/pull/95) recorded it
  (ynw-17g.27, from the design in ynw-17g.20).
- 2026-11-09: `ynh.session.id` is the one named exception to rule 8: allowed on vendor metrics, one
  value per launch, dropped or rolled up by ynr before rollups (ynw-j4a7, option B; ynw-8w20).
