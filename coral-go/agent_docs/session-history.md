# Session History API

The Session History API provides access to past agent sessions — browsing, searching, viewing conversation logs, and managing notes and tags.

All endpoints are prefixed with `/api/sessions/history`.

---

## Listing & Search

### GET `/api/sessions/history`

Paginated, filterable list of historical sessions. Supports both agent sessions and group chats (message board projects).

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `page` | int | 1 | Page number |
| `page_size` | int | 50 | Results per page (max 200) |
| `q` | string | | Full-text search query |
| `fts_mode` | string | | FTS search mode |
| `type` | string | `all` | Filter by type: `all`, `agent`, or `group` |
| `tag_ids` | string | | Comma-separated tag IDs to filter by |
| `tag_logic` | string | | Tag matching logic (e.g., `and`, `or`) |
| `source_types` | string | | Comma-separated source types (e.g., `claude,gemini`) |
| `date_from` | string | | Start date filter (ISO format) |
| `date_to` | string | | End date filter (ISO format) |
| `min_duration_sec` | int | | Minimum session duration in seconds |
| `max_duration_sec` | int | | Maximum session duration in seconds |

**Response:**
```json
{
  "sessions": [
    {
      "session_id": "abc123-uuid",
      "source_type": "claude",
      "first_timestamp": "2025-01-15T10:00:00Z",
      "last_timestamp": "2025-01-15T11:30:00Z",
      "message_count": 42,
      "summary": "AI-generated summary of the session",
      "summary_title": "Short title",
      "has_notes": true,
      "tags": [{"id": 1, "name": "feature", "color": "#58a6ff"}],
      "branch": "feature/new-thing",
      "duration_sec": 5400,
      "type": "agent"
    },
    {
      "session_id": "board:my-team",
      "title": "my-team",
      "type": "group",
      "source_type": "board",
      "summary": "128 messages, 4 participants",
      "first_timestamp": "2025-01-14T09:00:00Z",
      "last_timestamp": "2025-01-15T11:00:00Z",
      "message_count": 128,
      "subscriber_count": 4,
      "participant_names": "architect, developer, reviewer",
      "tags": [],
      "has_notes": false
    }
  ],
  "total": 156,
  "page": 1,
  "page_size": 50
}
```

When `type` is `all`, agent and group sessions are merged and sorted by `last_timestamp` descending.

---

## Session Detail

### GET `/api/sessions/history/{sessionID}`

Full conversation messages for a historical session. Reads from the agent's JSONL log files.

**Response:**
```json
{
  "session_id": "abc123-uuid",
  "messages": [
    {
      "role": "user",
      "content": "Fix the bug in main.go",
      "timestamp": "2025-01-15T10:00:00Z"
    },
    {
      "role": "assistant",
      "content": "I'll look at main.go...",
      "timestamp": "2025-01-15T10:00:05Z"
    }
  ]
}
```

If the session is not found:
```json
{"error": "Session 'abc123' not found"}
```

---

## Notes

### GET `/api/sessions/history/{sessionID}/notes`

Get notes (user-edited and auto-generated summary) for a session.

**Response:**
```json
{
  "notes_md": "User-written notes in markdown",
  "auto_summary": "AI-generated session summary",
  "is_user_edited": true
}
```

When no summary exists yet:
```json
{
  "notes_md": "",
  "auto_summary": "",
  "is_user_edited": false,
  "summarizing": true
}
```

### PUT `/api/sessions/history/{sessionID}/notes`

Save user-edited notes for a session.

**Request Body:**
```json
{"notes_md": "My notes about this session..."}
```

**Response:**
```json
{"ok": true}
```

### GET `/api/sessions/history/{sessionID}/agent-notes`

Get agent-created notes (from the agent's notes feature, not user notes).

**Response:** Array of note objects.

---

## Summarization

### POST `/api/sessions/history/{sessionID}/resummarize`

Trigger re-summarization of a session (runs synchronously).

**Response:**
```json
{
  "ok": true,
  "auto_summary": "Updated AI-generated summary..."
}
```

---

## Tags

### GET `/api/sessions/history/{sessionID}/tags`

Get tags assigned to a session.

**Response:**
```json
[
  {"id": 1, "name": "feature", "color": "#58a6ff"},
  {"id": 2, "name": "bug-fix", "color": "#f85149"}
]
```

### POST `/api/sessions/history/{sessionID}/tags`

Add a tag to a session.

### DELETE `/api/sessions/history/{sessionID}/tags/{tagID}`

Remove a tag from a session.

> Note: Tag CRUD (create/list/delete tags themselves) is under `/api/tags` — see the Settings & System API docs.

---

## Git History

### GET `/api/sessions/history/{sessionID}/git`

Git commit snapshots captured during the session.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `limit` | int | 20 | Max number of commits to return |

**Response:**
```json
{
  "session_id": "abc123-uuid",
  "commits": [
    {
      "commit_hash": "abc1234",
      "branch": "feature/new-thing",
      "subject": "Add new feature",
      "timestamp": "2025-01-15T10:30:00Z"
    }
  ]
}
```

---

## Events & Tasks

### GET `/api/sessions/history/{sessionID}/events`

Events (tool calls, notifications) from a historical session.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `limit` | int | 200 | Max events to return |

**Response:** Array of event objects (same shape as live session events).

### GET `/api/sessions/history/{sessionID}/tasks`

Tasks from a historical session.

**Response:** Array of task objects (same shape as live session tasks).

### GET `/api/sessions/history/{sessionID}/files`

Returns files observed in agent edit events, normalized against the session's
latest recorded working directory.

```json
{"session_id":"abc123","files":[{"filepath":"src/app.go","status":"agent_only","agents":["worker"],"source":"agent_events"}]}
```

### GET `/api/sessions/history/{sessionID}/resume-info`

Returns the metadata available for resuming the session: `session_id` and, when
the session is still registered, `working_dir`, `agent_type`, `board_name`, and
`display_name`.
