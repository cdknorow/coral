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
| `source_types` | string | | Comma-separated source types (e.g., `claude,agy`) |
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

---

## Agent Session History Search & Context (Post-Compaction Recovery)

When an agent's context window is compacted or when resuming work across restarts, agents can search their own prior conversation history and proven ancestry (`resume_from_id` lineage) to recover facts, instructions, or configuration details previously conveyed by or to the user.

### Trust Boundary & Authorization

- **Caller Scope**: Endpoints require `session_id`, authenticated against active Coral agent sessions.
- **Lineage Boundary**: Search strictly defaults to the caller's own session plus its proven `resume_from_id` ancestor chain.
- **Cross-Agent Isolation**: Querying arbitrary session IDs outside the caller's proven ancestry is rejected with HTTP 403 Forbidden. Shared workspace fallback is forbidden.
- **Honest Availability**: When transcript files are not ready or missing, the API returns status `"partial"` or `"unavailable"` with diagnostic status per session rather than misleading zero results.

### GET `/api/agent/history/search`

Search messages across the caller's own session and its proven resumed ancestors.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `session_id` | string | required | Caller's authenticated Coral session ID |
| `query` / `q` | string | required | Literal keywords or quoted phrases (`"exact phrase"`) |
| `roles` | string | `assistant,user` | Comma-separated roles to search (`assistant`, `user`) |
| `include_ancestors` | bool | `true` | Include proven `resume_from_id` ancestor sessions |
| `target_session_id` | string | | Scope to a specific session in the caller's lineage |
| `limit` | int | 20 | Maximum matches to return (max 100) |
| `offset` | int | 0 | Pagination offset |
| `max_excerpt_chars`| int | 300 | Maximum excerpt length (50-1000 chars) |

**Response:**
```json
{
  "query": "port 8420",
  "status": "complete",
  "total_matches": 2,
  "has_more": false,
  "limit": 20,
  "offset": 0,
  "results": [
    {
      "session_id": "14c7245b-...",
      "is_current": true,
      "message_index": 14,
      "role": "assistant",
      "timestamp": "2026-09-30T10:00:00Z",
      "excerpt": "...I have confirmed the server starts on deployment port 8420...",
      "match_offsets": [[40, 49]],
      "match_terms": ["port", "8420"]
    }
  ],
  "sessions_searched": [
    {
      "session_id": "14c7245b-...",
      "role": "current",
      "status": "ready",
      "messages_searched": 28
    }
  ]
}
```

### GET `/api/agent/history/context`

Retrieve a sliding window of conversation turns surrounding a target turn so matched statements can be fully verified with surrounding context.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `session_id` | string | required | Caller's authenticated Coral session ID |
| `message_index` | int | required | 0-indexed turn position within the session |
| `target_session_id` | string | caller ID | Session ID containing the turn (must be in lineage) |
| `window` | int | 2 | Surrounding messages before and after (1-10) |

**Response:**
```json
{
  "session_id": "14c7245b-...",
  "target_index": 14,
  "total_messages": 28,
  "window": 2,
  "messages": [
    {
      "message_index": 12,
      "role": "user",
      "timestamp": "2026-09-30T09:58:00Z",
      "content": "What port should we bind?",
      "is_target": false
    },
    {
      "message_index": 14,
      "role": "assistant",
      "timestamp": "2026-09-30T10:00:00Z",
      "content": "I have confirmed the server starts on deployment port 8420.",
      "is_target": true
    }
  ]
}
```

---

## CLI Usage (`coral-agent`)

Coral agents can execute history searches directly from their environment without leaving their terminal.

```bash
# Search conversation history for facts or instructions
coral-agent history search "deployment port"

# Search only what you told the user (assistant messages)
coral-agent history search "database credentials" --roles assistant

# Limit results or paginate
coral-agent history search "migration" --limit 5 --offset 5

# Inspect surrounding context turns around turn #14
coral-agent history context 14

# Inspect context in an ancestor session
coral-agent history context <ancestor_session_id> 14

# Output raw JSON for scripting
coral-agent history search "api key" --json
```

