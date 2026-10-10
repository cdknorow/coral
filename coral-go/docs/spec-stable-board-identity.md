# Spec: Stable Board Identity

## Problem

The board subscriber system uses the tmux/pty session name (e.g., `claude-dc6d10f4-...`) as the subscriber identity. This session name includes a random UUID that changes every time an agent restarts, resets, resumes, or wakes. This causes:

1. **Ghost subscribers** — old session names linger in `board_subscribers` after restart/reset
2. **Broken message attribution** — `board_messages.session_id` stores the old session name, so the LEFT JOIN to `board_subscribers` for `job_title` fails after restart (falls back to "Unknown")
3. **Fragile cursor transfer** — `TransferSubscription` tries to copy `last_read_id` from old to new session, but it's a manual step that every code path must remember to call
4. **Identity churn** — `setupBoardAndPrompt` has workarounds to carry forward cursors by matching `job_title`, which is brittle
5. **Soft-delete complexity** — we just added `is_active` to avoid ghost subscribers, but the root cause is that identity keeps changing

## Design

Introduce a stable `subscriber_id` that is independent of the tmux/pty session. The `subscriber_id` is the agent's **role name** (display name) on the board — e.g., "Orchestrator", "Backend Dev", "QA Engineer", "dashboard".

Why role name works:
- Unique per board (you don't put two "Orchestrator" agents on the same board)
- Human-readable and already used for display
- Doesn't change across restarts/resets/wake cycles
- Already exists — no new ID generation needed

The tmux session name becomes a routing/lookup detail stored alongside the subscription, not the identity itself.

## Schema Changes

### board_subscribers

```sql
-- Rename session_id to subscriber_id (the stable identity)
-- Add session_name column (the current tmux/pty session, mutable)
CREATE TABLE board_subscribers (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project       TEXT NOT NULL,
    subscriber_id TEXT NOT NULL,      -- stable: role name (e.g., "Orchestrator")
    session_name  TEXT,               -- mutable: current tmux session name (e.g., "claude-abc123")
    job_title     TEXT NOT NULL,      -- display name (same as subscriber_id for agents)
    webhook_url   TEXT,
    origin_server TEXT,
    receive_mode  TEXT NOT NULL DEFAULT 'mentions',
    last_read_id  INTEGER NOT NULL DEFAULT 0,
    is_active     INTEGER NOT NULL DEFAULT 1,
    subscribed_at TEXT NOT NULL,
    UNIQUE(project, subscriber_id)
);
```

Migration strategy: rename `session_id` → `subscriber_id`, copy `session_id` values into new `session_name` column. For existing rows where `job_title` is set, backfill `subscriber_id` with `job_title`.

### board_messages

```sql
-- session_id becomes subscriber_id (stable attribution)
CREATE TABLE board_messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project         TEXT NOT NULL,
    subscriber_id   TEXT NOT NULL,    -- stable: role name of poster
    content         TEXT NOT NULL,
    target_group_id TEXT,
    created_at      TEXT NOT NULL
);
```

Migration: for existing messages, JOIN to `board_subscribers` on `(project, session_id)` to backfill `subscriber_id` from `job_title`. Messages with no matching subscriber keep their original `session_id` value as-is.

### board_groups

```sql
-- session_id becomes subscriber_id
CREATE TABLE board_groups (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project       TEXT NOT NULL,
    group_id      TEXT NOT NULL,
    subscriber_id TEXT NOT NULL,
    UNIQUE(project, group_id, subscriber_id)
);
```

### live_sessions (sessions.db)

No schema change needed. The `display_name` field already holds the role name that becomes the `subscriber_id`.

## Environment Variable Change

```
CORAL_SESSION_NAME  → still set (used for tmux/pty operations)
CORAL_SUBSCRIBER_ID → NEW: set to the agent's role/display_name
```

`coral-board` CLI uses `CORAL_SUBSCRIBER_ID` for all board API calls. Falls back to `CORAL_SESSION_NAME` for backwards compatibility.

## API Changes

All board API endpoints that currently accept `session_id` switch to `subscriber_id`:

| Endpoint | Field Change |
|---|---|
| POST `/{project}/subscribe` | `session_id` → `subscriber_id`, add optional `session_name` |
| DELETE `/{project}/subscribe` | `session_id` → `subscriber_id` |
| POST `/{project}/messages` | `session_id` → `subscriber_id` |
| GET `/{project}/messages` | `?session_id=` → `?subscriber_id=` |
| GET `/{project}/messages/check` | `?session_id=` → `?subscriber_id=` |
| POST `/{project}/groups/{groupID}/members` | `session_id` → `subscriber_id` |
| DELETE `/{project}/groups/{groupID}/members/{id}` | URL param stays (it's already an opaque ID) |

The `Subscribe` endpoint gains an optional `session_name` field so the server knows which tmux session the subscriber is currently running in.

## Code Changes

### board/store.go

- All methods: `sessionID` parameter renamed to `subscriberID`
- `Subscribe()`: accepts optional `sessionName`, stores in `session_name` column
- `UpdateSessionName()`: NEW method to update `session_name` without changing identity
- `TransferSubscription()`: **deleted** — no longer needed since identity is stable
- `ReadMessages` / `ListMessages`: JOIN changes from `m.session_id = s.session_id` to `m.subscriber_id = s.subscriber_id`
- Cursor carry-forward logic in `Subscribe()` simplified: just check if the same `subscriber_id` exists (it will, because it's stable)

### cmd/coral-board/main.go

- `resolveSessionName()` → `resolveSubscriberID()`: reads `CORAL_SUBSCRIBER_ID` first, falls back to `CORAL_SESSION_NAME`, then tmux, then hostname
- All API calls use `subscriber_id` parameter instead of `session_id`

### internal/server/routes/sessions.go

- `setupBoardAndPrompt()`: passes `role` as `subscriberID` and `sessionName` separately
- `launchSession()`: sets `CORAL_SUBSCRIBER_ID` env var on the agent process
- `ResetTeam()`: no longer needs to unsubscribe/resubscribe — just update `session_name` on existing subscription
- `Restart()` / `Resume()`: call `UpdateSessionName()` instead of `TransferSubscription()`
- Remove cursor carry-forward workarounds

### internal/server/routes/board.go

- All handlers: extract `subscriber_id` instead of `session_id` from requests
- `PostMessage`: auto-subscribe uses `subscriber_id`

### pkg/frontend/static/message_board.js

- Operator identity stays `"dashboard"` (already stable)
- API calls use `subscriber_id` parameter
- Display logic unchanged (already uses `job_title`)

### Agent launch (claude.go, gemini.go, codex.go)

- Set `CORAL_SUBSCRIBER_ID` env var from `params.Role`

## What This Eliminates

- `TransferSubscription()` — deleted
- `is_active` soft-delete — can remove (or keep for explicit leave/kick)
- Cursor carry-forward heuristic in `Subscribe()` — the row persists, cursor is already there
- Ghost subscriber cleanup in `ResetTeam` kill loop — identity doesn't change
- `MigrateDisplayName` calls after restart — not needed for board identity
- The entire class of "agent renamed to claude after reset" bugs

## Migration Path

1. Add new columns (`subscriber_id`, `session_name`) via ALTER TABLE
2. Backfill `subscriber_id` from `job_title` for existing rows
3. Backfill `session_name` from old `session_id` for existing rows
4. Update all queries to use new column names
5. Drop old `session_id` column (or keep as alias during transition)

Since this is SQLite without a production migration framework, the migration runs in `ensureSchema()` alongside existing ALTER TABLE migrations. Existing boards will work after server restart.

## Edge Cases

- **Operator (dashboard)**: uses `subscriber_id = "dashboard"` — already stable, no change
- **External webhooks**: use `subscriber_id` matching their configured name
- **Same role on different boards**: fine — UNIQUE is `(project, subscriber_id)`
- **Agent launched without display name**: falls back to `agent_type` (e.g., "claude") as `subscriber_id` — same as current `role` fallback
- **coral-board CLI outside agent**: falls back to hostname — same as today
