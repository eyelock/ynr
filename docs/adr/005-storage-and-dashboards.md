# ADR-005: Storage, central and dashboards

Status: draft (2026-10-05)
Satisfies: FR-3, FR-11, FR-12, FR-13, NFR-10, NFR-13 to NFR-20, NFR-22

## Context

Someone watching one job, or working on a laptop, needs to see that work now. Someone running
factories needs to see all of them, over time, no more than a minute behind. Both must work on a
laptop with nothing extra to run, and in the cloud with the same design (NFR-10). Collectors run
the slim build, without DuckDB (ADR-001).

## Decision

**The path.**

```
tools ──► spool ──► ynr serve ──────► object store ──────► ynr central (full build)
          (files)    ships batches     batches (JSON),      hot tier, compaction,
                     (slim or full)    then Parquet         item index, fleet UI
```

On a laptop, the full build's `ynr serve` plays both parts: it ships to a local folder
(`$XDG_DATA_HOME/ynr/store`, default), and keeps the hot tier, compaction and the local dashboard
over it.

**Three ports, each with a laptop adapter and a first cloud adapter:**

| Port | Laptop | First cloud adapter | Later |
|---|---|---|---|
| Object store | a folder (`file:///…`) | S3 (`s3://bucket/prefix?region=…`), which also covers S3-compatible stores such as MinIO and R2 | Google Cloud Storage, Azure Blob |
| Change feed: tells the reader new files landed | polling the folder | polling the current hour's prefix; S3 event notifications as an optional faster adapter | each cloud's own notifications |
| Write identity: each collector writes only its own segments | file permissions | one IAM role per collector (a runner pool or host, ADR-003), its writes limited to its own segments by a policy variable on a principal or session tag (below) | a service account, a managed identity |

Configuration is a URL, as in ynf ADR-004.

**Collectors ship the spool's own format.** `ynr serve` uploads compressed batches of OTLP JSON
lines, one file per signal every 15 seconds or 16 MB, whichever comes first (default), so the
reader stays within NFR-14. Collectors never write Parquet, which keeps DuckDB and cgo out of the
slim build. DuckDB reads compressed JSON lines directly, so a batch is queryable the moment it
lands. Nothing is shipped for a signal with no new records, so idle collectors add no files.

**Shipping is at least once, so batches carry their source.** A crash after a batch is committed
but before `ynr serve` records its position re-ships the same lines, and a vendor SDK that times
out and retries can send the relay the same spans twice. Each batch's key therefore ends with
where its lines came from: the spool file and the byte range within it. The key starts with the
time it was shipped, so a collector's keys sort in shipping order for polling. Two batch files
with the same source suffix are the same data, and readers and compaction treat them as one.
Compaction also removes duplicate records: spans by trace id and span id, log records and metric
points by a hash of their content. Named queries over batches not yet compacted dedupe the same
way, so counts and costs are never doubled.

**Layout.** Files are partitioned by the hour they were received, hour first, so a reader polls
one hour's prefix rather than every collector's:

```
<root>/<signal>/<yyyy>/<mm>/<dd>/<hh>/<collector id>/<ship ulid>_<source>-<from>-<to>.jsonl.gz
<root>/compacted/<signal>/<yyyy>/<mm>/<dd>/<hh>/part-<n>.parquet           compacted
<root>/compacted/<signal>/<yyyy>/<mm>/<dd>/<hh>/_manifest-<n>.json
<root>/rollups/metrics/<daily|monthly>/<period>.parquet                    metric rollups
<root>/index/items/<yyyy>/<mm>/<dd>.parquet                               item index, daily
<root>/registries/<collector id>/<tool>/<version>/<sha256>.json           ADR-007
```

Signal is `traces`, `logs` (events are log records with a name) or `metrics`. Event time is a
column; a query over an event-time window reads the receipt hours from the window's start to its
end plus a slack (1 hour, default).

**Collectors can write only their own batches and registries.** An IAM `*` matches across `/`, so
a pattern such as `*/<collector>/*` would also match keys under `compacted/`, `index/` and other
collectors' registries. Each collector's role therefore allows writes anchored per prefix
(`traces/*/<id>/*`, `logs/*/<id>/*`, `metrics/*/<id>/*` and `registries/<id>/*`) and explicitly
denies writes to `compacted/*`, `rollups/*` and `index/*`, which only central's role may write.
Because a policy still cannot fix a key's depth, the reader accepts only keys of the exact shape
above under every prefix it reads, batches, registries, compacted parts, rollups and the index
alike: fixed depth, valid date and hour segments, a well-formed collector id. Anything else is
ignored and counted.

**Schema.** One Parquet table per signal, modelled on the OpenTelemetry Collector's ClickHouse
exporter tables, which already map every OpenTelemetry field to columns: timestamp, trace and
span ids, name, kind, service, resource and record attributes as maps, duration, status, events
and links. ynr adds `received_at`, `provenance`, `collector_id` and `collector_instance`, and
promotes what dashboards filter on to columns: item key, step id, lane id, harness, focus,
repository, outcome and actor handle. The exact schema is settled in the walking skeleton
(ADR-009) and versioned in each file's metadata.

**One engine: DuckDB,** in the full build. It reads JSON lines and Parquet from a folder and from
S3, so there is one SQL dialect everywhere.
- **Hot tier.** The reader keeps recent records in a local DuckDB database: a laptop's `ynr serve`
  everything within laptop retention, `ynr central` the last 6 hours (default), fed by the change
  feed. It also holds the whole item index (below). The hot tier is a cache: on restart it is
  rebuilt from the spool or the object store, so losing it loses nothing (NFR-19).
- **Named queries** are written once in DuckDB SQL over the hot tier, the compacted Parquet and
  any uncompacted batches (runs by outcome per lane, an item's history, one trace, cost by
  model). Each is served by the running `ynr serve` or `ynr central` as a JSON endpoint, which is
  what both the dashboard and `ynr query` use (FR-13). Only one process may open a DuckDB file,
  so `ynr query` asks the running server, and reads the object store directly when none is
  running. Adapters only provide file access, so a new adapter never needs its own queries.

**Compaction supersedes, never rewrites.** Once an hour has closed, plus a grace for late uploads
(10 minutes, default), the reader of a store compacts it: `ynr central` for a shared store, a
laptop's `ynr serve` for its folder. It writes the hour's Parquet parts with DuckDB, then a
manifest last, listing exactly which batch files it covers. Readers use the compacted parts for
the files a manifest lists, and also read any batch in that hour the manifest does not list, so a
file that lands late, from a collector that was offline or retried, is never invisible. The next
compaction of that hour absorbs it into a new part and a new manifest. Batch files are never
changed; they are deleted after a further grace (1 hour, default) so a reader already holding them
can finish. Two central instances agree on who compacts an hour with a lease object written by
conditional put.

**Item index.** Compaction maintains a small index: each item key with its first and last seen
times, the hours it appears in, and the trace id of each step. It is small (items times steps),
so the reader's hot tier holds all of it, rebuilt on restart from one index file per day.
"What happened to this ticket" asks the hot tier which hours and traces are involved, then reads
only those hours, never a scan of the whole retention window.

**Metric rollups.** Compaction also writes daily and monthly aggregates of every metric, by its
attributes. Long-range views, such as cost by model this year, read the rollups. Hourly metric
detail is kept for 30 days and the rollups for 13 months (NFR-15).

**Retention** is per signal (NFR-15): lifecycle rules on each signal's prefixes for S3, and
deletion by age and total size for a folder.

**Erasure.** An erasure list in the reader's configuration names handles to remove (NFR-16).
Named queries mask them immediately. The affected hours and rollups are re-compacted without
them; the superseded parts and the batch files for those hours are then deleted, not merely
superseded. Where the bucket keeps object versions, a lifecycle rule expires noncurrent versions
after a day, so a deleted object does not linger as an old version. Erasure covers the object
store and the readers' hot tiers. It does not reach copies outside them: collector spools, which
hold data only until it is committed; ynf's run captures, under ynf ADR-010's retention; a
laptop's own folders; and any upstream OTLP backend an operator configures, which is the
operator's responsibility.

**Polling scales with collectors.** The polling change feed lists the current hour's collector
segments once, then keeps a listing position per collector and lists only keys after it, so a
poll reads what is new rather than the whole hour. Up to about 50 collectors (default,
2026-10-05) polling is enough; beyond that, the S3 event notification adapter is required.

**Central** is `ynr central --store <url>`. It holds no state that cannot be rebuilt (NFR-19), so
it can restart, or run twice, without losing data. Collectors never talk to it; they write to the
object store.

**No topic.** Nothing in the path needs one. Anyone who wants to react as files land subscribes to
the object store's own notifications.

**Dashboards are server-rendered pages,** built from parts rather than an embedded product:
- **Pages** are Go templates written with `templ`, made interactive with htmx, and embedded in the
  full build. No Node or front-end build step is involved, at build time or at runtime.
- **Each page fragment comes from a named query,** rendered on the server, so sorting, filtering
  and paging are DuckDB queries.
- **Charts** use ECharts, re-created whenever htmx swaps the fragment holding them, with uPlot
  for any time series that needs to redraw very fast.
- **The live tail** uses htmx's server-sent events extension.
- **The trace waterfall** is server-rendered, with one small script of our own for expanding,
  collapsing, hover details and zooming into a time range. Factory traces run to hundreds of
  spans, not tens of thousands, which keeps this tractable. If a view ever outgrows htmx, a
  single interactive component can be embedded in that page alone.

The first views are curated: lanes (runs by outcome, yield, and cost by model and repository),
an item's history, a trace, and the live tail. Both dashboards are off by default and each is
enabled on its own (`--ui <addr>`, with an environment fallback). The local dashboard binds to
loopback. The central dashboard requires sign-in, implementing ynm ADR-017's OIDC design in Go
(with `coreos/go-oidc`, default, 2026-10-05).

**Infrastructure.** ynr ships Terraform per cloud adapter, starting with `infra/aws`: a bucket with
public access blocked, encryption on and lifecycle rules per signal; one IAM role per collector,
with writes anchored per prefix and explicit denies as above, and a noncurrent-version expiry rule
if versioning is on; a central role that reads everything and writes only `compacted/`, `rollups/`
and `index/`. It follows the shape of ynm's existing buckets.

## Alternatives

- **A topic as the hub** (Kafka, SNS and SQS, NATS). Not chosen: there is no laptop equivalent,
  every job would need topic credentials, and no consumer needs per-record latency.
- **Collectors push over OTLP to central.** Not chosen: central would hold data in flight and
  become a single point of failure.
- **Collectors write Parquet.** Not chosen: it puts a Parquet writer, or DuckDB and cgo, into
  every run image, for files that live only until their hour is compacted.
- **Collector-first key layout** (`<collector>/<signal>/<hour>`). Not chosen: a reader would list
  every collector's prefix on every poll.
- **An existing backend for storage** (OpenObserve, ClickHouse). Not the default (ADR-001); both
  remain candidate adapters if volume outgrows files.
- **SQLite on a laptop.** Not chosen: a second engine and dialect beside DuckDB.
- **Managed table formats** (Iceberg in S3 Tables). Not now: they solve compaction, but not on a
  laptop.
- **A Svelte single-page app,** built with Vite and embedded, with views borrowed from
  otel-desktop-viewer. Not chosen: it brings a Node build step and a second language into the
  repository, for interactivity that factory-sized traces and mostly tabular views do not need.
- **An embedded dashboard product.** Not chosen. Rill is an application rather than a library,
  with no trace or item views. Perses has no DuckDB source. DuckDB's own UI is a SQL editor whose
  front end is not open source. Grafana, Superset and Metabase are separate servers. Evidence fixes
  its data when the site is built. DuckDB in the browser bypasses ynr's named queries.

## Consequences

- YNR's own storage is a layout, compaction, an item index and named queries; durability,
  querying and retention come from DuckDB and the object store.
- Freshness costs one small file per collector per signal every 15 seconds, until compaction.
- The trace waterfall is the one piece of front-end code we write and maintain ourselves.

## Open questions

- Authorisation in central: every signed-in user currently sees every lane, repository and
  handle. Should a team see only its own lanes?
- The schema and item index format are settled in the walking skeleton (ADR-009).

## History

- 2026-10-05: drafted.
