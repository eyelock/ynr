# Environment variables

Every setting of ynr has a flag. Where a flag has an environment fallback, the flag wins when both are
given. Two secrets are read from the environment only.

## ynr reads

| Variable | Used by | Meaning |
|---|---|---|
| `YNR_SPOOL_ROOT` | `info`, `doctor`, `serve`, `tail`, `query` | The spool root. Default `$XDG_STATE_HOME/ynr/spool`. |
| `YNR_STORE` | `doctor`, `serve`, `central`, `query` | The store URL. An empty value means no store. Default `file://$XDG_DATA_HOME/ynr/store`. |
| `YNR_UPSTREAM` | `serve` | An OTLP/HTTP endpoint to also forward to. |
| `YNR_COLLECTOR_ID` | `serve` | This collector's identity. Default `local-<host>`. |
| `YNR_COLLECTOR_INSTANCE` | `serve` | The job within the pool. |
| `YNR_UI` | `serve`, `central` | The dashboard's address. Loopback for `serve`. |
| `YNR_ERASE` | `serve`, `central`, `query` | A file of handles to erase. |
| `YNR_REGISTRY_TOOLS` | `serve` | Comma-separated tools whose registries to learn, by bare name on `PATH`. |
| `YNR_CENTRAL_STATE` | `central` | The folder for the hot tier's database. |
| `YNR_CENTRAL_SOCKET` | `central`, `query` | The Unix socket central answers on, and `query` asks. |
| `YNR_OIDC_ISSUER` | `central` | The OpenID Connect issuer URL. |
| `YNR_OIDC_CLIENT_ID` | `central` | The client id. |
| `YNR_OIDC_REDIRECT_URL` | `central` | The sign-in return URL. |
| `YNR_ALLOW_EMAILS` | `central` | Comma-separated emails that may sign in. |
| `YNR_ALLOW_DOMAIN` | `central` | An email domain whose users may sign in. |
| `YNR_ALLOW_GROUP` | `central` | A group whose members may sign in. |
| `YNR_GROUP_CLAIM` | `central` | The ID-token claim that lists groups. Default `groups`. |
| `YNR_OIDC_CLIENT_SECRET` | `central --ui` | The client secret. Environment only. |
| `YNR_SESSION_SECRET` | `central --ui` | The key that signs sessions, at least 32 bytes. Environment only. Without it, a random key is made and sessions end when central restarts. |
| `YNR_SPOOL` | `relay` | The writer folder to write into, when `--spool` is not given. |
| `XDG_STATE_HOME` | all | Where the spool and central's state default to: `$XDG_STATE_HOME/ynr/...`, else `~/.local/state/ynr/...`. |
| `XDG_DATA_HOME` | all | Where the folder store defaults to: `$XDG_DATA_HOME/ynr/store`, else `~/.local/share/ynr/store`. |
| `PATH` | `serve`, `conformance` | Where `ynr` finds the tools it runs by bare name: those named in `--registry-tools`, and the stub vendor and the tool under test. |
| `AWS_*` | S3 stores | The AWS SDK's usual chain: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, `AWS_REGION` and the rest. |

`ynr doctor` also reads any `OTEL_EXPORTER_OTLP_*ENDPOINT` variable, to warn that it is set.

## Tools read

These are not ynr's settings; they are what a tool reads to decide where to write
([ADR-004](../adr/004-spool-detection-and-wiring.md)).

| Variable | Meaning |
|---|---|
| `YNR_SPOOL` | The writer folder a tool writes to. ynh sets it for a run, and an MCP server such as ynm reads it. |
| `OTEL_EXPORTER_OTLP_*` | When set explicitly, a tool exports to that collector instead of to the spool. |
| `OTEL_RESOURCE_ATTRIBUTES` | The standard resource attributes; every tool honours it. |
| `TRACEPARENT`, `TRACESTATE` | The trace a process joins. |

## The stub vendor reads

`ynr-stub-vendor`, used by `ynr conformance`, takes its script from flags or these variables:

| Variable | Meaning |
|---|---|
| `YNR_STUB_TURNS` | The number of turns to answer. |
| `YNR_STUB_EXIT` | The exit code. |
| `YNR_STUB_TURN_DELAY` | How long each turn takes, such as `200ms`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Where it exports, as a vendor CLI does. |
| `TRACEPARENT` | The trace its spans join. |
