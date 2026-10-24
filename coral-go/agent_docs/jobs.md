# Jobs API

Submit one-shot agent tasks, monitor their progress, and cancel running jobs via the `/api/tasks/*` endpoints.

For persistent work queues, see [Personal Tasks](agent-tasks.md) and
[Board Tasks](board.md#tasks). Their dependency and artifact workflow is
described in [Task Workflows](task-workflows.md).

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

**Operator only:** starting a run (`POST /api/tasks/run`) and cancelling one (`POST /api/tasks/runs/{run_id}/kill`) are refused to an agent's session token with 403 `forbidden_for_agent`. An agent that needs more work done posts a board task ([Board Tasks](board.md#tasks)) or asks the Operator.

### Request body

| Field | Type | Default | Description |
|---|---|---|---|
| `prompt` | string | **required** | The instruction sent to the agent. |
| `repo_path` | string | **required** | Absolute path to the git repository. |
| `agent_type` | string | `"claude"` | Agent to use (`claude`, `codex`, `agy` (Antigravity), or `pi`). **In v1.0.8 any other value silently falls back to `claude`** — fixed in the repo (unreleased), where an unknown value is rejected. Omitting the field still means `claude`. |
| `base_branch` | string | `"main"` | Branch to create the worktree from. |
| `create_worktree` | bool | `true` | Create a fresh git worktree **per run** (`<repo>_task_run_<runID>`). Unlike team launches, jobs are designed to be isolated from each other — but see the defect note above. |
| `cleanup_worktree` | bool | `true` | Remove the worktree when the run finishes. |
| `max_duration_s` | int | `3600` | Timeout in seconds. After this the agent is killed and the run is recorded as `killed` (`timeout`). |
| `flags` | string | `""` | Extra CLI flags passed to the agent. |
| `display_name` | string | `null` | Label shown in the UI. Defaults to `Task #<run_id>`. |
| ~~`webhook_url`~~ | | | Not supported: Coral doesn't send webhooks, and a request with one is refused with 400. |
| ~~`max_auto_accepts`~~ | | | Not supported: auto-accept has no count (it is the agent's permission flag), and a request with the field is refused with 400. |
| `auto_accept` | bool | `false` | Adds the agent's permission flag so the run does not stop at permission prompts: `--permission-mode auto` for Claude, `--full-auto` for Codex, `--dangerously-skip-permissions` for Antigravity; a permission flag in `flags` is kept as it is. |

### Example

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -X POST http://localhost:8420/api/tasks/run \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Add input validation to the signup form",
    "repo_path": "/home/user/myproject",
    "display_name": "Signup validation",
    "auto_accept": true
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
| `exit_reason` | One of the values below, the text of a failed launch's own error, or `null` while the run is going |
| `error_msg` | A plain sentence for a run that failed, or `null` |
| `error_code` | A fixed word for some failures (`start_command_failed`, `agent_never_started`, `agent_exited_before_start`, `worktree_failed`, `agent_turn_failed`, ...), or `null` |

`exit_reason` values, with the `status` each comes with:

| `exit_reason` | `status` | Meaning |
|---|---|---|
| `agent_done` | `completed` | The agent finished its work (its last event was a stop or a session end), or ended while Coral could not tell. |
| `turn_complete` | `completed` | A scheduled Claude or Codex prompt run: the agent ended its turn on the job's prompt (a stored stop after the prompt, settled for a few seconds); its agent is ended by Coral. |
| `turn_failed` | `failed` | The same, and the turn ended in an error (a StopFailure); `error_code` is `agent_turn_failed`. |
| `timeout` | `killed` | The run passed `max_duration_s`; its agent was killed. |
| `user_cancelled` | `killed` | `POST /api/tasks/runs/{run_id}/kill`. |
| `timeout_reap` | `killed` | A run found at startup past twice its maximum duration. |
| `interrupted_by_restart` | `failed` | Coral restarted before this run launched its agent. |
| `ended_while_coral_was_down` | `completed` | The agent ended while Coral was stopped, and nothing it reported shows it finished: its result was not seen. |
| `agent_never_started` | `failed` | The agent never reported anything and its terminal sat at an idle shell (after about 3 minutes), or it was gone at the next start with no event at all. `error_msg` holds the sentence. |
| `start_command_failed` | `failed` | Coral could not hand the agent its start command (its temporary folder was not usable, and the data folder fallback failed too). |
| `agent_exited_before_start` | `failed` | The agent ended within seconds without reporting anything. |

A launch that fails for another reason (the worktree could not be made, the CLI is not signed in) is `failed` with that reason's own words in `exit_reason` and `error_msg`.

Returns `{"error": "Run not found"}, 404` if the run_id doesn't exist.

---

## Cancel a run

```
POST /api/tasks/runs/{run_id}/kill
```

Kills the tmux session and sets status to `killed` with `exit_reason: "user_cancelled"`.

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
    running ──── session_id written
       |         prompt sent to agent after 2s init delay
       |         watchdog starts polling tmux every 30s
       |
       ├── agent (Claude, Codex) ends its turn on the job's prompt → completed (exit_reason: turn_complete), or failed (exit_reason: turn_failed) when the turn ended in an error; the agent is ended
       ├── agent exits normally → completed (exit_reason: agent_done)
       ├── timeout reached → killed (exit_reason: timeout)
       └── POST /kill called → killed (exit_reason: user_cancelled)
       |
       v
  [cleanup] ──── worktree removed (if cleanup_worktree: true)
```

---

## Worktrees

When `create_worktree: true` (the default), each job gets its own copy of the repo, keyed on the
run id:

- **Path**: `<Coral data dir>/worktrees/job-run-<run_id>-<repo name>` (for example
  `~/.coral/worktrees/job-run-42-myproject`), not beside your repository
- **Branch**: `coral/job-run-<run_id>`, created from `base_branch`
- **Name**: a run without a `display_name` is called `Job run <run_id>` (not `Task #N`, which is
  how board tasks are numbered)
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

When `auto_accept: true`, the agent's permission flag is added to its CLI flags, which skips most prompts at the agent level: `--permission-mode auto` for Claude (Auto mode, with Claude's safety checks), `--full-auto` for Codex, `--dangerously-skip-permissions` for Antigravity. A permission flag you pass in `flags` (a mode, or a bypass) is kept and nothing is added. Coral does not answer prompts itself and has no count of acceptances: the flag is the whole mechanism.

!!! warning
    Auto-accept is inherently risky. The agent may accept destructive operations. Prefer reviewing agent output in the dashboard for sensitive tasks.

---

## Concurrency limits

Coral limits concurrent running jobs to prevent resource exhaustion.

| Setting | Default | Description |
|---|---|---|
| `CORAL_MAX_CONCURRENT_JOBS` | `5` | Environment variable, set before starting the server. |

When the limit is reached, `POST /api/tasks/run` returns HTTP 429. The cron scheduler also skips firing jobs while at capacity.

!!! note
    The concurrency count is based on in-memory watchdog tasks. A server restart resets the count, which could temporarily allow more runs than the configured limit.
