# Diagnose with ynr doctor

Find out why records are not arriving, or not being answered. `ynr doctor` only reads: it never
starts, stops or changes anything.

## Run it

```bash
ynr doctor
```

Example, in a shell where `OTEL_EXPORTER_OTLP_ENDPOINT` is set and no `ynr serve` is running:

```text
ynr v0.2.0-14-g47b1a89 (full)
warn  otlp      OTEL_EXPORTER_OTLP_ENDPOINT set: tools export there instead of to ynr's spool
ok    path      ynr on PATH is this one: /Users/you/ynr-tutorial/bin/ynr
ok    spool     /Users/you/ynr-tutorial/state/ynr/spool
warn  serve     no ynr serve holds the spool, so nothing is shipped
ok    backlog   0 files, 0.0 MiB
ok    store     file:///Users/you/ynr-tutorial/data/ynr/store (0 rollup files)
warn  queries   no hot tier answering: ynr query reads the store directly, and needs the full build
```

The command exits 0 when every line is `ok` and 1 when any line is `warn` or `fail`. With `--format
json` it prints the version, the build and the checks as one object, for a script. It reads the spool
at `--spool` (or `YNR_SPOOL_ROOT`) and the store at `--store` (or `YNR_STORE`); pass the same ones
your `ynr serve` uses.

## Read each line

| Check | `ok` means | If it is not `ok` |
|---|---|---|
| `otlp` | No `OTEL_EXPORTER_OTLP_*ENDPOINT` is set in this environment. | Tools started from this environment export to that endpoint and the spool receives nothing from them. Unset it, or start the tool without it. Set globally for other tooling, this is a silent gap. |
| `path` | The `ynr` your shell finds is the one running. | `warn`: no `ynr` is on `PATH`, so other tools will not find one, or a different `ynr` is. Fix the `PATH`; tools find each other by bare name only. |
| `spool` | The spool exists and only its owner can open its `.ynr` folder. | `warn`: it does not exist yet (`ynr serve` creates it) or its permissions are wider than the owner's. `fail`: no spool root is set, or it cannot be read. |
| `serve` | A `ynr serve` holds the spool; the pid and start time are shown. | `warn`: none does, so nothing is shipped. Start `ynr serve`. |
| `backlog` | No closed spool file has waited long to be shipped. | `warn`: a closed file has waited over 5 minutes. `ynr serve` ships a closed file at once, so it is not running or cannot reach its store. |
| `store` | The store opens and lists. | `warn`: no store is set, so `ynr serve` ships only to an upstream. `fail`: the URL is invalid or the store cannot be listed, such as an S3 error or missing credentials. |
| `queries` | A running `ynr serve` answers `ynr query` from its hot tier. | `warn`: nothing answers. `ynr query` then reads the store directly, which needs the full build. The slim build can never answer. |

## After a fix

Run `ynr doctor` again. `serve` and `queries` turn `ok` as soon as a `ynr serve` is running on the
spool you are checking, which `ynr info` also reports as `serving`. If `backlog` warns, read the
terminal of the `ynr serve` that should be shipping for the error from the store, then run
`ynr doctor --store <url>` with the store URL it uses.

For what each file in the spool or the store should look like, see [Spool](../reference/spool.md)
and [Store](../reference/store.md).
