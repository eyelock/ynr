# ADR-007: Tool-owned registries

Status: draft (2026-10-05)
Satisfies: FR-9, NFR-1, NFR-8

## Context

The names a tool emits, its events, attributes and metrics, are part of that tool's interface.
The tool knows what it does, and it may be public while ynr is private. ynr's job is to honour
what each tool says it will emit, not to define it, and never to let a sender tell it what to run
or fetch.

## Decision

**Each tool owns its registry.** A tool's names live in its own repository as an OpenTelemetry
Weaver registry, under its own prefix, pinned to the upstream semantic-conventions version it
follows. The tool generates its constants from it and checks its output against it in CI
(ADR-008). No tool builds against anything of ynr's.

**Prefixes are owned exclusively.** `ynh.*` belongs to ynh, `ynf.*` to ynf, `ynm.*` to ynm.
Standard OpenTelemetry names are shared by all. The factory attributes ynr stamps from ynf's run
manifest (`ynf.lane`, `ynf.lane.harness`, `ynf.lane.focus`, ADR-003) are ynf's names, declared in
ynf's registry; ynr applies them on ynf's behalf.

**ynr's registry covers only what ynr itself writes:** `ynr.provenance`, `ynr.collector.id`, and
ynr's own telemetry, such as dropped records and unknown names.

**A registry is identified by its tool's name and version.** The registry is embedded in the
tool's binary at build time and printed by `<tool> telemetry registry --format json`, so
`service.name` and `service.version`, which every record already carries, say which registry
applies. The resource's `schema_url` is left to the SDK, which uses it for the upstream
conventions version.

**ynr learns registries only from binaries its configuration names.** ynr's configuration lists
the tools to ask (`ynh`, `ynf`, `ynm`), each by its bare name on the shell's `PATH`, as every YN
tool finds every other one: what is installed, and which copy, is decided by `PATH`, never by
ynr's configuration. `ynr serve` asks
each once at startup, and again only when its configuration is reloaded. Nothing in a record ever
causes ynr to run a program or fetch a URL. A record whose tool and version have no learned
registry is kept, with its registry marked unknown.

**Central accepts registries only from trusted collectors.** `ynr serve` writes each registry it
learns to its own object-store prefix, keyed by tool, version and content hash
(`registries/<collector id>/<tool>/<version>/<sha256>.json`, ADR-005). Central reads registries
only from collectors its configuration trusts for `factory` or `service` provenance (ADR-003). Two
different registries for the same tool and version are both kept and shown as a conflict; neither
wins by arriving first.

**ynr honours, it never rejects.** At runtime ynr only checks each name against its registry's
list of names, a cheap lookup, and counts names the registry does not declare
(`ynr.registry.unknown_names`, by service, version and name, capped). Full validation of types,
values and requirement levels happens in CI, with Weaver. A record is never dropped for not
matching (NFR-1).

**Dashboards use what tools declare.** Where a view shows a name, it uses the registry's
description, unit and enumerated values. The first views are curated (ADR-005); a curated view
states the registry versions it needs and shows "not reported" when a tool's registry no longer
has a name.

## Alternatives

- **One registry for every tool, kept in ynr.** Not chosen: a tool would depend on ynr to describe
  itself, and a public tool on a private repository.
- **A shared public registry repository.** Not chosen: every tool change becomes a change to a
  repository the tool does not own.
- **A copy of one registry in each tool.** Not chosen: copies drift, and nobody owns the truth.
- **Learn a registry from a URL the record declares.** Not chosen: the run's records are
  untrusted, so a sender could make the collector run a program or fetch a URL of its choosing.
- **Identify the registry by the resource's `schema_url`.** Not chosen: the SDK already uses it
  for the upstream conventions version, and Go's SDK refuses to merge resources whose
  `schema_url` values differ.
- **Reject records that do not match.** Not chosen: telemetry must never fail a producer, and
  dropped data hides the drift worth seeing.

## Consequences

- Each tool gains a `telemetry registry` subcommand.
- ynr keeps registries beside the telemetry they describe, so old data stays readable after a
  tool renames something.

## Open questions

- When do dashboards gain generic views built from any registry, so a new tool's events appear
  with no change to ynr?

## History

- 2026-10-05: drafted.
