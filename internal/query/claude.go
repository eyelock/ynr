package query

import (
	"fmt"
	"time"

	"github.com/eyelock/ynr/internal/store"
)

// The queries over Claude Code's own records (ynw-17g.7): what its metrics and events say, not
// what ynh's ynh.run spans say. They read what ynr relay wrote, so a seat that ynh run launched
// with the relay on is a seat here, and a desk subagent is named by the agent.name or agent_type
// Claude Code puts on its api_request events. None has rollups: they read records, as recent does.

// sessionSeats is the CTE that names a seat for Claude Code's records. Under ynh run, Claude Code's
// own records carry ynh.session.id but not the harness name; the name is on ynh's ynh.session span
// (ynw-17xrm). So a record is joined to that span by ynh.session.id, and the seat is the span's
// ynh.harness.name. The span is read from the hours the window touches, like the records: a session
// that began before the window, with its span in an earlier hour, leaves its records unnamed.
const sessionSeats = `sessions AS (
  SELECT pick(resource, attributes, 'ynh.session.id') AS sid,
         min(pick(resource, attributes, 'ynh.harness.name')) AS session_seat
  FROM spans
  WHERE name = 'ynh.session' AND pick(resource, attributes, 'ynh.session.id') IS NOT NULL
  GROUP BY sid
)`

// seatSQL is the seat of a record: a harness name the record itself carries, else the one its
// session span names, else (unknown). It needs the sessions CTE joined as s.
const seatSQL = "coalesce(harness, s.session_seat, '(unknown)')"

// claudeUsage is the shared body of seats and accounts: Claude Code's token, cost and active-time
// metrics as one row per day and dim, where dim is a SQL expression over the columns resource and
// attributes. A delta point counts as it is. A cumulative series counts its growth since the
// previous day it reported, so a session that spans days is not counted whole each day; the first
// day of a series inside the window counts its whole total.
func claudeUsage(dimName, dim string) string {
	return fmt.Sprintf(`
WITH %[3]s, pts AS (
  SELECT CAST(time AS DATE) AS day, %[2]s AS dim, name, attr(attributes, 'type') AS kind,
         temporality, value, list_sort(resource) AS resource, list_sort(attributes) AS attributes, start_time
  FROM metric_points
  LEFT JOIN sessions s ON s.sid = pick(resource, attributes, 'ynh.session.id')
  WHERE name IN ('claude_code.token.usage', 'claude_code.cost.usage', 'claude_code.active_time.total')
    AND time >= $since AND time < $until AND ($lane = '' OR lane = $lane) AND labels_match(labels, $labels)
), series AS (
  SELECT day, dim, name, kind, temporality, resource, attributes, start_time,
         CASE WHEN temporality = 'cumulative' THEN max(value) ELSE sum(value) END AS v
  FROM pts
  GROUP BY day, dim, name, kind, temporality, resource, attributes, start_time
), inc AS (
  SELECT *, CASE WHEN temporality = 'cumulative'
                 THEN v - coalesce(lag(v) OVER (PARTITION BY name, resource, attributes, start_time ORDER BY day), 0)
                 ELSE v END AS n
  FROM series
)
SELECT strftime(day, '%%Y-%%m-%%d') AS day, dim AS %[1]s,
       sum(n) FILTER (WHERE name = 'claude_code.token.usage' AND kind = 'input')::BIGINT AS input_tokens,
       sum(n) FILTER (WHERE name = 'claude_code.token.usage' AND kind = 'output')::BIGINT AS output_tokens,
       sum(n) FILTER (WHERE name = 'claude_code.token.usage' AND kind = 'cacheRead')::BIGINT AS cache_read_tokens,
       sum(n) FILTER (WHERE name = 'claude_code.token.usage' AND kind = 'cacheCreation')::BIGINT AS cache_creation_tokens,
       round(sum(n) FILTER (WHERE name = 'claude_code.cost.usage'), 4) AS cost_usd,
       round(sum(n) FILTER (WHERE name = 'claude_code.active_time.total'), 1) AS active_s
FROM inc
GROUP BY day, dim
ORDER BY day DESC, cost_usd DESC NULLS LAST, dim`, dimName, dim, sessionSeats)
}

var claudeSeats = Query{
	Name:    "seats",
	Help:    "Claude Code's tokens, cost and active time by day for each seat (named by the ynh.session span the records join by ynh.session.id)",
	Since:   7 * 24 * time.Hour,
	Signals: []string{store.Metrics, store.Traces},
	SQL:     claudeUsage("seat", seatSQL),
}

var claudeAccounts = Query{
	Name:    "accounts",
	Help:    "Claude Code's tokens, cost and active time by day for each hashed account",
	Since:   7 * 24 * time.Hour,
	Signals: []string{store.Metrics, store.Traces},
	SQL:     claudeUsage("account", "coalesce(pick(resource, attributes, 'user.account_uuid'), '(none)')"),
}

var claudeSubagents = Query{
	Name:    "subagents",
	Help:    "Claude Code's requests, tokens, cost and request time by day for each seat's subagents",
	Since:   7 * 24 * time.Hour,
	Signals: []string{store.Logs, store.Traces},
	SQL: `
WITH ` + sessionSeats + `
SELECT strftime(CAST(time AS DATE), '%Y-%m-%d') AS day, ` + seatSQL + ` AS seat,
       coalesce(attr(attributes, 'agent.name'), attr(attributes, 'agent_type'),
                attr(attributes, 'subagent_type'), '(main)') AS agent,
       count(*) AS requests,
       sum(TRY_CAST(attr(attributes, 'input_tokens') AS BIGINT)) AS input_tokens,
       sum(TRY_CAST(attr(attributes, 'output_tokens') AS BIGINT)) AS output_tokens,
       sum(TRY_CAST(attr(attributes, 'cache_read_tokens') AS BIGINT)) AS cache_read_tokens,
       sum(TRY_CAST(attr(attributes, 'cache_creation_tokens') AS BIGINT)) AS cache_creation_tokens,
       round(sum(TRY_CAST(attr(attributes, 'cost_usd') AS DOUBLE)), 4) AS cost_usd,
       round(sum(TRY_CAST(attr(attributes, 'duration_ms') AS DOUBLE)) / 1000, 1) AS request_s
FROM logs
LEFT JOIN sessions s ON s.sid = pick(resource, attributes, 'ynh.session.id')
WHERE event IN ('api_request', 'claude_code.api_request')
  AND time >= $since AND time < $until AND ($lane = '' OR lane = $lane) AND labels_match(labels, $labels)
GROUP BY ALL
ORDER BY day DESC, cost_usd DESC NULLS LAST, seat, agent`,
}
