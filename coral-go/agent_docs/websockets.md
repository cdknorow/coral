# WebSocket API

Coral provides two WebSocket endpoints for real-time streaming.

---

## `/ws/coral` — Session List Streaming

Streams the live session list with diff-based updates to minimize bandwidth.

### Connection

```
ws://localhost:PORT/ws/coral
```

No query parameters required for a dashboard.

Machine clients that hold a long-lived feed instead of showing a dashboard (a
Coral hub, the Coral Hub) should connect with `?feed=hub`:

```
ws://localhost:PORT/ws/coral?feed=hub
```

The session data is identical; the only difference is that a `feed=hub`
connection never receives, and never marks delivered, the `notifications`
field (see [Notifications](#notifications)). A feed without the marker counts
as a dashboard and would take a waiting notification away from the people it
was meant for.

### Message Flow

**First message** — full snapshot of all sessions, sent as soon as the
connection opens (not after the first poll tick):

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
      "archived": false,
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

- `changed`: Full session objects for sessions whose state changed. A session appears only when some field changed, and then with every field, so an unarchive carries `archived: false` explicitly.
  - **Field-level diffs (opt-in):** a dashboard that connects with `?diff=fields` gets, for a session it already has on this connection, only `"_partial": true`, the session's identity (`session_id`, `name`, `agent_type` and `server` when present), the fields that changed, and `null` for every field the session no longer has (never omitted: an omitted field keeps its old value). A session new to the connection still arrives whole. A client that gets a partial row for a session it no longer has must not show it alone; reload `GET /api/sessions/live` instead. Coral's own dashboard asks for this; `?feed=hub` always gets whole rows.
- `removed`: Session IDs (the agent name when there is none) that are no longer live.
- `active_runs`: Only sent when the active runs list changes.
- `notifications`: Only when this connection has undelivered notifications (dashboards only).

Sessions are ordered with archived agents last, as in
`GET /api/sessions/live`.

If nothing changed since the last poll, no diff is sent. Instead, when a
connection has received nothing for 15 seconds, the server sends a heartbeat:

```json
{"type": "coral_heartbeat"}
```

It carries no data and clients may ignore it. It comes from the same loop
that builds the session list, so a client holding a long-lived feed (such as
a hub) can use it to tell an idle server from one whose session list has
stopped updating.

### Poll Interval and Immediate Updates

The list is rebuilt every poll tick, configurable via `WSPollIntervalS` in
server config. Default: **5 seconds**.

State changes made through this server's API do not wait for the tick:
sleep, wake (including `sleep-all` / `wake-all`), archive, unarchive,
restart, resume, kill, launch, and team launch / kill / reset push a
`coral_diff` to every connection (dashboards and `feed=hub`) within about
50 ms. A burst of changes is coalesced into one rebuild. Remote-machine
changes on a hub rebuild within about a second; everything else (agent
activity, log status, notifications) arrives on the tick.

### Session Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `status` | string \| null | Human-readable status from log parsing, null when empty |
| `summary` | string \| null | Current goal/activity summary, null when empty |
| `display_name` | string | Custom name, `""` when unset |
| `staleness_seconds` | number | Seconds since last log activity, rounded down to the unit the dashboard shows (whole seconds under 1 min, whole minutes under 1 h, then whole hours) |
| `waiting_for_input` | bool | Agent is waiting for user input (notification event, or a sign-in) |
| `needs_login` | bool | The agent's CLI waits for the user to sign in, in its terminal (`waiting_kind` is `"login"`) |
| `done` | bool | Agent has stopped (stop event) |
| `working` | bool | Agent is actively running tools |
| `stuck` | bool | Reserved (currently always false) |
| `sleeping` | bool | Session is in sleep state |
| `archived` | bool | Agent is archived; present on every entry (`false` when not) |
| `changed_file_count` | int | Number of files changed by agent |
| `board_project` | string | Associated message board name |
| `board_unread` | int | Unread board messages |
| `branch` | string \| null | Current git branch, null when unknown |
| `repo_name` | string \| null | `owner/repo` parsed from the git remote, null when unknown |
| `git_state` | object \| null | Worktree/base-branch state: `is_worktree`, `base_branch`, `ahead`, `behind`, `dirty`. Null unless the agent is in a worktree or its branch has a known base |
| `capabilities` | object | The agent's tool permissions (`allow`/`deny` lists); absent when none were set |
| `context_window` | int \| null | Model context window in tokens, null when the model is unknown |
| `context_pct` | int \| null | Latest turn's context use as a whole percentage of `context_window` (0-100), null when unknown |

The six fields `branch`, `repo_name`, `git_state`, `capabilities`,
`context_window` and `context_pct` (and `status`, `summary`, `display_name`)
have the same names, types and values as in
`GET /api/sessions/live`, so a client following this feed does not need to
fetch the list as well. Some keys can be absent rather than null: entries for
sleeping sessions omit `git_state` and `repo_name`, and `capabilities` is
omitted when none were set. Clients should treat an absent key and `null` the
same.

### Notifications

UI notifications (`POST /api/notifications`, see the Notifications doc) are
delivered on `/ws/coral` as a `notifications` array on `coral_update` or
`coral_diff`:

- **Every dashboard gets every notification.** Each connection keeps its own
  cursor, so open tabs and windows do not race for them.
- A notification counts as delivered only after a write to some connection
  succeeded; a failed send does not lose it.
- A dashboard that connects later receives, with its first message, the
  notifications no dashboard has received yet from the last 5 minutes.
  Already-delivered ones are not replayed on reload.
- `feed=hub` connections never receive or acknowledge notifications.

---

## `/ws/terminal/{name}` — Terminal Streaming

Bidirectional WebSocket for real-time terminal interaction. Both PTY and tmux backends use the same unified streaming protocol — raw byte output delivered as binary WebSocket frames.

### Connection

```
ws://localhost:PORT/ws/terminal/{name}?agent_type=claude&session_id=<uuid>
```

`session_id` is required unless `{name}` is the exact terminal name (`claude-<uuid>`); without it the socket closes with 1008 `session_id is required` (a bare name could attach to another agent's terminal).

### Client → Server Messages (JSON text frames)

**Send terminal input:**
```json
{
  "type": "terminal_input",
  "data": "ls -la\n",
  "id": 42
}
```

`id` (optional, an integer) asks for an answer: `terminal_input_ack` with the same id once the input is delivered, or `terminal_gone` / `terminal_input_failed` with that id if it was not. Input without an id gets no answer (older clients). Only send ids after the server's `terminal_hello` says `input_acks: true`; an older server ignores them and never answers.

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

**Hello (JSON text frame):** sent right after the replay seed. `input_acks: true` means this server answers every `terminal_input` that carries an `id` (#3276):
```json
{ "type": "terminal_hello", "input_acks": true }
```

**Input acknowledged (JSON text frame):** the input with this `id` was delivered to the terminal. The dashboard shows "Sent" and clears the saved draft only on the acknowledgements of both frames of a composer send (the text, then the Enter); without a hello it falls back to waiting 1.2 s for a refusal.
```json
{ "type": "terminal_input_ack", "id": 42 }
```

**Input failed (JSON text frame):** the input with this `id` could not be delivered for another reason (the terminal still exists). Nothing was typed; keep the draft.
```json
{ "type": "terminal_input_failed", "id": 42, "error": "The input could not be sent to the agent's terminal.", "not_sent": true }
```

Through the Coral Hub, a read-only hub drops the input and answers `hub_read_only` (with the input's `id` when it echoes one); the dashboard counts that as a refusal of that input, or of every unanswered input when there is no id.

**Terminal closed (JSON text frame):**

Sent when the session ends, including when its tmux session is killed or disappears (checked about once a second; the stream ends within a couple of seconds), and at connect when the terminal is already gone. The socket then closes normally (1000, `session ended`); before #3276 a terminal gone at connect closed with 1011 `attach failed`:
```json
{
  "type": "terminal_closed"
}
```

**Terminal gone (JSON text frame):**

Sent in answer to a `terminal_input` (or `terminal_resize`) for a terminal that no longer exists. Nothing was typed anywhere; it is the socket's counterpart of the HTTP 409 `terminal_gone` from `/send` and `/keys`, and the client should treat it the same way (keep the user's draft, say so). `not_sent` is true for input, and `id` names the input when it carried one.
```json
{
  "type": "terminal_gone",
  "code": "terminal_gone",
  "error": "The agent's terminal is gone",
  "not_sent": true,
  "id": 42
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
