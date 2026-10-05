# ADR-003: Control and observation planes, and provenance

Status: draft (2026-10-05)
Satisfies: FR-4, FR-8, NFR-5, NFR-18, NFR-21

## Context

ynf acts on CloudEvents (ynf ADR-002): webhooks, topic messages and its own internal events. ynr
receives telemetry from anything that can write to it, and inside a factory job that includes the
agent. If the two were one channel, a prompt-injected agent could emit a forged `ynf.pr.merged`
and steer the factory, bypassing ynf ADR-007's rule that the agent never holds forge or tracker
write rights. And a dashboard is only trustworthy if it can tell what ynf recorded from what a
run claimed.

CloudEvents and OpenTelemetry are different standards for different jobs: CloudEvents carries
facts between systems to be acted on; OpenTelemetry describes how software ran.

## Decision

**Two planes.**

| Plane | Format | Who may write | Who acts on it |
|---|---|---|---|
| Control | CloudEvents | ynf's intake adapters only: signed webhooks, topics only trusted producers can write, ynf itself | ynf's decider |
| Observation | OpenTelemetry | any sibling, the vendor CLIs, the agent | nobody; it is reported |

**CloudEvents are mirrored into telemetry, one way.** ynf records every CloudEvent it receives as a
short `ynf.intake` span carrying a `ynf.intake.received` event, with the standard
`cloudevents.event_id`, `cloudevents.event_source`, `cloudevents.event_type` and
`cloudevents.event_subject` attributes and what ynf did with it: accepted, deduplicated or
rejected. ynf's mirror is the only source; ynr never subscribes to a control topic itself, so each
CloudEvent appears once.

**Telemetry never becomes a CloudEvent.** No component, ynr included, converts observation into
control. Limits that need fleet-wide numbers are computed in the control plane (NFR-5).

**Provenance comes from the folder a record was written in.** The spool (ADR-004) has one folder
per writer, and the folder is set by whoever starts the writer, not by the writer:

| Folder | Written by | Provenance |
|---|---|---|
| `factory/` | ynf | `factory` |
| `services/<service>/` | a long-lived hosted server, such as a hosted ynm, with its own `ynr serve` beside it | `service` |
| `runs/<run id>/` | one run: ynh, its relay, the vendor CLI, the agent | `run` |
| `local/` | anything on a laptop outside a factory | `local` |

In docker mode a run's container has only its own run folder mounted, so it cannot write
anywhere else and needs no network path to a collector. In inline mode the run's user can write
only its own run folder; that is as strong as ynf ADR-007's user separation, and inherits its
open question. ynr stamps `ynr.provenance` from the folder, overwriting anything the record says.

**A run folder is hostile input.** The agent can write files straight into its folder, so:
- **The environment enforces a quota** the agent cannot bypass: a size-limited volume in docker
  mode, a filesystem quota in inline mode. This belongs to ynf ADR-007, because a reader cannot
  stop a disk filling, and a full disk would stall ynf (NFR-21).
- **Where the runner cannot give per-run quotas,** as on hosted CI runners without the privileges
  for filesystem quotas or per-run mounts, the spool lives on a filesystem of its own, such as a
  tmpfs or a separate volume, apart from ynf's state, and ynf tolerates that filesystem being
  full (default, 2026-10-05). The guarantee is then weaker: one run can fill the spool and cost
  the job the rest of its telemetry, but it cannot stall ynf. ynf ADR-007 states which runners
  give which guarantee.
- **`ynr serve` is the backstop.** It stops reading a run folder that exceeds its budget and
  records a provenance warning.
- **`ynr serve` reads regular files only,** opens them without following links, refuses any
  file with more than one link, and checks each file's owner and device, so a planted link to
  `manifests/`, `factory/` or a host file is ignored and counted, never read, shipped or
  deleted. A file must be owned by its folder's owner or, in a run's folder, by the user the
  run's manifest names, since a run in an image writes as the image's user. It must be on the
  same device as its own folder, so a run's folder may be a size-limited volume of its own.
- **Lines are bounded** in length, and a malformed line is skipped and counted.

A hosted server's spans join a factory's trace through W3C headers, but they are shipped by a
different collector, so the full trace assembles at central rather than in a job's local
dashboard.

**Factory attributes come from ynf's run manifest.** For each run, ynf writes a manifest to
`manifests/<run id>.json`, a folder the run cannot reach, naming the run's lane id, harness, focus,
item and step, and the user the run writes as when that is not the folder's owner. ynr stamps those
attributes on everything from that run's folder, overwriting what the sender set. A run cannot claim
to belong to another factory.

**Central trusts collectors, not stamps.** A collector is a runner pool or a host, never a single
job: a GitHub Actions runner group, a worker pool, a hosted server's host, one developer's laptop.
Its id is set in `ynr serve`'s configuration and is stable. The job a record came from is data,
`ynr.collector.instance`, not identity. Each collector writes only its own segment of the object
store (ADR-005), so the segment is its verified identity. Central stamps `ynr.collector.id` from
it, and its configuration lists which collectors may carry `factory` or `service` provenance. A
record claiming either from any other collector, a laptop for instance, is marked `unverified`.

## Alternatives

- **One plane for both, filtered by attribute.** Not chosen: attributes are claims the sender
  makes.
- **Provenance from network receivers and per-run tokens.** Not chosen: a docker-mode run cannot
  reach a collector on the job's loopback, every endpoint would need a hole in the run's egress
  policy, and tokens are one more secret in the run. Folders need none of that.
- **Signed batches between collector and central.** Not needed while each collector's object
  store prefix is writable by it alone.

## Consequences

- ynf ADR-002 gains an addendum for the mirror; ynf ADR-007 gains the spool mount.
- Dashboards show provenance wherever a fact could have come from a run.

## Open questions

- None of its own; inline-mode strength follows ynf ADR-007's open question on separate users.

## History

- 2026-10-05: drafted.
