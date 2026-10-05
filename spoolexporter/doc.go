// Package spoolexporter writes OpenTelemetry traces, metrics and logs to a spool folder as OTLP
// JSON lines, one export request per line, for a collector to read and ship.
//
// It is an ordinary OpenTelemetry exporter: it knows nothing of the collector that reads the
// folder, and the folder is the only contract between them. Each process writes its own files,
// named <service>-<instance>-<seq>, so there is no locking between writers. The active file ends
// .open.jsonl and is renamed to .jsonl when it is rotated or the spool is closed. A reader may
// read an open file up to its last complete line, and may delete a closed file once it has
// shipped it.
//
// Writing never fails the caller. A record that cannot be written, because a file cap is reached
// or the folder cannot be written, is dropped and counted (see Stats).
//
// Typical use, after the caller has decided to write to dir:
//
//	sp, err := spoolexporter.Open(dir, spoolexporter.WithService("mytool"))
//	if err != nil { ... }
//	se, _ := sp.SpanExporter(ctx)
//	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(se, sdktrace.WithBatchTimeout(time.Second)))
//	...
//	// at the end of each unit of work, so a host crash loses nothing already finished:
//	_ = tp.ForceFlush(ctx)
//	sp.Sync(ctx)
//	...
//	// on exit, after every provider has shut down:
//	_ = tp.Shutdown(ctx)
//	_ = sp.Close()
//
// Records are handed to the exporters by the SDK's batch processors; configure them to export at
// least every second so that a process crash loses at most a second of records.
package spoolexporter
