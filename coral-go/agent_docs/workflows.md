# Workflows API

Define reusable multi-step automations, trigger them on demand, and inspect step-by-step results.

Workflows are higher-level than one-shot jobs: each workflow stores a reusable definition with ordered `shell` and `agent` steps, optional default agent settings, and its own execution history.

> **Every `/api/workflows…` route is the Operator's, reads included** (list, get, a workflow's runs, recent runs, a run's output and a step's files, as well as create, change, delete, trigger and kill): the dashboard, an API key or the Operator's signed command line. An agent's session token gets 403 `forbidden_for_agent` on all of them, so `coral-board workflow list`, `status` and `runs` print a refusal inside an agent session, like `create`, `trigger` and `kill`. A workflow's steps hold commands, prompts and often credentials, and belong to no board. An agent that needs work done posts a board task ([Board Tasks](board.md#tasks)) or asks the Operator.

---

## Workflow object

Workflow responses use this shape:

```json
{
  "id": 12,
  "name": "nightly-release",
  "description": "Build, test, and prepare release notes",
  "repo_path": "/home/user/project",
  "base_branch": "main",
  "max_duration_s": 3600,
  "cleanup_worktree": 1,
  "enabled": 1,
  "created_at": "2025-03-11T10:00:00+00:00",
  "updated_at": "2025-03-11T10:00:00+00:00",
  "steps": [
    {
      "name": "test",
      "type": "shell",
      "command": "go test ./..."
    },
    {
      "name": "summarize",
      "type": "agent",
      "prompt": "Summarize failures and likely fixes."
    }
  ],
  "default_agent": {
    "agent_type": "claude"
  },
  "step_count": 2
}
```

List responses also include `last_run` when available.

---

## List workflows

```
GET /api/workflows
```

### Response

```json
{
  "workflows": [
    {
      "id": 12,
      "name": "nightly-release",
      "description": "Build, test, and prepare release notes",
      "repo_path": "/home/user/project",
      "base_branch": "main",
      "max_duration_s": 3600,
      "cleanup_worktree": 1,
      "enabled": 1,
      "created_at": "2025-03-11T10:00:00+00:00",
      "updated_at": "2025-03-11T10:00:00+00:00",
      "steps": [{ "name": "test", "type": "shell", "command": "go test ./..." }],
      "default_agent": { "agent_type": "claude" },
      "step_count": 1,
      "last_run": {
        "id": 44,
        "status": "completed",
        "trigger_type": "api",
        "started_at": "2025-03-12T03:00:01+00:00",
        "finished_at": "2025-03-12T03:02:30+00:00"
      }
    }
  ]
}
```

---

## Get a workflow

```
GET /api/workflows/{workflowID}
GET /api/workflows/by-name/{name}
```

Returns one workflow definition.

Returns `{"error": "workflow not found"}, 404` if there is no matching workflow.

---

## Create a workflow

```
POST /api/workflows
```

### Request body

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | **required** | Unique name. Allowed characters: letters, numbers, `_`, `-`. |
| `description` | string | `""` | Optional description. |
| `steps` | array | **required** | Ordered list of 1 to 20 steps. |
| `default_agent` | object | `null` | Default agent config used by agent steps. |
| `repo_path` | string | `""` | Optional repo path. Must exist if provided. |
| `base_branch` | string | `"main"` | Base branch for runs. |
| `max_duration_s` | int | `3600` | Max run duration, 1 to 86400 seconds. |
| `cleanup_worktree` | int | `1` | Remove the worktree after the run finishes. |
| `enabled` | int | `1` | `1` to allow triggers, `0` to disable. |

### Step hooks

Any step (shell or agent) can define `hooks` to run commands on lifecycle events. See [Hooks](hooks.md) for the full format and event reference. A hook command runs in the workflow's `repo_path` (or the step's run directory when there is none), in its own process group, with a 60 s limit.

```json
{
  "name": "test",
  "type": "shell",
  "command": "go test ./...",
  "hooks": {
    "StepFailed": [
      {
        "hooks": [
          { "type": "command", "command": "curl -X POST $SLACK_WEBHOOK -d '{\"text\": \"Tests failed\"}'" }
        ]
      }
    ]
  }
}
```

Agent steps can also use Claude-native events (`PreToolUse`, `PostToolUse`, `Stop`) which are injected into the agent's settings. `StepComplete` and `StepFailed` work for all step types.

### Environment variables

Every workflow step (shell and agent) receives the following environment variables automatically.

**Server variables** — available in all steps:

| Variable | Description | Example |
|----------|-------------|---------|
| `CORAL_PORT` | Server port | `8420` |
| `CORAL_HOST` | Server bind address (default `127.0.0.1`, this computer only; remote access also needs Settings > Privacy or `--remote`) | `127.0.0.1` |
| `CORAL_URL` | Full server URL | `http://127.0.0.1:8420` |
| `CORAL_DIR` | Coral data directory | `~/.coral` |
| `CORAL_DATA_DIR` | Alias for `CORAL_DIR` | `~/.coral` |

Use these to make API calls from within workflow steps:
```bash
curl -X POST ${CORAL_URL}/api/notifications -H 'Content-Type: application/json' \
  -d '{"title": "Step done", "message": "All tests passed", "level": "success"}'
```

**Workflow variables** — set per run and step:

| Variable | Description |
|----------|-------------|
| `CORAL_WORKFLOW_NAME` | Workflow name |
| `CORAL_WORKFLOW_RUN_ID` | Current run ID |
| `CORAL_WORKFLOW_RUN_DIR` | Run artifacts directory |
| `CORAL_WORKFLOW_STEP` | Current step index (0-based) |
| `CORAL_WORKFLOW_STEP_DIR` | Current step's artifact directory |
| `CORAL_WORKFLOW_REPO_PATH` | Workflow's configured repo path |

The run folder is private (0700, files 0600). Coral makes each step's folder and its `stdout.txt`, `stderr.txt` and `exit_code` itself, and never reuses or follows anything already there. Don't create a later step's folder, or a link in place of those files: the step fails, and a link out of the run folder is never read or written through. A folder of your own inside your step's folder (`$CORAL_WORKFLOW_STEP_DIR/…`) is fine.

**Step chaining variables** — reference previous step output:

| Variable | Description |
|----------|-------------|
| `CORAL_PREV_DIR` | Previous step's artifact directory |
| `CORAL_PREV_STDOUT` | Path to previous step's stdout file |
| `CORAL_PREV_STDERR` | Path to previous step's stderr file |
| `CORAL_PREV_ARTIFACT` | Path to previous step's output artifact (if `output_artifact` was set) |
| `CORAL_PREV_ARTIFACT_CONTENT` | Contents of the previous step's output artifact, up to 64 KiB (NUL bytes removed). A bigger artifact is left out, and `CORAL_PREV_ARTIFACT_TRUNCATED=1` is set instead: read the file at `$CORAL_PREV_ARTIFACT` |
| `CORAL_STEP_N_DIR` | Artifact directory for step N (0-indexed) |
| `CORAL_STEP_N_STDOUT` | Path to stdout file for step N |

**Template variables** — shorthand placeholders expanded in `command` and `prompt` fields:

| Template | Equivalent env var |
|----------|--------------------|
| `{{prev_stdout}}` | `$CORAL_PREV_STDOUT` |
| `{{prev_stderr}}` | `$CORAL_PREV_STDERR` |
| `{{prev_artifact_content}}` | the content of `$CORAL_PREV_ARTIFACT` |
| `{{prev_stdout_content}}`, `{{step_N_stdout_content}}` | the content of that step's stdout |
| `{{step_N_stdout}}` | `$CORAL_STEP_N_STDOUT` |
| `{{step_dir}}` | `$CORAL_WORKFLOW_STEP_DIR` |
| `{{run_dir}}` | `$CORAL_WORKFLOW_RUN_DIR` |
| `{{run_id}}` | `$CORAL_WORKFLOW_RUN_ID` |

**Path templates in a shell `command`** (`{{prev_stdout}}`, `{{run_dir}}`, `{{step_N_dir}}` and the rest) are inserted quoted for where they sit, so a data folder with a space, an apostrophe or a shell character still names exactly that file (#3930): bare, a path is one quoted word (`cat {{prev_stdout}}`, `{{run_dir}}/out.txt`); inside double or single quotes, it is escaped for them. Write them bare.

**Content templates in a shell `command`** (`{{…_content}}`): the content is inserted as **one single-quoted shell word**, so whatever a previous step printed can never change your command (no `;`, `$(…)` or quotes in it are run). Trailing newlines are dropped, as `$(…)` does (`git rev-parse --verify {{prev_stdout_content}}` works). A missing or refused artifact is an empty word. Content over 64 KiB fails the step with a message: use the file (`{{prev_stdout}}`, `"$CORAL_PREV_STDOUT"`) for big output. Don't put quotes around them: `printf %s {{prev_stdout_content}}`, not `"{{prev_stdout_content}}"`; saving a workflow with a quoted one returns a `warnings` list. The preferred forms are the variables and files: `"$CORAL_PREV_ARTIFACT_CONTENT"`, `cat "$CORAL_PREV_STDOUT"`. In an agent `prompt` the content is inserted as plain text.

**PATH** — the Coral tools directory (containing `coral-board`, hooks, and agent CLIs) is prepended to `PATH` automatically.

### Step rules

- Every step needs a unique `name`.
- `type` must be `shell` or `agent`.
- `shell` steps require `command`.
- `agent` steps require `prompt`.
- `agent` steps also need an `agent_type`, either in the step-level `agent` object or inherited from `default_agent`.
- `timeout_s`, when set, must be between `1` and `86400`. When a step times out, its whole process group is killed, including anything it started in the background.
- `output_artifact`, when set, must be a relative path inside the step's folder (no absolute path, no `..`). Coral writes it as a new file (a name already there in the step's folder isn't overwritten) and never writes or reads it through a link, so a step can't make it point elsewhere; a refused one is shown in that step's `note` and at the end of its stderr.
- `hooks`, when set, must use valid event names and hook group structure (see [Hooks](hooks.md)).

### Example

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -X POST http://localhost:8420/api/workflows \
  -H "Content-Type: application/json" \
  -d '{
    "name": "nightly-release",
    "description": "Build, test, and prepare release notes",
    "repo_path": "/home/user/project",
    "steps": [
      {
        "name": "test",
        "type": "shell",
        "command": "go test ./..."
      },
      {
        "name": "summarize",
        "type": "agent",
        "prompt": "Summarize failures and likely fixes."
      }
    ],
    "default_agent": {
      "agent_type": "claude"
    }
  }'
```

### Response

Returns the created workflow object with hydrated `steps`, `default_agent`, and `step_count`.

### Errors

| Status | Body | Cause |
|---|---|---|
| 400 | `{"error": "invalid JSON"}` | Malformed request body. |
| 400 | `{"error": "name is required"}` | Missing name. |
| 400 | `{"error": "name contains invalid characters"}` | Name includes unsupported characters. |
| 400 | `{"error": "at least one step is required"}` | `steps` is empty. |
| 400 | `{"error": "maximum 20 steps allowed"}` | Too many steps. |
| 400 | `{"error": "repo_path does not exist"}` | Repo path doesn't exist. |
| 400 | `{"error": "repo_path is not a directory"}` | Repo path is a file or other non-directory. |
| 400 | `{"error": "invalid max_duration"}` | `max_duration_s` is out of range. |
| 400 | `{"error": "duplicate step name"}` | Two steps share the same name. |
| 400 | `{"error": "shell step missing command"}` | Shell step omitted `command`. |
| 400 | `{"error": "agent step missing prompt"}` | Agent step omitted `prompt`. |
| 400 | `{"error": "agent step missing agent_type"}` | No effective `agent_type` was provided. |
| 409 | `{"error": "workflow name already exists"}` | Name collision. |

---

## Update a workflow

```
PUT /api/workflows/{workflowID}
```

Updates only the fields you include. When updating `steps`, Coral re-validates the full step list using the existing `default_agent` unless you also send a new one.

### Example

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -X PUT http://localhost:8420/api/workflows/12 \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": 0,
    "description": "Paused pending infra changes"
  }'
```

### Response

Returns the updated workflow object.

Returns `{"error": "workflow not found"}, 404` if the workflow ID does not exist.

---

## Delete a workflow

```
DELETE /api/workflows/{workflowID}
```

Deletes the workflow and its run history: its runs still in progress are stopped, and every run's directory (`<data>/workflows/runs/<runID>/`) is removed.

Each workflow keeps its newest 50 runs. Starting a run removes older finished runs, records and directories; a run still in progress is never removed.

### Response

```json
{"ok": true}
```

---

## Trigger a workflow

```
POST /api/workflows/{workflowID}/trigger
POST /api/workflows/by-name/{name}/trigger
```

The request body is optional.

At most **5 workflow runs** are in progress at once. A trigger beyond that answers `429` with `Retry-After: 30`, `X-Coral-Limit: action` and `{"code": "too_many_runs"}`; a scheduled job that fires then is recorded as failed with the same reason.

`X-Coral-Limit: action` marks a 429 that limits one action (also task runs, cross-team posts, sign-in attempts and Coral Hub enrollment), as opposed to a busy server: the dashboard shows its message and keeps everything else running.

### Request body

| Field | Type | Default | Description |
|---|---|---|---|
| `trigger_type` | string | `"api"` | Stored on the run record. |
| `context` | any JSON | `null` | Arbitrary JSON stored as `trigger_context`. |

### Example

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -X POST http://localhost:8420/api/workflows/by-name/nightly-release/trigger \
  -H "Content-Type: application/json" \
  -d '{
    "trigger_type": "manual",
    "context": {
      "requested_by": "release-bot",
      "ticket": "REL-42"
    }
  }'
```

### Response

```json
{
  "run_id": 44,
  "workflow_id": 12,
  "workflow_name": "nightly-release",
  "status": "pending",
  "trigger_type": "manual",
  "created_at": "2025-03-12T03:00:00+00:00"
}
```

If the workflow is disabled, returns `{"error": "workflow is disabled"}, 409`.

**Signed-in check (#5469).** Before a run's first step Coral asks each agent CLI the run's agent steps use (print or interactive) whether it is signed in, with the CLI's own status command (`claude auth status --json`, `codex login status`; agy, Gemini and Pi have none and are not checked). If one is signed out the whole run fails at once: its status is `failed`, its `error_msg` is `Claude is not signed in` (or `Codex is not signed in`), its `error_code` is `not_signed_in` and its `agent_type` names the CLI (`claude` or `codex`; both fields are left out of every other run), every step is `skipped`, and no step (shell steps included) starts. Coral never tries to sign in, and never waits for a person. A CLI that cannot be asked (an old version without the command, a timeout) does not stop the run. A workflow with no agent step is not checked. The same rule holds for Coral's other background Claude jobs (session summaries, goals, knowledge distill, team and theme generation, the model list): a signed-out Claude is never started; they wait and ask again later (asked again after 2 seconds, then 4, 8 and so on up to every 5 minutes while it stays signed out). `GET /api/system/status` carries `claude_signed_in` and `claude_checked_at` once a check has been made (`false` while the last check found Claude signed out; it clears when a check finds it signed in).

---

## List runs for one workflow

```
GET /api/workflows/{workflowID}/runs?limit=20&offset=0&status=running
```

### Parameters

| Parameter | Type | Default | Description |
|---|---|---|---|
| `limit` | int | `20` | Max results to return. |
| `offset` | int | `0` | Pagination offset. |
| `status` | string | `null` | Optional status filter. |

### Response

```json
{
  "runs": [
    {
      "id": 44,
      "workflow_id": 12,
      "trigger_type": "manual",
      "trigger_context": "{\"requested_by\":\"release-bot\"}",
      "status": "completed",
      "current_step": 2,
      "worktree_path": "/Users/user/.coral/workflows/runs/44/worktree",
      "started_at": "2025-03-12T03:00:01+00:00",
      "finished_at": "2025-03-12T03:02:30+00:00",
      "error_msg": null,
      "created_at": "2025-03-12T03:00:00+00:00",
      "steps": [
        { "name": "test", "status": "completed" },
        { "name": "summarize", "status": "completed" }
      ]
    }
  ]
}
```

---

## List recent workflow runs

```
GET /api/workflows/runs/recent?limit=20&offset=0&status=failed
```

Returns recent runs across all workflows.

Each run also includes `workflow_name`.

---

## Get one workflow run

```
GET /api/workflows/runs/{runID}
```

Returns the full run record plus parsed `steps` data.

Returns `{"error": "run not found"}, 404` if the run ID does not exist.

---

## Kill a workflow run

```
POST /api/workflows/runs/{runID}/kill
```

Kills a run when its status is `pending` or `running`.

### Response

```json
{
  "ok": true,
  "run_id": 44,
  "status": "killed"
}
```

### Errors

| Status | Body | Cause |
|---|---|---|
| 400 | `{"error": "invalid run ID"}` | Bad path parameter. |
| 404 | `{"error": "run not found"}` | Unknown run ID. |
| 409 | `{"error": "run is not active"}` | Run is already terminal. |

---

## Download a step file

```
GET /api/workflows/runs/{runID}/steps/{stepIndex}/files/*
```

Serves a file from the run artifact directory for one step.

### Example

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" http://localhost:8420/api/workflows/runs/44/steps/0/files/test-report.txt
```

### Errors

| Status | Body | Cause |
|---|---|---|
| 400 | `{"error": "invalid run ID"}` | Bad run ID. |
| 400 | `{"error": "invalid step index"}` | Bad step index. |
| 400 | `{"error": "file path required"}` | Missing wildcard path. |
| 400 | `{"error": "invalid file path"}` | Path traversal attempt or invalid path. |
| 404 | `{"error": "run not found"}` | Run does not exist. |
| 404 | `{"error": "workflow not found"}` | Parent workflow was removed. |
| 404 | `{"error": "file not found"}` | Artifact file is absent. |
