# 2. Start ynr serve

Start the process that reads the spool, ships what it finds to a store, and serves a dashboard. Then
watch `info` and `doctor` notice it.

## Start it

Use terminal A. Load the environment and start `ynr serve` with the dashboard on loopback:

```bash
. ~/ynr-tutorial/env.sh
ynr serve --ui 127.0.0.1:4319
```

Expected:

```text
ynr: dashboard at http://127.0.0.1:4319/
```

`ynr serve` runs in the foreground and logs to this terminal until you press Ctrl-C. It says
nothing more unless something is wrong: the OpenTelemetry Collector it is built on reports only
warnings and errors, as `ynr: collector warn: ...` lines. Leave it running.

It creates the spool the first time it starts. In terminal C, look:

```bash
. ~/ynr-tutorial/env.sh
ls -a ~/ynr-tutorial/state/ynr/spool
```

Expected:

```text
.
..
.ynr
local
```

`local` is the folder anything on a laptop writes into. `.ynr` holds `ynr serve`'s own files: its
lock, where it has read to in each spool file, and the hot tier's database.

## See that it noticed

Still in terminal C:

```bash
ynr info
```

Expected:

```text
version       v0.2.0-14-g47b1a89 (full)
capabilities  0.1.0
spool         /Users/you/ynr-tutorial/state/ynr/spool
serving       pid 64318 since 2026-10-08T10:30:54Z
```

`serving` now names the process. `info` reads it from the lock file `ynr serve` keeps in the spool.

```bash
ynr doctor
```

Expected:

```text
ynr v0.2.0-14-g47b1a89 (full)
ok    otlp      no OTEL_EXPORTER_OTLP_*ENDPOINT set, so tools write to ynr's spool
ok    path      ynr on PATH is this one: /Users/you/ynr-tutorial/bin/ynr
ok    spool     /Users/you/ynr-tutorial/state/ynr/spool
ok    serve     pid 64318 since 2026-10-08T10:30:54Z
ok    backlog   0 files, 0.0 MiB
ok    store     file:///Users/you/ynr-tutorial/data/ynr/store (0 rollup files)
ok    queries   ynr serve answers ynr query from its hot tier
```

Every line is `ok`, and `doctor` exits 0.

## Open the dashboard

Open http://127.0.0.1:4319/ in a browser. The page is titled Lanes, and it is empty: no run has
been reported yet. The dashboard listens on loopback only, so nothing else on your network can
reach it.

Next: [3. Send telemetry](03-send-telemetry.md).
