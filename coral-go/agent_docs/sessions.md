# Sessions API

The Sessions API manages live agent sessions — launching, monitoring, controlling, and terminating AI agents running in terminal sessions.

All endpoints are prefixed with `/api/sessions`.

---

## Session Listing & Discovery

### GET `/api/sessions/live`

List all live (running) agent sessions with enriched status.

**Response:**
```json
[
  {
    "name": "my-project",
    "agent_type": "claude",
    "session_id": "abc123-uuid",
    "tmux_session": "coral_claude_abc123",
    "status": "Working",
    "summary": "Implementing feature X",
    "staleness_seconds": 5,
    "display_name": "Custom Name",
    "icon": "🤖",
    "working_directory": "/home/user/project",
    "waiting_for_input": false,
    "done": false,
    "waiting_reason": null,
    "waiting_summary": null,
    "working": true,
    "stuck": false,
    "changed_file_count": 3,
    "commands": {"compress": "/compact", "clear": "/clear"},
    "board_project": "my-board",
    "board_job_title": "Task Title",
    "board_unread": 0,
    "log_path": "/path/to/log",
    "sleeping": false
  }
]
```

Sleeping sessions (no active tmux) are included as placeholder entries with `"sleeping": true` and `"status": "Sleeping"`.

---

### GET `/api/sessions/live/{name}`

Get detailed info for a single session, including pane capture, status, and recent logs.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `agent_type` | string | Agent type (e.g., `claude`, `codex`) |
| `session_id` | string | Session UUID |

---

### GET `/api/sessions/resolve`

Resolve session info by process IDs (used by CLI integrations).

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `pids` | string | Comma-separated list of PIDs |

**Response:**
```json
{
  "subscriber_id": "session-name",
  "project": "board-name",
  "session_name": "coral_claude_abc123"
}
```

---

## Session Metadata & Monitoring

### GET `/api/sessions/live/{name}/capture`

Capture current terminal content (tmux pane snapshot).

**Query Parameters:** `agent_type`, `session_id`

**Response:**
```json
{
  "name": "my-project",
  "capture": "terminal output text...",
  "error": null
}
```

### GET `/api/sessions/live/{name}/poll`

Batch endpoint returning capture, tasks, and events in a single call. Reduces round-trips for the dashboard.

**Query Parameters:** `agent_type`, `session_id`, `events_limit`

**Response:**
```json
{
  "capture": "terminal output...",
  "tasks": [],
  "events": []
}
```

### GET `/api/sessions/live/{name}/info`

Session metadata: tmux info, git state, log path, prompt, board association.

**Query Parameters:** `agent_type`, `session_id`

### GET `/api/sessions/live/{name}/chat`

Read the agent's JSONL conversation log with pagination.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `agent_type` | string | Agent type |
| `session_id` | string | Session UUID |
| `working_directory` | string | Working directory for log lookup |
| `after` | int | Message index to start after |
| `limit` | int | Max messages to return |
| `offset` | int | Skip N messages |

**Response:**
```json
{
  "messages": [...],
  "total": 42,
  "has_more": true
}
```

---

## File Management

### GET `/api/sessions/live/{name}/files`

List files changed by the agent (git diff).

**Query Parameters:** `session_id`

**Response:**
```json
{
  "agent_name": "my-project",
  "files": [
    {"filepath": "main.go", "additions": 10, "deletions": 3, "status": "M"}
  ]
}
```

File status values: `"M"` (modified), `"A"` (added), `"D"` (deleted).

### POST `/api/sessions/live/{name}/files/refresh`

Force re-compute the git diff for changed files.

**Request Body:**
```json
{"session_id": "abc123"}
```

**Response:**
```json
{"files": [...]}
```

### GET `/api/sessions/live/{name}/file-content`

Read the current content of a file in the agent's working directory.

**Query Parameters:** `filepath`, `session_id`

**Response:**
```json
{
  "filepath": "main.go",
  "content": "package main\n...",
  "working_directory": "/home/user/project"
}
```

### GET `/api/sessions/live/{name}/file-original`

Read the git base version of a file (before the agent's changes).

**Query Parameters:** `filepath`, `session_id`

### PUT `/api/sessions/live/{name}/file-content`

Save modified content to a file in the agent's working directory.

**Query Parameters:** `filepath`, `session_id`

**Request Body:**
```json
{"content": "updated file content..."}
```

**Response:**
```json
{"ok": true, "filepath": "main.go"}
```

### GET `/api/sessions/live/{name}/diff`

Get git diff for a specific file.

**Query Parameters:** `filepath`, `session_id`

**Response:**
```json
{
  "filepath": "main.go",
  "diff": "--- a/main.go\n+++ b/main.go\n...",
  "working_directory": "/home/user/project"
}
```

### GET `/api/sessions/live/{name}/search-files`

Fuzzy file search or directory browsing within the agent's working directory.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `q` | string | Fuzzy search query |
| `dir` | string | Directory path to browse |
| `session_id` | string | Session UUID |

**Response (search):**
```json
{"files": ["main.go", "cmd/server.go"]}
```

**Response (browse):**
```json
{"entries": [...], "dir": "/home/user/project/cmd"}
```

---

## Git Information

### GET `/api/sessions/live/{name}/git`

Git snapshot for the agent's working directory.

**Query Parameters:** `session_id`

**Response:**
```json
{
  "branch": "feature/new-thing",
  "commit_hash": "abc1234",
  "commit_subject": "Add new feature",
  "working_directory": "/home/user/project"
}
```

---

## Terminal Control

### POST `/api/sessions/live/{name}/send`

Send a text command to the agent's terminal.

**Request Body:**
```json
{
  "command": "/compact",
  "agent_type": "claude",
  "session_id": "abc123"
}
```

**Response:**
```json
{"ok": true, "command": "/compact"}
```

### POST `/api/sessions/live/{name}/keys`

Send raw tmux key codes to the terminal (e.g., Enter, Ctrl-C).

**Request Body:**
```json
{
  "keys": ["Enter"],
  "agent_type": "claude",
  "session_id": "abc123"
}
```

**Response:**
```json
{"ok": true, "keys": ["Enter"]}
```

### POST `/api/sessions/live/{name}/resize`

Resize the terminal pane.

**Request Body:**
```json
{
  "columns": 120,
  "agent_type": "claude",
  "session_id": "abc123"
}
```

**Response:**
```json
{"ok": true, "columns": 120}
```

---

## Session Lifecycle

### POST `/api/sessions/launch`

Launch a new agent session.

**Request Body:**
```json
{
  "working_dir": "/home/user/project",
  "agent_type": "claude",
  "display_name": "My Agent",
  "flags": ["--flag1"],
  "prompt": "System prompt for the agent",
  "board_name": "my-board",
  "board_server": "http://remote:8420",
  "backend": "pty",
  "board_type": "default",
  "model": "claude-sonnet-4-20250514",
  "capabilities": ["tool_use"],
  "tools": ["Read", "Write"],
  "mcpServers": {"server1": {"command": "npx", "args": ["-y", "@server/mcp"]}}
}
```

Required: `working_dir`. All other fields are optional.

**Response:**
```json
{
  "ok": true,
  "session_id": "abc123-uuid",
  "session_name": "coral_claude_abc123",
  "tmux_session": "coral_claude_abc123"
}
```

### POST `/api/sessions/launch-team`

Launch a team of agents sharing a message board.

**Request Body:**
```json
{
  "board_name": "my-team",
  "working_dir": "/home/user/project",
  "agent_type": "claude",
  "flags": ["--global-flag"],
  "board_server": null,
  "board_type": "default",
  "agents": [
    {
      "name": "architect",
      "prompt": "You are an architect...",
      "agent_type": "claude",
      "model": "claude-sonnet-4-20250514",
      "capabilities": ["tool_use"],
      "tools": ["Read", "Grep"],
      "mcpServers": {}
    },
    {
      "name": "developer",
      "prompt": "You are a developer..."
    }
  ]
}
```

**Response:**
```json
{
  "ok": true,
  "board": "my-team",
  "agents": [
    {"name": "architect", "session_id": "abc123", "session_name": "coral_claude_abc123"},
    {"name": "developer", "session_id": "def456", "session_name": "coral_claude_def456", "error": null}
  ]
}
```

### POST `/api/sessions/live/{name}/restart`

Kill and relaunch a session with the same or updated configuration.

**Request Body:**
```json
{
  "agent_type": "claude",
  "session_id": "abc123",
  "extra_flags": ["--verbose"],
  "prompt": "Updated prompt",
  "model": "claude-sonnet-4-20250514",
  "capabilities": ["tool_use"]
}
```

**Response:**
```json
{"ok": true, "session_id": "new-uuid", "session_name": "coral_claude_newuuid"}
```

### POST `/api/sessions/live/{name}/resume`

Resume a session from a checkpoint (conversation continuation).

**Request Body:**
```json
{
  "session_id": "original-session-id",
  "agent_type": "claude",
  "current_session_id": "current-uuid"
}
```

**Response:**
```json
{"ok": true, "session_id": "new-uuid", "session_name": "coral_claude_newuuid"}
```

### POST `/api/sessions/live/{name}/kill`

Before terminating the runtime or removing an owned worktree, Coral captures the session's complete git patch at:

```text
~/.coral/artifacts/sessions/{sessionID}/changes.diff
```

The response includes `changes_artifact` when the snapshot succeeds and `changes_artifact_error` when it cannot be generated. Snapshot failure does not prevent terminating a non-git agent session.

Download the persisted artifact after termination:

```bash
curl -fsS http://localhost:8420/api/sessions/{sessionID}/changes.diff -o changes.diff
```

The download response uses `Content-Type: text/x-diff`. Team kill snapshots every non-terminal member before removing the shared worktree.

Terminate a session.

**Request Body:**
```json
{
  "agent_type": "claude",
  "session_id": "abc123"
}
```

**Response:**
```json
{"ok": true}
```

### POST `/api/sessions/live/{name}/attach`

Open the session in a native terminal window (macOS only — uses `open -a Terminal`).

**Request Body:**
```json
{
  "agent_type": "claude",
  "session_id": "abc123"
}
```

---

## Display & Metadata

### PUT `/api/sessions/live/{name}/display-name`

Set a custom display name for a session.

**Request Body:**
```json
{"display_name": "My Custom Name", "session_id": "abc123"}
```

**Response:**
```json
{"ok": true, "display_name": "My Custom Name"}
```

### PUT `/api/sessions/live/{name}/icon`

Set or clear the emoji icon for a session.

**Request Body:**
```json
{"session_id": "abc123", "icon": "🤖"}
```

Pass `"icon": null` to clear.

**Response:**
```json
{"ok": true, "icon": "🤖"}
```

---

## Agent Tasks

Per-agent task lists (visible in the session detail panel).

### GET `/api/sessions/live/{name}/tasks`

**Query Parameters:** `session_id` (optional)

**Response:**
```json
[
  {
    "id": 1,
    "agent_name": "my-project",
    "session_id": "abc123",
    "title": "Implement feature X",
    "completed": 0,
    "sort_order": 0,
    "created_at": "2025-01-15T10:30:00Z"
  }
]
```

### POST `/api/sessions/live/{name}/tasks`

**Request Body:**
```json
{"title": "New task", "session_id": "abc123"}
```

### PATCH `/api/sessions/live/{name}/tasks/{taskID}`

**Request Body:**
```json
{"title": "Updated title", "completed": 1, "sort_order": 2}
```

All fields are optional.

### DELETE `/api/sessions/live/{name}/tasks/{taskID}`

### POST `/api/sessions/live/{name}/tasks/reorder`

**Request Body:**
```json
{"task_ids": [3, 1, 2]}
```

---

## Agent Notes

Per-agent notes (visible in the session detail panel).

### GET `/api/sessions/live/{name}/notes`

**Query Parameters:** `session_id` (optional)

### POST `/api/sessions/live/{name}/notes`

**Request Body:**
```json
{"content": "Note text here", "session_id": "abc123"}
```

### PATCH `/api/sessions/live/{name}/notes/{noteID}`

**Request Body:**
```json
{"content": "Updated note"}
```

### DELETE `/api/sessions/live/{name}/notes/{noteID}`

---

## Agent Events

Structured events tracking agent activity (tool calls, notifications, etc.).

### GET `/api/sessions/live/{name}/events`

**Query Parameters:** `session_id` (optional), `limit` (int)

**Response:**
```json
[
  {
    "id": 1,
    "agent_name": "my-project",
    "session_id": "abc123",
    "event_type": "tool_use",
    "summary": "Ran: Edit main.go",
    "tool_name": "Edit",
    "detail_json": "{\"file\": \"main.go\"}",
    "created_at": "2025-01-15T10:30:00Z"
  }
]
```

### POST `/api/sessions/live/{name}/events`

**Request Body:**
```json
{
  "event_type": "tool_use",
  "summary": "Ran: Edit main.go",
  "tool_name": "Edit",
  "session_id": "abc123",
  "detail_json": "{}"
}
```

### GET `/api/sessions/live/{name}/events/counts`

Aggregate tool usage counts.

**Query Parameters:** `session_id` (optional)

**Response:**
```json
[
  {"tool_name": "Edit", "count": 15},
  {"tool_name": "Read", "count": 42}
]
```

### DELETE `/api/sessions/live/{name}/events`

Clear all events for a session.

**Query Parameters:** `session_id` (optional)

---

## Sleep / Wake

Sleep suspends agent sessions (kills the process, preserves state). Wake relaunches them.

### Team Sleep/Wake

#### GET `/api/sessions/live/team/{boardName}/sleep-status`

```json
{"sleeping": false}
```

#### POST `/api/sessions/live/team/{boardName}/sleep`

```json
{
  "ok": true,
  "sleeping": true,
  "sessions_affected": 3,
  "sessions_killed": 3,
  "board_paused": true
}
```

#### POST `/api/sessions/live/team/{boardName}/wake`

```json
{
  "ok": true,
  "sleeping": false,
  "sessions_relaunched": 3,
  "board_paused": false
}
```

#### POST `/api/sessions/live/team/{boardName}/reset`

Kills and relaunches all team members with their original configuration.

```json
{
  "ok": true,
  "board": "my-team",
  "agents": [...]
}
```

### Individual Session Sleep/Wake

#### POST `/api/sessions/live/{sessionID}/sleep`

```json
{"ok": true, "sleeping": true}
```

#### POST `/api/sessions/live/{sessionID}/wake`

```json
{"ok": true, "sleeping": false}
```

### Bulk Sleep/Wake

#### POST `/api/sessions/live/sleep-all`

```json
{"ok": true, "sessions_affected": 5, "sessions_killed": 5}
```

#### POST `/api/sessions/live/wake-all`

```json
{"ok": true, "sessions_relaunched": 5}
```

---

## Error Responses

All endpoints return JSON. Error format:

```json
{"error": "description of what went wrong"}
```

**Status Codes:**

| Code | Meaning |
|------|---------|
| 200 | Success |
| 400 | Bad request (missing/invalid parameters) |
| 403 | Forbidden (auth required) |
| 404 | Session not found |
| 500 | Internal server error |
| 503 | Demo limit reached (beta tier) |
