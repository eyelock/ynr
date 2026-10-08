# Set retention and erase a person

Change how long a store keeps records, and remove a person's handle from every query and from the
store. Retention is done by ynr for a folder store and by the bucket for an S3 store; erasure works
the same on both.

## What you need

- The full build for erasure, which works through DuckDB's queries and compaction ([Install ynr](install.md)).
  Retention of a folder store works in both builds.
- The handle exactly as it appears in `user.name` on the records: the person's handle in the system
  where they acted, qualified by its host, such as `github.com/octocat`.

## Set retention on a folder store

A folder store keeps records for 7 days, at most 1 GiB, and rollups for 13 months. ynr applies this
when `ynr serve` or `ynr central` starts and every ten minutes after. Change the first two with
flags:

```bash
ynr serve --retain 72h --retain-bytes 536870912
```

`--retain` is how long batches, compacted parts and the item index are kept. `--retain-bytes` caps
their total size; when the store is over it, the oldest hour goes first, whole, so an hour never
keeps a manifest without its part. `0` removes the size cap. Rollups are always kept for 13 months,
and `--retain` does not change that. The same flags exist on `ynr central`, which applies them only
when its store is a folder.

`--hot-window` is separate: it sets how far back the hot tier holds records for queries (7 days on
`ynr serve`, 6 hours on `ynr central`), not how long the store keeps them.

## Set retention on an S3 bucket

ynr does not delete from a bucket. The bucket's lifecycle rules are its retention, and the Terraform
in `infra/aws` makes one rule for each prefix. Set the days in `retention_days`:

```hcl
retention_days = { logs = 90, compacted_logs = 90 }
```

Unset prefixes keep their defaults: 30 days for batches, compacted parts and the item index,
396 days (13 months) for rollups, 7 days for leases, and never for registries. Events are log
records, so they live under `logs/` and take its retention. See
[Deploy the AWS infrastructure](deploy-the-aws-infrastructure.md).

## Erase a person's handle

Write the handles to erase in a file, one on each line. Blank lines and lines starting with `#` are
ignored; a handle has no spaces:

```text
# left the team 2026-10
github.com/octocat
```

Start every process that reads the store with the file:

```bash
ynr serve --erase ~/erase.txt
ynr central --store "s3://my-ynr-bucket?region=eu-west-2" --erase /etc/ynr/erase.txt
ynr query runs --erase ~/erase.txt        # a direct read of the store with no server running
```

`YNR_ERASE` sets it for each. A bad line stops the command with exit code 30 and names the line.

From then on the named handle reads as `(erased)` wherever it is the value of a `user.name` attribute. At the next compaction, ynr
re-compacts the hours and rollups that hold the handle without it, then deletes the superseded parts
and the batch files for those hours, rather than leaving them superseded. If the bucket keeps object
versions, the Terraform expires noncurrent versions after a day (`noncurrent_version_days`), so a
deleted object does not linger.

Erasure reaches the object store and the hot tiers. It does not reach collector spools, which hold
data only until it is committed; ynf's run captures, under ynf's retention; a laptop's own folders
outside the store; or any upstream OTLP backend you forward to with `--upstream`, which is yours to
clear.

Keep the file in place for as long as the records may still exist: the list is what masks the handle
for a reader, including a hot tier that is rebuilt from the store.
