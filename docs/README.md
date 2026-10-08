# ynr

**Your named reporting.** The observation plane for the ynh, ynm and ynf software factory, built on
OpenTelemetry. The tools write what they did into a crash-safe file spool; `ynr serve` reads it,
stamps where each record came from, ships it to a store, and, in the full build, answers questions
about it: runs by outcome for each lane, what happened to one ticket, one trace, what the models
cost.

```bash
brew install eyelock/tap/ynr        # the slim build: relay, serve as a shipper, tail, conformance
make install                        # from a clone: the full build, with DuckDB, the dashboard and ynr query
ynr serve --ui 127.0.0.1:4319       # read the spool, ship it, and serve a dashboard on loopback
```

## What ynr is, and is not

ynr reports what the tools did. The other three tools do different jobs:

- **[ynh](https://github.com/eyelock/ynh)** runs one agent against a harness.
- **[ynm](https://github.com/eyelock/ynm)** remembers.
- **[ynf](https://github.com/eyelock/ynf)** decides what happens next.

ynr is **not memory**: telemetry is never written to ynm. It is **not control**: nothing ynf decides
reads telemetry, so an agent that can emit telemetry cannot steer the factory. It is **not
required**: every other tool runs unchanged without it. And it is **not self-starting**: no tool
starts ynr because it found it; you run it, or a configuration you wrote does.

## Two builds

One source, two binaries ([ADR-001](adr/001-positioning.md)).

| Build | Has | Install |
|---|---|---|
| **slim** | `relay`, `serve` as a shipper to a store, `info`, `doctor`, `tail`, `conformance`, `telemetry`. No cgo, no DuckDB. | `brew install eyelock/tap/ynr`, or a release download |
| **full** | The slim build's commands plus DuckDB: the hot tier, `query`, the dashboard (`serve --ui`), compaction, and `central`. | `make install` from a clone |

The releases and the Homebrew formula carry the slim build today. Anything that needs the full build
says so and, in the slim build, refuses with a message rather than doing less.
[Install ynr](how-to/install.md) has the three routes.

## What is here

This documentation follows [Diátaxis](https://diataxis.fr), as ynm's and ynf's does: four kinds of
page, each with one job.

| | |
|---|---|
| [Tutorials](tutorial/README.md) | One track of lessons: ynr on a laptop, from a build to your first query. |
| [How-to guides](how-to/README.md) | Install ynr; run it with Claude Code; run central on S3; add conformance to a tool's CI; set retention; diagnose; deploy the AWS infrastructure; cut a release. |
| [Reference](reference/README.md) | The CLI, the named queries, the spool, the store, environment variables and sign-in. |
| [Explanation](explanation/README.md) | How the parts fit, and why a spool, provenance, at-least-once delivery and two builds. |
| [Architecture decisions](adr/README.md) | Each decision, its alternatives and its consequences, with the requirements they cite. |

New to ynr? Start with the [tutorial](tutorial/laptop/README.md).

## The shape of it

- **Tools write files; ynr reads them.** The spool is a folder of OpenTelemetry JSON lines, one
  subfolder per writer. A tool that dies leaves every batch it had written.
- **The folder decides what a record is.** Provenance comes from where the file was, never from
  what the record says.
- **Shipping is at least once.** A batch's name carries the spool file and byte range it came from,
  so a re-sent batch is recognisably the same data and is counted once.
- **A laptop and the cloud are the same design.** The store is a folder or an S3 bucket;
  `ynr serve` plays both parts on a laptop, and `ynr central` reads a shared bucket.
- **Dashboards are off until you ask.** `--ui` serves a loopback dashboard; central's requires
  sign-in.
- **ynr observes the other tools and takes nothing from them.** A tool finds `ynr` only by its bare
  name on `PATH`, and works without it.
