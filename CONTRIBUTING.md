# Contributing to ynr

Thanks for helping. ynr is the factory's observation plane: a spool, collector, storage and
dashboards on OpenTelemetry. The design is in the ADRs under [docs/adr](docs/adr/README.md); read
the relevant one before changing behaviour, and propose a change to it in the same PR if the design
needs to move.

## Workflow

ynr uses gitflow. `develop` is the default branch.

1. Fork the repository (or branch, if you have write access) from `develop`.
2. Name the branch for the change: `feature/...`, `fix/...`, `chore/...`.
3. Make the change, with tests.
4. Run `make check` (see below) until it passes.
5. Open a pull request into `develop` and fill in the template. Resolve every review conversation.

`main` takes PRs only from `develop`, `release/*` and `hotfix/*`; do not target it. Branch rules
are described in [.github/BRANCH_PROTECTION.md](.github/BRANCH_PROTECTION.md). A PR needs the
**All Clear** check green and is squash-merged.

## Development

Needs Go (the version in `go.mod`), Node 24 for the npm spool exporter, and a C toolchain for the
full build (DuckDB, cgo).

```bash
make check   # formatting, generated code, vet, lint, the slim-build guard, npm build and tests, Go tests with -race
make build   # the slim build (no cgo) into bin/ynr
make full    # the full build, with DuckDB, into bin/ynr-full
make help    # every target
```

Code style:

- Short doc comments in plain English on every exported thing; errors wrapped with context.
- Anything using DuckDB is behind `//go:build full` with a `!full` stub; the slim build must never
  import DuckDB (`make check` verifies it).
- The dashboard's templ templates are generated into Go and committed: run `make generate` after
  changing them.

## Commits and pull requests

Write a one-line summary, a blank line, then a short paragraph on why. In the PR, say what changed,
which ADR it follows, and how you verified it.

## Reporting problems

Use the issue templates for bugs and feature requests, and Discussions for questions. Report
security problems privately, as described in [SECURITY.md](SECURITY.md).

By contributing you agree that your work is licensed under the [MIT License](LICENSE).
