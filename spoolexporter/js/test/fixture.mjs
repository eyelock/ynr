// Writes one of each signal into the spool folder named by argv[2], for ynr's
// receiver test to read: proof that the reader accepts what this package writes.
import { resourceFromAttributes } from "@opentelemetry/resources";
import { LoggerProvider, SimpleLogRecordProcessor } from "@opentelemetry/sdk-logs";
import { MeterProvider, PeriodicExportingMetricReader } from "@opentelemetry/sdk-metrics";
import { SimpleSpanProcessor, TracerProvider } from "@opentelemetry/sdk-trace";
import { SpoolLogExporter, SpoolMetricExporter, SpoolSpanExporter, SpoolWriter } from "../dist/index.js";

const w = new SpoolWriter({ dir: process.argv[2], service: "ynm", instanceId: "js1" });
const resource = resourceFromAttributes({ "service.name": "ynm", "ynr.provenance": "factory" });
const tp = new TracerProvider({ resource, spanProcessors: [new SimpleSpanProcessor({ exporter: new SpoolSpanExporter(w) })] });
const lp = new LoggerProvider({ resource, processors: [new SimpleLogRecordProcessor({ exporter: new SpoolLogExporter(w) })] });
const mp = new MeterProvider({
  resource,
  readers: [new PeriodicExportingMetricReader({ exporter: new SpoolMetricExporter(w), exportIntervalMillis: 60_000 })],
});

lp.getLogger("ynm").emit({ eventName: "ynm.request.started", attributes: { n: 1 } });
tp.getTracer("ynm").startSpan("ynm.request", { attributes: { i: 42, f: 1.5, ok: true } }).end();
const m = mp.getMeter("ynm");
m.createCounter("requests").add(2);
m.createGauge("queue").record(4);
m.createHistogram("latency").record(12);

await tp.shutdown();
await lp.shutdown();
await mp.shutdown();
await w.close();
const s = w.stats();
if (s.dropped || s.errors) {
  console.error(JSON.stringify(s));
  process.exit(1);
}
