# CLI

```
ynr <command> [flags]
```

This page is written from the full build's usage text. A command or flag marked **full** exists
only in the full build; the slim build refuses it with a message that says so. A flag is written as
`--name`; the single-dash form `-name` is also accepted.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | `ynr doctor` found a `warn` or `fail`; or `ynr conformance` failed a check. |
| 2 | Usage: a bad flag or argument, an unknown command or query, a bad `--since`. |
| 20 | An adapter failed: the spool, the collector, the store or the upstream. |
| 30 | A setting is invalid, or the command needs the full build. |

## Commands

| Command | Build | Does |
|---|---|---|
| `ynr version` | both | Prints the version and the build: `v0.2.2 (full)`. |
| `ynr info` | both | Prints the version, the build, the capabilities version, the spool root, and whether a `ynr serve` holds the spool. |
| `ynr doctor` | both | Checks this machine's setup. Only reads. |
| `ynr serve` | both; some flags full | Reads the spool, stamps, ships to a store. |
| `ynr central` | full | Reads a shared store; hot tier, compaction, queries, dashboard behind sign-in. |
| `ynr relay` | both | Receives OTLP/HTTP from a vendor CLI into a spool folder. |
| `ynr telemetry registry` | both | Prints the names ynr itself writes. |
| `ynr conformance` | both | Runs a tool's conformance scenarios. |
| `ynr tail` | both | Follows what tools write to the spool. |
| `ynr query` | full | Asks a named query. With no name, lists them. |

### ynr info

```
ynr info [--spool <root>] [--format text|json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--spool` | `$XDG_STATE_HOME/ynr/spool`, or `~/.local/state/ynr/spool` | The spool root. `YNR_SPOOL_ROOT` sets the default. |
| `--format` | `text` | `text` or `json`. |

With `--format json` the output has `version`, `build`, `capabilities`, `spool` and `serving`, which
is `null` or an object with `pid` and `since`.

### ynr doctor

```
ynr doctor [--spool <root>] [--store <url>] [--format text|json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--spool` | the spool root, as for `info` | The spool to check. `YNR_SPOOL_ROOT`. |
| `--store` | `file://$XDG_DATA_HOME/ynr/store`, or `file://~/.local/share/ynr/store` | The store `ynr serve` ships to. `YNR_STORE`. |
| `--format` | `text` | `text` or `json`: `version`, `build` and `checks`, each with `name`, `status` and `detail`. |

Checks, in order: `otlp`, `path`, `spool`, `serve`, `backlog`, `store`, `queries`. Each is `ok`,
`warn` or `fail`. Exit 1 if any is not `ok`.

### ynr serve

```
ynr serve [--spool <root>] [--store <url>] [--upstream <otlp-http-endpoint>]
          [--collector-id <id>] [--collector-instance <id>] [--poll 1s]
          [--max-line <bytes>] [--spool-cap <bytes>] [--hot-window 168h] [--retain 168h]
          [--retain-bytes <bytes>] [--erase <file>] [--ui 127.0.0.1:4319]
          [--registry-tools ynh,ynf,ynm] [--debug]
```

Runs in the foreground until Ctrl-C or `SIGTERM`.

| Flag | Default | Meaning |
|---|---|---|
| `--spool` | the spool root | The spool root. `YNR_SPOOL_ROOT`. |
| `--store` | the laptop folder store | The object store to ship batches to, such as `file:///path` or `s3://bucket?region=...`. An empty value ships only to the upstream. `YNR_STORE`. |
| `--upstream` | none | An OTLP/HTTP endpoint to also forward to, such as `http://localhost:4318`. With a store, the upstream is a best-effort copy; without one, it commits the spool. `YNR_UPSTREAM`. |
| `--collector-id` | `local-<host>` | This collector's identity: a runner pool or a host. Matches `^[a-z0-9][a-z0-9._-]{0,62}$`. `YNR_COLLECTOR_ID`. |
| `--collector-instance` | none | The job within the pool, recorded as data. `YNR_COLLECTOR_INSTANCE`. |
| `--poll` | `1s` | How often to read the spool. |
| `--max-line` | `4194304` | The longest line accepted, in bytes. Longer lines are skipped and counted. |
| `--spool-cap` | `1073741824` | The most the spool may hold, in bytes. Over it, the oldest closed files are evicted and counted. |
| `--hot-window` | `168h` | **full.** How far back the hot tier holds records, for queries. |
| `--retain` | `168h` | How long a folder store keeps records. Rollups are kept 13 months. |
| `--retain-bytes` | `1073741824` | The most a folder store's records may take, in bytes; the oldest go first. `0` is no cap. |
| `--erase` | none | A file of handles to erase, one on each line. `YNR_ERASE`. |
| `--ui` | off | **full.** Serves the local dashboard on this address, which must be loopback (`127.0.0.1`, `::1` or `localhost`). `YNR_UI`. |
| `--registry-tools` | none | Tools whose telemetry registries to learn at startup, comma separated, each run by bare name on `PATH` as `<tool> telemetry registry --format json`. A tool that is missing or fails is logged and skipped. `YNR_REGISTRY_TOOLS`. |
| `--debug` | off | Also prints a summary of what is shipped. |

At least one of `--store` and `--upstream` is needed. In the full build `ynr serve` also keeps the
hot tier, compacts, and answers `ynr query` on a
Unix socket: `serve.sock` in the spool's `.ynr` folder, or under the temporary directory when that
path is too long for a socket.

### ynr central

```
ynr central --store <url> [--hot-window 6h] [--socket <path>] [--state <folder>] [--poll 5s]
            [--compact-lookback 24h] [--compact-every 5m] [--erase <file>]
            [--ui <addr> --oidc-issuer <url> --oidc-client-id <id> --oidc-redirect-url <url>
             --allow-emails <list> | --allow-domain <domain> | --allow-group <group>
             [--group-claim groups] [--session-ttl 8h]]
            [--retain 168h] [--retain-bytes <bytes>]
```

**Full build only.**

| Flag | Default | Meaning |
|---|---|---|
| `--store` | none | The shared object store to read. Required. `YNR_STORE`. |
| `--hot-window` | `6h` | How far back the hot tier holds records. |
| `--state` | `$XDG_STATE_HOME/ynr/central`, or `~/.local/state/ynr/central` | The folder for the hot tier's database, which only this user can open. A cache: deleting it loses nothing. `YNR_CENTRAL_STATE`. |
| `--socket` | `central.sock` in `--state` | The Unix socket to answer queries on. `YNR_CENTRAL_SOCKET`. |
| `--poll` | `5s` | How often to poll the store for new batches. |
| `--compact-lookback` | `24h` | How far back hours are compacted. |
| `--compact-every` | `5m` | How often compaction runs. |
| `--erase` | none | A file of handles to erase. `YNR_ERASE`. |
| `--ui` | off | Serves the dashboard behind sign-in on this address, such as `0.0.0.0:4320`. Refused unless sign-in is configured. `YNR_UI`. |
| `--oidc-issuer` | none | The OpenID Connect issuer URL. `YNR_OIDC_ISSUER`. |
| `--oidc-client-id` | none | The client id registered with the issuer. `YNR_OIDC_CLIENT_ID`. |
| `--oidc-redirect-url` | none | The dashboard's public sign-in return URL, ending `/auth/callback`; its host is the only `Host` answered. `YNR_OIDC_REDIRECT_URL`. |
| `--allow-emails` | none | Comma-separated emails that may sign in. `YNR_ALLOW_EMAILS`. |
| `--allow-domain` | none | An email domain whose users may sign in. `YNR_ALLOW_DOMAIN`. |
| `--allow-group` | none | A group whose members may sign in. `YNR_ALLOW_GROUP`. |
| `--group-claim` | `groups` | The ID-token claim that lists a user's groups. `YNR_GROUP_CLAIM`. |
| `--session-ttl` | `8h` | How long a sign-in lasts. |
| `--retain` | `168h` | How long a folder store keeps records. Applies only when `--store` is a folder; a bucket's lifecycle rules do this on S3. |
| `--retain-bytes` | `0` | The most a folder store's records may take. `0` is no cap. |

The client secret and the session secret are read from the environment only: `YNR_OIDC_CLIENT_SECRET`
and `YNR_SESSION_SECRET`. See [Sign-in (OIDC)](sign-in.md).

### ynr relay

```
ynr relay --spool <writer folder> [--listen 127.0.0.1:0] [--format text|json]
          [--max-request <bytes>] [--max-memory <bytes>] [--rate <per second>]
          [--exit-on-stdin-eof]
```

Prints its OTLP/HTTP endpoint as its first line of output, then runs until stopped (Ctrl-C,
`SIGTERM`, or with `--exit-on-stdin-eof` its standard input closing), flushing what it received. On
exit it prints a line on stderr counting what it accepted and refused.

| Flag | Default | Meaning |
|---|---|---|
| `--spool` | none | The writer folder to write into, such as a run's folder. Required. `YNR_SPOOL`. |
| `--listen` | `127.0.0.1:0` | A loopback address; port `0` picks a free port. Any other address is refused. |
| `--format` | `text` | The first line of output: the endpoint as text, or `{"endpoint":...,"pid":...}` as `json`. |
| `--max-request` | `4194304` | The largest request accepted, in bytes after decompression. |
| `--max-memory` | `67108864` | The most bytes of requests held at once. |
| `--rate` | `100` | Requests accepted a second, sustained (burst 200). |
| `--exit-on-stdin-eof` | off | Also stops when standard input closes, so the relay ends with the process that started it. |

It accepts OTLP/HTTP, protobuf or JSON, optionally gzipped, on `/v1/traces`, `/v1/metrics` and
`/v1/logs`.

### ynr telemetry registry

```
ynr telemetry registry [--format text|json]
```

Prints the names ynr itself writes: `ynr.provenance`, `ynr.provenance.warning`, `ynr.collector.id`,
`ynr.collector.instance`, `ynr.registry`, `ynr.registry.name`, and the metrics `ynr.spool.evicted` and
`ynr.registry.unknown_names`. `--format json` prints the registry as JSON, in the shape every YN tool's
`telemetry registry --format json` prints: `tool`, `version`, `semantic_conventions`, `attributes`,
`spans`, `events` and `metrics`.

### ynr conformance

```
ynr conformance [--file .ynr/conformance.yaml] [--format text|json] [--timeout 60s]
                [--flush-limit 2s] [--keep]
```

| Flag | Default | Meaning |
|---|---|---|
| `--file` | `.ynr/conformance.yaml` | The tool's conformance file. |
| `--format` | `text` | `text` or `json`. |
| `--timeout` | `60s` | The longest one run of a scenario may take. |
| `--flush-limit` | `2s` | The tool's bound on its flush at exit (rule 12). |
| `--keep` | off | Keeps the temporary folders, for looking at what was written. |

Exit 1 if any check fails, 30 if the file cannot be read. The stub vendor and the tool under test
are found by bare name on `PATH`. The file's format is in
[Add ynr conformance to a tool's CI](../how-to/add-conformance-to-ci.md).

### ynr tail

```
ynr tail [--spool <root>] [--service <name>] [--item <key>] [--from-start] [--format text|json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--spool` | the spool root | The spool root. `YNR_SPOOL_ROOT`. |
| `--service` | all | Only this service's records, such as `ynh`. |
| `--item` | all | Only this work item's records, by its key. |
| `--from-start` | off | Begin with what the spool already holds, not only what comes next. |
| `--poll` | `500ms` | How often to look for new lines. |
| `--format` | `text` | `text`, or `json` for one object a line. |

Follows until Ctrl-C. It only watches: `ynr serve` still ships everything. Times are UTC.

### ynr query

```
ynr query [<name> [<argument>] [--store <url>] [--since 7d] [--until <time>] [--lane <id>]
           [--format text|json] [--socket <path>] [--spool <root>] [--erase <file>]]
```

**Full build.** With no name, lists the [named queries](named-queries.md). The argument may come
before or after the flags.

| Flag | Default | Meaning |
|---|---|---|
| `--since` | the query's own window | The start of the window: a duration back from now (`24h`, `7d`) or an RFC 3339 time. |
| `--until` | now | The end of the window, the same way. |
| `--lane` | all | Only this lane's records, where the query takes one. |
| `--format` | `text` | `text` or `json`: `query`, `since`, `until`, `columns` and `rows`. |
| `--spool` | the spool root | The spool root of the `ynr serve` to ask. `YNR_SPOOL_ROOT`. |
| `--socket` | none | The Unix socket of the `ynr central` to ask. `YNR_CENTRAL_SOCKET`. |
| `--store` | the laptop folder store | Read this store directly, instead of asking a server. `YNR_STORE`. |
| `--erase` | none | A file of handles to mask, for a direct read. `YNR_ERASE`. |

Where the answer comes from, in order: if `--store` is given, the store is read directly; otherwise
the server at `--socket`, or the `ynr serve` on `--spool`, is asked; and if no `ynr serve` answers,
the store is read directly. A given `--socket` that nothing answers is an error (exit 20), not a
fallback. The slim build can only ask a running full `ynr serve`.

Exit 2 for an unknown query, a missing argument, an argument to a query that takes none, or a bad
window.
