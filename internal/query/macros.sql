-- The macros the batch views and the named queries use (ADR-005). Defined once per database.

-- An attribute value as text: strings as they are, numbers and booleans as written, arrays and
-- maps as JSON.
CREATE MACRO otel_str(v) AS coalesce(
  v->>'stringValue', v->>'intValue', v->>'boolValue', v->>'doubleValue', v->>'bytesValue',
  (v->'arrayValue')::VARCHAR, (v->'kvlistValue')::VARCHAR);

-- Attributes as a list of (k, v), not a map: a map refuses duplicate or null keys, and one bad
-- record must not fail every query.
CREATE MACRO otel_attrs(l) AS
  coalesce(list_transform(l, lambda a: struct_pack(k := a.key, v := otel_str(a.value))), []);

-- One attribute's value, or NULL.
CREATE MACRO attr(l, key) AS list_filter(l, lambda a: a.k = key)[1].v;

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

-- A run's labels (ADR-006 rule 10): a map of key to value from the resource's ynh.label.<key>
-- attributes, the key without the prefix. A key set twice keeps its first value, because a map
-- refuses a duplicate key and one bad record must not fail every query.
CREATE MACRO labels_of(res) AS map_from_entries(
  list_filter(
    list_transform(
      list_filter(res, lambda a: starts_with(a.k, 'ynh.label.') AND length(a.k) > 10 AND a.v IS NOT NULL),
      lambda a: struct_pack(k := substr(a.k, 11), v := a.v)),
    lambda e, i: i = list_position(
      list_transform(
        list_filter(res, lambda a: starts_with(a.k, 'ynh.label.') AND length(a.k) > 10 AND a.v IS NOT NULL),
        lambda a: substr(a.k, 11)), e.k)));

-- True when every wanted label matches. want is the labels joined by chr(31), each as key:value,
-- or empty for no filter. A key never holds a colon, so the first colon splits a pair.
CREATE MACRO labels_match(m, want) AS want = '' OR list_bool_and(list_transform(
  string_split(want, chr(31)),
  lambda w: coalesce(element_at(m, split_part(w, ':', 1))[1] = substr(w, strpos(w, ':') + 1), false)));
