# Sessions API

The Sessions API manages live agent sessions — launching, monitoring, controlling, and terminating AI agents running in terminal sessions.

All endpoints are prefixed with `/api/sessions`.

New Codex launches default to `--sandbox workspace-write -a never` with tool networking enabled so Coral's hooks and commands can reach the local server. This keeps the file-write sandbox, which bypass did not; general outbound network is the known cost. A saved Settings permission mode takes precedence. To disable tool networking while keeping the autonomous workspace sandbox, pass `-c sandbox_workspace_write.network_access=false`.

---

## Session Listing & Discovery

### GET `/api/sessions/live`

List all live (running) agent sessions with enriched status. Archived agents are listed last.

**An agent's token gets a roster for other boards.** The row of the agent's own session and of the sessions on its own board is complete. A session on any other board appears with the roster fields only: `name`, `agent_type`, `session_id`, `tmux_session`, `display_name`, `icon`, `name_color`, `board_project`, `board_label`, `board_job_title`, `board_is_orchestrator`, `status`, `sleeping`, `archived`, `working`, `waiting_for_input`, `waiting_kind`, `awaiting_user`, `not_started`, `done`, `stuck`, `staleness_seconds`, `model`, `thinking`, `list_at`, `server`. It carries no prompt, first prompt, summary, waiting text, branch, working directory, log path or git state, and a field added later stays private until it is listed. The `/ws/coral` feed is reduced the same way, and the rows of a registered remote server are not listed to an agent. The Operator, the dashboard and the command line get every field.

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
    "needs_login": false,
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

Sleeping sessions (no active tmux) are included as placeholder entries with `"sleeping": true`, `"status": "Sleeping"` and `"staleness_seconds": null`. Every row carries `archived` (`true` only for an archived agent, which is always also `sleeping`; see [Sleep / Wake](#sleep--wake)). A sleeping row also carries `sleep_reason` when Coral, not the user, put the agent to sleep: `"terminal_gone"` when its terminal session no longer exists (killed, or its tmux server died). It is `""` for a sleep the user asked for and is cleared on wake. The live feed's rows carry it too.

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

`waiting_for_input` is true only while the agent has asked the user something directly, such as a permission or approval prompt, and is blocked on the answer, or while its CLI waits for the user to sign in (then `needs_login` is true and `waiting_kind` is `"login"`). It is not set by every notification: the idle reminder an agent sends after finishing a turn reports `done` instead. `waiting_reason` and `waiting_summary` are `null` unless a request is pending.

An Antigravity (agy) agent waits the same way (#4819), from its own signals: agy sends no hook for a permission prompt, so Coral records the request itself when the tool its PreToolUse hook named is still pending after 1.5 s and the terminal shows a prompt with options (the same `permission_request` event Claude's hook sends, which the tool's PostToolUse or the turn's end clears); its `ask_question` tool is recorded by the hook at once, as a question. A tool that is only slow records nothing.

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

**An agent's token reads its own session and the sessions on its own board, not another's.** The reads about one live session (`GET live/{name}` and its `capture`, `chat`, `attachment/{ref}`, `changes.diff`, `diff`, `events`, `events/counts`, `file-content`, `file-original`, `files`, `git`, `history/context`, `history/search`, `info`, `poll`, `resolve-path`, `search-files`, `subagents`) and `GET /api/sessions/{sessionID}/changes`, `changes.diff` and `status` answer 403 `forbidden_other_session` for a session on another board; `DELETE live/{name}/events` is limited the same way. Acting on a session (typing, keys, kill, restart, sleep, wake, goal, rename) is the Operator's, 403 `forbidden_for_agent`. Both limits apply in every mode. The Operator, the dashboard and the command line (no agent token) are not limited.

**Every per-agent route that reaches its terminal names the session.** `GET live/{name}`, `/capture`, `/poll`, `/info`, `/ws/terminal/{name}` (query `session_id`) and `/kill`, `/restart`, `/attach` (body `session_id`), `/resume` (body `current_session_id`) answer 400 `{"error": "session_id is required", "code": "session_id_required"}` without one (the WebSocket closes with 1008), unless `{name}` is the agent's exact terminal name (`claude-<uuid>`). A bare agent or folder name is shared by teammates and would reach whichever terminal matched it. Take `name` and `session_id` from `GET /api/sessions/live`.

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

`start` is the absolute index of the first message returned; pass it as the next `before` to get the page above. History pages are gzip-compressed when the request sends `Accept-Encoding: gzip`. A poll (`after>0`) returns `{messages, total, limit_notice, needs_login, login_reason}`.

`needs_login` is true while a Claude agent waits for the user to sign in: its transcript's latest message is the CLI's authentication error (invalid API key, expired or revoked OAuth, not logged in), or, before it has a transcript, its login screen is open. `login_reason` is the CLI's message, or `null`. The sign-in happens in the agent's terminal. `limit_notice` is the CLI's own usage-limit message (Claude's rate-limit error, Codex's "You've hit your usage limit"), `""` otherwise and always while `needs_login` is true.

**Needs-input fields (a request that has a session id, not an older-page `before` request).** Both a poll and a history page also carry what the dashboard shows for an open prompt, so it needs no separate poll:

| Field | Description |
|-------|-------------|
| `pending` | The tool call the agent started and has not finished (`tool_use_id`, `tool_name`, `input`, `at`), or `null`. Always present. |
| `prompt_options` | The prompt open on the terminal as `{"question", "options": [{"n", "label", "action"}], "review"}`, read from a pane capture. Present only when a prompt may be open: a tool is pending, the agent is Codex, Agy, Antigravity or Gemini (their prompts have no hook), the session's latest request-changing hook event is `permission_request` or `notification`, or no such event has been seen since the server started. It is read for the exact target only (a `session_id` that is not that terminal's gets none). |

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

**Automated input never answers a prompt.** Input from the dashboard (signed in, or this computer's browser), from Coral Hub at Interact, or from the operator's signed `coral` command line is a person typing and is sent at once. From any other caller (an agent's tool or session token, a script, an API key) it is automation: while the agent is waiting on a prompt for the user (a permission or selection menu, an approval question, a trust-this-folder or login screen, Claude Code's bug-report draft or its session survey), nothing is typed and Coral answers **202** `{"ok": true, "held": true}`; the text is typed once the prompt is answered. The same holds for `/keys`. Coral's own notifications, nudges and task messages are held the same way.

**Automated messages can wait for a Claude agent's turn to end** (gate phase 2, on by default; the Operator turns it off with the setting `gate_signals_claude` = `false`, which only the Operator can change (an agent's token can't change settings), and the kill switch is the server's environment variable `CORAL_GATE_MODE=screen`, set where Coral is started). With it on, Coral decides from the agent's own hook signals: while the agent is in a turn, routine board nudges and board messages, and text an unsigned script sends, wait, then go once when the turn ends; a nudge about a message that @-mentions the agent (its name or job title, a numbered name of its own, `@all` or `@notify-all`) goes at once, as before. A prompt or a menu on screen still holds everything. After 30 minutes in one turn, the screen decides again, so nothing waits on a turn for ever. What a person types in the dashboard or with the signed command line is never held. The agent's tooltip shows how many automated messages are held. Codex, Gemini and other CLIs keep the screen rule.

**A request with a `session_id` only ever reaches that session's own terminal.** If it is gone, Coral answers `terminal_gone` and sends nothing; it never falls back to another terminal with the same name, folder or a similar name (teammates often share a folder). The same holds for `/keys`, `/resize` and `/answer-prompt` (409 `terminal_gone`); restart and resume instead start the agent in a fresh terminal in its own working directory; kill, sleep and Kill All (they end only that session), board and task nudges, and reads such as `/prompt-options` and capture (an empty result). `/send`, `/keys`, `/resize` and `/answer-prompt` require `session_id` and `agent_type` (400 `session_id_required` otherwise), so input is never routed by name; only reads and internal callers without a `session_id` still match by name.

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -s -X POST http://localhost:PORT/api/sessions/live/NAME/send \
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

Send raw tmux key codes to the terminal (e.g., Enter, Ctrl-C). The same exact-target rules and errors as `/send` apply (`agent_type` and `session_id` required; 409 for a sleeping or archived agent), except that a metadata failure is a plain 503 `{error}`. `keys` must be a non-empty list (else 400). Each entry is a tmux key name: `Enter`, `Escape`, `Tab`, `BTab`, `BSpace`, `Space`, `Up`, `Down`, `Left`, `Right`, `Home`, `End`, `PageUp`, `PageDown`, `DC`, `IC`, `F1`-`F12`, `C-a`-`C-z` (Ctrl), `M-x` (Alt), or a single character or digits to type (`"1"`). The keys are sent one at a time, so `{"keys": ["Down"]}` then `{"keys": ["Enter"]}` (or both in one list) works on every backend. On the PTY backend (Windows, and wherever tmux is not used) an entry that is not one of these is refused with 400 and nothing is sent; tmux types a name it does not know as text.

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -s -X POST http://localhost:PORT/api/sessions/live/NAME/keys \
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
  "board_id": "b-7k3m9q2x4p",
  "board_server": "http://remote:PORT",
  "backend": "pty",
  "board_type": "default",
  "model": "claude-sonnet-4-20250514",
  "capabilities": ["tool_use"],
  "tools": ["Read", "Write"]
}
```

Required: `working_dir`. All other fields are optional.

**MCP servers:** Coral doesn't configure MCP servers for an agent. Set them up in the agent CLI's
own configuration. A request that includes `mcpServers` is refused with 400
(`mcpServers is not supported`).

**Boards (#3536):** `board_id` joins that existing board (the non-interactive way for scripts
and agents; `coral-agent launch --board <id>` sends it). `board_name` starts a **new** board
with that name; if a board with that id or name already exists, the answer is 409
`board_name_exists` with `candidates` (`board_id`, `board_label`, `created_at`, `members`).
Retry with `board_id` to join one, or add `"new_board": true` to start another with the same
name. Starting another board with a name that's taken is the Operator's choice (it makes that
name ambiguous for anyone using it): with strict local auth an agent's token gets 403
`forbidden_for_agent`, and otherwise the call is listed in the identity report. A new name follows the board name rule (400 `invalid_board_name`); an unknown
`board_id` is 404 `board_not_found`. The answer includes `board_id` and `board_label`.

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

Launch a team of agents sharing a message board. `board_name` starts a new board for the
team, `board_id` relaunches onto an existing one, and the same 409 `board_name_exists` choice
as `/api/sessions/launch` applies (see above).

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
      "tools": ["Read", "Grep"]
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
  "board": "b-7k3m9q2x4p",
  "board_id": "b-7k3m9q2x4p",
  "board_label": "my-team",
  "agents": [
    {"name": "architect", "session_id": "abc123", "session_name": "coral_claude_abc123"},
    {"name": "developer", "session_id": "def456", "session_name": "coral_claude_def456", "error": null}
  ]
}
```

**Outcomes.** A member that fails to start has `{"name": ..., "error": ...}` in `agents` instead of a session.

- Every member started: `200`, `"ok": true`.
- Some started and some failed: `200`, `"ok": true`, `"partial": true`, with `"started"` and `"failed"` counts; each failure is in its entry.
- None started: an error, not `ok: true` over an empty team. `422` when every failure is a setup problem (no CLI where the setting points or on PATH, no terminal backend, a working folder that is not there, a saved default model that is not a model ID), `500` otherwise, and `400` `no_named_agents` when no member has a name. The body is `{"ok": false, "code": "team_launch_failed", "error": "no agent in the team started (A: ...; B: ...)", "board": ..., "agents": [...each member's error...], "rolled_back": true}`. What the request made is taken back: its team record, its worktree and that worktree's branch (when this request created the branch), and the board when this request created it (a board that was already there is kept). A team template's knowledge files, working mode and presets are not taken back separately: they go with a board this request created, and stay on one that was already there.

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
  "thinking": "high",
  "capabilities": ["tool_use"]
}
```

`thinking` is the thinking (reasoning effort) level to restart with: omitted keeps the agent's stored level, `""` clears it (the CLI's own default), and a level the agent type does not take is a 400 with `"field": "thinking"`. Claude takes `low`, `medium`, `high`, `xhigh` and `max` (passed as `--effort`); other types take none yet.

**Response:**
```json
{"ok": true, "session_id": "new-uuid", "session_name": "coral_claude_newuuid"}
```

### POST `/api/sessions/live/{name}/model`

Switch a running agent's model and/or thinking level without restarting it (the operator's chat-bar chip). Operator only: an agent token is refused with 403 (`forbidden_for_agent` from the identity layer; the handler's own `operator_only` check is a second guard). Claude is the only type with a checked live switch; others get 409 `needs_restart` (restart with the new `model`/`thinking` instead).

```json
{"session_id": "abc123", "agent_type": "claude", "model": "claude-sonnet-5-5", "thinking": "high"}
```

At least one of `model` and `thinking` (`""` asks for the CLI's default, `/effort auto`). Coral types the CLI's own command (`/model <model>`, then `/effort <level>`) once the agent is between turns: a switch asked for mid-turn waits for the reply to end (`after_reply: true`). 202 with the switch state; a second switch while one is pending is 409 `switch_in_progress`. The agent's stored model and level change only when the transcript confirms: the `/model` entry Claude Code writes when it takes the command, and the next reply's `effort` for a level. The state is on the agent's row (the list, the feed and `info`) as `model_switch`: `{"state": "pending"|"done"|"failed", "model", "thinking", "after_reply", "message"}`, kept for two minutes after it ends. `message` is plain text from the CLI's own output (control characters removed, at most 300 characters): show it with `textContent` or `escapeHtml`, never as markup. A `/model` switch is confirmed only by the entry for the model asked for whose output says it was set; "Kept model as ..." and an entry for another model fail the switch with that text.

Rows also carry `thinking` (the level the agent was started with, when it has one) and `thinking_actual` (the level its newest turn ran at, from the CLI's transcript).

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
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -fsS http://localhost:PORT/api/sessions/SID/changes -o changes.diff
```

`GET /api/sessions/{sessionID}/changes` prefers a freshly generated live diff and
falls back to the saved artifact. `GET /api/sessions/{sessionID}/changes.diff`
serves only the artifact captured at termination. For a live session,
`GET /api/sessions/live/{name}/changes.diff?session_id={sessionID}` generates the
current checkout diff.

**What a stored or Coral Hub diff leaves out (#3555):** a diff Coral stores (a task's or a
session's `changes.diff`) and any diff sent through Coral Hub never carries a sensitive file
(the sensitive-path list: keys, `.env*`, credentials and the like), a dot-file or dot-folder,
a binary file, or a file whose section is over 256 KiB; each is replaced by a line such as
`# Coral: new file shot.png, 412000 bytes, not stored` (lines `git apply` skips). The whole
is cut at 8 MiB, with a note. Identical diffs are stored once. Through Coral Hub a diff has
no `X-Coral-Working-Directory` header, and a diff stored before this release is filtered the
same way when served. The live diff on this computer is unchanged.

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

Operator only: an agent's session token is refused (403 `forbidden_for_agent`). The dashboard, the signed command line and an API key may use it.

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

Operator only, like the display name above.

**Request Body:**
```json
{"session_id": "abc123", "icon": "🤖"}
```

Pass `"icon": null` to clear.

**Response:**
```json
{"ok": true, "icon": "🤖"}
```

### PUT `/api/sessions/live/{name}/name-color`

Set the colour a session's name is drawn in (`#rrggbb`; an empty `color` clears it). Operator only, like the display name above.

**Request Body:**
```json
{"session_id": "abc123", "color": "#3b82f6"}
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

#### Searching the events (#4745)

With any of `type`, `q`, `since`, `until`, `sort` or `cursor` the route is a search: the same parameter names and reply shape as the Team
Artifacts and board message searches, so one client bar serves every panel. Without them the reply is the plain array above (newest
`limit` events, at most 200; parameters it does not know are ignored). In a search an unknown or malformed parameter is a 400 naming it.

| Parameter | Values |
|---|---|
| `type` | Multi: an event type (`tool_use`, `prompt_submit`, `notification`, `stop`, `permission_request`, `auto_accept` ...), a lower-case word; comma separated or repeated, at most 20 |
| `q` | A plain, case-insensitive substring of the event's summary or tool name (not its detail): at most 200 characters, no control characters, given once; `%` and `_` are ordinary characters; an empty `q` is no search |
| `since`, `until` | RFC 3339 times; `since` inclusive, `until` exclusive, compared in the microsecond layout the events are stored in |
| `sort` | `newest` (default) or `oldest` |
| `limit` | 1 to 200, default 50 |
| `cursor` | A `next_cursor` or `prev_cursor` of an earlier reply, with the same filters, sort, agent and `session_id` |
| `session_id` | As in the plain list |

**Reply:** `{"events": [ ... ], "total": N, "limit": N, "next_cursor": "...", "prev_cursor": "..."}`: whole events (detail included) in the
sort's order; `total` is every match, whatever the page; `next_cursor` goes on, `prev_cursor` goes back (each present only when there is such
a page); `"truncated": true` says the search stopped at its bound (200,000 events or 64 MiB of text) and the total and pages are those of
what it read. Through Coral Hub the match is made on what the viewer sees (events are stored with tokens already redacted, and the connector
redacts the reply), and a `q` holding a token, key or password is refused with a 400.

A session's events in History take the same search: `GET /api/sessions/history/{sessionID}/events` with `type`, `q`, `since`, `until`, `sort`,
`limit` and `cursor` (#4779), the same reply, the same Coral Hub rules, over that one session's events (any agent name). It has no
`session_id` parameter (the path names it), and without a search parameter it is the plain array of the newest `limit` (at most 200).

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
- **Where:** on the Coral server, so it works with the dashboard closed and through the Coral Hub. Each approval is an `auto_accept` event (e.g. "Auto-approved Bash: npm test") in the agent's Activity.
- **Safety:** a prompt is answered once. The same prompt approved 5 times in a minute, or more than 30 approvals in a minute, turns auto-accept off (`auto_accept_paused`). Turning it on or off is itself an event (`auto_accept_enabled`, naming whether it came from the dashboard or the Coral Hub, and `auto_accept_disabled`), so every change shows in Activity.
- **Time limit:** optional when turned on in the dashboard (15 minutes to 8 hours, or none); from the Coral Hub always set (1 hour unless chosen, at most 8 hours).
- **Turned off automatically** (an `auto_accept_cleared` event says why): when the agent process starts again (relaunch, restart, resume, wake from sleep) or the time limit runs out. A dashboard-enabled auto-accept survives a Coral server restart while the same agent run is alive; one turned on from the Coral Hub also ends when Coral restarts, the Cloud link is removed or revoked, the hub's access drops below Launch, or the owner turns off the switch below.
- **From the Coral Hub:** needs Launch access on this computer (the owner's opt-in) and the local switch **Coral Hub can turn on auto-accept** (on unless the owner turned it off; shown while access is Launch, in the dashboard under Settings > Network and Hub, and in the Connect to Coral Hub screen. The owner of this computer changes it there; the Coral Hub owner, the account this computer is linked to, can also change it from Coral Hub (`POST /api/hub-auto-accept {"allow": true|false}`, hub.md); nobody else can; turning it off ends any hub-enabled auto-accept). The same switch lets the Coral Hub start an agent that skips permission prompts ([hub.md](hub.md)). Turning it on through the hub needs Launch; turning it off needs Interact.
- **Stop all:** the top-bar menu's **Stop all auto-accept**, or `coral auto-accept off --all`, turns it off for every agent at once.

```
POST /api/sessions/live/{session_id}/auto-accept/enable    {"session_id": "...", "acknowledged": true, "duration_s": 3600}
POST /api/sessions/live/{session_id}/auto-accept/disable   {"session_id": "..."}
POST /api/auto-accept/disable-all                          -> {"stopped": N}
```

These routes are for the dashboard (and the Coral Hub's dashboard); `{session_id}` may also be the agent's name, and it must name the same agent as the body's `session_id` (404 otherwise). Enabling requires `acknowledged: true` (the client showed the warning) and a `duration_s` of 1 to 28800 seconds if given (400 otherwise). It is refused with 403 and a `code`: `agent_identity` (the request carries an agent's identity), `no_dashboard_provenance` (it is not a dashboard in a browser: the dashboard on this computer, or one opened from another device (a LAN address or the phone's QR link) after logging in; an API key, in a header or the URL, never qualifies, and neither does a request whose `Origin` or `Referer` names another page (so a reverse proxy in front of Coral that rewrites the `Host` header makes the dashboard behind it unable to turn auto-accept on); nor from the Coral Hub) or `hub_auto_accept_disabled` (from the hub while the owner has turned that off). An asleep agent is 409. An agent type Coral cannot answer for is 409 and an unknown session 404. Turning it off is always allowed. A request counts as coming from the Coral Hub only when Coral's own connector marked it (with a secret that exists only in Coral's memory), so a copied hub header does not unlock anything. The limits are honest ones: the dashboard check (the browser's `Sec-Fetch-Site`) stops ordinary clients and agent tools, not a deliberately hostile program on the same machine, and an agent with an unrestricted shell is outside what Coral can enforce (it could type into its own terminal). The point is that no Coral agent API or CLI path turns auto-accept on.

The live session always carries `auto_accept` (bool), `auto_accept_until` (unix seconds or null), `auto_accept_source` (`hub`, `local` or null), `auto_accept_supported` (bool), `auto_accept_unsupported_code` (`agent_type_unsupported` or null) and `auto_accept_paused_code` (`repeat_loop`, `rate_cap`, `expired`, `mode_lowered`, `hub_allow_off`, `link_removed`, `stopped_by_owner`, `coral_restarted`, or null) and `auto_accept_paused_at` (unix seconds when Coral turned it off, set and cleared together with `auto_accept_paused_code`, or null). The code alone is resent on every connect, so a client that shows a pause once keys it by `paused_at`: a new pause has a new time. Prompt text and tool details are only in the agent's events.

## Sleep / Wake

Sleep suspends agent sessions (kills the process, preserves state). Wake relaunches them.

**An agent whose CLI exited** (it quit, crashed or was killed, leaving its terminal at a plain shell prompt) is marked sleeping after about a minute. From the moment its terminal shows a shell, Coral types nothing automated into it: no board nudges or notifications, task or UI notifications, scheduled prompts or auto-accept answers, because a shell would run that text as commands. Wake resumes the agent in the same terminal. A `terminal` session is a shell by design and is never marked. What you type yourself in the dashboard is always sent.

**Archived** is a sleeping agent put away on purpose (stored as `live_sessions.is_archived`; archived always implies sleeping). It stays asleep through Wake All and team Wake, does not count against the live-agent limit, is never assigned tasks (task create/edit/reassign naming it answers 409 `{error, code: "assignee_archived"}`; board status reports `availability: "archived"`) and is never nudged by the task queue. Waking it individually unarchives it; Unarchive returns it to plain sleeping without starting it.

Sleep, wake, archive and unarchive (like launch, restart and kill) push the change to `/ws/coral` at once instead of on the next tick.

**Dashboard:** selecting a sleeping agent shows its chat history (scrollable, older pages load as you scroll up); the composer reads "This agent is sleeping" with **Wake** and **Restart…** (the popout offers Wake only), the input is disabled and every send path is refused. The agent menu offers **Archive** (asks to confirm; a running agent is stopped first), shown only for servers whose rows carry `archived`. Archived agents leave the main list, team groups and counts and appear in one collapsed **Archived (N)** section at the bottom of the list, captioned by team ("No team" last). Their composer reads "This agent is archived" with **Unarchive** and **Wake** (no Restart). An agent asleep because its terminal is gone (`sleep_reason: "terminal_gone"`) reads **Terminal gone** in the list, stays in view (not folded into the sleeping row) with a **Terminal gone** chip that restarts it, and its composer reads "This agent's terminal is gone" with Restart… only (there is no terminal to wake)

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
| 403 | Forbidden (auth required) |
| 404 | Session not found |
| 409 | Conflict (e.g. input sent to a sleeping or archived agent) |
| 500 | Internal server error |
| 503 | Agent metadata temporarily unavailable (`code: "agent_metadata_unavailable"`); retry |

## Limits (#4602)

- **Terminal size.** `POST /api/sessions/live/{name}/resize` takes `columns` from **10 to 1000** (otherwise `400`), and a `terminal_resize` frame on the terminal socket is clamped to 10-1000 columns and 1-300 rows (no rows, or a negative number, is 50).
- **Pages.** A chat or history page is at most 500 messages (see [board.md](board.md#limits-and-refusals)).
- **Agents an agent may start.** `POST /api/sessions/launch` and `POST /api/sessions/launch-team` called with an agent's token are refused with `429` and `"code": "agent_limit"` (the reply has `live`, `max`, `message` and `not_started: true`, and the `X-Coral-Limit` marker, no `Retry-After`) when Coral already has that many live agents running (sleeping ones are not counted), or the launch would take it past: a launch-team counts every agent it names. The ceiling is the `agent_launch_limit` setting (default **64**, `0` for none; set it with `PUT /api/settings`). The Operator's dashboard and command line, and a person's session, are never refused: the person decides how many agents their machine runs. It is a ceiling against a runaway loop, not an exact cap: the check and the launch are not one step, so launches that arrive together can each pass just under it and overshoot by the number in flight. If the settings or the live count cannot be read, an unreadable setting means the default (64), never no ceiling, and a failed count lets the launch through.
