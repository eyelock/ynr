# Named queries

The questions `ynr query` and the dashboard ask. Each is written once, in DuckDB SQL, over the
store's records. They need the full build. The list below is the catalogue in
`internal/query/query.go`.

```
ynr query <name> [argument] [--since <window>] [--until <time>] [--lane <id>] [--format text|json]
```

A **run** is a span named `ynh.run`: one `ynh agent run`, whether a factory or a person started it.
A **step** is identified by `ynf.step.id`, an **item** by `ynf.item.key`, and a **lane** by `ynf.lane`.

| Query | Argument | Default window | Reads | Limit |
|---|---|---|---|---|
| [`runs`](#runs) | none | 1 day | traces | none |
| [`recent`](#recent) | none | 1 day | traces | 200 rows |
| [`items`](#items) | none | 7 days | traces | 200 rows |
| [`item`](#item) | `<item key>` | 7 days | traces and logs | none |
| [`trace`](#trace) | `<trace id>` | 7 days | traces | tree depth 64 |
| [`cost`](#cost) | none | 7 days | traces | none |

`--since` is a duration back from now (`24h`, `7d`) or an RFC 3339 time; `--until` is a time and
defaults to now. `--since` must be before `--until`. A query that takes an argument needs it, and a
query that does not take one refuses it. `--lane` narrows `runs`, `recent`, `items` and `cost` to one
lane id; `item` and `trace` ignore it.

## runs

Runs by outcome for each lane.

| Column | Meaning |
|---|---|
| `lane` | The lane id, or `(by hand)` for a run no lane started. |
| `outcome` | The run's outcome, or `(none)`. |
| `runs` | How many runs. |
| `median_s` | The median duration in seconds. |
| `cost_usd` | The summed cost, as the runner reported it. |
| `last` | The time of the latest run. |

Ordered by lane, then runs descending, then outcome.

## recent

The latest runs, newest first.

| Column | Meaning |
|---|---|
| `time` | When the run started. |
| `lane` | The lane id, or `(by hand)`. |
| `item` | The work item key. |
| `outcome` | The outcome, or `(none)`. |
| `model` | The model. |
| `duration_s` | The duration in seconds. |
| `cost_usd` | The cost. |
| `trace_id` | The trace to pass to `trace`. |

## items

The work items seen lately, most recent first.

| Column | Meaning |
|---|---|
| `item` | The item key. |
| `lane` | A lane id seen for it. |
| `first_seen`, `last_seen` | The earliest and latest span time. |
| `steps` | How many distinct `ynf.step.id` values. |
| `traces` | How many distinct traces. |
| `last_outcome` | The outcome of the latest span that has one. |

## item

What happened to one work item: its spans and its events, in time order. The argument is the item
key, such as `github.com/eyelock/ynr#12`. It uses the item index, so it reads only the hours in which
the item appears.

| Column | Meaning |
|---|---|
| `time` | The span's start or the event's time. |
| `record` | `span` or `event`. |
| `name` | The span name, or the event name. |
| `service` | The service that wrote it. |
| `outcome` | The outcome, if any. |
| `duration_ms` | The span's duration in milliseconds; empty for an event. |
| `step_id` | The `ynf.step.id`. |
| `trace_id`, `span_id` | The ids. |

Events are log records with an event name (`event.name`, or the log record's own event name).

## trace

One trace's spans as a tree. The argument is the trace id, in any case.

| Column | Meaning |
|---|---|
| `depth` | The span's depth in the tree. The text output shows it as indentation of `name`, and the JSON output has the column. |
| `time` | The span's start. |
| `name`, `service`, `kind` | What the span is. |
| `status` | `ok`, `error` or `unset`. |
| `outcome` | The outcome, if any. |
| `duration_ms` | The duration in milliseconds. |
| `span_id`, `parent_span_id` | The ids. A span whose parent is not in the trace is a root. |

## cost

What runs cost and the tokens they used, by model, as the runners reported them: ynr never prices
tokens.

| Column | Meaning |
|---|---|
| `model` | `gen_ai.response.model`, else `gen_ai.request.model`, else `(unknown)`. |
| `runs` | How many runs. |
| `cost_usd` | The summed `ynh.run.cost_usd`. |
| `input_tokens` | The summed `gen_ai.usage.input_tokens`. |
| `output_tokens` | The summed `gen_ai.usage.output_tokens`. |

Ordered by cost, highest first.

## Long windows

`runs` and `cost` answer a window longer than 7 days from the daily and monthly rollups instead of
from records, in whole days. The `median_s` column is then empty, because a median cannot be rolled
up. The other queries always read records.

## Promoted columns

The queries read `spans`, `logs` and `metric_points`, which promote these attributes to columns. Where
a tool has its own name for the same thing, the first one present wins.

| Column | Attribute |
|---|---|
| `service` | `service.name` |
| `item_key` | `ynf.item.key` |
| `step_id` | `ynf.step.id` |
| `run_id` | `ynf.run.id` |
| `lane` | `ynf.lane` |
| `harness` | `ynf.lane.harness`, else `ynh.harness.name` |
| `focus` | `ynf.lane.focus`, else `ynh.run.focus` |
| `repo` | `ynf.repo`, else `vcs.repository.url.full` |
| `outcome` | `ynf.outcome`, else `ynh.run.outcome`, else `ynm.outcome` |
| `actor` | `user.name` |
| `model` | `gen_ai.response.model`, else `gen_ai.request.model` |
| `provenance` | `ynr.provenance` |
| `collector_id` | `ynr.collector.id` |

A span's `cost_usd` is `ynh.run.cost_usd`. A record is kept once: a span by its trace id and span id,
and a log record or metric point by a hash of its content and resource, so a batch shipped twice is
not counted twice. A handle on the erasure list reads as `(erased)` wherever it is the value of a `user.name`
attribute, `actor` included.
