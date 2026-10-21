# At-least-once delivery

`ynr serve` can ship the same lines twice, and a vendor SDK can send the relay the same spans twice.
This page explains why that is the design, and how a count is never doubled.

## Why not exactly once

Between reading a spool file and recording that it was read, there is a gap. `ynr serve` uploads a
batch to the store, and only after the store has it does it record its position in the file and
delete the file once everything in it is committed. A crash in the gap re-reads the lines from the
last recorded position and ships them again. The alternative, recording the position first, would
lose the lines if the upload then failed. For telemetry about failing jobs, a duplicate is cheaper
than a gap.

The relay has the same property for a different reason: an SDK that times out and retries sends the
same request again.

## A batch carries its source

Each batch's key ends with where its lines came from: the spool file and the byte range within it.

```
traces/2026/10/08/10/ci-pool-a/01M4DH2D7WG82KEPG8SRHFCS3Y_local.ynr-relay-64344-000001-0-2657.jsonl.gz
                              └────── ship time ────────┘ └────────── source file ──────────┘ └ bytes ┘
```

The key starts with the time it was shipped, so a collector's keys sort in shipping order and a
reader can poll for what is new. Two batch files with the same source suffix are the same data, and
readers treat them as one.

## Records are deduplicated where they are read

A named query over batches not yet compacted keeps one copy of a record: a span by its trace id and
span id, and a log record or metric point by a hash of its content and resource. Compaction removes
duplicates the same way. So the counts and costs in the dashboard are not doubled by a re-shipped
batch, or by a retried request.

## Compaction supersedes, it never rewrites

An hour's batches are compacted into Parquet once the hour has closed and a grace of 10 minutes has
passed, to let late uploads in. The compaction writes the Parquet parts, then a manifest last, which
lists exactly which batches the part covers. Readers use a part for the batches its manifest lists,
and also read any batch in that hour the manifest does not list, so a file that lands late, from a
collector that was offline or retrying, is never invisible. The next compaction of the hour absorbs
it into a new part and a new manifest.

Batch files are never changed. They are deleted an hour after they were covered, so a reader already
holding them can finish. That is also why compaction needs no lock on the readers.

Two centrals could both decide to compact an hour. They agree with a lease object written by
conditional put: the holder takes `leases/compaction/<signal>/<yyyy>/<mm>/<dd>/<hh>.json`, a lease
that has expired may be taken over, and one that has not means someone else has the hour.

## What this buys, and costs

- A crash loses nothing that was committed, and a kill in the middle of a run leaves its `started`
  event and every batch already written.
- A late file is read, not lost, at the cost of readers listing an hour's batches as well as its
  manifest.
- A record shipped twice is stored twice until compaction. Storage is briefly larger, and queries pay
  to deduplicate.

See [ADR-005](../adr/005-storage-and-dashboards.md).
