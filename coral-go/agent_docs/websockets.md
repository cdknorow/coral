# WebSocket API

Coral provides two WebSocket endpoints for real-time streaming.

---

## `/ws/coral` — Session List Streaming

Streams the live session list with diff-based updates to minimize bandwidth.

### Connection

```
ws://localhost:8420/ws/coral
```

No query parameters required for a dashboard.

Machine clients that hold a long-lived feed instead of showing a dashboard (a
Coral hub, the Cloud Hub) should connect with `?feed=hub`:

```
ws://localhost:8420/ws/coral?feed=hub
```

The session data is identical; the only difference is that a `feed=hub`
connection never receives the `notifications` field. UI notifications
(`POST /api/notifications`) are delivered to every connected dashboard, and
one pushed while no dashboard is open waits for the next dashboard to
connect. A feed without the marker counts as a dashboard and would take such
a waiting notification away from the people it was meant for.

### Message Flow

**First message** — full snapshot of all sessions:

```json
{
  "type": "coral_update",
  "sessions": [
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
      "branch": "feature/x",
      "repo_name": "acme/widgets",
      "git_state": {"is_worktree": true, "base_branch": "main", "ahead": 2, "behind": 1, "dirty": 3},
      "capabilities": {"allow": ["Read", "Edit"], "deny": ["Bash"]},
      "context_window": 200000,
      "context_pct": 20
    }
  ],
  "active_runs": []
}
```

**Subsequent messages** — only changes since last update:

```json
{
  "type": "coral_diff",
  "changed": [
    { /* full session object for each changed session */ }
  ],
  "removed": ["session-id-1", "session-id-2"],
  "active_runs": [/* only included if runs changed */]
}
```

- `changed`: Full session objects for sessions whose state changed (no field-level diffs).
- `removed`: Session IDs that are no longer live.
- `active_runs`: Only sent when the active runs list changes.

If nothing changed since the last poll, no diff is sent. Instead, when a
connection has received nothing for 15 seconds, the server sends a heartbeat:

```json
{"type": "coral_heartbeat"}
```

It carries no data and clients may ignore it. It comes from the same loop
that builds the session list, so a client holding a long-lived feed (such as
a hub) can use it to tell an idle server from one whose session list has
stopped updating.

### Poll Interval

Configurable via `WSPollIntervalS` in server config. Default: **5 seconds**.

### Session Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | Human-readable status from log parsing |
| `summary` | string | Current activity summary |
| `staleness_seconds` | number | Seconds since last log activity, rounded down to the unit the dashboard shows (whole seconds under 1 min, whole minutes under 1 h, then whole hours) |
| `waiting_for_input` | bool | Agent is waiting for user input (notification event) |
| `done` | bool | Agent has stopped (stop event) |
| `working` | bool | Agent is actively running tools |
| `stuck` | bool | Reserved (currently always false) |
| `sleeping` | bool | Session is in sleep state |
| `changed_file_count` | int | Number of files changed by agent |
| `board_project` | string | Associated message board name |
| `board_unread` | int | Unread board messages |
| `branch` | string \| null | Current git branch, null when unknown |
| `repo_name` | string \| null | `owner/repo` parsed from the git remote, null when unknown |
| `git_state` | object \| null | Worktree/base-branch state: `is_worktree`, `base_branch`, `ahead`, `behind`, `dirty`. Null unless the agent is in a worktree or its branch has a known base |
| `capabilities` | object | The agent's tool permissions (`allow`/`deny` lists); absent when none were set |
| `context_window` | int \| null | Model context window in tokens, null when the model is unknown |
| `context_pct` | int \| null | Latest turn's context use as a whole percentage of `context_window` (0-100), null when unknown |

These six fields have the same names, types and values as in
`GET /api/sessions/live`, so a client following this feed does not need to
fetch the list as well. Some keys can be absent rather than null: entries for
sleeping sessions omit `git_state` and `repo_name`, and `capabilities` is
omitted when none were set. Clients should treat an absent key and `null` the
same.

---

## `/ws/terminal/{name}` — Terminal Streaming

Bidirectional WebSocket for real-time terminal interaction. Both PTY and tmux backends use the same unified streaming protocol — raw byte output delivered as binary WebSocket frames.

### Connection

```
ws://localhost:8420/ws/terminal/{name}
```

### Client → Server Messages (JSON text frames)

**Send terminal input:**
```json
{
  "type": "terminal_input",
  "data": "ls -la\n"
}
```

**Resize terminal:**
```json
{
  "type": "terminal_resize",
  "cols": 120,
  "rows": 30
}
```

### Server → Client Messages

**Stream data (binary frames):**

Raw terminal output bytes sent as binary WebSocket frames (`MessageBinary`). The client should set `ws.binaryType = 'arraybuffer'` and write directly to xterm.js:

```js
ws.onmessage = (event) => {
  if (event.data instanceof ArrayBuffer) {
    terminal.write(new Uint8Array(event.data));
  } else {
    // JSON control message
    const msg = JSON.parse(event.data);
    // handle terminal_closed, etc.
  }
};
```

On connect, the server sends an initial replay seed (last ~256 KiB of output, configurable via `terminal_replay_bytes` setting) as a binary frame, prefixed with clear codes (`\x1b[2J\x1b[3J\x1b[H`) to prevent scrollback duplication on reconnect.

**Terminal closed (JSON text frame):**

Sent when the session ends:
```json
{
  "type": "terminal_closed"
}
```

### Backend Behavior

Both backends implement the same `Attach`/`Replay` interface:

- **PTY backend** (Windows): In-process fan-out from PTY read loop. 256 KiB ring buffer for replay.
- **Tmux backend** (macOS/Linux): Tails the pipe-pane log file via fsnotify. Replay reads the last N bytes of the log file.

Multiple WebSocket clients can attach to the same session simultaneously. Input from any client is serialized to the terminal. Resize is last-writer-wins.

---

## Origin Validation

Both WebSocket endpoints validate the `Origin` header:

- **Localhost** connections are always allowed (`localhost`, `127.0.0.1`, `[::1]`).
- **Same-origin** requests (where Origin host matches the request Host) are allowed for remote access.
- The request's `Host` header is added as an allowed origin pattern for remote access scenarios.

---

## Authentication

WebSocket connections go through the same auth middleware as REST endpoints:

- **Localhost**: Auth bypassed.
- **Remote**: Requires API key (passed as a cookie or query parameter by the frontend).
