# Spec: Agent Internal Task Tracking

> Historical design proposal. Some commands and behavior below have been superseded.
> For the implemented contract, see [personal task CLI and API](../../agent_docs/agent-tasks.md) and [current queue behavior](../BOARD_TASKS/task-queue-behavior.md).

## Problem

Claude agents create internal task lists (via `TaskCreate`/`TaskUpdate` in Claude Code) to plan and track their own work. These tasks represent granular work items — "Fix auth bug", "Write tests", "Refactor handler" — that map directly to API cost. Today Coral only tracks **board-level tasks** (assignments posted to the message board). Agent-internal tasks are invisible, even though they represent the actual unit of work and cost.

## Opportunity

By capturing agent-internal tasks alongside board tasks, we get:

- **Cost-per-task granularity**: Know that "Write tests" cost $0.12 and "Refactor handler" cost $0.45
- **Work visibility**: See what each agent is actually doing, not just what it was assigned
- **Efficiency insights**: Compare cost across similar tasks, identify expensive patterns
- **Audit trail**: Full record of what work was done and what it cost

## Data Source

Claude Code's hook system fires events that include task state. The relevant hooks:

### TaskCreate / TaskUpdate hooks (available in Claude Code)

When an agent creates or updates a task, the hook payload includes:

```json
{
  "hook_event_name": "PostToolUse",
  "tool_name": "TaskCreate",
  "session_id": "abc-123",
  "tool_input": {
    "subject": "Fix proxy auth passthrough",
    "description": "Update proxy to forward Authorization headers"
  }
}
```

```json
{
  "hook_event_name": "PostToolUse",
  "tool_name": "TaskUpdate",
  "session_id": "abc-123",
  "tool_input": {
    "taskId": "1",
    "status": "completed"
  }
}
```

### Stop hook (already captured)

The Stop hook includes cumulative token usage for the session. Combined with task timestamps, we can attribute cost windows to tasks.

## Design

### Capture

Extend `coral-hook-agentic-state` to intercept `TaskCreate` and `TaskUpdate` tool use events:

```go
if hookType == "PostToolUse" {
    toolName := d["tool_name"]
    if toolName == "TaskCreate" || toolName == "TaskUpdate" {
        // Forward to Coral API
    }
}
```

### Store

New `agent_tasks` table in the sessions database:

```sql
CREATE TABLE IF NOT EXISTS agent_tasks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT NOT NULL,
    agent_task_id   TEXT NOT NULL,          -- The task ID from Claude Code (e.g. "1", "2")
    subject         TEXT NOT NULL,
    description     TEXT,
    status          TEXT NOT NULL DEFAULT 'pending',  -- pending, in_progress, completed
    created_at      TEXT NOT NULL,
    started_at      TEXT,                   -- When status changed to in_progress
    completed_at    TEXT,                   -- When status changed to completed
    cost_usd        REAL,
    input_tokens    INTEGER,
    output_tokens   INTEGER,
    cache_read_tokens INTEGER,
    cache_write_tokens INTEGER,
    board_name      TEXT,                   -- Which board/team this agent belongs to
    agent_name      TEXT                    -- Display name of the agent
);

CREATE INDEX IF NOT EXISTS idx_agent_tasks_session ON agent_tasks(session_id);
CREATE INDEX IF NOT EXISTS idx_agent_tasks_board ON agent_tasks(board_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_tasks_unique ON agent_tasks(session_id, agent_task_id);
```

### Cost Attribution

Same approach as board task cost tracking — when a task is completed, query proxy costs within the task's time window:

```sql
SELECT COALESCE(SUM(cost_usd), 0)        AS cost_usd,
       COALESCE(SUM(input_tokens), 0)     AS input_tokens,
       COALESCE(SUM(output_tokens), 0)    AS output_tokens,
       COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
       COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens
FROM proxy_requests
WHERE session_id = :sessionID
  AND started_at >= :startedAt
  AND started_at <= :completedAt
```

For agents without proxy (e.g. Codex with OAuth), fall back to token usage from JSONL logs if available.

### API

**POST /api/sessions/live/{name}/agent-task** — Called by hook to create/update agent tasks.

```json
{
  "session_id": "abc-123",
  "task_id": "1",
  "subject": "Fix proxy auth passthrough",
  "description": "...",
  "status": "in_progress"
}
```

**GET /api/sessions/{sessionID}/agent-tasks** — List all internal tasks for a session with costs.

**GET /api/agent-tasks/summary** — Aggregate view:
- `?board_name=X` — all agent tasks across a team
- `?session_id=X` — single session
- Returns total cost, task count, average cost per task

### API Response

```json
{
  "tasks": [
    {
      "id": 1,
      "agent_task_id": "1",
      "session_id": "abc-123",
      "subject": "Fix proxy auth passthrough",
      "status": "completed",
      "agent_name": "Lead Developer",
      "started_at": "2026-04-05T07:30:00Z",
      "completed_at": "2026-04-05T07:35:00Z",
      "cost_usd": 0.0423,
      "input_tokens": 45000,
      "output_tokens": 3200
    },
    {
      "id": 2,
      "agent_task_id": "2",
      "session_id": "abc-123",
      "subject": "Write unit tests",
      "status": "in_progress",
      "agent_name": "Lead Developer",
      "started_at": "2026-04-05T07:36:00Z",
      "cost_usd": null
    }
  ],
  "total_cost_usd": 0.0423,
  "total_tasks": 2,
  "completed_tasks": 1
}
```

## Relationship to Existing Specs

### Board Task Cost Tracking (TASK_COST_TRACKING)

Board tasks are **assignments** — high-level work items posted to the message board. An agent may create multiple internal tasks to fulfill a single board assignment. The relationship is:

```
Board Task: "Fix proxy routing for Codex"
  └─ Agent Task 1: "Investigate auth flow" ($0.02)
  └─ Agent Task 2: "Update proxy handler"  ($0.04)
  └─ Agent Task 3: "Write tests"           ($0.03)
  Total board task cost: $0.09
```

Board tasks get their cost from proxy_requests aggregated by time window. Agent tasks provide the granular breakdown within that window.

### Token Tracking (TOKEN_TRACKING)

Token tracking captures **session-level** cumulative totals (via Stop hooks and JSONL logs). Agent task tracking uses the same underlying data but slices it by task time windows. They complement each other:

- Token tracking: "This session used 500K tokens total"
- Agent task tracking: "This session had 8 tasks, averaging $0.06 each"

## UI Display

### Session Detail View

When viewing an agent's session, show a collapsible "Tasks" section listing internal tasks with status and cost:

```
Tasks (5 completed, 1 in progress)  Total: $0.28
  [x] Fix proxy auth passthrough       $0.04  2m
  [x] Update ChatGPT backend routing   $0.06  4m
  [x] Add per-agent proxy settings     $0.08  5m
  [x] Write unit tests                 $0.03  2m
  [x] Update settings UI               $0.05  3m
  [~] Implement MITM proxy             $0.02  running...
```

### Team Summary

In the team/board view, show aggregate task stats:

```
Lead Developer: 12 tasks completed, $0.45 total
QA Engineer:     8 tasks completed, $0.18 total
Frontend Dev:    5 tasks completed, $0.12 total
```

## Implementation Phases

### Phase 1: Capture + Store
- Extend `coral-hook-agentic-state` to intercept TaskCreate/TaskUpdate
- Add `agent_tasks` table and store methods
- POST endpoint to receive task events
- Basic cost computation on task completion

### Phase 2: API + Display
- GET endpoints for querying agent tasks
- Session detail view showing task list with costs
- Team summary view with per-agent task totals

### Phase 3: Board Task Integration
- Link agent tasks to board tasks (when an agent works on a board assignment, its internal tasks are children of that assignment)
- Roll up agent task costs into the parent board task
- Combined view showing board task with internal task breakdown

## Files to Modify

| File | Change |
|------|--------|
| `cmd/coral-hook-agentic-state/main.go` | Intercept TaskCreate/TaskUpdate hook events |
| `internal/store/sessions.go` | New agent_tasks table, migrations, CRUD methods |
| `internal/server/routes/sessions.go` | POST endpoint for task events, GET for queries |
| `internal/server/server.go` | Register new routes |
| `internal/server/frontend/static/sessions.js` | Task list UI in session detail view |

## Edge Cases

- **Task with no proxy data**: If proxy is disabled, cost fields are NULL. Display "N/A" in UI.
- **Overlapping tasks**: An agent may work on multiple tasks simultaneously (unlikely with Claude's sequential execution, but possible). Cost during overlapping windows is attributed to all active tasks proportionally, or to the most recently started task.
- **Task abandoned (never completed)**: Remains in `in_progress` status. No cost computed. Could add a cleanup job that closes stale tasks.
- **Agent restart mid-task**: Session ID changes. The old task remains in the old session. The agent typically creates new tasks after restart.
- **Codex agents**: Codex uses a different task system. This spec focuses on Claude's TaskCreate/TaskUpdate. Codex task tracking can be added as a follow-up by parsing Codex's JSONL logs for task-like patterns.
