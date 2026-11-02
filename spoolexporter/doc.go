// Package spoolexporter is an OpenTelemetry exporter that writes traces,
// metrics and logs as OTLP JSON lines into a spool folder, where a separate
// reader collects them. It can also post the same OTLP/JSON to an operator's
// OTLP/HTTP endpoint (see "Sending to an operator's endpoint").
//
// It imports nothing but the standard library and the OpenTelemetry SDK: the
// network exporter is net/http, with no gRPC and no protobuf. It knows
// nothing of the program using it or of the reader.
//
// # Format
//
// A spool folder belongs to one or more writer processes. Each process writes
// its own files, so there is no locking:
//
//	<service>-<instance id>-<seq>.open.jsonl   the file being written
//	<service>-<instance id>-<seq>.jsonl        a closed file
//
// Each line is one OTLP export request in the OTLP/JSON encoding: an
// ExportTraceServiceRequest (resourceSpans), an ExportMetricsServiceRequest
// (resourceMetrics) or an ExportLogsServiceRequest (resourceLogs). Trace and
// span ids are lowercase hex, 64-bit integers and timestamps are decimal
// strings, enums are numbers, as the OTLP specification requires of JSON.
// All signals share a file.
//
// # Guarantees
//
//   - A file is rotated, renamed from .open.jsonl to .jsonl, once the next
//     line would take it past MaxFileBytes (8 MiB by default), and on Close.
//   - The writer's files on disk are capped at MaxBytes (64 MiB by default).
//     Over the cap, new records are dropped and counted; nothing fails. A
//     closed file the reader has removed no longer counts.
//   - An export request larger than MaxLineBytes (4 MiB by default) is
//     dropped and counted.
//   - A line is written with one write call, so a batch already written
//     survives the process being killed.
//   - Files are created exclusively and never through a link planted at
//     their name.
//   - Sync flushes the open file to disk, bounded by SyncTimeout (2 seconds
//     by default): past it the flush is abandoned, so a slow network
//     filesystem never blocks the caller.
//   - Every error is swallowed and counted (see Stats). The exporters never
//     return one, so a failing spool cannot reach the program's output or
//     its exit code.
//
// # Use
//
// One Writer serves all three exporters:
//
//	w := spoolexporter.NewWriter(spoolexporter.Options{Dir: dir, Service: "mytool", InstanceID: id})
//	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(spoolexporter.NewTraceExporter(w),
//		sdktrace.WithBatchTimeout(time.Second)))
//	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(
//		spoolexporter.NewLogExporter(w), sdklog.WithExportInterval(time.Second))))
//	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
//		spoolexporter.NewMetricExporter(w))))
//
// # Sending to an operator's endpoint
//
// A tool that must also export to the endpoint an operator names in
// OTEL_EXPORTER_OTLP_* uses an HTTPSender, which posts the same OTLP/JSON
// over OTLP/HTTP, and the HTTP exporters beside the spool ones. It adds no
// module: it is net/http and the encoder above.
//
//	opts, configured, unsupported := spoolexporter.HTTPOptionsFromEnv(os.Getenv)
//	if unsupported != "" {
//		fmt.Fprintln(os.Stderr, "not exporting to OTEL_EXPORTER_OTLP_*:", unsupported)
//	} else if configured {
//		h := spoolexporter.NewHTTPSender(opts)
//		tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(spoolexporter.NewHTTPTraceExporter(h)))
//		// NewHTTPLogExporter and NewHTTPMetricExporter likewise.
//	}
//
// It reads OTEL_EXPORTER_OTLP_ENDPOINT (with /v1/traces, /v1/logs and
// /v1/metrics appended), the per-signal OTEL_EXPORTER_OTLP_{TRACES,LOGS,
// METRICS}_ENDPOINT (used as given), HEADERS and the per-signal HEADERS (a
// key=value list, values percent-decoded, the per-signal list winning), and
// TIMEOUT in milliseconds (the longest of the general and per-signal values
// applies to every signal). The body is application/json.
//
// A signal with no endpoint is not sent: if only some per-signal endpoints
// are set and there is no general OTEL_EXPORTER_OTLP_ENDPOINT, the other
// signals' exporters discard what they are given, and do not count it in
// Stats.
//
// It does not support, and HTTPOptionsFromEnv reports by name so the caller
// can print one note and send nothing: a PROTOCOL other than http/json (grpc
// and http/protobuf), a custom CA or client certificate (the *CERTIFICATE*
// and *CLIENT_KEY* settings) and compression (COMPRESSION=gzip).
//
// An HTTPSender holds the same promise as the Writer. Each export, and so the
// flush on shutdown, is bounded by the timeout (DefaultHTTPTimeout, 2
// seconds, unless the operator sets one); there is no retry; a refused
// connection, a timeout or a non-2xx answer is counted in Stats, never
// returned. Redirects are not followed. Header values, which carry
// credentials, appear in no Stats, error or log.
//
// # Batching
//
// Batching is the SDK's job: register the exporters with the SDK's batch
// processors, at an export interval of one second or less, so records reach
// the file at least every second. Call the providers' ForceFlush and then
// Writer.Sync at the end of each unit of work, and Writer.Close after the
// providers have shut down.
package spoolexporter
