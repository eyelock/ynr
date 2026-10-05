-- The batch views (ADR-005): batch_spans, batch_logs and batch_metric_points, flattened from the
-- OTLP JSON export requests in a set of the store's batches. @traces@, @logs@ and @metrics@ are
-- replaced with a read of that signal's batch files, or an empty relation of the same shape.
-- The named queries read spans, logs and metric_points: views over these when reading the store
-- directly, and the hot tier's tables in a running ynr serve.
--
-- Shipping is at least once, so the same lines can be in two batches. Each view keeps one copy
-- of a record, by its record_id: a span's trace id and span id, and a hash of a log record's or
-- metric point's content with its resource.

CREATE OR REPLACE VIEW batch_spans AS
WITH r AS (
  SELECT filename AS file, unnest(resourceSpans) AS rs FROM @traces@
), s AS (
  SELECT file, otel_attrs(rs.resource.attributes) AS resource, unnest(rs.scopeSpans) AS ss FROM r
), x AS (
  SELECT file, resource, ss.scope.name AS scope, unnest(ss.spans) AS sp FROM s
), y AS (
  SELECT file, resource, scope, sp, otel_attrs(sp.attributes) AS attributes,
         lower(sp.traceId) AS trace_id, lower(sp.spanId) AS span_id
  FROM x
)
SELECT trace_id || '/' || span_id AS record_id,
       otel_time(sp.startTimeUnixNano) AS time, otel_time(sp.endTimeUnixNano) AS end_time,
       CAST(otel_ns(sp.endTimeUnixNano) - otel_ns(sp.startTimeUnixNano) AS DOUBLE) / 1e6 AS duration_ms,
       trace_id, span_id, nullif(lower(sp.parentSpanId), '') AS parent_span_id,
       sp.name AS name, otel_kind(sp.kind) AS kind, otel_status(sp.status.code) AS status,
       sp.status.message AS status_message,
       @promoted@,
       TRY_CAST(attr(attributes, 'ynh.run.cost_usd') AS DOUBLE) AS cost_usd,
       scope, resource, attributes, file
FROM y
QUALIFY row_number() OVER (PARTITION BY record_id ORDER BY file) = 1;

CREATE OR REPLACE VIEW batch_logs AS
WITH r AS (
  SELECT filename AS file, unnest(resourceLogs) AS rl FROM @logs@
), s AS (
  SELECT file, rl.resource AS raw_resource, otel_attrs(rl.resource.attributes) AS resource,
         unnest(rl.scopeLogs) AS sl
  FROM r
), x AS (
  SELECT file, raw_resource, resource, sl.scope.name AS scope, unnest(sl.logRecords) AS lr FROM s
), y AS (
  SELECT file, resource, scope, lr, otel_attrs(lr.attributes) AS attributes,
         md5(raw_resource::VARCHAR || lr::VARCHAR) AS record_hash
  FROM x
)
SELECT record_hash AS record_id,
       coalesce(otel_time(lr.timeUnixNano), otel_time(lr.observedTimeUnixNano)) AS time,
       coalesce(nullif(lr.eventName, ''), attr(attributes, 'event.name')) AS event,
       lr.severityText AS severity, TRY_CAST(otel_text(lr.severityNumber) AS INTEGER) AS severity_number,
       coalesce(lr.body->>'stringValue', lr.body::VARCHAR) AS body,
       nullif(lower(lr.traceId), '') AS trace_id, nullif(lower(lr.spanId), '') AS span_id,
       @promoted@,
       scope, resource, attributes, file
FROM y
QUALIFY row_number() OVER (PARTITION BY record_id ORDER BY file) = 1;

CREATE OR REPLACE VIEW batch_metric_points AS
WITH r AS (
  SELECT filename AS file, unnest(resourceMetrics) AS rm FROM @metrics@
), s AS (
  SELECT file, rm.resource AS raw_resource, otel_attrs(rm.resource.attributes) AS resource,
         unnest(rm.scopeMetrics) AS sm
  FROM r
), x AS (
  SELECT file, raw_resource, resource, sm.scope.name AS scope, unnest(sm.metrics) AS m FROM s
), p AS (
  SELECT file, raw_resource, resource, scope, m.name, m.unit, 'gauge' AS type,
         NULL::VARCHAR AS temporality, NULL::BOOLEAN AS monotonic, unnest(m.gauge.dataPoints) AS dp
  FROM x
  UNION ALL
  SELECT file, raw_resource, resource, scope, m.name, m.unit, 'sum',
         otel_temporality(m.sum.aggregationTemporality), m.sum.isMonotonic, unnest(m.sum.dataPoints)
  FROM x
), h AS (
  SELECT file, raw_resource, resource, scope, m.name, m.unit, 'histogram' AS type,
         otel_temporality(m.histogram.aggregationTemporality) AS temporality,
         unnest(m.histogram.dataPoints) AS dp
  FROM x
  UNION ALL
  SELECT file, raw_resource, resource, scope, m.name, m.unit, 'exponential_histogram',
         otel_temporality(m.exponentialHistogram.aggregationTemporality),
         unnest(m.exponentialHistogram.dataPoints)
  FROM x
), y AS (
  SELECT file, resource, scope, name, unit, type, temporality, monotonic,
         dp.startTimeUnixNano AS start_ns, dp.timeUnixNano AS time_ns,
         coalesce(otel_num(dp.asDouble), otel_num(dp.asInt)) AS value, NULL::DOUBLE AS count,
         otel_attrs(dp.attributes) AS attributes,
         md5(raw_resource::VARCHAR || name || dp::VARCHAR) AS record_hash
  FROM p
  UNION ALL
  SELECT file, resource, scope, name, unit, type, temporality, NULL,
         dp.startTimeUnixNano, dp.timeUnixNano, otel_num(dp.sum), otel_num(dp.count),
         otel_attrs(dp.attributes), md5(raw_resource::VARCHAR || name || dp::VARCHAR)
  FROM h
)
SELECT record_hash AS record_id,
       otel_time(time_ns) AS time, otel_time(start_ns) AS start_time, name, unit, type,
       temporality, monotonic, value, count,
       @promoted@,
       scope, resource, attributes, file
FROM y
QUALIFY row_number() OVER (PARTITION BY record_id ORDER BY file) = 1;
