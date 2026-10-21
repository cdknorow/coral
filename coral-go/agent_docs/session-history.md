# Session History API

The Session History API provides access to past agent sessions — browsing, searching, viewing conversation logs, and managing notes and tags.

All endpoints are prefixed with `/api/sessions/history`, except the live chat transcript (`/api/sessions/live/{name}/chat`, under Session Detail).

> **What an agent's token can read.** The list and the search (`GET /api/sessions/history`, `GET /api/sessions/history/search`) cover every session of every board, so they are the Operator's: an agent's session token gets 403 `forbidden_for_agent`. An agent searches its own history with `coral-agent history` (`/api/agent/history/…`, [below](#agent-session-history-search--context-post-compaction-recovery)). The reads about one session (`/api/sessions/history/{sessionID}`, `agent-notes`, `events`, `files`, `git`, `notes`, `resume-info`, `tags`, `tasks`) answer an agent only for its own session, its resume ancestors and the sessions on its own board; another session's gets 403 `forbidden_other_session`. Changing notes, tags or a summary is the Operator's too. The Operator, the dashboard and the command line are not limited.

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

### GET `/api/sessions/live/{name}/chat` (live agent transcript, paged)

The transcript of a live (or sleeping/archived) agent, read from its JSONL log. Unlike the history routes it lives under `/api/sessions/live`. Pass `session_id` and `agent_type` (and `working_directory` when known); missing ones are looked up from the live session.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `after` | int | 0 | `0` (or absent): a history page. `> 0`: a poll, returns `messages[after:]` (everything after the client's own message count) |
| `limit` | int | 100 | History page size |
| `before` | int | | Absolute message index (exclusive) the page ends at. Wins over `offset`. Non-integer or negative: 400 `{"error": "before must be a non-negative integer"}` |
| `offset` | int | 0 | Legacy paging: skip the newest N messages. Shifts when messages arrive between pages; prefer `before` |

**History page response** (`after` 0/absent; an empty transcript is a page with `start: 0`):
```json
{"messages": [...], "total": 842, "start": 742, "has_more": true, "limit_notice": ""}
```

- `start` is the absolute index of the first returned message; pass it as the next `before` to get the page above. `has_more` is `start > 0`.
- Gzip-compressed when the request sends `Accept-Encoding: gzip` and the body is at least 8 KB.

**Poll response** (`after > 0`): `{"messages": [...], "total": N, "limit_notice": ..., "needs_login": false, "login_reason": null}`, never compressed, no `start`/`has_more`. Both responses carry `needs_login` and `login_reason` (see sessions.md: the agent waits for a sign-in in its terminal).

```bash
# Newest 100 messages, then the 200 above them
curl -H "Authorization: Bearer $(coral api-key)" -s --compressed "http://localhost:PORT/api/sessions/live/NAME/chat?session_id=SID&agent_type=claude&after=0&limit=100"
curl -H "Authorization: Bearer $(coral api-key)" -s --compressed "http://localhost:PORT/api/sessions/live/NAME/chat?session_id=SID&agent_type=claude&limit=200&before=742"
```

**Message keys (#4510):** each message in a chat or history reply carries `key`, a stable 20-hex-character key for Save for later, and `find_key=KEY` answers `{found, index, total}` instead of a page: see [saved-messages.md](saved-messages.md#the-message-key).

**Dashboard behaviour:** the Chat view loads the newest 100 messages, then polls every second with `after=<count>`. Older pages (200 messages, `before=<start>`) load automatically as you scroll near the top; the **Load older messages** button at the top is the scroll sentinel and stays for find-in-chat and retrying a failed load. If a server or hub route refuses `before=` (400/403/404, or 502 `path_not_allowed`), the dashboard pages that server by `offset` instead. Sleeping and archived agents show the same scrollable history.

---

### GET `/api/sessions/live/{name}/attachment/{ref}` (a pasted image of the chat)

A user turn that carried a pasted image has a chip in the chat page: `attachments: [{name: "[Image #1]", file, ref}]`. Claude records the image
as an `image` content block and Codex as a data URL in the transcript; the reader gives the chip `ref` (32 hex characters: the hash of the image's
base64 text) and never the bytes. This route finds the image in the session's own transcript by that hash and serves it. Same query as the chat
(`agent_type`, `session_id`, `working_directory`) and the same caller rules. `400` for a `ref` that is not 32 lowercase hex characters, `404` when
the session has no transcript or none of its images has that hash, `413` for an image over 20 MiB, `415` unless the bytes are a PNG, JPEG, GIF or
WebP (SVG never, whatever the transcript's media type says). The reply is nosniff, inline, sandboxed by CSP and `private, no-cache`. Local only: never
through Coral Hub.

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

With `type`, `q`, `since`, `until`, `sort` or `cursor` it is a search with the live route's parameters and reply (`{events, total, next_cursor, ...}`), see "Searching the events" in sessions.md (#4779); `session_id` is not accepted there.

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

- **Caller Scope**: An agent's session token decides whose history it searches or reads (#4512): a `session_id` naming another session is refused with 403 `identity_mismatch`, and none means the token's own. Only the Operator (the dashboard, the command line, an authentic Coral Hub request) names a session; a caller with neither is refused with 401 `session_token_required`.
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

