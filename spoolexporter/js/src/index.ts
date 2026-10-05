/**
 * OpenTelemetry exporters that write traces, metrics and logs as OTLP JSON
 * lines into a spool folder, where a separate reader collects them. The
 * format and guarantees match the Go module github.com/eyelock/ynr/spoolexporter.
 *
 * Each process writes its own files, `<service>-<instance id>-<seq>.open.jsonl`,
 * renamed to `.jsonl` on rotation (8 MiB) and close. Each line is one OTLP
 * export request. Files are capped at 64 MiB per writer and lines at 4 MiB;
 * over either, records are dropped and counted. Nothing ever throws or
 * reports failure to the SDK.
 *
 * Register the exporters with batch processors exporting at least every
 * second; call the providers' forceFlush and then `writer.sync()` at the end
 * of each unit of work, and `writer.close()` after the providers shut down.
 */
export {
  SpoolLogExporter,
  SpoolMetricExporter,
  type SpoolMetricExporterOptions,
  SpoolSpanExporter,
} from "./exporters.js";
export {
  DEFAULT_MAX_BYTES,
  DEFAULT_MAX_FILE_BYTES,
  DEFAULT_MAX_LINE_BYTES,
  DEFAULT_SYNC_TIMEOUT_MS,
  fileNamePart,
  type SpoolStats,
  SpoolWriter,
  type SpoolWriterOptions,
} from "./writer.js";
