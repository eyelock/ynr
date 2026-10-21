# Add ynr conformance to a tool's CI

Make the instrumentation contract ([ADR-006](../adr/006-sibling-instrumentation-contract.md)) a
required check on a tool's main branch, with no model, no secret and no running ynr. The check is
strict in CI and never touches production ([ADR-008](../adr/008-conformance-in-ci.md)).

## What you need

- A tool that already writes telemetry through the spool exporter for its language, and has its own
  Weaver registry that `<tool> telemetry registry --format json` prints ([ADR-007](../adr/007-tool-owned-registries.md)).
- The tool built and on `PATH` in CI, under the name its scenarios use.
- A GitHub Actions runner on Linux. The same steps work anywhere `gh` and `tar` do.

## Write the conformance file

Create `.ynr/conformance.yaml` in the tool's repository:

```yaml
service: ynh
registry: telemetry/registry            # the tool's own Weaver registry, relative to the repository root
vendor: ynr-stub-vendor                 # the deterministic vendor CLI; no model, no secrets
vendor_aliases: [claude]                # optional: other names the stub answers to on PATH
units: [ynh.run]                        # optional: span names that are units of work
scenarios:
  - name: agent run, converged
    run: ynh agent run --harness testdata/harness --task @canary:prompt
    expect: { outcome: converged }
  - name: agent run, budget
    run: ynh agent run --harness testdata/harness --max-turns 1 --task @canary:prompt
    expect: { outcome: budget }
```

| Key | Meaning |
|---|---|
| `service` | The tool's `service.name`, and its prefix: records of `ynh` are named `ynh.*`. Required. |
| `registry` | The path to the tool's registry folder, relative to where `ynr conformance` runs. |
| `vendor` | The stub vendor's command name, found by bare name on `PATH`. |
| `vendor_aliases` | Further names the stub answers to, such as `claude`, for a tool that starts the vendor CLI by its own name. |
| `units` | The spans that are units of work, for the started-event and outcome rules. Without it, a unit of work is a direct child of the `TRACEPARENT` span, or a span a `<unit>.started` event names. |
| `scenarios` | At least one. Each has a `name`, a `run` command and `expect.outcome`, the outcome the unit-of-work span must end with. |

Each `run` command goes through `sh -c` from the repository root, so paths such as
`testdata/harness` work as written. `@canary:<kind>` becomes a unique random string on each run,
where `<kind>` is `prompt`, `ticket`, `file`, `memory` or `secret`; the check then fails if that
string appears in any record. Unknown keys and unknown canary kinds are errors.

## Put ynr and the stub vendor on PATH in CI

A tool finds `ynr` and the stub vendor only by their bare names on `PATH`, so the job downloads a
pinned release, checks it against the release's checksums, and adds the folder to `PATH`. This is the
job ynf runs:

```yaml
conformance:
  runs-on: ubuntu-latest
  env:
    YNR_VERSION: 0.2.2
  steps:
    - uses: actions/checkout@v7
    - name: ynr and its stub vendor, checked against the release's checksums
      env:
        GH_TOKEN: ${{ github.token }}
      run: |
        set -euo pipefail
        dl="$RUNNER_TEMP/ynr-download"
        bin="$RUNNER_TEMP/ynr-bin"
        mkdir -p "$dl" "$bin"
        ynr="ynr_${YNR_VERSION}_linux_amd64.tar.gz"
        stub="ynr-stub-vendor_${YNR_VERSION}_linux_amd64.tar.gz"
        gh release download "v${YNR_VERSION}" -R eyelock/ynr -D "$dl" -p "$ynr" -p "$stub" -p checksums.txt
        cd "$dl"
        grep -E " (${ynr}|${stub})\$" checksums.txt > wanted.txt
        [ "$(wc -l < wanted.txt)" -eq 2 ]
        sha256sum --check --strict wanted.txt
        tar -xzf "$ynr" -C "$bin" ynr
        tar -xzf "$stub" -C "$bin" ynr-stub-vendor
        echo "$bin" >> "$GITHUB_PATH"
    # ...build the tool and put it on PATH, then:
    - name: ynr conformance
      run: |
        ynr version
        ynr conformance --format json | tee "$RUNNER_TEMP/conformance.json"
```

ynr is a public repository, so `github.token` can download its releases; no other secret is needed.
Both archives are the slim build, which is all `ynr conformance` needs. Raise `YNR_VERSION` on
purpose when you want a newer check.

## Make it a required check

Add the job to the jobs your "all clear" job needs, and require that one check on `main`, as
[BRANCH_PROTECTION.md](https://github.com/eyelock/ynr/blob/develop/.github/BRANCH_PROTECTION.md)
describes for ynr itself. A failure blocks the merge.

## Run it locally

With `ynr`, `ynr-stub-vendor` and the tool on `PATH`, from the tool's repository root:

```bash
ynr conformance
```

It prints one line for each run of each scenario, then a line for each check with `PASS`, `FAIL` or
`SKIPPED`, and ends with `conformance: ok` or `conformance: FAILED`. It exits 0 when every check
that can run passes, 1 when any fails, and 30 when the file cannot be read. `--format json` prints
the whole report; `--keep` keeps the temporary spools for looking at what was written; `--timeout`
bounds one run of a scenario (60 seconds by default) and `--flush-limit` is the tool's bound on its
flush at exit (2 seconds by default).

## What it skips

A check that cannot run says `SKIPPED` and does not fail the job:

- Weaver's live check of names runs only when `weaver` is on `PATH`.
- Rule 4 is skipped when no process the tool started wrote spans, and HTTP calls between tools are
  not checked.
- Rule 13, that ynm's store is unchanged, is skipped: the check does not implement it yet.
- Rules 6 and 9 need judgement. The report prints span counts and names for review instead.
