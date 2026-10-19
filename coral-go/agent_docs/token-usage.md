# Token Usage API

Read token and cost records captured by Coral's agent usage hooks.

## Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/token-usage` | Records plus totals; filters: `session_id`, `board_name`, `team_id`, `since`. |
| `GET` | `/api/token-usage/summary` | Totals grouped by agent type, agent and model (`by_agent_type`, `by_agent`, `by_model`, `by_thinking`); optional `since`. |
| `GET` | `/api/token-usage/session/{sessionID}/turns` | Per-turn and cumulative cost for a session. |
| `GET` | `/api/token-usage/timeseries` | Bucketed cost and tokens; `interval` defaults to `1h` and accepts `5m`, `1h`, or `1d`; optional `since`. |
| `GET` | `/api/token-usage/by-team` | Usage grouped by board/team; optional `since`. |
| `GET` | `/api/token-usage/by-branch` | Usage grouped by repository branch; optional `since` and exact `branch`. |

`since` is an RFC 3339 timestamp. Monetary values are USD. The list endpoint
returns `{ "records": [...], "totals": {...} }`; grouping endpoints return
their named collection (`by_agent_type`, `turns`, `buckets`, `teams`, or
`branches`) and, where applicable, a `totals` object.

`by_model` has one row per agent type and model id: `agent_type`, `model`,
`num_sessions`, `input_tokens`, `output_tokens`, `cache_read_tokens`,
`cache_write_tokens`, `total_tokens` and `cost_usd`. The model is the one each
usage row recorded (a Claude assistant message, a Codex turn context), so a
session that switched models is split between the models' rows. Usage with no
recorded model, including everything stored before models were recorded, is the
row with `model` `""`; it is never filled with a guess. The list is `[]` when
there is no usage.

`by_thinking` has one row per agent type and thinking level (`agent_type`,
`thinking`, `input_tokens`, `output_tokens`, `cache_read_tokens`,
`cache_write_tokens`, `total_tokens`, `thinking_tokens`, `cost_usd`,
`num_calls`). The level is the one each call's turn ran at as the CLI's
transcript recorded it (Claude's `effort` on an assistant entry, Codex's
`turn_context` effort), kept by UTC hour as calls are recorded, so a `since`
inside an hour includes that hour. `thinking_tokens` is what the CLI reported
for thinking (Claude's `thinking_tokens`, the difference of Codex's cumulative
`reasoning_output_tokens`); it is shown apart and never added to a total.
Each agent type also has a row with `thinking` `""` ("Not reported") for the
rest of its usage: calls whose turn recorded no level, a CLI that writes none
(agy, Pi), and everything from before levels were tracked. It is the type's
total less the tracked levels, and never the level an agent was launched with.

A live session's `model` (the sidebar feed and `GET /api/sessions/live`) is the
model the agent uses now: the transcript's latest (Claude, Codex), else the one
agy's hooks last reported (`modelName`), else the one it was launched with.
`model_source` says which: `transcript`, `hook` or `launch`. The field is absent
when none is known.

To submit a session snapshot, see
`POST /api/sessions/live/{name}/token-usage` in [Sessions](sessions.md).
