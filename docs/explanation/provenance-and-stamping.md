# Provenance and stamping

A dashboard is only worth reading if it can tell what the factory recorded from what a run claimed.
This page explains how ynr decides where a record came from, and why it does not take the record's
word for it.

## Two planes

ynf acts on CloudEvents: webhooks, topic messages, its own events. ynr receives telemetry from
anything that can write to the spool, and in a factory job that includes the agent. If both were one
channel, a prompt-injected agent could emit a forged `ynf.pr.merged` and steer the factory, which
would bypass the rule that the agent never holds forge or tracker write rights.

So they are two planes. The control plane is CloudEvents, written only by ynf's intake adapters. The
observation plane is OpenTelemetry, written by anyone and acted on by nobody. ynf mirrors each
CloudEvent it receives into telemetry as a short `ynf.intake` span, one way, and ynr never subscribes
to a control topic itself. Nothing, ynr included, converts telemetry into a control event.

## The folder decides

A record's attributes are claims the sender makes. A folder is set by whoever starts the writer, not
by the writer. So ynr stamps `ynr.provenance` from the folder a file was in, overwriting anything the
record said:

| Folder | Provenance | Written by |
|---|---|---|
| `factory/` | `factory` | ynf |
| `services/<service>/` | `service` | a long-lived hosted server |
| `runs/<run id>/` | `run` | one run: ynh, its relay, the vendor CLI, the agent |
| `local/` | `local` | anything on a laptop outside a factory |

Every `ynr.*` attribute a sender set is removed first. In docker mode a run's container has only its
own run folder mounted, so it cannot write anywhere else.

## The manifest says which factory

A run's records also need to say which lane, harness, focus, item and step they belong to. A run must
not be able to claim to belong to another factory, so these come from a manifest ynf writes for each
run into a folder the run cannot reach, `manifests/<run id>.json`. ynr stamps them on everything from
that run's folder, replacing what the sender set. A run folder with no manifest, or a bad one, gets a
`ynr.provenance.warning` instead of factory attributes.

## A run folder is hostile input

The agent can write straight into its folder, so the reader reads nothing on trust:

- it reads regular files only, opened without following links, refusing any file with more than one
  link, so a planted link to `manifests/`, `factory/` or a host file is ignored and counted, never read,
  shipped or deleted;
- it checks each file's owner and device;
- it bounds lines in length and skips and counts a malformed one.

The environment, not ynr, enforces disk quotas on a run folder, because a reader cannot stop a disk
filling. Where the runner cannot give per-run quotas, the spool lives on a filesystem of its own, so a
run can fill it and cost the job its remaining telemetry, but cannot stall ynf. Those guarantees
belong to ynf.

## Central trusts collectors, not stamps

In the cloud, many machines ship to one bucket. A **collector** is a runner pool or a host, never a
single job, and its id is part of its configuration. Each collector may write only its own segment of
the bucket, so the segment is its verified identity, and central stamps `ynr.collector.id` from it.
The job a record came from is data, `ynr.collector.instance`, not identity.

Central's configuration lists which collectors may carry `factory` or `service` provenance. A record
claiming either from any other collector, a laptop for instance, is marked unverified. The reader
also accepts only keys of the exact shape, because an IAM policy cannot fix a key's depth.

## People appear by their handle

Records name people by their handle in the system where they acted, qualified by its host, such as
`github.com/octocat`, in the standard `user.name` attribute, never by name or email. Handles are still
personal data. Retention bounds them, the central dashboard requires sign-in, and an erasure list
masks a handle in every query and removes it from the store at the next compaction.

See [ADR-002](../adr/002-telemetry-model.md) and [ADR-003](../adr/003-control-and-observation-planes.md).
