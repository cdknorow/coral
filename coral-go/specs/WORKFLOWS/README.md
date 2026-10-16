# Workflows

## Overview

A workflow is a named, reusable automation consisting of one or more steps that execute sequentially. Steps can be shell commands or agent prompts (Claude Code, Gemini, Codex). Workflows are triggered via API, CLI, or Claude hooks and are visible in the Coral UI with full run history.

## Problem

Today, automation in Coral is limited to single-prompt scheduled jobs or manual agent launches. There's no way to chain operations — for example, "run tests, then have an agent fix failures, then verify the fix." Teams need visible, trackable multi-step automations that combine shell commands with AI agent work.

## Design Principles

1. **Simple steps** — A step is either a shell command or an agent prompt. No DAGs, no parallel branches (v1).
2. **File-based artifacts** — Steps communicate through the filesystem. No custom serialization.
3. **Extend, don't replace** — Agent steps reuse the existing `FireOneshot` / `AgentLauncher` infrastructure.
4. **Visible** — Every run and step is tracked with status, output, and timing.
5. **Triggerable** — API endpoint, CLI command, or Claude hook. No special event bus needed.
6. **Secure by default** — Template values passed as environment variables to shell steps (never string-interpolated). Inputs validated at creation time. Steps share the scheduler's concurrency pool.

## Step Types

### Shell Step

Runs a command in the workflow's working directory. Stdout/stderr captured automatically.

```json
{
  "name": "run tests",
  "type": "shell",
  "command": "go test ./... 2>&1",
  "timeout_s": 300
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Display label |
| `type` | string | yes | `"shell"` |
| `command` | string | yes | Shell command to execute |
| `timeout_s` | int | no | Step timeout in seconds (default: 300) |
| `continue_on_failure` | bool | no | If true, continue to next step even on non-zero exit (default: false) |

### Agent Step

Launches a Claude Code (or Gemini/Codex) agent with a prompt. The agent runs in non-interactive mode and exits when done. Uses the same agent configuration schema as the launch API.

```json
{
  "name": "fix failures",
  "type": "agent",
  "prompt": "Read the test output at {{prev_stdout}} and fix all failing tests.",
  "timeout_s": 600,
  "agent": {
    "agent_type": "claude",
    "model": "claude-sonnet-4-6",
    "capabilities": {
      "allow": ["file_read", "file_write", "shell"],
      "deny": ["git_write"]
    },
    "tools": ["Read", "Edit", "Bash"]
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Display label |
| `type` | string | yes | `"agent"` |
| `prompt` | string | yes | Prompt sent to the agent. Supports template variables. |
| `agent` | object | no | Agent configuration (see Agent Config below). Falls back to workflow-level `default_agent` if omitted. |
| `timeout_s` | int | no | Step timeout in seconds (default: 600) |

> **Note:** `max_turns` is deferred to Phase 3. For v1, use `timeout_s` per step and `max_duration_s` per workflow as safety nets. Wall-clock timeout is the primary runaway prevention mechanism.

### Agent Config

The `agent` object uses the same schema as `POST /api/sessions/launch`:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent_type` | string | yes | `"claude"`, `"gemini"`, `"codex"` |
| `model` | string | no | Model override (e.g., `"claude-sonnet-4-6"`) |
| `capabilities` | object | no | `{"allow": [...], "deny": [...]}` — see `internal/agent/permissions.go` |
| `tools` | []string | no | Allowed tools (e.g., `["Read", "Edit", "Bash(npm *)"]`) |
| ~~`mcpServers`~~ | | | Not supported (#3644): saving a workflow that names it is refused; configure MCP servers in the agent CLI |
| `flags` | []string | no | Additional CLI flags |

### Workflow-Level Default Agent

To reduce boilerplate when multiple agent steps share the same configuration, workflows support a `default_agent` field. Step-level `agent` fields override (merge with) the workflow default:

```json
{
  "name": "lint-and-fix",
  "default_agent": {
    "agent_type": "claude",
    "model": "claude-sonnet-4-6",
    "capabilities": {"allow": ["file_read", "file_write", "shell"]}
  },
  "steps": [
    {"name": "fix", "type": "agent", "prompt": "Fix issues in {{prev_stdout}}"},
    {"name": "review", "type": "agent", "prompt": "Review changes", "agent": {"model": "claude-opus-4-6"}}
  ]
}
```

Agent steps without an `agent` field inherit `default_agent`. If a step provides a partial `agent` object, it is merged with `default_agent` (step fields take precedence). If neither the step nor the workflow defines an agent config, the step fails validation.

## Artifact Directory Convention

Each workflow run creates a directory structure for inter-step communication. All paths are **absolute**, rooted at `{repo_path}`:

```
{repo_path}/.coral/workflows/runs/{run_id}/
├── context.json            # Run metadata (workflow name, trigger, step defs)
├── step_0/
│   ├── stdout.txt          # Auto-captured stdout
│   ├── stderr.txt          # Auto-captured stderr
│   ├── exit_code           # Exit code as text ("0", "1", etc.)
│   └── artifacts/          # Step can write anything here
│       ├── test_results.json
│       └── coverage.html
├── step_1/
│   ├── stdout.txt
│   ├── stderr.txt
│   ├── exit_code
│   └── artifacts/
└── step_2/
    └── ...
```

**Rules:**
- Each step gets `step_{n}/` with auto-captured stdout, stderr, and exit_code
- `artifacts/` is a convention — steps can write arbitrary files there
- All steps share the same repo working directory
- Previous step directories are available as read-only context
- **Trust model:** Steps are NOT sandboxed from each other. Agent steps have repo-level access controlled by their `capabilities` allow/deny list. Previous step directories are read-only by convention, not enforced by filesystem permissions in v1.

### Environment Variables

Injected into every step (shell and agent):

```
CORAL_WORKFLOW_RUN_DIR=/Users/dev/myproject/.coral/workflows/runs/42
CORAL_WORKFLOW_STEP=0
CORAL_WORKFLOW_STEP_DIR=/Users/dev/myproject/.coral/workflows/runs/42/step_0
CORAL_WORKFLOW_NAME=lint-and-fix
CORAL_WORKFLOW_RUN_ID=42
CORAL_WORKFLOW_REPO_PATH=/Users/dev/myproject
```

> **Security note:** For shell steps, template variables (`{{prev_stdout}}`, `{{step_dir}}`, etc.) are also injected as environment variables (`CORAL_PREV_STDOUT`, `CORAL_STEP_DIR`, etc.) rather than string-interpolated into the command. Shell commands should reference `$CORAL_PREV_STDOUT` instead of inline `{{prev_stdout}}`. Template syntax in `command` fields is expanded via env vars to prevent shell injection. For agent step `prompt` fields, string substitution is safe since prompts are not shell-interpreted.

### Template Variables

Available in step `command` and `prompt` fields, expanded by the runner before execution:

| Variable | Expands to | Env var equivalent |
|----------|------------|--------------------|
| `{{run_dir}}` | `{repo_path}/.coral/workflows/runs/42` | `CORAL_WORKFLOW_RUN_DIR` |
| `{{run_id}}` | `42` | `CORAL_WORKFLOW_RUN_ID` |
| `{{step_dir}}` | `{repo_path}/.coral/workflows/runs/42/step_1` | `CORAL_WORKFLOW_STEP_DIR` |
| `{{prev_dir}}` | `{repo_path}/.coral/workflows/runs/42/step_0` | `CORAL_PREV_DIR` |
| `{{prev_stdout}}` | `{repo_path}/.coral/workflows/runs/42/step_0/stdout.txt` | `CORAL_PREV_STDOUT` |
| `{{prev_stderr}}` | `{repo_path}/.coral/workflows/runs/42/step_0/stderr.txt` | `CORAL_PREV_STDERR` |
| `{{step_N_dir}}` | `{repo_path}/.coral/workflows/runs/42/step_N` (any step by index) | `CORAL_STEP_N_DIR` |
| `{{step_N_stdout}}` | `{repo_path}/.coral/workflows/runs/42/step_N/stdout.txt` | `CORAL_STEP_N_STDOUT` |

### Template Validation Rules

Templates are validated at **workflow creation time** (not at runtime):

1. `{{prev_dir}}`, `{{prev_stdout}}`, `{{prev_stderr}}` — **invalid on step 0** (no previous step). Creation fails with a validation error.
2. `{{step_N_dir}}`, `{{step_N_stdout}}` — **N must be < current step index** and within bounds. Forward references and out-of-range indices are rejected at creation time.
3. All paths expand to **absolute paths** at runtime.

## Data Model

### SQLite Schema

```sql
CREATE TABLE IF NOT EXISTS workflows (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT NOT NULL UNIQUE,
    description        TEXT DEFAULT '',
    steps_json         TEXT NOT NULL DEFAULT '[]',
    default_agent_json TEXT DEFAULT '',
    repo_path          TEXT DEFAULT '',
    base_branch        TEXT DEFAULT 'main',
    max_duration_s     INTEGER NOT NULL DEFAULT 3600,
    cleanup_worktree   INTEGER NOT NULL DEFAULT 1,
    enabled            INTEGER NOT NULL DEFAULT 1,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    workflow_id     INTEGER NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    trigger_type    TEXT NOT NULL DEFAULT 'api',
    trigger_context TEXT,
    status          TEXT NOT NULL DEFAULT 'pending',
    current_step    INTEGER NOT NULL DEFAULT 0,
    step_results    TEXT NOT NULL DEFAULT '[]',
    worktree_path   TEXT DEFAULT '',
    started_at      TEXT,
    finished_at     TEXT,
    error_msg       TEXT,
    created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_workflow_runs_workflow
    ON workflow_runs(workflow_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_status
    ON workflow_runs(status);
```

### Workflow

| Field | Type | Description |
|-------|------|-------------|
| `id` | int | Auto-incrementing |
| `name` | string | Unique name, restricted to `[a-zA-Z0-9_-]` (e.g., `"lint-and-fix"`) |
| `description` | string | Human-readable description |
| `steps_json` | string | JSON array of step definitions (max 20 steps) |
| `default_agent_json` | string | JSON object with default agent config for agent steps (optional) |
| `repo_path` | string | Working directory for the workflow (must be an existing directory) |
| `base_branch` | string | Git branch for worktree creation (default: `"main"`) |
| `max_duration_s` | int | Total workflow timeout in seconds (default: 3600, max: 86400) |
| `cleanup_worktree` | int | Whether to clean up worktree after run (default: 1) |
| `enabled` | int | Whether the workflow can be triggered (default: 1) |
| `created_at` | datetime | Creation timestamp |
| `updated_at` | datetime | Last update timestamp |

### WorkflowRun

| Field | Type | Description |
|-------|------|-------------|
| `id` | int | Auto-incrementing |
| `workflow_id` | int | FK to workflows |
| `trigger_type` | string | `"api"`, `"cli"`, `"hook"` |
| `trigger_context` | string | JSON with trigger metadata (treat as untrusted in UI — escape for XSS) |
| `status` | enum | `"pending"`, `"running"`, `"completed"`, `"failed"`, `"killed"` |
| `current_step` | int | Index of currently executing step |
| `step_results` | string | JSON array of per-step results |
| `worktree_path` | string | Path to worktree if created (for cleanup on kill) |
| `started_at` | datetime | When execution started |
| `finished_at` | datetime | When execution completed |
| `error_msg` | string | Error message if failed |
| `created_at` | datetime | Creation timestamp |

### Step Result Format

Stored in `step_results` JSON array:

```json
[
  {
    "index": 0,
    "name": "run tests",
    "type": "shell",
    "status": "completed",
    "exit_code": 1,
    "output_tail": "last 100 lines of stdout+stderr...",
    "files": ["stdout.txt", "stderr.txt", "exit_code"],
    "started_at": "2026-03-31T06:00:01Z",
    "finished_at": "2026-03-31T06:00:04Z"
  },
  {
    "index": 1,
    "name": "fix failures",
    "type": "agent",
    "status": "running",
    "session_id": "abc-123-def",
    "session_name": "claude-abc-123-def",
    "files": [],
    "started_at": "2026-03-31T06:00:05Z",
    "finished_at": null
  }
]
```

| Field | Type | Description |
|-------|------|-------------|
| `files` | []string | Files created in the step directory, relative to `step_{n}/` (e.g. `["stdout.txt", "stderr.txt", "exit_code", "artifacts/coverage.html"]`) |

**Step status values:** `"pending"`, `"running"`, `"completed"`, `"failed"`, `"skipped"`

## API Endpoints

### Workflow CRUD

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/workflows` | Create a workflow |
| `GET` | `/api/workflows` | List all workflows (includes `last_run` summary per workflow) |
| `GET` | `/api/workflows/{id}` | Get workflow by ID |
| `GET` | `/api/workflows/by-name/{name}` | Get workflow by name |
| `PUT` | `/api/workflows/{id}` | Update a workflow |
| `DELETE` | `/api/workflows/{id}` | Delete a workflow |

### Create Workflow

```json
POST /api/workflows
{
  "name": "lint-and-fix",
  "description": "Run linter, fix issues with Claude, verify fix",
  "repo_path": "/Users/dev/myproject",
  "default_agent": {
    "agent_type": "claude",
    "model": "claude-sonnet-4-6",
    "capabilities": {"allow": ["file_read", "file_write", "shell"]}
  },
  "steps": [
    {
      "name": "run linter",
      "type": "shell",
      "command": "golangci-lint run ./... 2>&1"
    },
    {
      "name": "fix lint issues",
      "type": "agent",
      "prompt": "Fix all linting issues found in {{prev_stdout}}"
    },
    {
      "name": "verify",
      "type": "shell",
      "command": "golangci-lint run ./... 2>&1"
    }
  ]
}
```

Response: `201` + workflow JSON

### Create Validation Rules

The following are validated at creation (and update) time. Invalid requests return `400`:

| Rule | Error |
|------|-------|
| `name` must match `[a-zA-Z0-9_-]+` | `"name contains invalid characters"` |
| `name` must be unique | `409 Conflict` |
| `repo_path` must be an existing directory | `"repo_path does not exist"` |
| At least 1 step required | `"at least one step is required"` |
| Max 20 steps | `"maximum 20 steps allowed"` |
| Step names must be unique within the workflow | `"duplicate step name"` |
| Shell steps must have non-empty `command` | `"shell step missing command"` |
| Agent steps must have non-empty `prompt` | `"agent step missing prompt"` |
| Agent steps must have `agent_type` (from step or `default_agent`) | `"agent step missing agent_type"` |
| `{{prev_*}}` templates invalid on step 0 | `"step 0 cannot reference previous step"` |
| `{{step_N_*}}` must reference a prior step within bounds | `"template references invalid step index"` |
| `timeout_s` must be > 0 and ≤ 86400 if specified | `"invalid timeout"` |
| `max_duration_s` must be > 0 and ≤ 86400 if specified | `"invalid max_duration"` |
| Triggering a disabled workflow returns `409 Conflict` | `"workflow is disabled"` |

### Trigger & Runs

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/workflows/{id}/trigger` | Trigger a workflow run |
| `POST` | `/api/workflows/by-name/{name}/trigger` | Trigger a workflow run by name |
| `GET` | `/api/workflows/{id}/runs` | List runs for a workflow (`?limit=&offset=&status=`) |
| `GET` | `/api/workflows/runs/recent` | List recent runs across all workflows (`?limit=&offset=&status=`) |
| `GET` | `/api/workflows/runs/{runID}` | Get run status with step results |
| `POST` | `/api/workflows/runs/{runID}/kill` | Kill a running workflow |

### List Workflows Response

`GET /api/workflows` includes a `last_run` summary per workflow to avoid N+1 queries from the UI:

```json
{
  "workflows": [
    {
      "id": 3,
      "name": "lint-and-fix",
      "description": "Run linter, fix issues with Claude, verify fix",
      "enabled": true,
      "step_count": 3,
      "created_at": "2026-03-31T05:00:00Z",
      "updated_at": "2026-03-31T05:00:00Z",
      "last_run": {
        "id": 42,
        "status": "completed",
        "trigger_type": "api",
        "started_at": "2026-03-31T06:00:01Z",
        "finished_at": "2026-03-31T06:01:30Z"
      }
    }
  ]
}
```

`last_run` is `null` if the workflow has never been triggered.

### Trigger Workflow

```json
POST /api/workflows/{id}/trigger
{
  "trigger_type": "api",
  "context": {"source": "manual"}
}
```

Response:
```json
{
  "run_id": 42,
  "workflow_id": 3,
  "workflow_name": "lint-and-fix",
  "status": "pending",
  "trigger_type": "api",
  "created_at": "2026-03-31T06:00:00Z"
}
```

### Get Run Status

```json
GET /api/workflows/runs/42

{
  "run_id": 42,
  "workflow_id": 3,
  "workflow_name": "lint-and-fix",
  "status": "running",
  "current_step": 1,
  "trigger_type": "api",
  "trigger_context": {"source": "manual"},
  "started_at": "2026-03-31T06:00:01Z",
  "finished_at": null,
  "error_msg": null,
  "steps": [
    {
      "index": 0,
      "name": "run linter",
      "type": "shell",
      "status": "completed",
      "exit_code": 1,
      "output_tail": "Found 3 issues...",
      "started_at": "2026-03-31T06:00:01Z",
      "finished_at": "2026-03-31T06:00:04Z"
    },
    {
      "index": 1,
      "name": "fix lint issues",
      "type": "agent",
      "status": "running",
      "session_id": "abc-123",
      "started_at": "2026-03-31T06:00:05Z"
    },
    {
      "index": 2,
      "name": "verify",
      "type": "shell",
      "status": "pending"
    }
  ]
}
```

## CLI Interface

```bash
# Create a workflow from a JSON file
coral-board workflow create --file workflow.json

# List all workflows
coral-board workflow list

# Trigger a workflow by name
coral-board workflow trigger lint-and-fix
coral-board workflow trigger lint-and-fix --context '{"task_id": 5}'

# Check run status
coral-board workflow status 42

# List recent runs
coral-board workflow runs
coral-board workflow runs --workflow lint-and-fix

# Kill a running workflow
coral-board workflow kill 42
```

## Execution Engine

### Shell Step Execution

1. Create `{{step_dir}}/` and `{{step_dir}}/artifacts/`
2. Set environment variables (`CORAL_WORKFLOW_*` and template equivalents like `CORAL_PREV_STDOUT`)
3. Expand template variables in `command` **via environment variables** — the runner sets env vars and the command references `$CORAL_PREV_STDOUT` etc. For convenience, `{{var}}` syntax in commands is also expanded, but expansion uses the pre-computed absolute paths (no user-controlled content is interpolated)
4. Run via `exec.CommandContext` with working dir set to `repo_path`
5. **Set process group** (`cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`) so kill can terminate the entire process tree
6. Capture stdout to `{{step_dir}}/stdout.txt`, stderr to `{{step_dir}}/stderr.txt`
7. Write exit code to `{{step_dir}}/exit_code`
8. Update `step_results` in DB
9. If exit code != 0 and `continue_on_failure` is false, mark run as `"failed"`, skip remaining steps (set status to `"skipped"`)

### Agent Step Execution

1. Create `{{step_dir}}/` and `{{step_dir}}/artifacts/`
2. Set environment variables (`CORAL_WORKFLOW_*`)
3. Expand template variables in `prompt` (string substitution is safe for prompts — not shell-interpreted)
4. Resolve agent config: merge step-level `agent` over workflow-level `default_agent`
5. **Acquire concurrency slot** from the shared scheduler pool before launching (release after step completes)
6. Call `FireOneshot` with:
   - `prompt`: expanded prompt text
   - `agent_type`: from resolved agent config
   - `repo_path`: workflow's repo_path
   - `flags`: from agent config `flags` + model flag
   - `max_duration_s`: from step's `timeout_s`
   - Agent config: capabilities and tools passed through (MCP servers are not, #3644)
7. `FireOneshot` launches agent via `AgentLauncher`, calls `watchSession` which polls every **5 seconds** (reduced from 30s for workflow steps to minimize inter-step latency)
8. When agent exits (tmux session dies), step is complete. **Release concurrency slot.**
9. Check session exit status — if error, mark step as `"failed"`
10. Update `step_results` in DB

### Agent Completion Detection

Agent steps use `FireOneshot` which blocks until the tmux session exits. Claude Code exits naturally after completing its prompt in non-interactive mode. Safety nets:

- `timeout_s` per step (default: 600s for agent steps)
- `max_duration_s` on the workflow (default: 3600s total)
- Manual kill via `POST /api/workflows/runs/{runID}/kill`
- Shared concurrency pool prevents resource exhaustion (per-step acquisition/release, not per-workflow)

### Kill Semantics

`POST /api/workflows/runs/{runID}/kill` dispatches based on the currently executing step type:

| Step type | Kill action |
|-----------|-------------|
| Shell | Send `SIGTERM` to the **process group** (not just the parent). Wait up to 5s, then `SIGKILL` the group. |
| Agent | Kill the tmux session via `tmux kill-session -t {session_name}`. |

The runner tracks the active child for each run (either an `exec.Cmd` handle or a tmux session name) so the kill handler can dispatch correctly. After killing the active step, all remaining steps are marked `"skipped"` and the run status is set to `"killed"`.

## Integration Points

### Triggering from board task completion

After completing a board task, call the workflow trigger API:

```bash
curl -X POST http://localhost:8420/api/workflows/by-name/lint-and-fix/trigger \
  -H "Content-Type: application/json" \
  -d '{"trigger_type": "hook", "context": {"task_id": 5}}'
```

### Triggering from Claude hooks

A Claude hook can call the trigger endpoint when specific events occur (e.g., task completion, tool use). Use `coral-board workflow trigger <name>` from a hook script.

### Triggering from CLI

```bash
coral-board workflow trigger lint-and-fix --context '{"branch": "feature-x"}'
```

## Implementation Plan

### Phase 1: Core (MVP)

| Component | File | Description |
|-----------|------|-------------|
| Schema | `internal/store/connection.go` | Add `workflows` and `workflow_runs` tables |
| Store | `internal/store/workflow.go` | CRUD for workflows and runs |
| Runner | `internal/background/workflow_runner.go` | Step execution engine (shell + agent) |
| API | `internal/server/routes/workflow.go` | HTTP handlers |
| Routes | `internal/server/server.go` | Register `/api/workflows/` routes |
| Wiring | `internal/startup/startup.go` | Wire WorkflowStore and WorkflowRunner |
| CLI | `cmd/coral-board/main.go` | `workflow` subcommand |

### Phase 2: UI & Polish

- Workflows tab in dashboard showing workflow list and run history
- Live step progress view for running workflows (WebSocket events: `workflow_step_update`)
- Trigger workflow from UI button
- Step output streaming/tailing for long-running agent steps
- Computed `duration_s` field in step results for frontend convenience

### Phase 3: Advanced Triggers

- `max_turns` support for agent steps (via CLI flag passthrough)
- Cron-based workflow triggers
- Automatic triggers on board task completion
- Webhook callbacks on workflow completion
- Conditional step execution (`if_prev_failed`, `if_prev_succeeded`)
- Run history retention/cleanup (`max_run_history` per workflow, artifact directory pruning)
- Filesystem-enforced read-only on previous step directories

## Example Workflows

### Test and Fix

```json
{
  "name": "test-and-fix",
  "description": "Run tests, fix failures, verify",
  "repo_path": "/Users/dev/myproject",
  "default_agent": {
    "agent_type": "claude",
    "capabilities": {"allow": ["file_read", "file_write", "shell"]}
  },
  "steps": [
    {"name": "test", "type": "shell", "command": "go test ./... 2>&1"},
    {
      "name": "fix",
      "type": "agent",
      "prompt": "The tests failed. Output is at {{prev_stdout}}. Fix all failures."
    },
    {"name": "verify", "type": "shell", "command": "go test ./... 2>&1"}
  ]
}
```

### Pre-commit Hook

```json
{
  "name": "pre-commit",
  "description": "Lint, format, and validate before commit",
  "repo_path": "/Users/dev/myproject",
  "steps": [
    {"name": "lint", "type": "shell", "command": "golangci-lint run ./..."},
    {"name": "format", "type": "shell", "command": "gofmt -w ."},
    {"name": "test", "type": "shell", "command": "go test ./..."}
  ]
}
```

### Code Review

```json
{
  "name": "review-pr",
  "description": "AI-powered code review on current diff",
  "repo_path": "/Users/dev/myproject",
  "default_agent": {
    "agent_type": "claude",
    "model": "claude-sonnet-4-6",
    "capabilities": {"allow": ["file_read"]}
  },
  "steps": [
    {"name": "capture-diff", "type": "shell", "command": "git diff main...HEAD > {{step_dir}}/artifacts/diff.patch"},
    {
      "name": "review",
      "type": "agent",
      "prompt": "Review the code changes in {{step_0_dir}}/artifacts/diff.patch. Write your review to {{step_dir}}/artifacts/review.md"
    }
  ]
}
```
