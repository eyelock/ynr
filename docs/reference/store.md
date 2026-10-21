# Store

The store is where `ynr serve` ships and where `ynr central` and `ynr query` read
([ADR-005](../adr/005-storage-and-dashboards.md)). It is a set of objects under keys.

## URLs

| Form | Store |
|---|---|
| `file:///path/to/folder` | A folder. On a laptop, the default is `$XDG_DATA_HOME/ynr/store`, or `~/.local/share/ynr/store`. |
| `s3://<bucket>[/<prefix>]?region=<region>` | An S3 bucket, with an optional key prefix. |

An S3 URL takes these parameters:

| Parameter | Meaning |
|---|---|
| `region` | The bucket's region. |
| `endpoint` | An S3-compatible server's address, such as `http://127.0.0.1:9000` for MinIO. |
| `path_style` | `true` for path-style requests, which MinIO needs. |
| `cache` | A folder to fetch objects into for DuckDB to read, instead of the default. |

Credentials come from the AWS SDK's usual chain: the environment, a profile, or the role of the
machine or job. Objects DuckDB reads are fetched into a local cache first, so the full build needs no
DuckDB extension to reach S3. Writing is conditional and never overwrites an object, except where a
key's meaning is to be replaced (leases, and the compaction that supersedes).

## Keys

Under the store's root (or prefix):

```text
<signal>/<yyyy>/<mm>/<dd>/<hh>/<collector id>/<ship ulid>_<source>-<from>-<to>.jsonl.gz     batches
compacted/<signal>/<yyyy>/<mm>/<dd>/<hh>/part-<n>.parquet                                   compacted parts
compacted/<signal>/<yyyy>/<mm>/<dd>/<hh>/_manifest-<n>.json                                 manifests
index/items/<yyyy>/<mm>/<dd>.parquet                                                        item index, daily
rollups/<runs|metrics>/daily/<yyyy>-<mm>-<dd>.parquet                                       rollups
rollups/<runs|metrics>/monthly/<yyyy>-<mm>.parquet
leases/compaction/<signal>/<yyyy>/<mm>/<dd>/<hh>.json                                       compaction leases
registries/<collector id>/<tool>/<version>/<sha256>.json                                    learned registries
```

| Part | Meaning |
|---|---|
| `<signal>` | `traces`, `logs` or `metrics`. Events are log records with an event name. |
| `<yyyy>/<mm>/<dd>/<hh>` | The UTC hour the batch was **received**, not the hour the events happened in. |
| `<collector id>` | The collector that shipped it; matches `^[a-z0-9][a-z0-9._-]{0,62}$`. |
| `<ship ulid>` | A ULID made when the batch was shipped, so a collector's keys sort in shipping order. |
| `<source>-<from>-<to>` | The spool file and the byte range of it the lines came from. Two batches with the same suffix are the same data. |
| `<n>` | The hour's n'th compaction, from 1. |

### Batches

A batch is OTLP JSON lines, gzip-compressed, in the spool's own format, with the `ynr.*` attributes
added by the collector. `ynr serve` ships one for each signal every 15 seconds or 16 MB, whichever
comes first, and nothing for a signal with no new records. Collectors write no Parquet.

### Compacted parts and manifests

Once an hour has closed, plus a grace of 10 minutes, the reader of the store compacts it: one Parquet
file for each signal, then a manifest, written last, that lists exactly which batches the part covers.
Readers use the parts for the batches a manifest lists, and still read any batch in that hour the
manifest does not list. The next compaction of the hour absorbs it into a new part and manifest.
Batches are deleted an hour after compaction.

A manifest names its `signal`, `hour`, its number `n`, the `parts` and `batches` it covers, the number
of `records`, and when it was `compacted`.

A part has one row for each record. For spans the columns are `record_id`, `time`, `end_time`,
`duration_ms`, `trace_id`, `span_id`, `parent_span_id`, `name`, `kind`, `status`, `status_message`,
then the promoted columns listed in [Named queries](named-queries.md#promoted-columns) (`service`,
`item_key`, `step_id`, `run_id`, `lane`, `harness`, `focus`, `repo`, `outcome`, `actor`, `model`,
`provenance`, `collector_id`), `cost_usd`, `scope`, the `resource` and `attributes` as lists of
key and value, and the batch `file` the record came from. Logs and metric points have the columns
that suit them.

### Item index

A Parquet file for each day with a row for each item and step in each hour: `item_key`, `hour`,
`signal`, `step_id`, `trace_id`, `first_seen`, `last_seen` and the `manifest` it came from.
`ynr query item` reads only the hours it names.

### Rollups

Daily and monthly aggregates of every metric by its attributes, and of every run (`ynh.run`) by lane,
model, outcome, harness and repository, with run counts, cost and token totals. A day is rolled up by
the hours it was received in, so a record is counted on one day.

### Leases

A JSON object written by conditional put before an hour is compacted: the holder and an expiry. An
expired lease may be taken over; a live one means another central is compacting that hour.

### Registries

A tool's `telemetry registry --format json`, written by `ynr serve` for each tool named in
`--registry-tools`, at the key its tool, version and content hash give. Two different registries for
the same tool and version sit side by side.

## What a reader accepts

A reader accepts only keys of the exact shape above under every prefix it reads: fixed depth, a real
date and hour, a well-formed collector id, and `part-` ending `.parquet` or `_manifest-` ending
`.json`. Anything else is ignored and counted. Retention never touches keys of no shape it knows, nor
`leases/` and `registries/`.

## Retention

| | Folder store | S3 |
|---|---|---|
| Batches, compacted parts, item index | `--retain`, 7 days by default | The bucket's lifecycle rule for each prefix; 30 days with the Terraform defaults |
| Size | `--retain-bytes`, 1 GiB by default; the oldest hour goes first | None |
| Rollups | 396 days (13 months) | 396 days with the Terraform defaults |
| Who applies it | `ynr serve` or `ynr central`, at start and every 10 minutes | S3 |

Retention is measured from when the records were received, by the span of their key: an hour for
batches and parts, a day for the index and daily rollups, a month for monthly rollups.
