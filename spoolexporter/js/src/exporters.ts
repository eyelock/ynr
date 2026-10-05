import { type ExportResult, ExportResultCode } from "@opentelemetry/core";
import {
  JsonLogsSerializer,
  JsonMetricsSerializer,
  JsonTraceSerializer,
} from "@opentelemetry/otlp-transformer";
import type { LogRecordExporter, ReadableLogRecord } from "@opentelemetry/sdk-logs";
import type {
  AggregationTemporality,
  InstrumentType,
  PushMetricExporter,
  ResourceMetrics,
} from "@opentelemetry/sdk-metrics";
import type { ReadableSpan, SpanExporter } from "@opentelemetry/sdk-trace";
import type { SpoolWriter } from "./writer.js";

// The exporters always report success: a failing spool is counted by the
// writer, never surfaced to the SDK, so it cannot reach the program's output.
const SUCCESS: ExportResult = { code: ExportResultCode.SUCCESS };

// OpenTelemetry's own OTLP/JSON serializers: hex ids, string timestamps,
// numeric enums, as the OTLP specification requires of JSON.
function encode(serialize: () => Uint8Array | undefined, w: SpoolWriter, records: number): void {
  let json: Uint8Array | undefined;
  try {
    json = serialize();
  } catch {
    json = undefined;
  }
  if (!json) {
    w.drop(records);
    return;
  }
  w.writeLine(json, records);
}

/** Writes spans to a SpoolWriter as ExportTraceServiceRequest lines. */
export class SpoolSpanExporter implements SpanExporter {
  private stopped = false;
  constructor(private readonly w: SpoolWriter) {}

  export(spans: ReadableSpan[], done: (result: ExportResult) => void): void {
    if (spans.length > 0) {
      if (this.stopped) this.w.drop(spans.length);
      else encode(() => JsonTraceSerializer.serializeRequest(spans), this.w, spans.length);
    }
    done(SUCCESS);
  }

  /** Flushes the writer's open file to disk, bounded by its sync timeout. */
  forceFlush(): Promise<void> {
    return this.w.sync();
  }

  /** Stops the exporter. It leaves the writer open; close it once every exporter has shut down. */
  async shutdown(): Promise<void> {
    this.stopped = true;
  }
}

/** Writes log records to a SpoolWriter as ExportLogsServiceRequest lines. */
export class SpoolLogExporter implements LogRecordExporter {
  private stopped = false;
  constructor(private readonly w: SpoolWriter) {}

  export(logs: ReadableLogRecord[], done: (result: ExportResult) => void): void {
    if (logs.length > 0) {
      if (this.stopped) this.w.drop(logs.length);
      else encode(() => JsonLogsSerializer.serializeRequest(logs), this.w, logs.length);
    }
    done(SUCCESS);
  }

  forceFlush(): Promise<void> {
    return this.w.sync();
  }

  async shutdown(): Promise<void> {
    this.stopped = true;
  }
}

/** Options for SpoolMetricExporter. */
export interface SpoolMetricExporterOptions {
  /** Chooses each instrument's temporality. The default is cumulative. */
  temporalitySelector?: (instrumentType: InstrumentType) => AggregationTemporality;
}

// AggregationTemporality.CUMULATIVE, without a runtime import of sdk-metrics.
const CUMULATIVE = 1 as AggregationTemporality;

/** Writes metrics to a SpoolWriter as ExportMetricsServiceRequest lines. */
export class SpoolMetricExporter implements PushMetricExporter {
  private stopped = false;
  private readonly temporality: (instrumentType: InstrumentType) => AggregationTemporality;

  constructor(
    private readonly w: SpoolWriter,
    opts: SpoolMetricExporterOptions = {},
  ) {
    this.temporality = opts.temporalitySelector ?? (() => CUMULATIVE);
  }

  export(metrics: ResourceMetrics, done: (result: ExportResult) => void): void {
    const points = metrics.scopeMetrics.reduce(
      (n, sm) => n + sm.metrics.reduce((m, md) => m + md.dataPoints.length, 0),
      0,
    );
    if (points > 0) {
      if (this.stopped) this.w.drop(points);
      else encode(() => JsonMetricsSerializer.serializeRequest(metrics), this.w, points);
    }
    done(SUCCESS);
  }

  selectAggregationTemporality(instrumentType: InstrumentType): AggregationTemporality {
    return this.temporality(instrumentType);
  }

  forceFlush(): Promise<void> {
    return this.w.sync();
  }

  async shutdown(): Promise<void> {
    this.stopped = true;
  }
}
