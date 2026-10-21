# Tutorial: ynr on a laptop

Six lessons that take telemetry from a command to an answer, all on your machine. You build ynr, start
`ynr serve`, send it a run the way a vendor CLI would, watch it arrive in the dashboard and the live
tail, ask what happened with `ynr query`, and look at what ynr leaves on disk.

The telemetry in this track is made by hand with `curl`, so there is no model, no API key, and no
ynh, ynm or ynf. They would write to the same place and be read the same way.

Each lesson builds on the one before. Every step is a command followed by what you should see.
Timestamps, process ids, the relay's port and your machine's name will differ from the ones shown.
Times that ynr prints are UTC.

## Lessons

| Lesson | What you will learn |
|---|---|
| [1. Build ynr and look around](01-build-and-look-around.md) | Build the full binary, keep everything in one folder, and ask `version`, `info` and `doctor` |
| [2. Start ynr serve](02-start-ynr-serve.md) | `ynr serve --ui`, what it creates, and how `info` and `doctor` change once it runs |
| [3. Send telemetry](03-send-telemetry.md) | `ynr relay`, and a `curl` that posts a run to it as OTLP/JSON |
| [4. Watch it arrive](04-watch-it-arrive.md) | The dashboard, `ynr tail`, and a second run |
| [5. Ask what happened](05-ask-what-happened.md) | `ynr query`: runs, recent, items, item, trace and cost |
| [6. What is on disk](06-what-is-on-disk.md) | Stopping everything, the store's files, and querying with nothing running |

## What you need

- **Go and git,** at the version in `go.mod`.
- **A C toolchain,** because the full build links DuckDB through cgo.
- **curl.**
- **Three terminal windows,** called A, B and C. A runs `ynr serve`, B runs `ynr relay`, and C is
  where you type everything else.
- **About twenty minutes.**

## Afterwards

Everything this track creates is in one folder, `~/ynr-tutorial`, apart from the clone. Delete
the folder and nothing remains.
