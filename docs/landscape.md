# Landscape

Notes from competitive analyses on 2026-10-05: which open-source tools overlap with ynr, and how
they fit its constraints. Claims about other projects are as reported then, and should be checked
before anything relies on them. The decisions that use these notes are in ADR-001 and ADR-005.

## What only ynr does

No tool found covers these, because they are particular to the factory:

- Provenance from spool folders and run manifests, so a dashboard can tell what ynf recorded from
  what a run claimed (ADR-003).
- The one-way mirror of control-plane CloudEvents into telemetry (ADR-003).
- Tool-owned registries for ynf, ynh and ynm, learned from configured binaries, with a shared CI
  conformance check (ADR-007, ADR-008).
- The wiring: the spool, the relay, and trace context from a ynf step through `ynh agent run`
  into the vendor CLI (ADR-004).
- Factory concepts: lanes as factories, items, steps and leases, with dashboards built on them.

## General OpenTelemetry backends

| Tool | Licence | Same design on a laptop and in the cloud? | Notes |
|---|---|---|---|
| OpenObserve | AGPL-3.0 | Yes: one binary, Parquet on local disk, or the same binary writing Parquet to S3 | OTLP in, SQL and PromQL, dashboards and alerts. Single sign-on, advanced access control and sensitive-data redaction are enterprise-only. |
| SigNoz | MIT core | No, needs ClickHouse | Has guides for coding agents. |
| HyperDX / ClickStack | MIT | No, needs ClickHouse | ClickHouse acquired HyperDX in 2025. |
| Uptrace | AGPL-3.0 | No, needs ClickHouse and Postgres | |
| Grafana (Loki, Tempo, Mimir) | AGPL / Apache | Several services; an all-in-one image for laptops | The most flexible and the most to operate. |
| Jaeger v2 | Apache-2.0 | Yes, one binary | Traces only. Built on the OpenTelemetry Collector, as ynr is. Used as the viewer in ADR-009's first slice. |
| otel-desktop-viewer, otel-tui | Apache / MIT | Yes | Laptop viewers with no central mode. |

## Agent and model observability tools

| Tool | Licence | Laptop | Notes |
|---|---|---|---|
| Langfuse | MIT core | No: Postgres, ClickHouse, Redis and S3 | Acquired by ClickHouse in January 2026; built around prompts and evaluations. |
| Arize Phoenix | ELv2, source-available | Yes, one container with SQLite | Python; uses OpenInference names rather than `gen_ai.*`. |
| Opik | Apache-2.0 | No: ClickHouse, MySQL and Redis | Ingests from the Codex CLI. |
| Laminar | Apache-2.0 | Docker Compose | OpenTelemetry-native and built around agents. |
| OpenLIT | Apache-2.0 | No, needs ClickHouse | A coding-agent mode maps Claude Code and Codex hook events onto OpenTelemetry; the closest to ynh's part. |

These tools model one application's model calls: prompts, evaluations and datasets. None models
an outer loop of items, steps, leases and decisions across many hosts.

## Fit

OpenObserve is the only full backend that runs as one binary on a laptop and moves to S3 without
changing architecture; internally it is close to ADR-005's design. ynr does not adopt it as the
default because it would be a second server beside `ynr` on every laptop, and AGPL-licensed under
the reporting product. Its dashboards could be built in SQL over factory attributes, so that is not
a reason against it. It remains a candidate adapter behind ynr's store and query ports.

## Vendor CLI findings

- **Claude Code** reportedly reads `TRACEPARENT` and `TRACESTATE` in headless (`-p`) and Agent SDK
  mode and nests its spans under the caller's trace. Tracing is beta and off by default; the
  reported settings are `CLAUDE_CODE_ENABLE_TELEMETRY=1`, `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`
  and `OTEL_TRACES_EXPORTER=otlp`. Content fields are redacted by default.
- **Codex** is configured through `config.toml`, with an `[otel]` trace exporter block that needs
  the full signal path in its endpoint. Whether it reads an incoming `TRACEPARENT` is unverified.

Both are verified in ADR-009's first slice.

## Dashboard building blocks

From a second analysis on 2026-10-05, for ADR-005's dashboards: embedded in the Go binary, fully
offline, a local and a central mode, data from ynr's named DuckDB queries, and views for lanes and
cost, an item's history, a trace waterfall and a live tail.

| Option | Licence | Verdict |
|---|---|---|
| Rill | Apache-2.0, Go with embedded DuckDB | The closest product, with dashboards as SQL and YAML, but an application, not a library, built around metrics exploration with no trace or item views. Ideas to borrow, not something to embed. |
| Perses (CNCF) | Apache-2.0, Go and React | Data sources are Prometheus, Tempo, Loki and Pyroscope; a DuckDB source would mean writing a plugin and carrying its CUE schema stack. Too heavy. |
| DuckDB UI | MIT extension | Its front end is not open source and loads from ui.duckdb.org by default; a SQL editor, not dashboards. |
| Grafana, Superset, Metabase | AGPL / Apache | Separate servers; they break "one binary, nothing extra to run". |
| Evidence, Observable Framework | MIT / ISC | Data is fixed when the site is built; ynr's changes constantly. |
| DuckDB-WASM with Mosaic or Perspective | MIT / BSD / Apache-2.0 | Queries run in the browser, bypassing ynr's named queries, and central would need S3 access from the browser. |
| otel-desktop-viewer | Apache-2.0 | Prior art: Go on the OpenTelemetry Collector, DuckDB storage, a Svelte UI embedded with `go:embed`. Effectively a local dashboard without factory views. |
| Jaeger UI | Apache-2.0, React | An embedded mode, but it calls Jaeger's internal API, a moving target. |

The analysis recommended a Svelte single-page app borrowing otel-desktop-viewer's views. ynr
chose server-rendered pages with `templ` and htmx instead (ADR-005): Go only, no Node build step,
and factory traces are small enough that a server-rendered waterfall with one small script is
tractable. Central sign-in implements ynm's OIDC design in Go with `coreos/go-oidc`.

## Sources

- [OpenObserve on GitHub](https://github.com/openobserve/openobserve)
- [OpenObserve storage](https://openobserve.ai/docs/storage-management/storage/)
- [OpenObserve architecture](https://openobserve.ai/docs/architecture/)
- [Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)
- [ClickHouse acquires Langfuse](https://clickhouse.com/blog/clickhouse-acquires-langfuse-open-source-llm-observability)
- [Phoenix licence](https://arize.com/docs/phoenix/self-hosting/license)
- [OpenLIT coding agents](https://docs.openlit.io/latest/openlit/coding-agents/overview)
- [Codex CLI OpenTelemetry](https://codex.danielvaughan.com/2026/04/16/codex-cli-opentelemetry-observability-tracing-agent-sessions/)
- [LangWatch Codex integration](https://langwatch.ai/docs/integration/tools/integrations/openai-codex)
- [Open-source LLM observability tools 2026](https://openobserve.ai/blog/llm-observability-tools/)
- [Laminar: Langfuse alternatives](https://laminar.sh/article/langfuse-alternatives-2026)
- [HyperDX vs Uptrace](https://openalternative.co/compare/hyperdx/vs/uptrace)
- [SigNoz vs HyperDX](https://cubeapm.com/blog/signoz-vs-hyperdx-2/)
- [otel-tui](https://beta.pkg.go.dev/github.com/ymtdzzz/otel-tui)
- [otel-desktop-viewer](https://github.com/CtrlSpice/otel-desktop-viewer)
- [Rill](https://pkg.go.dev/github.com/rilldata/rill) and [why Rill uses DuckDB](https://rilldata.com/blog/why-we-built-rill-with-duckdb)
- [Perses](https://pkg.go.dev/github.com/perses/perses)
- [Jaeger frontend UI](https://www.jaegertracing.io/docs/2.5/frontend-ui/) and [repository](https://github.com/uber/jaeger-ui)
- [DuckDB UI](https://github.com/duckdb/duckdb-ui), [docs](https://duckdb.org/docs/stable/extensions/ui) and [licensing coverage](https://www.devclass.com/databases/2025/03/19/duckdb-project-releases-local-web-ui-but-not-as-open-source/1630263)
- [Evidence](https://motherduck.com/glossary/evidence/)
- [Mosaic with DuckDB-WASM](https://idl.uw.edu/mosaic-framework-example/mosaic-duckdb-wasm)
- [Perspective](https://github.com/finos/perspective/blob/asan-expr/README.md)
