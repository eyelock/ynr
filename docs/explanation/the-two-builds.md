# The two builds

ynr is one source built two ways. This page explains why, and what each build is for.

## Why two

The query engine is DuckDB, which needs cgo. That is fine on a laptop and on the machine running
central. It is a cost in an image that exists to run an agent: a C toolchain to build, a larger
binary, and a dependency in every run image. But those images need ynr's other half, the relay, and
the collector that ships a job's spool, and those need no query engine.

So the source builds twice:

| Build | Has | For |
|---|---|---|
| **full** | Everything, including DuckDB: the hot tier, `query`, the dashboards, `central`, compaction | Laptops and central |
| **slim** | No cgo, no DuckDB: `relay`, `serve` as a shipper, `info`, `doctor`, `tail`, `telemetry` and `conformance` | The base image of ynh and so every run and factory job; CI |

The slim build ships batches in the spool's own format. A collector never writes Parquet, which keeps
DuckDB and cgo out of every run image: the batches live only until their hour is compacted.

## The slim build refuses loudly

A build that quietly did less would be worse than one that says it cannot. In the slim build,
`--ui`, `central` and `query` stop with a message that names the full build. `ynr info` reports which
build it is, so a script can ask. `ynr query` in the slim build can still ask a running full `ynr serve`
over its socket, since asking needs no DuckDB.

## One source

Both are the same Go source. Anything using DuckDB is behind a `full` build tag, with a stub for the
slim build that returns the error. CI builds and tests both, and checks the slim build never links
DuckDB. A feature in one build is therefore a feature in the other wherever it needs no engine,
and `ynr conformance`, which a tool's CI runs, is slim so that it needs no toolchain.

## Which is distributed

Each release carries both builds as separate archives. The slim archives, `ynr_<version>_<os>_<arch>`
(`CGO_ENABLED=0`), are what ynh's image and a tool's CI download, and their names do not change. The
full archives, `ynr-full_<version>_<os>_<arch>`, hold the build with DuckDB, and the Homebrew formula
installs that one, so `brew install eyelock/tap/ynr` has `query`, `central` and `--ui`. The release
workflow builds the full binaries for all four platforms on one macOS runner: clang for the two
darwin ones and GCC cross toolchains for the two linux ones, because DuckDB's prebuilt static
libraries need libstdc++. The linux binaries link libstdc++ and libgcc statically, so a host needs only
glibc (2.25 or newer) and not the toolchain's newer libstdc++; a check in the release build stops it if one slips
back to a dynamic link. A clone can still make the full build with `make install`.

See [ADR-001](../adr/001-positioning.md).
