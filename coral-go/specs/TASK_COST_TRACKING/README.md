# Spec: Board Task Cost Tracking

## Context

We want to track how much individual features/tasks cost at the board task level. The infrastructure exists in pieces — proxy requests track per-API-call costs by `session_id`, and board tasks track work assignments with `claimed_at`/`completed_at` timestamps. The missing link is connecting a board task to the proxy costs incurred while working on it.

## Identity Model: How Agents Are Identified Across Systems

An agent has multiple identities across the system. Understanding how they link is critical for cost tracking.

### Identity Chain

```
tmux session name (e.g. "coral-go")
    ↕ stored as live_sessions.agent_name
live_sessions.session_id (UUID, e.g. "a1b2c3d4-...")
    ↕ stored on each API call
proxy_requests.session_id (same UUID)
    ↕ cost_usd, input_tokens, output_tokens, etc.
```

### Board Subscriber Identity

```
board_subscribers.subscriber_id  = agent's job_title (e.g. "Lead Developer")
board_subscribers.session_name   = tmux session name (e.g. "coral-go")
board_subscribers.job_title      = display name (e.g. "Lead Developer")
```

### Board Task Identity

```
board_tasks.assigned_to   = subscriber_id (e.g. "Lead Developer")
board_tasks.completed_by  = subscriber_id
board_tasks.session_id    = (NEW) live_sessions.session_id UUID
```

### The Linkage Path: Task → Subscriber → Live Session → Proxy Costs

```
board_tasks.assigned_to
    ↓ matches
board_subscribers.subscriber_id (WHERE project = board_id AND is_active = 1)
    ↓ has
board_subscribers.session_name (tmux session name)
    ↓ matches
live_sessions.agent_name
    ↓ has
live_sessions.session_id (UUID)
    ↓ matches
proxy_requests.session_id
    ↓ aggregate
SUM(cost_usd) WHERE started_at BETWEEN claimed_at AND completed_at
```

### Why We Store session_id on the Task

Rather than resolving this chain at query time (which breaks when agents disconnect), we snapshot `live_sessions.session_id` onto the board task at claim time. This means:

- Cost queries are a simple `WHERE session_id = ? AND started_at BETWEEN ? AND ?`
- No dependency on live_sessions existing (agent may have terminated)
- No dependency on board_subscribers existing (subscription may have changed)

## Schema Changes

### board_tasks Table — New Columns

```sql
ALTER TABLE board_tasks ADD COLUMN session_id TEXT;
ALTER TABLE board_tasks ADD COLUMN cost_usd REAL;
ALTER TABLE board_tasks ADD COLUMN input_tokens INTEGER;
ALTER TABLE board_tasks ADD COLUMN output_tokens INTEGER;
ALTER TABLE board_tasks ADD COLUMN cache_read_tokens INTEGER;
ALTER TABLE board_tasks ADD COLUMN cache_write_tokens INTEGER;
```

### Task Struct — New Fields

```go
type Task struct {
    // ... existing fields ...
    SessionID         *string  `db:"session_id" json:"session_id,omitempty"`
    CostUSD           *float64 `db:"cost_usd" json:"cost_usd,omitempty"`
    InputTokens       *int     `db:"input_tokens" json:"input_tokens,omitempty"`
    OutputTokens      *int     `db:"output_tokens" json:"output_tokens,omitempty"`
    CacheReadTokens   *int     `db:"cache_read_tokens" json:"cache_read_tokens,omitempty"`
    CacheWriteTokens  *int     `db:"cache_write_tokens" json:"cache_write_tokens,omitempty"`
}
```

## Behavior Changes

### On Task Claim (`ClaimTask`)

After successfully claiming a task, resolve the agent's `session_id`:

```sql
-- Resolve: subscriber_id → session_name → live session UUID
SELECT ls.session_id 
FROM live_sessions ls
JOIN board_subscribers bs ON bs.session_name = ls.agent_name
WHERE bs.subscriber_id = :subscriberID 
  AND bs.project = :project 
  AND bs.is_active = 1
LIMIT 1
```

Store the result on the task:

```sql
UPDATE board_tasks SET session_id = :sessionID WHERE id = :taskID
```

If the lookup fails (agent not live, subscriber not found), `session_id` remains NULL. Cost tracking will be unavailable for that task but claiming still succeeds.

### On Task Complete (`CompleteTask`)

After marking the task completed, compute the proxy cost:

```sql
SELECT COALESCE(SUM(cost_usd), 0)            AS cost_usd,
       COALESCE(SUM(input_tokens), 0)         AS input_tokens,
       COALESCE(SUM(output_tokens), 0)        AS output_tokens,
       COALESCE(SUM(cache_read_tokens), 0)    AS cache_read_tokens,
       COALESCE(SUM(cache_write_tokens), 0)   AS cache_write_tokens
FROM proxy_requests
WHERE session_id = :taskSessionID
  AND started_at >= :claimedAt
  AND started_at <= :completedAt
```

Store the result:

```sql
UPDATE board_tasks 
SET cost_usd = ?, input_tokens = ?, output_tokens = ?,
    cache_read_tokens = ?, cache_write_tokens = ?
WHERE id = ?
```

### On Task Cancel/Skip (`CancelTask`)

Same cost computation as complete — skipped tasks should also capture cost spent before cancellation.

## API Impact

No new endpoints needed. The existing task endpoints return the `Task` struct, which now includes cost fields:

| Endpoint | Returns |
|----------|---------|
| `POST /api/board/{project}/tasks/claim` | Task with `session_id` populated |
| `POST /api/board/{project}/tasks/{id}/complete` | Task with cost fields populated |
| `GET /api/board/{project}/tasks` | All tasks, completed ones include cost |
| `POST /api/board/{project}/tasks/current` | Current task with session_id |

### Example API Response (completed task)

```json
{
    "id": 42,
    "board_id": "Coral-go-v3",
    "title": "Fix proxy auth passthrough",
    "status": "completed",
    "assigned_to": "Lead Developer",
    "completed_by": "Lead Developer",
    "session_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "claimed_at": "2026-04-04T22:30:00Z",
    "completed_at": "2026-04-04T22:45:00Z",
    "cost_usd": 0.0847,
    "input_tokens": 125000,
    "output_tokens": 8500,
    "cache_read_tokens": 45000,
    "cache_write_tokens": 12000
}
```

## Frontend Display

Show cost on completed/skipped tasks in the task list UI (`tasks.js`). For tasks with `cost_usd` populated:
- Display formatted cost (e.g. "$0.08") next to the task status
- Optionally show token breakdown on hover/expand

## Files to Modify

| File | Change |
|------|--------|
| `internal/board/store.go` | Task struct fields, migrations, update all SELECT queries, modify ClaimTask/CompleteTask/CancelTask |
| `pkg/frontend/static/tasks.js` | Display cost on completed tasks |

## Edge Cases

- **Agent not live at claim time**: `session_id` will be NULL, cost tracking unavailable — task still works normally
- **Agent restarts mid-task**: `session_id` captured at claim time stays valid — proxy requests from the original session are still in the DB
- **Task reassigned**: `ReassignTask()` resets to pending — should clear `session_id` (new agent will get new session_id on re-claim)
- **No proxy requests during task**: Cost fields will be 0, not NULL
- **Task completed before proxy requests finish**: Some in-flight requests may not be counted — acceptable, captures vast majority

## Verification

1. `go test ./internal/board/...` — existing tests pass with schema changes
2. Manual: claim task → make proxy requests → complete → verify cost in API response
3. Check task list UI shows cost for completed tasks
