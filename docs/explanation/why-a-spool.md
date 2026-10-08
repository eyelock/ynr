# Why a spool

Tools do not send their telemetry anywhere. They append it to files in a folder, and ynr reads the
folder. This page explains why that is the one contract between a tool and ynr.

## Telemetry held in memory is lost when the process dies

The data most worth having, a job failing, is the data most likely to be lost that way. An exporter
that batches in memory and sends over the network loses the batch when the process is killed. A tool
that appends a line to a file has handed it to the operating system: a batch already written
survives the process being killed, and the file is flushed to disk at the end of each unit of work
and on exit, which covers the host crashing too. Each flush is bounded in time, so a slow network
filesystem never blocks the tool.

Spans are written when they end, so a crash would still lose the span that was open. Every unit of
work therefore writes a `started` event when it begins; a start with no matching span is how a crash
shows up.

## A run cannot reach a collector

A docker-mode run cannot reach a collector on the job's loopback, and every endpoint is one more hole
in the run's egress policy. A folder needs neither: the run has only its own folder mounted, so it
writes nowhere else and needs no path to the network. The same folder is what makes the writer's
identity something the starter of the run decides, not the run ([Provenance and stamping](provenance-and-stamping.md)).

## ynr stays optional

Every tool runs unchanged without ynr, and ynr starts nothing on its own. A tool picks where to write
once, at start: another collector only when the operator set `OTEL_EXPORTER_OTLP_*` explicitly,
otherwise ynr's spool if one is named or the laptop default exists, otherwise the SDK's no-op
providers. With no spool, nothing is written and nothing fails. The spool exporter swallows and
counts every error, so a failing spool cannot reach the tool's output or its exit code, and a writer
caps its own files and drops new records, counted, rather than fill a disk.

Because detection only decides *where* to write, and configuration decides *what runs*, finding
`ynr` on the `PATH` never starts it. A `ynr serve` is something a person, or a configuration they
wrote, starts, and it runs in the foreground: ynr ships no service installer and never daemonises.

## One exporter, written once

OpenTelemetry's Go and JavaScript SDKs do not ship an OTLP file exporter, and writing a spool
correctly is subtle: exclusive creation, no links followed, one write for each line, rotation,
bounded flushes, the caps. It is written once, beside the receiver that reads it, as a Go module and
an npm package that each depend only on their language's SDK. The format and both ends of it live in
one repository, and ynr's tests read what both write.

## What it costs

A file is not an endpoint. The reader finds out about new data by reading, so there is a short delay:
it ships what it has read in batches every 15 seconds or 16 MB, whichever comes first.
Vendor CLIs export only over the network, so they need the relay as a translator. And a file in a
run's folder is hostile input, since the agent can write straight into it; the reader treats it as
such ([Provenance and stamping](provenance-and-stamping.md)).

## What was not chosen

- **Exporting to a local collector over the network.** In-memory loss, no path from a docker run, an
  egress exception each. Kept only for vendor CLIs, through the relay, and for operators who set
  `OTEL_*`.
- **Starting a collector when ynr is found.** A background process nobody asked for.
- **A per-user service install for laptops.** ynr is a command that does one thing while it runs;
  supervising it belongs to whoever starts it.

See [ADR-004](../adr/004-spool-detection-and-wiring.md).
