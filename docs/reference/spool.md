# Spool

The spool is a folder of OpenTelemetry JSON lines, with one subfolder for each writer. It is the only
contract between a tool and ynr ([ADR-004](../adr/004-spool-detection-and-wiring.md)).

## Location

The spool root is, in order: `--spool`, `YNR_SPOOL_ROOT`, `$XDG_STATE_HOME/ynr/spool`, then
`~/.local/state/ynr/spool`. A tool writes into one writer folder under it, named by `YNR_SPOOL`, or by
default `<root>/local`.

## Layout

```text
<root>/
  factory/                    written by ynf, outside any run
  services/<service>/         written by a long-lived hosted server
  runs/<run id>/              written inside one run
  local/                      written on a laptop outside a factory
  manifests/<run id>.json     ynf's manifest for a run; the run cannot reach this folder
  .ynr/                       ynr serve's own state
```

| Folder | Provenance (`ynr.provenance`) |
|---|---|
| `factory/` | `factory` |
| `services/<service>/` | `service` |
| `runs/<run id>/` | `run` |
| `local/` | `local` |

A service or run name matches `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`. Anything else at the root, a
folder with another name, a name that does not match, and any folder that is a link rather than a
directory, is ignored and counted.

### The state folder `.ynr`

| File | Holds |
|---|---|
| `serve.lock` | The pid and start time of the `ynr serve` that holds the spool. `ynr info` and `ynr doctor` read it. |
| `positions.json` | How far each spool file has been committed, by the file's device and inode, so a rename does not lose the position. A damaged file means re-reading from the start. |
| `hot.duckdb`, `hot.duckdb.wal` | The hot tier's database (full build). A cache. |
| `serve.sock` | The Unix socket that answers `ynr query` (full build). Under the temporary directory instead when the path is too long. |

## Files

Each process writes its own files in its writer folder, so there is no locking:

```text
<service>-<instance id>-<seq>.open.jsonl    the file being written
<service>-<instance id>-<seq>.jsonl         a closed file
```

`ynr relay` names its files `ynr-relay-<pid>-<seq>`.

Each line is one OTLP export request in the OTLP/JSON encoding: an `ExportTraceServiceRequest`
(`resourceSpans`), an `ExportMetricsServiceRequest` (`resourceMetrics`) or an
`ExportLogsServiceRequest` (`resourceLogs`). Trace and span ids are lowercase hex, 64-bit integers and
timestamps are decimal strings, and enums are numbers, as the OTLP specification requires of JSON. All
three signals share a file.

## What the writers guarantee

These are the defaults of the `spoolexporter` Go module and the `@eyelock/otel-spool-exporter` npm
package.

| | Default |
|---|---|
| A file is rotated, renamed from `.open.jsonl` to `.jsonl`, when the next line would pass | 8 MiB |
| A line is larger than this and is dropped and counted | 4 MiB |
| A writer's files on disk are capped at; over it, new records are dropped and counted | 64 MiB |
| A flush to disk is abandoned after | 2 seconds |

A line is written with one write call, so a batch already written survives the process being killed.
Files are created exclusively and never through a link planted at their name. Every error is swallowed
and counted, and an exporter never returns one.

## What `ynr serve` does to it

| Rule | Value |
|---|---|
| Reads | regular files only, opened without following links, with one link, owned by the folder's owner (or, in a run's folder, the user its manifest names) and on the folder's device |
| Reads open files up to | their last complete line |
| Longest line | `--max-line`, 4 MiB; a longer or malformed line is skipped and counted |
| Whole spool cap | `--spool-cap`, 1 GiB; over it, the oldest closed files are evicted first and counted in `ynr.spool.evicted` |
| Deletes a closed file | only when everything in it is committed to the store (or to the upstream, if there is no store) |
| Poll | `--poll`, 1 second |

A file the rules refuse is ignored and counted, never read, shipped or deleted.

## Choosing where to write

A tool picks once, at start, the first that applies ([ADR-004](../adr/004-spool-detection-and-wiring.md)):

1. `OTEL_EXPORTER_OTLP_*` set explicitly: export to that endpoint, not to ynr.
2. `YNR_SPOOL` names a writer folder, or `<state>/ynr/spool/local` exists: write there.
3. Otherwise: the SDK's no-op providers. Nothing is written and nothing fails.

A long-lived process that found no spool checks again once a minute.

## A run's manifest

`manifests/<run id>.json` is written by ynf, outside the run's reach, and read by `ynr serve` with the
same safety rules as any spool file, up to 64 KiB:

| Field | Meaning |
|---|---|
| `run` | The run id. It must equal the file's name. |
| `lane` | The lane id. Required. |
| `harness`, `focus` | What the run used. |
| `item` | The work item key. |
| `step` | The step id. |
| `uid` | Optional: the user the run writes as, when that is not the folder's owner. |

From it `ynr serve` stamps `ynf.run.id`, `ynf.lane`, `ynf.lane.harness`, `ynf.lane.focus`,
`ynf.item.key` and `ynf.step.id` on everything from that run's folder, replacing what the sender set.
