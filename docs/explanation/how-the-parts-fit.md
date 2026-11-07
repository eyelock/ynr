# How the parts fit

ynr is several small programs in one binary, and a layout of files that joins them. This page follows
one record from the tool that wrote it to the dashboard that shows it, and explains why the path is
the same on a laptop and in the cloud.

## The path

![How a record travels through ynr: tools and ynr relay write the spool, ynr serve ships it to the store, and a reader shows it](how-the-parts-fit-path.svg)

1. **A tool writes a file.** `ynh agent run`, ynf and ynm write OpenTelemetry JSON lines into their
   writer folder in the spool with a shared exporter: the Go module `spoolexporter`, or the npm package
   `@eyelock/otel-spool-exporter` in ynm's case. The spool is the only contract between a tool and
   ynr, and the folder a file is in decides its provenance. A vendor CLI such as Claude Code or Codex
   can only export over the network, so `ynr relay` receives it on a loopback port, strips the
   attributes that name a person, the machine or a path on it, and writes the same kind of file.
2. **`ynr serve` reads the spool, stamps and ships.** It reads every writer folder, adds where each
   record came from, and uploads compressed batches to the object store. With `--upstream` it also
   forwards to any OTLP/HTTP endpoint. It advances its position in a file only once the batch holding
   those lines is committed.
3. **The object store holds batches, then Parquet.** A batch is the spool's own format, so a
   collector never needs a Parquet writer or DuckDB. After an hour closes, the reader of the store
   compacts it into Parquet parts, keeps an item index, and writes daily and monthly rollups.
4. **A reader answers questions.** It keeps the recent records in a local DuckDB database, the hot
   tier, and runs the named queries over the hot tier, the compacted Parquet and any batches not yet
   compacted. The dashboard and `ynr query` use the same queries.

The reader is `ynr serve` on a laptop, and `ynr central` over a shared bucket. Collectors never talk
to central. They write to the bucket, and central reads from it.

## One design for a laptop and the cloud

On a laptop, the full build's `ynr serve` plays the collector's part and the reader's: it ships to a
folder, keeps the hot tier, compacts, and serves the local dashboard. In the cloud, the same code is
split across machines: many slim `ynr serve` collectors ship to a bucket, and `ynr central` reads it.

![One design for a laptop and the cloud: the same code, with three ports that differ](how-the-parts-fit-laptop-and-cloud.svg)

What differs is a small set of adapters behind three ports:

| Port | Laptop | Cloud |
|---|---|---|
| Object store | a folder | S3, and S3-compatible servers |
| Change feed, which tells the reader new files landed | listing the folder | listing the current hour's prefix |
| Write identity, so a collector writes only its own segment | file permissions | one IAM role for each collector |

The queries are written once against DuckDB and need only file access, so a new adapter never needs
its own. That is why the laptop is a complete test of the cloud design, and why a bug in one shows up
in the other.

## The hot tier is a cache

The hot tier is rebuilt from the spool or the store when the process starts, and compaction, the
index and the rollups can all be recomputed from the batches. So `ynr central` holds no state that
cannot be rebuilt: it can restart, or run twice, without losing anything. Two centrals agree on who
compacts an hour with a lease object written by a conditional put. The price is that a restart pays
for the rebuild, and that the hot tier is only as big as its window (7 days on a laptop, 6 hours
in central).

## Why there is no message bus

Nothing in the path needs a topic. A bus has no laptop equivalent, every job would need credentials
for it, and no consumer needs per-record latency: freshness costs one small file for each collector
and signal every 15 seconds until compaction. Anyone who wants to react as files land can subscribe
to the object store's own notifications.

The same reasoning keeps collectors from pushing OTLP to central. Central would then hold data in
flight and become a single point of failure.

## Observation, never control

Telemetry flows one way, from tools to the reader. Nothing a record says makes ynr run a program or
fetch a URL, and nothing ynf decides reads telemetry. That is what lets any tool, including an agent,
write to the spool without being able to steer the factory ([Provenance and stamping](provenance-and-stamping.md)
says how ynr keeps a record's claims apart from its origin).

## Further reading

- [ADR-001](../adr/001-positioning.md), what ynr is and is not, and the two builds.
- [ADR-003](../adr/003-control-and-observation-planes.md), the two planes and provenance.
- [ADR-004](../adr/004-spool-detection-and-wiring.md), the spool and the relay.
- [ADR-005](../adr/005-storage-and-dashboards.md), storage, central and dashboards, with the
  alternatives that were not chosen.
