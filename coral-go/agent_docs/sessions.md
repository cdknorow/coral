# Sessions API

The Sessions API manages live agent sessions — launching, monitoring, controlling, and terminating AI agents running in terminal sessions.

All endpoints are prefixed with `/api/sessions`.

---

## Session Listing & Discovery

### GET `/api/sessions/live`

List all live (running) agent sessions with enriched status. Archived agents are listed last.

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
    "sleeping": false,
    "archived": false
  }
]
```

Sleeping sessions (no active tmux) are included as placeholder entries with `"sleeping": true`, `"status": "Sleeping"` and `"staleness_seconds": null`. Every row carries `archived` (`true` only for an archived agent, which is always also `sleeping`; see [Sleep / Wake](#sleep--wake)).

`staleness_seconds` is rounded down to the unit the dashboard shows: whole seconds under a minute, whole minutes (a multiple of 60) under an hour, whole hours (a multiple of 3600) above. `/ws/coral` rows use the same value; detail endpoints keep the exact value.

---

### GET `/api/sessions/{sessionID}/status`

Poll lifecycle and agent-turn state using only the stable session ID. The `state` value is one of `active`, `waiting_for_input`, `done`, `sleeping`, or `finished`.

```json
{
  "session_id": "abc123-uuid",
  "state": "waiting_for_input",
  "active": true,
  "finished": false,
  "waiting_for_input": true,
  "awaiting_user": false,
  "waiting_reason": "notification",
  "waiting_summary": "Notification: Claude needs your permission to use Bash",
  "done": false,
  "sleeping": false,
  "latest_event": "notification",
  "latest_summary": "Notification: Claude needs your permission to use Bash",
  "stopped_at": null
}
```

`waiting_for_input` is true only while the agent has asked the user something directly, such as a permission or approval prompt, and is blocked on the answer. It is not set by every notification: the idle reminder an agent sends after finishing a turn reports `done` instead. `waiting_reason` and `waiting_summary` are `null` unless a request is pending.

`done` means the agent completed its current turn but the session remains active. `awaiting_user` carries the same value under the name the session list uses. `finished` means the session itself has stopped and will not accept more input without being resumed.

---

### GET `/api/sessions/live/{name}`

Get detailed info for a single session, including pane capture, status, and recent logs.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `agent_type` | string | Agent type (e.g., `claude`, `codex`) |
| `session_id` | string | Session UUID |

---

### GET `/api/sessions/{sessionID}/resolve`

Routing and identity record for one session ID (used by the popout). `sessionID` must be a canonical UUID (else 400). An unknown ID answers 200 with `state: "not_found"` (or `"finished"` if it is in the history index) so callers can retry.

```json
{
  "session_id": "6f1c2a7e-3b9d-4e2a-9c51-0d8b7a4e2f10",
  "state": "sleeping",
  "active": true,
  "sleeping": true,
  "archived": false,
  "agent_type": "claude",
  "name": "my-project",
  "tmux_session": null
}
```

`state` is `active`, `sleeping`, `finished` or `not_found`. `tmux_session` is `null` unless the session is awake. The full record also carries `working_directory`, `display_name`, `board_project`, `icon`, `status`, `first_prompt` and the turn-state fields of the list rows.

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

**Every per-agent route that reaches its terminal names the session.** `GET live/{name}`, `/capture`, `/poll`, `/info`, `/prompt-options`, `/ws/terminal/{name}` (query `session_id`) and `/kill`, `/restart`, `/attach` (body `session_id`), `/resume` (body `current_session_id`) answer 400 `{"error": "session_id is required", "code": "session_id_required"}` without one (the WebSocket closes with 1008), unless `{name}` is the agent's exact terminal name (`claude-<uuid>`). A bare agent or folder name is shared by teammates and would reach whichever terminal matched it. Take `name` and `session_id` from `GET /api/sessions/live`.

### GET `/api/sessions/live/{name}/capture`

Capture current terminal content (tmux pane snapshot).

**Query Parameters:** `agent_type`, `session_id` (required, see above)

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

**Query Parameters:** `agent_type`, `session_id` (required), `events_limit`

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

**Query Parameters:** `agent_type`, `session_id` (required)

### GET `/api/sessions/live/{name}/chat`

Read the agent's JSONL conversation log with pagination. Works for sleeping and archived agents too (their transcript stays on disk). Full details: [session-history.md](session-history.md).

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `agent_type` | string | Agent type |
| `session_id` | string | Session UUID |
| `working_directory` | string | Working directory for log lookup |
| `after` | int | Poll: return messages after this count. `0` (or omitted) returns a history page |
| `limit` | int | History page size (default 100) |
| `before` | int | History page: absolute message index to end before (exclusive). Wins over `offset`; 400 if negative or not a number |
| `offset` | int | History page (older paging): skip the newest N messages. Shifts as messages arrive; prefer `before` |

**Response (history page, `after=0`):**
```json
{
  "messages": [...],
  "total": 342,
  "start": 242,
  "has_more": true,
  "limit_notice": ""
}
```

`start` is the absolute index of the first message returned; pass it as the next `before` to get the page above. History pages are gzip-compressed when the request sends `Accept-Encoding: gzip`. A poll (`after>0`) returns `{messages, total, limit_notice}`.

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

Send a text command to the agent's terminal. `agent_type` and `session_id` are required and must match the live session named in the path (its name or its terminal name); a call by bare name is refused. Nothing is sent when the request is refused:

| Status | Body | When |
|--------|------|------|
| 400 | `{"error": "session_id and agent_type are required", "code": "session_id_required"}` | either field missing |
| 400 | `{"error": "invalid session_id"}` | `session_id` is not a UUID |
| 400 | `{"error": "session target mismatch"}` | `agent_type` or path name does not match that session |
| 404 | `{"error": "live session not found"}` | unknown or ended session |
| 409 | `{"error": "session is sleeping"}` | sleeping agent (wake it first) |
| 409 | `{"error": "session is archived"}` | archived agent |
| 503 | `{"error": "...", "code": "agent_metadata_unavailable", "not_sent": true}` | session metadata could not be read; retry |
| 409 | `{"error": "The agent's terminal is gone", "code": "terminal_gone", "not_sent": true}` | the session is listed as awake but its own terminal no longer exists |

Checks run in this order: required fields, `session_id` format, unknown session, sleeping/archived, then target match. So a mismatched request to a sleeping agent answers 409, not 400.

**A request with a `session_id` only ever reaches that session's own terminal.** If it is gone, Coral answers `terminal_gone` and sends nothing; it never falls back to another terminal with the same name, folder or a similar name (teammates often share a folder). The same holds for `/keys`, `/resize` and `/answer-prompt` (409 `terminal_gone`); restart and resume instead start the agent in a fresh terminal in its own working directory; kill, sleep and Kill All (they end only that session), board and task nudges, and reads such as `/prompt-options` and capture (an empty result). `/send`, `/keys`, `/resize` and `/answer-prompt` require `session_id` and `agent_type` (400 `session_id_required` otherwise), so input is never routed by name; only reads and internal callers without a `session_id` still match by name.

```bash
curl -s -X POST http://localhost:PORT/api/sessions/live/NAME/send \
  -H 'Content-Type: application/json' \
  -d '{"command": "/compact", "agent_type": "claude", "session_id": "SID"}'
```

**Request Body:**
```json
{
  "command": "/compact",
  "agent_type": "claude",
  "session_id": "6f1c2a7e-3b9d-4e2a-9c51-0d8b7a4e2f10"
}
```

**Response:**
```json
{"ok": true, "command": "/compact"}
```

### POST `/api/sessions/live/{name}/keys`

Send raw tmux key codes to the terminal (e.g., Enter, Ctrl-C). The same exact-target rules and errors as `/send` apply (`agent_type` and `session_id` required; 409 for a sleeping or archived agent), except that a metadata failure is a plain 503 `{error}`. `keys` must be a non-empty list (else 400).

```bash
curl -s -X POST http://localhost:PORT/api/sessions/live/NAME/keys \
  -H 'Content-Type: application/json' \
  -d '{"keys": ["Enter"], "agent_type": "claude", "session_id": "SID"}'
```

**Request Body:**
```json
{
  "keys": ["Enter"],
  "agent_type": "claude",
  "session_id": "6f1c2a7e-3b9d-4e2a-9c51-0d8b7a4e2f10"
}
```

**Response:**
```json
{"ok": true, "keys": ["Enter"]}
```

### POST `/api/sessions/live/{name}/resize`

Resize the terminal pane. The same exact-target rules and errors as `/send` apply (`agent_type` and `session_id` required).

**Request Body:**
```json
{
  "columns": 120,
  "agent_type": "claude",
  "session_id": "6f1c2a7e-3b9d-4e2a-9c51-0d8b7a4e2f10"
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
  "board_server": "http://remote:PORT",
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

Kill and relaunch a session with the same or updated configuration. If the agent's own terminal is gone (it crashed or was closed), the restart starts it in a fresh terminal in its working directory.

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

Resume a session from a checkpoint (conversation continuation). If the agent's own terminal is gone, it resumes in a fresh terminal in the agent's working directory (409 `terminal_gone` only when no working directory is recorded).

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

Download the current or persisted session changes using only the session ID:

```bash
curl -fsS http://localhost:PORT/api/sessions/SID/changes -o changes.diff
```

`GET /api/sessions/{sessionID}/changes` prefers a freshly generated live diff and
falls back to the saved artifact. `GET /api/sessions/{sessionID}/changes.diff`
serves only the artifact captured at termination. For a live session,
`GET /api/sessions/live/{name}/changes.diff?session_id={sessionID}` generates the
current checkout diff.

For a live session, Coral generates the diff from its registered working directory. After termination or worktree cleanup, it serves the captured artifact instead. The response uses `Content-Type: text/x-diff`. Team kill snapshots every non-terminal member before removing the shared worktree.

Terminate a session.

**Request Body:**
```json
{
  "agent_type": "claude",
  "session_id": "6f1c2a7e-3b9d-4e2a-9c51-0d8b7a4e2f10"
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
  "session_id": "6f1c2a7e-3b9d-4e2a-9c51-0d8b7a4e2f10"
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

### POST `/api/sessions/live/{name}/context-window`

Records the model-derived context-window size for a live session. This is
normally called by Coral's session-start hook.

```json
{"session_id": "abc123", "model": "claude-sonnet-4-6"}
```

Unknown models return `{"ok":true,"context_window":0,"skipped":true}`.

---

## Agent Tasks

Personal task queues visible in the session detail panel. For agent automation,
use the [coral-agent CLI and API](agent-tasks.md). These compatibility endpoints
share its dependency, artifact, and immutable completion rules.

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
    "status": "pending",
    "sort_order": 0,
    "created_at": "2025-01-15T10:30:00Z"
  }
]
```

Responses also include `workflow` and `blocked_by` when present. Numeric
`completed` values are pending `0`, completed `1`, in progress `2`, skipped `3`,
blocked `4`, and draft `5`. Check `workflow.outcome` for success versus failure.

### POST `/api/sessions/live/{name}/tasks`

**Request Body:**
```json
{"title": "New task", "session_id": "abc123"}
```

Also accepts `body`, `priority`, `workflow`, `blocked_by`, `draft`, and `notify`.
Returns **200** with the existing open task of the same title or the newly
created task. `notify: true` nudges the agent only for a newly created pending
task; blocked and draft work does not generate this creation nudge.

### PATCH `/api/sessions/live/{name}/tasks/{taskID}`

**Request Body:**
```json
{"title": "Updated title", "completed": 1, "sort_order": 2}
```

All fields are optional. `completed: 1` completes successfully, `2` claims, and
`3` cancels. Setting `0` to reset/reopen is rejected. These transitions enforce
the same active-task and required-output checks as the agent API. Use
`/api/agent/tasks/{id}/complete` to submit artifacts or a failed outcome.
`sort_order` changes display order, not priority-based claim order.

### DELETE `/api/sessions/live/{name}/tasks/{taskID}`

Only unstarted tasks without incoming dependency, parent, or retry references
can be deleted. Active and finished tasks cannot be deleted.

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

### POST `/api/sessions/live/{name}/token-usage`

Records a cumulative usage snapshot for integrations that report usage outside
the local agent hooks. The JSON body accepts `session_id`, `input_tokens`,
`output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `cost_usd`, and
`num_turns`.

---

## Auto-accept

Per agent, the user can let Coral answer that agent's **permission prompts** for it. While it is on, an amber **AUTO** chip shows on the agent's row and in its header.

**Agents must never turn auto-accept on, for themselves or another agent:** it is the user's decision, made in the dashboard after a warning. Coral refuses the request when it comes from an agent, and if a task seems to need it, ask the user instead.

- **What is answered:** only a prompt asking to approve a tool call. Coral picks the agent type's *approve once* option ("Yes", "Yes, proceed", "Allow once"); never "always", "don't ask again" or an option that changes settings.
- **What always waits for the user:** questions (AskUserQuestion and the like), plan approvals, free-text prompts and anything Coral does not recognise.
- **Risky actions can be approved.** Auto-accept is not a sandbox: the user is told so when turning it on, and should use it only for a task they trust. As a best-effort safety net, not a guarantee, Coral leaves some high-risk actions it recognises for the user:
  - a recursive delete (`rm -r`, `-R`, `-rf`, `--recursive`) of anything outside the agent's working directory, or aimed at `/` or `~`;
  - `git push --force` / `-f` / `--force-with-lease` / `--mirror` / a `+ref` push, and a push that deletes a remote branch (`--delete`, `-d`, `origin :branch`);
  - git commands that destroy uncommitted work: `git reset --hard`, `git clean` (unless a dry run, `-n`), `git checkout -- …` / `git checkout .` / `git checkout -f`, and `git restore` (except `--staged` only);
  - `su`, `sudo` or `doas`, however written: `\sudo`, `s\udo` (a backslash inside a word), `(sudo …)`, `$(sudo …)`, `` `sudo …` ``, `/usr/bin/sudo`;
  - `find … -delete` or `find … -exec rm …` with a search path outside the working directory, and `sed -i` or `perl -i` (`-pi`, `-i.bak`) on a file outside it;
  - recursive `chmod` / `chown` on a path outside the working directory;
  - a download run by a shell (`curl` or `wget` piped into `sh`, `bash` or `zsh`, or `bash <(curl …)`);
  - reading or writing a file outside the working directory (Write, Edit, MultiEdit, NotebookEdit, Read; a Codex edit's destination), with symbolic links resolved, so a link inside the repository that points out of it counts as outside;
  - a shell command that writes outside the working directory: a `>`, `>>`, `>|`, `&>` or `2>` redirection, `tee`, the destination of `cp`, `install` or `ln` (including `-t`), any path of `mv`, or `dd of=`. `$VAR` and `~` targets count as outside; `/dev/null`, `/dev/stdout`, `/dev/stderr`, `/dev/tty` and `2>&1` are fine.

  Coral looks at the tool's input as the agent reported it and the command shown in the prompt, including commands wrapped in `bash -c`, chained commands and common wrappers (`env`, `nice`, `timeout` and the like), and does not hold back ordinary work inside the repository (`rm -rf build`, a plain `git push`). It is not a full shell parser and does not follow scripts or interpreters, so risky actions may be approved. Each action left for the user is an `auto_accept_skipped` event saying why (at most one per prompt and one per minute).
- **Where:** on the Coral server, so it works with the dashboard closed and through the Cloud Hub. Each approval is an `auto_accept` event (e.g. "Auto-approved Bash: npm test") in the agent's Activity.
- **Safety:** a prompt is answered once. The same prompt approved 5 times in a minute, or more than 30 approvals in a minute, turns auto-accept off (`auto_accept_paused`). Turning it on or off is itself an event (`auto_accept_enabled`, naming whether it came from the dashboard or the Cloud Hub, and `auto_accept_disabled`), so every change shows in Activity.
- **Time limit:** optional when turned on in the dashboard (15 minutes to 8 hours, or none); from the Cloud Hub always set (1 hour unless chosen, at most 8 hours).
- **Turned off automatically** (an `auto_accept_cleared` event says why): when the agent process starts again (relaunch, restart, resume, wake from sleep) or the time limit runs out. A dashboard-enabled auto-accept survives a Coral server restart while the same agent run is alive; one turned on from the Cloud Hub also ends when Coral restarts, the Cloud link is removed or revoked, the hub's access drops below Launch, or the owner turns off the switch below.
- **From the Cloud Hub:** needs Launch access on this computer (the owner's opt-in) and the local switch **Cloud Hub can turn on auto-accept** in Coral's Settings (on by default while access is Launch; it cannot be changed through the hub; turning it off ends any hub-enabled auto-accept). Turning it on through the hub needs Launch; turning it off needs Interact.
- **Stop all:** the top-bar menu's **Stop all auto-accept**, or `coral auto-accept off --all`, turns it off for every agent at once.

```
POST /api/sessions/live/{session_id}/auto-accept/enable    {"session_id": "...", "acknowledged": true, "duration_s": 3600}
POST /api/sessions/live/{session_id}/auto-accept/disable   {"session_id": "..."}
POST /api/auto-accept/disable-all                          -> {"stopped": N}
```

These routes are for the dashboard (and the Cloud Hub's dashboard); `{session_id}` may also be the agent's name, and it must name the same agent as the body's `session_id` (404 otherwise). Enabling requires `acknowledged: true` (the client showed the warning) and a `duration_s` of 1 to 28800 seconds if given (400 otherwise). It is refused with 403 and a `code`: `agent_identity` (the request carries an agent's identity), `no_dashboard_provenance` (it is not a dashboard in a browser: the dashboard on this computer, or one opened from another device (a LAN address or the phone's QR link) after logging in; an API key, in a header or the URL, never qualifies, and neither does a request whose `Origin` or `Referer` names another page (so a reverse proxy in front of Coral that rewrites the `Host` header makes the dashboard behind it unable to turn auto-accept on); nor from the Cloud Hub) or `hub_auto_accept_disabled` (from the hub while the owner has turned that off). An asleep agent is 409. An agent type Coral cannot answer for is 409 and an unknown session 404. Turning it off is always allowed. A request counts as coming from the Cloud Hub only when Coral's own connector marked it (with a secret that exists only in Coral's memory), so a copied hub header does not unlock anything. The limits are honest ones: the dashboard check (the browser's `Sec-Fetch-Site`) stops ordinary clients and agent tools, not a deliberately hostile program on the same machine, and an agent with an unrestricted shell is outside what Coral can enforce (it could type into its own terminal). The point is that no Coral agent API or CLI path turns auto-accept on.

The live session always carries `auto_accept` (bool), `auto_accept_until` (unix seconds or null), `auto_accept_source` (`hub`, `local` or null), `auto_accept_supported` (bool), `auto_accept_unsupported_code` (`agent_type_unsupported` or null) and `auto_accept_paused_code` (`repeat_loop`, `rate_cap`, `expired`, `mode_lowered`, `hub_allow_off`, `link_removed`, `stopped_by_owner`, `coral_restarted`, or null). Prompt text and tool details are only in the agent's events.

## Sleep / Wake

Sleep suspends agent sessions (kills the process, preserves state). Wake relaunches them.

**Archived** is a sleeping agent put away on purpose (stored as `live_sessions.is_archived`; archived always implies sleeping). It stays asleep through Wake All and team Wake, does not count against the live-agent limit, is never assigned tasks (task create/edit/reassign naming it answers 409 `{error, code: "assignee_archived"}`; board status reports `availability: "archived"`) and is never nudged by the task queue. Waking it individually unarchives it; Unarchive returns it to plain sleeping without starting it.

Sleep, wake, archive and unarchive (like launch, restart and kill) push the change to `/ws/coral` at once instead of on the next tick.

**Dashboard:** selecting a sleeping agent shows its chat history (scrollable, older pages load as you scroll up); the composer reads "This agent is sleeping" with **Wake** and **Restart…** (the popout offers Wake only), the input is disabled and every send path is refused. The agent menu offers **Archive** (asks to confirm; a running agent is stopped first), shown only for servers whose rows carry `archived`. Archived agents leave the main list, team groups and counts and appear in one collapsed **Archived (N)** section at the bottom of the list, captioned by team ("No team" last). Their composer reads "This agent is archived" with **Unarchive** and **Wake** (no Restart).

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

Relaunches the board's sleeping agents, skipping archived ones, up to the live-agent limit.

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

#### POST `/api/sessions/live/team/{boardName}/kill`

Stops every live session on the board, captures each agent's changes, marks the
persisted team stopped, and removes a Coral-owned team worktree.

### Individual Session Sleep/Wake

#### POST `/api/sessions/live/{sessionID}/sleep`

```json
{"ok": true, "sleeping": true}
```

#### POST `/api/sessions/live/{sessionID}/wake`

Relaunches one sleeping agent and unpauses its board. Waking an archived agent also unarchives it.

```json
{"ok": true, "sleeping": false}
```

An unknown or awake session answers 200 `{"ok": false, "error": "Session not found" | "Session is not sleeping"}`; the live-agent limit answers 403.

#### POST `/api/sessions/live/{sessionID}/archive`

Archives an agent. A running agent is put to sleep first (its terminal is killed).

```json
{"ok": true, "sleeping": true, "archived": true}
```

#### POST `/api/sessions/live/{sessionID}/unarchive`

Returns an archived agent to plain sleeping; it does not start it (use wake).

```json
{"ok": true, "sleeping": true, "archived": false}
```

Both answer 200 `{"ok": false, "error": "Session not found"}` for an unknown session.

### Bulk Sleep/Wake

#### POST `/api/sessions/live/sleep-all`

```json
{"ok": true, "sessions_affected": 5, "sessions_killed": 5}
```

#### POST `/api/sessions/live/wake-all`

Wakes every sleeping agent except archived ones.

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
| 403 | Forbidden (auth required), or live-agent/demo limit reached |
| 404 | Session not found |
| 409 | Conflict (e.g. input sent to a sleeping or archived agent) |
| 500 | Internal server error |
| 503 | Agent metadata temporarily unavailable (`code: "agent_metadata_unavailable"`); retry |
