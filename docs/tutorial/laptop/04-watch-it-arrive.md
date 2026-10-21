# 4. Watch it arrive

See the run you posted in the dashboard and in `ynr tail`, then post a second run while you watch.

## The dashboard

Open http://127.0.0.1:4319/ and reload. The Lanes page now has one card, for
`github.com/example/factory#lint-paydown`, showing 1 run, 100% yield, a cost of $0.07 and a median
of 42 seconds. Below it are a chart of runs by outcome, a chart and table of cost by model, and a
Latest runs table with your run: its item, its outcome `converged`, its model, its cost and a
shortened trace id.

It can take up to about fifteen seconds for a run to appear after you post it: `ynr serve` ships
what it has read in batches.

Click the item `github.com/eyelock/ynr#12` in the table to see everything that happened to it, and
the trace id to see the trace's spans as a tree.

## ynr tail

`ynr tail` prints what tools write to the spool, as it is written. In terminal C:

```bash
ynr tail --from-start
```

Expected:

```text
10:30:17.000  span   ynh        ynh.run  lane=github.com/example/factory#lint-paydown  item=github.com/eyelock/ynr#12  outcome=converged  status=ok  42s  trace=5b8efff798038103d269b633813fc60c
10:30:17.000  span   ynh        claude_code.interaction  item=github.com/eyelock/ynr#12  status=ok  42s  trace=5b8efff798038103d269b633813fc60c
```

`--from-start` begins with what the spool already holds; without it, `ynr tail` shows only what
comes next. It keeps running until you press Ctrl-C, which you do now.

## A second run

Open http://127.0.0.1:4319/tail in your browser: the dashboard's live tail, which adds a line for
each record as it is shipped. Then, in terminal C, post a second run:

```bash
~/ynr-tutorial/post-run.sh 9a3c1f0e5d7b4a2c8e6f1d0b3a5c7e92 budget 0.2144
```

Expected:

```text
{}
```

This run ended with the outcome `budget`: it ran out of its allowance. Within a moment the live tail
in the browser shows it. Run `ynr tail` again, from the start:

```bash
ynr tail --from-start
```

Expected:

```text
10:30:17.000  span   ynh        ynh.run  lane=github.com/example/factory#lint-paydown  item=github.com/eyelock/ynr#12  outcome=converged  status=ok  42s  trace=5b8efff798038103d269b633813fc60c
10:30:17.000  span   ynh        claude_code.interaction  item=github.com/eyelock/ynr#12  status=ok  42s  trace=5b8efff798038103d269b633813fc60c
10:30:31.000  span   ynh        ynh.run  lane=github.com/example/factory#lint-paydown  item=github.com/eyelock/ynr#12  outcome=budget  status=ok  42s  trace=9a3c1f0e5d7b4a2c8e6f1d0b3a5c7e92
10:30:31.000  span   ynh        claude_code.interaction  item=github.com/eyelock/ynr#12  status=ok  42s  trace=9a3c1f0e5d7b4a2c8e6f1d0b3a5c7e92
```

Press Ctrl-C again. `ynr tail` only watches: `ynr serve` ships everything whether or not it is
running.

Next: [5. Ask what happened](05-ask-what-happened.md).
