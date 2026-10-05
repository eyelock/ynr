# ynr

**Your named reporting**: the observation plane for the ynh, ynm and ynf software factory, built on
OpenTelemetry. Tools write telemetry into a crash-safe file spool; `ynr serve` reads it, stamps
where each record came from, and ships it on. The design is in [the ADRs](docs/adr/README.md),
and the build order in [ADR-009](docs/adr/009-walking-skeleton.md).

**Status:** the walking skeleton's first slice is in progress. `ynr serve` (the slim build) reads
the spool and ships to an OTLP/HTTP endpoint, such as a local Jaeger; the object store, dashboards
and the full build follow in later slices.

## Try it

```bash
make build
docker run --rm -d --name jaeger -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:latest
./bin/ynr serve --upstream http://localhost:4318      # foreground; Ctrl-C to stop
./bin/ynr info                                        # in another terminal
```

`ynr serve` creates the spool at `$XDG_STATE_HOME/ynr/spool` (or `~/.local/state/ynr/spool`) on
first start. Anything a tool writes into its `local/` folder as OTLP JSON lines then appears in
Jaeger at http://localhost:16686.

## The spool exporter

[`spoolexporter/`](spoolexporter) is a separate Go module, `github.com/eyelock/ynr/spoolexporter`,
that other tools import to write the spool: OpenTelemetry's own OTLP exporters, with a transport
that writes OTLP JSON lines into a folder instead of sending them. It depends on nothing else in
ynr. It is released with tags `spoolexporter/vX.Y.Z`. While this repository is private, fetching
it needs `GOPRIVATE=github.com/eyelock/ynr` and a token with read access:

```bash
git config --global url."https://x-access-token:${TOKEN}@github.com/eyelock/ynr".insteadOf "https://github.com/eyelock/ynr"
```

## Develop

```bash
make check    # formatting, vet, lint, and tests with the race detector
make build    # bin/ynr, plus linux builds for images
```

Work goes on feature branches into `develop` by pull request; `main` moves only by release
(gitflow, as in ynh, ynm and ynf). The repository's own settings are Terraform in
[`infra/`](infra/README.md).
