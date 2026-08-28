# Jobs API

Submit one-shot agent tasks, monitor their progress, and cancel running jobs via the `/api/tasks/*` endpoints.

Each job launches an agent in a per-run git worktree, sends it a prompt, and monitors it until completion.

> **Known defect — jobs fail on the documented default.** `base_branch` defaults to `main`,
> and `git worktree add <dir> main` is rejected when `main` is checked out in your repo, so
> **every run fails** with `fatal: 'main' is already used by worktree at ...`. Set
> `base_branch` to a branch that is not checked out anywhere. See
> [Worktrees](#worktrees) below. Jobs appear in the **Jobs** sidebar in the Coral dashboard, separate from persistent live sessions.

This page documents the one-shot Tasks API surface. Coral uses "job" for the UI concept and "task" for the REST paths:

- `POST /api/tasks/run`
- `GET /api/tasks/runs`
- `GET /api/tasks/runs/{run_id}`
- `POST /api/tasks/runs/{run_id}/kill`
- `GET /api/tasks/active`

---

## Submit a task

```
POST /api/tasks/run
```

Queues a new agent task. Returns immediately — all work (worktree creation, agent launch, monitoring) happens in the background.

### Request body

| Field | Type | Default | Description |
|---|---|---|---|
| `prompt` | string | **required** | The instruction sent to the agent. |
| `repo_path` | string | **required** | Absolute path to the git repository. |
| `agent_type` | string | `"claude"` | Agent to use (`claude`, `codex`, `gemini`, or `pi`). **In v1.0.8 any other value silently falls back to `claude`** — fixed in the repo (unreleased), where an unknown value is rejected. Omitting the field still means `claude`. |
| `base_branch` | string | `"main"` | Branch to create the worktree from. |
| `create_worktree` | bool | `true` | Create a fresh git worktree **per run** (`<repo>_task_run_<runID>`). Unlike team launches, jobs are designed to be isolated from each other — but see the defect note above. |
| `cleanup_worktree` | bool | `true` | Remove the worktree when the run finishes. |
| `max_duration_s` | int | `3600` | Timeout in seconds. The run is killed after this. |
| `flags` | string | `""` | Extra CLI flags passed to the agent. |
| `display_name` | string | `null` | Label shown in the UI. Defaults to `Task #<run_id>`. |
| `webhook_url` | string | `null` | URL to receive POST callbacks on status changes. |
| `auto_accept` | bool | `false` | Auto-respond to permission prompts (also adds `--dangerously-skip-permissions`). |
| `max_auto_accepts` | int | `10` | Safety limit — auto-accept disables after this many acceptances. |

### Example

```bash
curl -X POST http://localhost:8420/api/tasks/run \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Add input validation to the signup form",
    "repo_path": "/home/user/myproject",
    "display_name": "Signup validation",
    "auto_accept": true,
    "max_auto_accepts": 20,
    "webhook_url": "https://example.com/hooks/coral"
  }'
```

### Response

```json
{"run_id": 42, "status": "pending"}
```

### Errors

| Status | Body | Cause |
|---|---|---|
| 400 | `{"error": "'prompt' is required"}` | Missing `prompt` field. |
| 400 | `{"error": "'repo_path' is required"}` | Missing `repo_path` field. |
| 400 | `{"error": "repo_path '...' does not exist"}` | Path is not a directory. |
| 429 | `{"error": "Concurrent task limit reached (max: 5). Try again later."}` | At capacity. |

---

## Poll run status

```
GET /api/tasks/runs/{run_id}
```

### Response

```json
{
  "id": 42,
  "status": "running",
  "trigger_type": "api",
  "session_id": "a1b2c3d4-...",
  "display_name": "Signup validation",
  "worktree_path": "/home/user/myproject_task_run_42",
  "scheduled_at": "2025-03-11T10:00:00+00:00",
  "started_at": "2025-03-11T10:00:05+00:00",
  "finished_at": null,
  "exit_reason": null,
  "error_msg": null
}
```

| Field | Values |
|---|---|
| `status` | `pending`, `running`, `completed`, `killed`, `failed` |
| `exit_reason` | `agent_done`, `timeout`, `user_cancelled`, or `null` |

Returns `{"error": "Run not found"}, 404` if the run_id doesn't exist.

---

## Cancel a run

```
POST /api/tasks/runs/{run_id}/kill
```

Kills the tmux session, sets status to `killed` with `exit_reason: "user_cancelled"`, and fires the webhook callback.

### Response

```json
{"ok": true}
```

Returns `{"error": "Run not found or not active"}, 404` if the run doesn't exist or is already in a terminal state.

---

## List one-shot runs

```
GET /api/tasks/runs?limit=50&status=running
```

Returns recent API-triggered runs (excludes cron-scheduled runs).

| Parameter | Type | Default | Description |
|---|---|---|---|
| `limit` | int | `50` | Max results (1–200). |
| `status` | string | `null` | Filter by status. |

### Response

```json
{
  "runs": [{ /* same shape as poll response */ }]
}
```

---

## List all active runs

```
GET /api/tasks/active
```

Returns all pending/running runs across both cron and API-triggered jobs.

```json
{
  "runs": [{ /* run objects with additional job_name field */ }]
}
```

---

## Job lifecycle

A submitted job moves through these states:

```
POST /api/tasks/run
       |
       v
    pending ──── run record created
       |
       |  (background)
       v
  [worktree creation] ──── git worktree add <repo>_task_run_<id> <branch>
       |                    failure → failed
       v
  [agent launch] ──── new tmux session with agent CLI
       |               failure → failed, worktree cleaned up
       v
    running ──── session_id written, webhook fires "running"
       |         prompt sent to agent after 2s init delay
       |         watchdog starts polling tmux every 30s
       |
       ├── agent exits normally → completed (exit_reason: agent_done)
       ├── timeout reached → killed (exit_reason: timeout)
       └── POST /kill called → killed (exit_reason: user_cancelled)
       |
       v
  [cleanup] ──── auto-accept state removed
                 worktree removed (if cleanup_worktree: true)
                 webhook fires terminal status
```

---

## Worktrees

When `create_worktree: true` (the default), each job is *intended* to get its own copy of the repo,
keyed on the run id (`internal/background/scheduler.go:551`):

- **Path**: `<repo_path>_task_run_<run_id>` (sibling directory to the repo)
- **Branch**: Checked out from `base_branch`
- **Cleanup**: Removed automatically when the run finishes (unless `cleanup_worktree: false`)
Set `create_worktree: false` to run the agent directly in `repo_path` — useful for tasks that
don't modify files, and currently the only configuration that works without changing
`base_branch`.

> **Verified 2026-08-28 against v1.0.8.** With `base_branch: "main"` (the default) every run
> fails at worktree creation and the agent never starts. With `base_branch` set to a branch
> that is not checked out anywhere, the per-run worktree is created, the agent launches, and
> cleanup removes it — but the run is still recorded `killed` / `timeout` rather than
> `completed`, because an interactive agent does not exit on its own.

Set `create_worktree: false` to run the agent directly in `repo_path` — useful for tasks that don't modify files.

---

## Auto-accept

When `auto_accept: true`, two mechanisms handle permission prompts:

1. **`--dangerously-skip-permissions`** is added to the agent's CLI flags, which skips most prompts at the agent level.
2. **Notification-based fallback** — if the agent fires a `notification` event (indicating a prompt the flag didn't cover), Coral sends `y` + Enter to the tmux session after a 0.5s delay.

### Safety limit

The `max_auto_accepts` parameter (default: `10`) caps how many times the fallback mechanism fires. Once the limit is reached, auto-accept is permanently disabled for that run and a warning is logged. This prevents runaway acceptance if something goes wrong.

!!! warning
    Auto-accept is inherently risky. The agent may accept destructive operations. Use `max_auto_accepts` to bound exposure, and prefer reviewing agent output in the dashboard for sensitive tasks.

---

## Webhooks

If `webhook_url` is set, Coral sends HTTP POST callbacks at each status transition.

### Payload

```json
{
  "run_id": 42,
  "session_id": "a1b2c3d4-...",
  "status": "completed",
  "exit_reason": "agent_done",
  "started_at": "2025-03-11T10:00:05+00:00",
  "finished_at": "2025-03-11T10:30:00+00:00",
  "duration_s": 1795,
  "source": "coral"
}
```

- Fires on: `running`, `completed`, `killed`, `failed`
- `finished_at` and `duration_s` are `null` for the `running` callback
- Retries: up to 3 attempts (delays: 5s, 15s, 60s)
- Timeout: 10s per attempt
- Any 2xx response is treated as success

---

## Concurrency limits

Coral limits concurrent running jobs to prevent resource exhaustion.

| Setting | Default | Description |
|---|---|---|
| `CORAL_MAX_CONCURRENT_JOBS` | `5` | Environment variable, set before starting the server. |

When the limit is reached, `POST /api/tasks/run` returns HTTP 429. The cron scheduler also skips firing jobs while at capacity.

!!! note
    The concurrency count is based on in-memory watchdog tasks. A server restart resets the count, which could temporarily allow more runs than the configured limit.
