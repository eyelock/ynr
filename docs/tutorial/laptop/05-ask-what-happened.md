# 5. Ask what happened

The dashboard and `ynr query` ask the same named queries. Use `ynr query` to put the two runs you
sent into words, one question at a time. Run these in terminal C. `ynr serve` is still running, so
`ynr query` asks it, and it answers from its hot tier.

## The list of questions

```bash
ynr query
```

Expected:

```text
Named queries (ynr query <name> [argument] [--since 7d] [--until <time>] [--lane <id>] [--format text|json]):
  cost              what runs cost and the tokens they used, by model, as the runners reported it (default --since 7d)
  item <item key>   what happened to one work item, by its key: its steps, runs and events in order (default --since 7d)
  items             the work items seen lately, most recent first, with their last outcome (default --since 7d)
  recent            the latest runs, newest first, with their trace ids (--lane to pick one) (default --since 1d)
  runs              runs by outcome for each lane (--lane to pick one) (default --since 1d)
  trace <trace id>  one trace's spans as a tree, by trace id (default --since 7d)
```

## Runs by outcome

```bash
ynr query runs
```

Expected:

```text
lane                                     outcome    runs  median_s  cost_usd  last
github.com/example/factory#lint-paydown  budget     1     42        0.2144    2026-10-08 10:30:31
github.com/example/factory#lint-paydown  converged  1     42        0.0731    2026-10-08 10:30:17
```

One line for each lane and outcome over the last day. A run's lane, outcome and cost come from the
attributes you posted: `ynf.lane`, `ynh.run.outcome` and `ynh.run.cost_usd`.

## The latest runs

```bash
ynr query recent
```

Expected:

```text
time                 lane                                     item                       outcome    model              duration_s  cost_usd  trace_id
2026-10-08 10:30:31  github.com/example/factory#lint-paydown  github.com/eyelock/ynr#12  budget     claude-sonnet-5-5  42          0.2144    9a3c1f0e5d7b4a2c8e6f1d0b3a5c7e92
2026-10-08 10:30:17  github.com/example/factory#lint-paydown  github.com/eyelock/ynr#12  converged  claude-sonnet-5-5  42          0.0731    5b8efff798038103d269b633813fc60c
```

Newest first, each with the trace id you can pass to `ynr query trace`.

## Work items

```bash
ynr query items
```

Expected:

```text
item                       lane                                     first_seen           last_seen            steps  traces  last_outcome
github.com/eyelock/ynr#12  github.com/example/factory#lint-paydown  2026-10-08 10:30:17  2026-10-08 10:30:31  0      2       budget
```

Both runs were about the same item, so there is one line: two traces, and the last outcome was
`budget`. `steps` is 0 because the runs you posted carry no `ynf.step.id`, which ynf sets.

## One item's history

```bash
ynr query item github.com/eyelock/ynr#12
```

Expected:

```text
time                 record  name                     service  outcome    duration_ms  step_id  trace_id                          span_id
2026-10-08 10:30:17  span    claude_code.interaction  ynh      -          42000        -        5b8efff798038103d269b633813fc60c  d269b633813fc60c
2026-10-08 10:30:17  span    ynh.run                  ynh      converged  42000        -        5b8efff798038103d269b633813fc60c  5b8efff798038103
2026-10-08 10:30:31  span    claude_code.interaction  ynh      -          42000        -        9a3c1f0e5d7b4a2c8e6f1d0b3a5c7e92  8e6f1d0b3a5c7e92
2026-10-08 10:30:31  span    ynh.run                  ynh      budget     42000        -        9a3c1f0e5d7b4a2c8e6f1d0b3a5c7e92  9a3c1f0e5d7b4a2c
```

Every span and event that carries the item's key, in time order.

## One trace

```bash
ynr query trace 9a3c1f0e5d7b4a2c8e6f1d0b3a5c7e92
```

Expected:

```text
time                 name                       service  kind      status  outcome  duration_ms  span_id           parent_span_id
2026-10-08 10:30:31  ynh.run                    ynh      internal  ok      budget   42000        9a3c1f0e5d7b4a2c  -
2026-10-08 10:30:31    claude_code.interaction  ynh      internal  ok      -        42000        8e6f1d0b3a5c7e92  9a3c1f0e5d7b4a2c
```

The spans as a tree: the child is indented under its parent.

## What it cost

```bash
ynr query cost
```

Expected:

```text
model              runs  cost_usd  input_tokens  output_tokens
claude-sonnet-5-5  2     0.2875    30400         4200
```

Cost and tokens by model, as the runs reported them: 0.0731 plus 0.2144, and twice 15200 input
and 2100 output tokens.

## The same answer as JSON

Add `--format json` to any query for something a program can read:

```bash
ynr query runs --since 1h --format json
```

Expected:

```text
{"query":"runs","since":"2026-10-08T09:31:27.375857Z","until":"2026-10-08T10:31:27.375857Z","columns":["lane","outcome","runs","median_s","cost_usd","last"],"rows":[{"lane":"github.com/example/factory#lint-paydown","outcome":"budget","runs":1,"median_s":42,"cost_usd":0.2144,"last":"2026-10-08T10:30:31Z"},{"lane":"github.com/example/factory#lint-paydown","outcome":"converged","runs":1,"median_s":42,"cost_usd":0.0731,"last":"2026-10-08T10:30:17Z"}]}
```

`--since 1h` narrowed the window to the last hour.

Next: [6. What is on disk](06-what-is-on-disk.md).
