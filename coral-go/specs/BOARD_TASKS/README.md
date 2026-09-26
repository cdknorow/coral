# Message Board Task List API

> Historical design proposal. Some commands and behavior below have been superseded.
> For the implemented contract, see [current queue behavior](task-queue-behavior.md) and [task workflows](../../agent_docs/task-workflows.md).

## Overview

An atomic task management system built into `coral-board` that allows multi-agent teams to coordinate work without contention. Tasks live alongside the message board and provide server-side locking so two agents can never claim the same task simultaneously.

## Problem

When using the message board for task coordination, multiple agents frequently claim the same task before seeing each other's messages. In a 10-agent session, this caused 5+ conflicts requiring manual orchestrator intervention. The message board is async and eventually consistent — two agents can post "Claiming Task #X" simultaneously, and neither sees the other's claim until the next `coral-board read`.

## Design Principles

1. **Atomic operations** — Claim, complete, and reassign are server-side atomic. No races.
2. **Board-scoped** — Tasks belong to the current message board project. Join a board first, then manage tasks.
3. **Agent-aware** — Tasks track who created, assigned, and completed them using the agent's subscriber name.
4. **Simple CLI** — Follows the existing `coral-board` command patterns. One command per operation.
5. **Orchestrator-friendly** — Supports both pre-assignment (orchestrator assigns tasks to specific agents) and self-service (agents grab the next available task).

## CLI Interface

### Add a task

```bash
coral-board task add "Fix auth bypass in server.go" --priority critical
coral-board task add "Extract shared helper" --priority high --assign "Go Expert"
coral-board task add "Write tests for tmux client" --priority medium --blocked-by 5,6
```

Options:
- `--priority` — `critical`, `high`, `medium`, `low` (default: `medium`)
- `--assign` — Pre-assign to a specific agent by subscriber name
- `--blocked-by` — Comma-separated task IDs that must complete before this task is available

### List tasks

```bash
coral-board task list                    # All tasks
coral-board task list --status pending   # Filter by status
coral-board task list --mine             # Tasks assigned to me
coral-board task list --available        # Unassigned, unblocked tasks
```

Output format:
```
ID  Status      Priority  Assignee      Description
#1  completed   critical  Sec Reviewer  Fix auth bypass in server.go
#2  in_progress high      Go Expert     Extract shared helper
#3  pending     medium    —             Write tests for tmux client (blocked by #2)
#4  pending     low       —             Clean up window.* exports
```

### Claim a task

```bash
coral-board task claim 4                 # Claim specific task
coral-board task next                    # Claim next available (by priority, then ID)
coral-board task next --mine             # Claim next task pre-assigned to me
```

Claim fails atomically if:
- Task is already claimed by another agent
- Task is blocked by incomplete dependencies
- Task does not exist

Response on success:
```
Claimed Task #4: Clean up window.* exports
```

Response on conflict:
```
Error: Task #4 already claimed by Frontend Dev
```

### Complete a task

```bash
coral-board task complete 4              # Mark as done
coral-board task complete 4 --message "Replaced 153 patterns with 5 helpers"
```

Completing a task:
- Sets status to `completed`
- Records completion time and optional message
- Unblocks any tasks that depended on it
- Posts a notification to the message board (optional, configurable)

### Update a task

```bash
coral-board task update 4 --priority high
coral-board task update 4 --assign "Lead Developer"
coral-board task update 4 --status pending          # Unclaim / reset
coral-board task update 4 --blocked-by 7,8          # Add dependencies
```

### Bulk operations

```bash
coral-board task add-batch <<'EOF'
critical | Fix auth bypass in server.go | @Security Reviewer
critical | Fix WebSocket origin checking | @Security Reviewer
high     | Extract server bootstrap | @Mobile UI Dev
high     | Add error response helpers | @Lead Developer
medium   | Consolidate writeJSON | @Go Expert
EOF
```

Format: `priority | description | assignee (optional)`

## Data Model

### Task

| Field | Type | Description |
|-------|------|-------------|
| `id` | int | Auto-incrementing, board-scoped |
| `board_id` | int | FK to message board project |
| `description` | string | Task description |
| `status` | enum | `pending`, `in_progress`, `completed`, `skipped` |
| `priority` | enum | `critical`, `high`, `medium`, `low` |
| `created_by` | string | Subscriber name of creator |
| `assigned_to` | string | Subscriber name of assignee (nullable) |
| `completed_by` | string | Subscriber name of completer (nullable) |
| `completion_message` | string | Optional completion note (nullable) |
| `blocked_by` | []int | Task IDs that must complete first |
| `created_at` | datetime | Creation timestamp |
| `claimed_at` | datetime | When claimed (nullable) |
| `completed_at` | datetime | When completed (nullable) |

### SQLite Schema

```sql
CREATE TABLE board_tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    board_id INTEGER NOT NULL,
    description TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'in_progress', 'completed', 'skipped')),
    priority TEXT NOT NULL DEFAULT 'medium'
        CHECK (priority IN ('critical', 'high', 'medium', 'low')),
    created_by TEXT NOT NULL,
    assigned_to TEXT,
    completed_by TEXT,
    completion_message TEXT,
    created_at TEXT NOT NULL,
    claimed_at TEXT,
    completed_at TEXT,
    FOREIGN KEY (board_id) REFERENCES board_projects(id) ON DELETE CASCADE
);

CREATE TABLE board_task_deps (
    task_id INTEGER NOT NULL,
    blocked_by_task_id INTEGER NOT NULL,
    PRIMARY KEY (task_id, blocked_by_task_id),
    FOREIGN KEY (task_id) REFERENCES board_tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (blocked_by_task_id) REFERENCES board_tasks(id) ON DELETE CASCADE
);

CREATE INDEX idx_board_tasks_board_status ON board_tasks(board_id, status);
CREATE INDEX idx_board_tasks_assigned ON board_tasks(board_id, assigned_to);
```

## API Endpoints

All endpoints are under `/api/board/tasks/` and require the standard board authentication.

### POST /api/board/tasks
Create a new task.

```json
{
    "description": "Fix auth bypass in server.go",
    "priority": "critical",
    "assigned_to": "Security Reviewer",
    "blocked_by": [5, 6]
}
```

### GET /api/board/tasks
List tasks with optional filters.

Query params: `status`, `assigned_to`, `available` (unassigned + unblocked), `mine` (uses subscriber name).

### POST /api/board/tasks/:id/claim
Atomic claim. Returns 409 Conflict if already claimed.

### POST /api/board/tasks/:id/complete
Mark complete with optional message body.

```json
{
    "message": "Replaced 153 patterns with 5 helpers"
}
```

### PATCH /api/board/tasks/:id
Update priority, assignment, status, or dependencies.

### POST /api/board/tasks/next
Claim the next available task (highest priority, lowest ID). Optional `mine=true` to only consider pre-assigned tasks.

### POST /api/board/tasks/batch
Create multiple tasks from an array.

## Atomicity

The claim operation uses SQLite's built-in row locking:

```sql
UPDATE board_tasks
SET status = 'in_progress',
    assigned_to = :agent,
    claimed_at = :now
WHERE id = :id
  AND status = 'pending'
  AND id NOT IN (
      SELECT task_id FROM board_task_deps d
      JOIN board_tasks bt ON d.blocked_by_task_id = bt.id
      WHERE bt.status != 'completed'
  );
```

If `rows_affected == 0`, the claim failed (already taken or blocked). This is a single atomic SQL statement — no races possible with SQLite's write lock.

## Message Board Integration

When configured, task state changes post automatic notifications to the message board:

```
[Task #4 claimed by Frontend Dev] Clean up window.* exports
[Task #4 completed by Frontend Dev] Replaced 220 lines with Object.assign — app.js reduced by 83 lines
[Task #7 unblocked] Dependencies #5, #6 completed — task is now available
```

This keeps the message board as the communication channel while tasks handle the coordination.

## Implementation Plan

### Phase 1: Core (MVP)
- SQLite schema + store methods (CRUD, atomic claim)
- CLI commands: `task add`, `task list`, `task claim`, `task complete`, `task next`
- API endpoints: POST/GET/PATCH tasks, claim, complete, next

### Phase 2: Dependencies & Notifications
- Blocked-by dependency tracking
- Auto-unblock on completion
- Message board notifications on state changes

### Phase 3: Bulk & Orchestrator Tools
- `task add-batch` for bulk creation
- `task list --available` / `--mine` filters
- Dashboard UI showing task board (kanban-style or table)

## Compatibility

- Tasks are stored in the existing `messageboard.db` SQLite database
- CLI follows the same state-file pattern as existing `coral-board` commands (board must be joined first)
- API authentication matches existing board API patterns
- No changes to the message board itself — tasks are a parallel data structure
