# 6. What is on disk

Stop everything you started, look at the files ynr leaves behind, and ask a question with nothing
running.

## Stop the relay and ynr serve

In terminal B, press Ctrl-C. The relay flushes what it holds into the spool and says what it did:

```text
ynr relay: 2 accepted; refused 0 too large, 0 busy, 0 rate limited, 0 malformed; 0 records dropped by the spool
```

In terminal A, press Ctrl-C. `ynr serve` stops after a bounded flush and returns you to the prompt. Back in terminal C, check:

```bash
ynr info
```

Expected:

```text
version       v0.2.0-14-g47b1a89 (full)
capabilities  0.1.0
spool         /Users/you/ynr-tutorial/state/ynr/spool
serving       no
```

Nothing of yours is running now. You can close terminals A and B.

## The spool is empty

```bash
ls -la ~/ynr-tutorial/state/ynr/spool/local
```

Expected:

```text
total 0
drwx------@ 2 you  staff  64 Oct  8 11:31 .
drwxr-xr-x@ 4 you  staff 128 Oct  8 11:30 ..
```

The relay's file is gone. `ynr serve` deletes a closed spool file only once everything in it has
been committed to the store, and the relay closed its file when it stopped.

## The store holds what was shipped

```bash
cd ~/ynr-tutorial
find data -type f
```

Expected:

```text
data/ynr/store/traces/2026/10/08/10/local-your-host/01M4DH2D7WG82KEPG8SRHFCS3Y_local.ynr-relay-64344-000001-0-2657.jsonl.gz
```

One file: the two runs, which `ynr serve` shipped together. Read the path from the left:

| Part | Meaning |
|---|---|
| `traces` | the signal; the others are `logs` and `metrics` |
| `2026/10/08/10` | the date and hour (UTC) the batch was received |
| `local-your-host` | the collector that shipped it; on a laptop, `local-` and your machine's name |
| `01M4DH2D7WG82KEPG8SRHFCS3Y` | the time it was shipped, so a collector's files sort in shipping order |
| `local.ynr-relay-64344-000001-0-2657` | the spool file the lines came from, and the bytes of it: 0 to 2657 |
| `.jsonl.gz` | OTLP JSON lines, compressed |

To see inside it:

```bash
gzip -dc data/ynr/store/traces/2026/10/08/10/*/*.jsonl.gz | head -c 400
```

Expected:

```text
{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ynh"}},{"key":"service.version","value":{"stringValue":"0.10.0"}},{"key":"service.instance.id","value":{"stringValue":"tutorial-1"}},{"key":"ynr.provenance","value":{"stringValue":"local"}},{"key":"ynr.collector.id","value":{"stringValue":"local-your-host"}}
```

It is what you posted, plus two attributes ynr added: `ynr.provenance`, set from the folder the file
was in, and `ynr.collector.id`, set from the collector's identity.

## The state folder

```bash
find state -type f
```

Expected:

```text
state/ynr/spool/.ynr/hot.duckdb
state/ynr/spool/.ynr/positions.json
state/ynr/spool/.ynr/serve.lock
```

`hot.duckdb` is the hot tier, a cache that `ynr serve` rebuilds when it starts. `positions.json`
records how far into each spool file `ynr serve` has read. `serve.lock` is the lock `ynr info`
reads.

## Ask with nothing running

```bash
ynr query runs
```

Expected:

```text
lane                                     outcome    runs  median_s  cost_usd  last
github.com/example/factory#lint-paydown  budget     1     42        0.2144    2026-10-08 10:30:31
github.com/example/factory#lint-paydown  converged  1     42        0.0731    2026-10-08 10:30:17
```

With no `ynr serve` to ask, `ynr query` read the store's files directly. The answer is the same.

## Clean up

```bash
rm -rf ~/ynr-tutorial
```

That removes the binary, the spool, the store and the scripts. The clone of ynr is left where you
made it.

That is the whole path, on one machine: a tool writes, `ynr serve` ships, and `ynr query` and the
dashboard answer. To go on, the [how-to guides](../../how-to/README.md) cover running it with Claude
Code and sharing a store between machines, and [How the parts fit](../../explanation/how-the-parts-fit.md)
explains why it is built this way.
