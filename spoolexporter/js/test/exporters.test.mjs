import assert from "node:assert/strict";
import { test } from "node:test";
import { context, trace, TraceFlags } from "@opentelemetry/api";
import { SeverityNumber } from "@opentelemetry/api-logs";
import { resourceFromAttributes } from "@opentelemetry/resources";
import { LoggerProvider, SimpleLogRecordProcessor } from "@opentelemetry/sdk-logs";
import { MeterProvider, PeriodicExportingMetricReader } from "@opentelemetry/sdk-metrics";
import { SimpleSpanProcessor, TracerProvider } from "@opentelemetry/sdk-trace";
import { SpoolLogExporter, SpoolMetricExporter, SpoolSpanExporter, SpoolWriter } from "../dist/index.js";
import { attrs, files, lines, tempDir } from "./helpers.mjs";

test("all signals are written as OTLP JSON lines in one file", async () => {
  const dir = tempDir();
  const w = new SpoolWriter({ dir, service: "svc", instanceId: "inst" });
  const resource = resourceFromAttributes({ "service.name": "svc" });

  const tp = new TracerProvider({ resource, spanProcessors: [new SimpleSpanProcessor({ exporter: new SpoolSpanExporter(w) })] });
  const lp = new LoggerProvider({ resource, processors: [new SimpleLogRecordProcessor({ exporter: new SpoolLogExporter(w) })] });
  const reader = new PeriodicExportingMetricReader({ exporter: new SpoolMetricExporter(w), exportIntervalMillis: 60_000 });
  const mp = new MeterProvider({ resource, readers: [reader] });

  const parent = trace.wrapSpanContext({
    traceId: "4bf92f3577b34da6a3ce929d0e0e4736",
    spanId: "00f067aa0ba902b7",
    traceFlags: TraceFlags.SAMPLED,
    isRemote: true,
  });
  const ctx = trace.setSpan(context.active(), parent);
  const span = tp.getTracer("scope", "1.2.3").startSpan("unit", { attributes: { i: 42, s: "v" } }, ctx);
  const spanCtx = trace.setSpan(ctx, span);
  lp.getLogger("scope").emit({
    eventName: "unit.started",
    severityNumber: SeverityNumber.INFO,
    body: "body",
    attributes: { count: 7 },
    context: spanCtx,
  });
  span.end();
  mp.getMeter("scope").createCounter("runs").add(3, { outcome: "ok" });

  await tp.shutdown();
  await lp.shutdown();
  await mp.shutdown();
  await w.close();

  assert.deepEqual(files(dir), ["svc-inst-000001.jsonl"]);
  const all = lines(dir);
  assert.equal(all.length, 3);
  const logs = all.find((l) => l.resourceLogs);
  const spans = all.find((l) => l.resourceSpans);
  const metrics = all.find((l) => l.resourceMetrics);

  const rec = logs.resourceLogs[0].scopeLogs[0].logRecords[0];
  assert.equal(rec.eventName, "unit.started");
  assert.equal(rec.traceId, "4bf92f3577b34da6a3ce929d0e0e4736");
  assert.equal(rec.spanId, span.spanContext().spanId);
  assert.equal(rec.severityNumber, 9);

  const sp = spans.resourceSpans[0].scopeSpans[0].spans[0];
  assert.equal(sp.traceId, "4bf92f3577b34da6a3ce929d0e0e4736");
  assert.equal(sp.parentSpanId, "00f067aa0ba902b7");
  assert.equal(typeof sp.startTimeUnixNano, "string");
  assert.equal(attrs(sp.attributes).i.intValue, 42);
  assert.equal(attrs(spans.resourceSpans[0].resource.attributes)["service.name"].stringValue, "svc");

  const m = metrics.resourceMetrics[0].scopeMetrics[0].metrics[0];
  assert.equal(m.name, "runs");
  assert.equal(m.sum.dataPoints[0].asDouble, 3);

  assert.deepEqual(w.stats(), { dropped: 0, errors: 0 });
});

test("records exported after shutdown are dropped and counted", async () => {
  const w = new SpoolWriter({ dir: tempDir() });
  const se = new SpoolSpanExporter(w);
  const le = new SpoolLogExporter(w);
  await se.shutdown();
  await le.shutdown();
  let result;
  le.export([{}, {}], (r) => {
    result = r;
  });
  assert.equal(result.code, 0);
  se.export([{}], () => {});
  assert.equal(w.stats().dropped, 3);
  await w.close();
});

test("a metric exporter defaults to cumulative temporality", () => {
  const e = new SpoolMetricExporter(new SpoolWriter({ dir: tempDir() }));
  assert.equal(e.selectAggregationTemporality(0), 1);
});
