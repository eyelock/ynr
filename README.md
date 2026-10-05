# ynr

**Your named reporting**: the observation plane for the ynh, ynm and ynf software factory, built on
OpenTelemetry. Tools write telemetry into a crash-safe file spool; `ynr serve` reads it, stamps
where each record came from, and ships it on. The design is in [the ADRs](docs/adr/README.md),
and the build order in [ADR-009](docs/adr/009-walking-skeleton.md).

**Status:** the walking skeleton's first slice is in progress. `ynr serve` (the slim build) reads
the spool and ships to an OTLP/HTTP endpoint, such as a local Jaeger; the object store, dashboards
and the full build follow in later slices. `ynr relay` receives a vendor CLI's OTLP, such as
Claude Code's, and writes it into a spool folder.

## Install

```bash
export HOMEBREW_GITHUB_API_TOKEN=<a token that can read eyelock/ynr>   # while ynr is private
brew install eyelock/tap/ynr
```

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

## The spool exporters

Other tools write the spool with one of two packages, each depending only on its language's
OpenTelemetry SDK and on nothing else in ynr. ynr's tests read what both write.

- **Go:** [`spoolexporter/`](spoolexporter), the module `github.com/eyelock/ynr/spoolexporter`,
  released with tags `spoolexporter/vX.Y.Z`.
- **npm:** [`spoolexporter/js/`](spoolexporter/js), the package `@eyelock/otel-spool-exporter`,
  published to GitHub Packages by the same tags. Installing it needs an `.npmrc` with
  `@eyelock:registry=https://npm.pkg.github.com` and a token that can read packages.

While this repository is private, fetching the Go module needs `GOPRIVATE=github.com/eyelock/ynr`
and a token with read access:

```bash
git config --global url."https://x-access-token:${TOKEN}@github.com/eyelock/ynr".insteadOf "https://github.com/eyelock/ynr"
```

## Develop

```bash
make check    # formatting, vet, lint, and tests with the race detector
make build    # bin/ynr, plus linux builds for images
```

`go.work` builds ynr against the spool exporter in this repository; `go.mod` requires its last
release, which is what `go install github.com/eyelock/ynr/cmd/ynr@<version>` uses. After
releasing a new `spoolexporter/v*`, raise that requirement.

Work goes on feature branches into `develop` by pull request; `main` moves only by release
(gitflow, as in ynh, ynm and ynf). The repository's own settings are Terraform in
[`infra/`](infra/README.md).
