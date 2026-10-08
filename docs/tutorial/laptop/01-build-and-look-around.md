# 1. Build ynr and look around

Build the full binary, keep everything the tutorial makes in one folder, and ask ynr three
questions that work before anything is running.

## Build ynr

Use terminal C. Clone the repository and build the full build, which includes DuckDB:

```bash
git clone https://github.com/eyelock/ynr
cd ynr
make full
```

The binary is `bin/ynr-full`. The tutorial folder will hold a copy of it named `ynr`, so that
`ynr` is found on your `PATH` the way every YN tool finds the others:

```bash
mkdir -p ~/ynr-tutorial/bin
cp bin/ynr-full ~/ynr-tutorial/bin/ynr
```

## Keep everything in one folder

ynr keeps its spool under `$XDG_STATE_HOME` and its store under `$XDG_DATA_HOME`. Point both into
the tutorial folder, and put its `bin` on the `PATH`. Put the lines in a file so each terminal can
load them:

```bash
cat > ~/ynr-tutorial/env.sh <<'EOF'
export XDG_STATE_HOME="$HOME/ynr-tutorial/state"
export XDG_DATA_HOME="$HOME/ynr-tutorial/data"
export PATH="$HOME/ynr-tutorial/bin:$PATH"
EOF
. ~/ynr-tutorial/env.sh
```

Do `. ~/ynr-tutorial/env.sh` in every terminal you open for this track.

## Ask three questions

```bash
ynr version
```

Expected:

```text
v0.2.0-14-g47b1a89 (full)
```

The last word is the build. A clone built from a tag prints just the tag, such as `v0.2.2`.

```bash
ynr info
```

Expected:

```text
version       v0.2.0-14-g47b1a89 (full)
capabilities  0.1.0
spool         /Users/you/ynr-tutorial/state/ynr/spool
serving       no
```

`spool` is where tools write. It is under your tutorial folder because of `XDG_STATE_HOME`.
`serving` says whether a `ynr serve` holds that spool. Nothing does yet.

```bash
ynr doctor
```

Expected:

```text
ynr v0.2.0-14-g47b1a89 (full)
ok    otlp      no OTEL_EXPORTER_OTLP_*ENDPOINT set, so tools write to ynr's spool
ok    path      ynr on PATH is this one: /Users/you/ynr-tutorial/bin/ynr
warn  spool     /Users/you/ynr-tutorial/state/ynr/spool does not exist yet; ynr serve creates it, and tools write nothing until then
warn  serve     no ynr serve holds the spool, so nothing is shipped
ok    backlog   no spool to read
ok    store     file:///Users/you/ynr-tutorial/data/ynr/store (0 rollup files)
warn  queries   no hot tier answering: ynr query reads the store directly, and needs the full build
```

`doctor` only reads; it starts and changes nothing. It exits 1 while any line says `warn` or
`fail`. The three warnings are what you expect before `ynr serve` has run. In the next lesson they
turn into `ok`.

Next: [2. Start ynr serve](02-start-ynr-serve.md).
