# Re-verify the Claude Code attribute list

Check which attribute names a Claude Code version exports, and move the pinned version when you
mean to. The relay's strip list fails open: a name it has never seen passes into the spool. So the
repository pins a Claude Code version and the names verified on it, the relay counts any name
outside that list, and [`ynr doctor`](diagnose-with-doctor.md) reports them.

Run this when `ynr doctor` shows a `vendor` warning, or before you move to a newer Claude Code.

## Run the probe

The probe makes real, small model calls with `claude -p`, once without and once with
`OTEL_LOG_TOOL_DETAILS=1`, each time asking a custom subagent and a Bash command. It listens on a
loopback port for OTLP/JSON and keeps attribute **names** only: no value is written to disk or
printed.

```bash
scripts/probe-claude-code-otel.sh
```

To probe a version other than the one you run, install it into a folder of its own and point
`CLAUDE_BIN` at it:

```bash
CLAUDE_BIN=/path/to/node_modules/.bin/claude scripts/probe-claude-code-otel.sh
```

It prints the names the version exported that are not in
`internal/relay/claude-code-attributes.txt`, and the names on the list it did not export this run
(not necessarily wrong: some appear only on certain events). It exits 2 when nothing reached the
listener.

## Decide each new name

For every name that is new, decide one of three things:

- **It carries no content** (a count, a duration, an id the relay already hashes): add it to
  `internal/relay/claude-code-attributes.txt`.
- **It carries content or names the machine or a person** (a command line, a path, prompt or
  response text): add it to `Stripped` in `internal/relay/scrub.go`, with a test, and then to the
  list. Being on the list means the name has been seen and read, not that it is safe.
- **You are not sure:** leave it off. `ynr doctor` keeps flagging it, which is the point.

## Move the pin

When the list is right for a new version, change `ClaudeCodePin` in `internal/relay/vendor.go` and
the header of `claude-code-attributes.txt` in the same commit.

## See what the relay found

`ynr doctor` reads `.ynr-relay-attrs.json` from the `local/` folder of the spool, which a standing
relay rewrites as names arrive. The record covers that relay's run only, so it starts empty when the
relay restarts. It holds names and counts, never values, and at most 256 names; a name that is
longer than 128 characters, or is not letters, digits, `_`, `.`, `-` or `:`, is counted under
`(unusable name)`. Only records under Claude Code's instrumentation scope
(`com.anthropic.claude_code`) are checked, and only their own attributes, not the resource's.
