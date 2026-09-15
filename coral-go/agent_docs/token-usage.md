# Token Usage API

Read token and cost records captured by Coral's hooks and local LLM proxy.

## Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/token-usage` | Records plus totals; filters: `session_id`, `board_name`, `team_id`, `since`. |
| `GET` | `/api/token-usage/summary` | Totals grouped by agent type and agent; optional `since`. |
| `GET` | `/api/token-usage/session/{sessionID}/turns` | Per-turn and cumulative cost for a session. |
| `GET` | `/api/token-usage/timeseries` | Bucketed cost and tokens; `interval` defaults to `1h` and accepts `5m`, `1h`, or `1d`; optional `since`. |
| `GET` | `/api/token-usage/by-team` | Usage grouped by board/team; optional `since`. |
| `GET` | `/api/token-usage/by-branch` | Usage grouped by repository branch; optional `since` and exact `branch`. |

`since` is an RFC 3339 timestamp. Monetary values are USD. The list endpoint
returns `{ "records": [...], "totals": {...} }`; grouping endpoints return
their named collection (`by_agent_type`, `turns`, `buckets`, `teams`, or
`branches`) and, where applicable, a `totals` object.

To submit a session snapshot, see
`POST /api/sessions/live/{name}/token-usage` in [Sessions](sessions.md).
