-- The views every named query reads (ADR-005): spans, logs and metric_points, flattened from the
-- OTLP JSON export requests in the store's batches. @traces@, @logs@ and @metrics@ are replaced
-- with a read of that signal's batch files, or an empty relation of the same shape.
--
-- Shipping is at least once, so the same lines can be in two batches. Each view keeps one copy
-- of a record: spans by trace id and span id, log records and metric points by a hash of their
-- content with their resource.

-- An attribute value as text: strings as they are, numbers and booleans as written, arrays and
-- maps as JSON.
CREATE MACRO otel_str(v) AS coalesce(
  v->>'stringValue', v->>'intValue', v->>'boolValue', v->>'doubleValue', v->>'bytesValue',
  (v->'arrayValue')::VARCHAR, (v->'kvlistValue')::VARCHAR);

-- Attributes as a list of (k, v), not a map: a map refuses duplicate or null keys, and one bad
-- record must not fail every query.
CREATE MACRO otel_attrs(l) AS
  coalesce(list_transform(l, a -> struct_pack(k := a.key, v := otel_str(a.value))), []);

-- One attribute's value, or NULL.
CREATE MACRO attr(l, key) AS list_filter(l, a -> a.k = key)[1].v;

-- An attribute from the resource, where ynr stamps what a run's manifest says, else the record.
CREATE MACRO pick(res, att, key) AS coalesce(attr(res, key), attr(att, key));

-- OTLP JSON writes 64-bit integers as strings and enums as numbers, and readers accept either.
CREATE MACRO otel_text(j) AS trim(j::VARCHAR, '"');
CREATE MACRO otel_num(j) AS TRY_CAST(otel_text(j) AS DOUBLE);
CREATE MACRO otel_ns(j) AS TRY_CAST(otel_text(j) AS HUGEINT);
CREATE MACRO otel_time(j) AS
  CASE WHEN otel_ns(j) > 0 THEN make_timestamp(CAST(otel_ns(j) // 1000 AS BIGINT)) END;
CREATE MACRO otel_kind(j) AS CASE otel_text(j)
  WHEN '1' THEN 'internal' WHEN 'SPAN_KIND_INTERNAL' THEN 'internal'
  WHEN '2' THEN 'server' WHEN 'SPAN_KIND_SERVER' THEN 'server'
  WHEN '3' THEN 'client' WHEN 'SPAN_KIND_CLIENT' THEN 'client'
  WHEN '4' THEN 'producer' WHEN 'SPAN_KIND_PRODUCER' THEN 'producer'
  WHEN '5' THEN 'consumer' WHEN 'SPAN_KIND_CONSUMER' THEN 'consumer'
  ELSE 'unspecified' END;
CREATE MACRO otel_status(j) AS CASE otel_text(j)
  WHEN '1' THEN 'ok' WHEN 'STATUS_CODE_OK' THEN 'ok'
  WHEN '2' THEN 'error' WHEN 'STATUS_CODE_ERROR' THEN 'error'
  ELSE 'unset' END;
CREATE MACRO otel_temporality(j) AS CASE otel_text(j)
  WHEN '1' THEN 'delta' WHEN 'AGGREGATION_TEMPORALITY_DELTA' THEN 'delta'
  WHEN '2' THEN 'cumulative' WHEN 'AGGREGATION_TEMPORALITY_CUMULATIVE' THEN 'cumulative'
  END;

CREATE VIEW spans AS
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
SELECT otel_time(sp.startTimeUnixNano) AS time, otel_time(sp.endTimeUnixNano) AS end_time,
       CAST(otel_ns(sp.endTimeUnixNano) - otel_ns(sp.startTimeUnixNano) AS DOUBLE) / 1e6 AS duration_ms,
       trace_id, span_id, nullif(lower(sp.parentSpanId), '') AS parent_span_id,
       sp.name AS name, otel_kind(sp.kind) AS kind, otel_status(sp.status.code) AS status,
       sp.status.message AS status_message,
       @promoted@,
       TRY_CAST(attr(attributes, 'ynh.run.cost_usd') AS DOUBLE) AS cost_usd,
       scope, resource, attributes, file
FROM y
QUALIFY row_number() OVER (PARTITION BY trace_id, span_id ORDER BY file) = 1;

CREATE VIEW logs AS
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
SELECT coalesce(otel_time(lr.timeUnixNano), otel_time(lr.observedTimeUnixNano)) AS time,
       coalesce(nullif(lr.eventName, ''), attr(attributes, 'event.name')) AS event,
       lr.severityText AS severity, TRY_CAST(otel_text(lr.severityNumber) AS INTEGER) AS severity_number,
       coalesce(lr.body->>'stringValue', lr.body::VARCHAR) AS body,
       nullif(lower(lr.traceId), '') AS trace_id, nullif(lower(lr.spanId), '') AS span_id,
       @promoted@,
       scope, resource, attributes, file
FROM y
QUALIFY row_number() OVER (PARTITION BY record_hash ORDER BY file) = 1;

CREATE VIEW metric_points AS
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
SELECT otel_time(time_ns) AS time, otel_time(start_ns) AS start_time, name, unit, type,
       temporality, monotonic, value, count,
       @promoted@,
       scope, resource, attributes, file
FROM y
QUALIFY row_number() OVER (PARTITION BY record_hash ORDER BY file) = 1;
