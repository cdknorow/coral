# Scheduled Jobs API

Create recurring jobs, inspect run history, and validate cron expressions via REST.

Scheduled jobs support two execution modes:

- `prompt` jobs launch a one-shot agent task on a cron schedule.
- `workflow` jobs trigger a saved workflow on a cron schedule.

A run refused because the job's agent CLI (Claude or Codex) is signed out is `failed` at once, before a worktree is made or anything is launched: its `error_msg` is `Claude is not signed in`, its `error_code` is `not_signed_in` and its `agent_type` names the CLI (a workflow job copies them from its workflow run; see [Workflows](workflows.md)). Such runs do not count toward the pause after three failed runs in a row; the job runs again on its schedule once the CLI is signed in.

For one-off launches, see the [Jobs API](jobs.md). For reusable multi-step automations, see the [Workflows API](workflows.md).

> **Every `/api/scheduled/…` route is the Operator's, reads included** (list, get, a job's runs, recent runs, validate-cron, and create, change, delete, toggle): the dashboard, an API key or the Operator's signed command line. An agent's session token gets 403 `forbidden_for_agent`. A job's prompt and flags are the Operator's and may hold a credential.

---

## List scheduled jobs

```
GET /api/scheduled/jobs
```

Returns all jobs except the internal `__oneshot__` sentinel row used for API-triggered tasks.

### Response

```json
{
  "jobs": [
    {
      "id": 7,
      "name": "Morning triage",
      "description": "Check issues and summarize blockers",
      "cron_expr": "0 9 * * 1-5",
      "timezone": "America/Los_Angeles",
      "agent_type": "claude",
      "repo_path": "/home/user/project",
      "base_branch": "main",
      "prompt": "Review new issues and summarize risks.",
      "enabled": 1,
      "max_duration_s": 3600,
      "cleanup_worktree": 1,
      "flags": "",
      "job_type": "prompt",
      "workflow_id": null,
      "created_at": "2025-03-11T10:00:00+00:00",
      "updated_at": "2025-03-11T10:00:00+00:00",
      "last_run": {
        "id": 42,
        "job_id": 7,
        "session_id": "a1b2c3d4-...",
        "worktree_path": "/home/user/.coral/worktrees/job-run-42-project",
        "status": "completed",
        "scheduled_at": "2025-03-12T16:00:00+00:00",
        "started_at": "2025-03-12T16:00:03+00:00",
        "finished_at": "2025-03-12T16:02:30+00:00",
        "exit_reason": "agent_done",
        "error_msg": null,
        "trigger_type": "cron",
        "display_name": null,
        "created_at": "2025-03-12T16:00:00+00:00"
      },
      "next_fire_at": "2025-03-13T09:00:00-07:00"
    }
  ]
}
```

`next_fire_at` is calculated from `cron_expr` and `timezone`. If the timezone is invalid, Coral falls back to UTC for that calculation.

---

## Get a scheduled job

```
GET /api/scheduled/jobs/{jobID}
```

Returns the stored job definition.

### Response

```json
{
  "id": 7,
  "name": "Morning triage",
  "description": "Check issues and summarize blockers",
  "cron_expr": "0 9 * * 1-5",
  "timezone": "America/Los_Angeles",
  "agent_type": "claude",
  "repo_path": "/home/user/project",
  "base_branch": "main",
  "prompt": "Review new issues and summarize risks.",
  "enabled": 1,
  "max_duration_s": 3600,
  "cleanup_worktree": 1,
  "flags": "",
  "job_type": "prompt",
  "workflow_id": null,
  "created_at": "2025-03-11T10:00:00+00:00",
  "updated_at": "2025-03-11T10:00:00+00:00"
}
```

Returns `{"error": "job not found"}, 404` if the job does not exist.

---

## Create a scheduled job

```
POST /api/scheduled/jobs
```

### Request body

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | **required** | Display name for the job. |
| `description` | string | `""` | Optional description shown in the UI. |
| `cron_expr` | string | **required** | Five-field cron expression (`minute hour day month weekday`). |
| `timezone` | string | `"UTC"` | IANA timezone name used for scheduling. |
| `agent_type` | string | `"claude"` | Agent for `prompt` jobs. |
| `repo_path` | string | required for `prompt` | Repo path for prompt jobs. Ignored for workflow jobs. |
| `base_branch` | string | `"main"` | Base branch for prompt jobs. |
| `prompt` | string | required for `prompt` | Prompt sent to the agent for prompt jobs. |
| `enabled` | int | `1` | `1` to enable, `0` to create paused. |
| `max_duration_s` | int | `3600` | Timeout in seconds. After this the agent is killed and the run is recorded as `killed` (`timeout`). |
| `cleanup_worktree` | int | `1` | Remove the worktree when the run finishes. |
| `flags` | string | `""` | Extra CLI flags for prompt jobs. |
| `job_type` | string | `"prompt"` | `prompt` or `workflow`. |
| `workflow_id` | int | required for `workflow` | Workflow to trigger when `job_type` is `workflow`. |

### Prompt job example

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -X POST http://localhost:8420/api/scheduled/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Morning triage",
    "cron_expr": "0 9 * * 1-5",
    "timezone": "America/Los_Angeles",
    "repo_path": "/home/user/project",
    "prompt": "Review new issues and summarize risks."
  }'
```

### Workflow job example

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -X POST http://localhost:8420/api/scheduled/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Nightly release workflow",
    "cron_expr": "0 2 * * *",
    "timezone": "UTC",
    "job_type": "workflow",
    "workflow_id": 12
  }'
```

### Response

Returns the created job object.

### Errors

| Status | Body | Cause |
|---|---|---|
| 400 | `{"error": "invalid JSON"}` | Malformed request body. |
| 400 | `{"error": "'name' is required"}` | Missing name. |
| 400 | `{"error": "'cron_expr' is required"}` | Missing cron expression. |
| 400 | `{"error": "'repo_path' is required"}` | Missing repo path for a prompt job. |
| 400 | `{"error": "'prompt' is required"}` | Missing prompt for a prompt job. |
| 400 | `{"error": "'workflow_id' is required for workflow jobs"}` | Missing workflow ID for a workflow job. |
| 400 | `{"error": "Invalid cron expression: ..."}` | Cron parser rejected the expression. |
| 400 | `{"error": "unknown timezone: ..."}` | `timezone` is not an IANA zone name (e.g. `Europe/Berlin`); empty means UTC. |
| 400 | `{"error": "repo_path is not an existing directory: ..."}` / `"repo_path is not a git repository: ..."` | The repository must exist and be a git work tree. |

The same timezone and `repo_path` checks apply when a job is updated.

---

## Update a scheduled job

```
PUT /api/scheduled/jobs/{jobID}
```

Updates only the fields you include. If `cron_expr` is present, Coral validates it before saving.

### Example

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -X PUT http://localhost:8420/api/scheduled/jobs/7 \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": 0,
    "timezone": "America/New_York"
  }'
```

### Response

Returns the updated job object.

Returns `{"error": "job not found"}, 404` if the job ID does not exist.

---

## Delete a scheduled job

```
DELETE /api/scheduled/jobs/{jobID}
```

Deletes the job and its run history.

### Response

```json
{"ok": true}
```

---

## Pause or resume a job

```
POST /api/scheduled/jobs/{jobID}/toggle
```

Flips `enabled` between `1` and `0`.

### Response

Returns the updated job object.

Returns `{"error": "job not found"}, 404` if the job does not exist.

---

### Automatic pause

A job whose last 3 runs all failed pauses itself (`enabled` becomes `0`) instead of starting a
fourth. `paused_reason` on the job says why, with the newest run's error, and the Jobs view shows
it. Fix the cause, then resume the job (toggle or `enabled: 1`); that clears `paused_reason`.

---

## List runs for one job

```
GET /api/scheduled/jobs/{jobID}/runs?limit=20
```

### Parameters

| Parameter | Type | Default | Description |
|---|---|---|---|
| `limit` | int | `20` | Maximum number of runs to return. |

### Response

```json
{
  "runs": [
    {
      "id": 42,
      "job_id": 7,
      "session_id": "a1b2c3d4-...",
      "worktree_path": "/home/user/.coral/worktrees/job-run-42-project",
      "status": "completed",
      "scheduled_at": "2025-03-12T16:00:00+00:00",
      "started_at": "2025-03-12T16:00:03+00:00",
      "finished_at": "2025-03-12T16:02:30+00:00",
      "exit_reason": "agent_done",
      "error_msg": null,
      "trigger_type": "cron",
      "display_name": null,
      "created_at": "2025-03-12T16:00:00+00:00"
    }
  ]
}
```

---

## List recent scheduled runs

```
GET /api/scheduled/runs/recent?limit=50
```

Returns recent runs across all scheduled jobs.

### Parameters

| Parameter | Type | Default | Description |
|---|---|---|---|
| `limit` | int | `50` | Maximum number of runs to return. |

### Response

```json
{
  "runs": [
    {
      "id": 42,
      "job_id": 7,
      "job_name": "Morning triage",
      "session_id": "a1b2c3d4-...",
      "worktree_path": "/home/user/.coral/worktrees/job-run-42-project",
      "status": "completed",
      "scheduled_at": "2025-03-12T16:00:00+00:00",
      "started_at": "2025-03-12T16:00:03+00:00",
      "finished_at": "2025-03-12T16:02:30+00:00",
      "exit_reason": "agent_done",
      "error_msg": null,
      "trigger_type": "cron",
      "display_name": null,
      "created_at": "2025-03-12T16:00:00+00:00"
    }
  ]
}
```

---

## When a job fires

The scheduler checks enabled jobs every 30 seconds. A job is due when a slot of its cron expression has passed since the later of:

- its last run (for a job that has never run: 24 hours ago), and
- its last change: creation, an edit, or pausing/resuming it.

So a slot missed while Coral was not running is caught up once, on the next check, but a job never fires a slot from before it existed or from while it was paused. A "weekdays at 9:00" job created at 10:00 first runs the next weekday at 9:00, not at once.

---

## Validate a cron expression

```
POST /api/scheduled/validate-cron
```

Validates a five-field cron expression and returns the next five fire times.

### Request body

| Field | Type | Default | Description |
|---|---|---|---|
| `cron_expr` | string | **required** | Expression to validate. |
| `timezone` | string | `"UTC"` | Optional IANA timezone for preview times. |

### Example

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -X POST http://localhost:8420/api/scheduled/validate-cron \
  -H "Content-Type: application/json" \
  -d '{"cron_expr": "0 9 * * 1-5", "timezone": "America/Los_Angeles"}'
```

### Responses

Valid cron:

```json
{
  "valid": true,
  "next_fire_times": [
    "2025-03-13T09:00:00-07:00",
    "2025-03-14T09:00:00-07:00"
  ]
}
```

Invalid cron:

```json
{
  "valid": false,
  "error": "Invalid cron expression"
}
```
