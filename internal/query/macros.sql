-- The macros the batch views and the named queries use (ADR-005). Defined once per database.

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
