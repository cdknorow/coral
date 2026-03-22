# Coral-Subgent Integration — Handoff to Coral Team

**Date:** 2026-03-18
**Status:** Subgent-side deliverables complete. Ready for Coral-side wiring.

---

## What Was Delivered

### 1. `before_id` Pagination on `/messages/all` (Go)

**Files changed:**
- `internal/handler/messages_all.go` — accepts `before_id` query param
- `internal/store/postgres.go` — `ListMessages()` filters `WHERE m.id < $N` when `beforeID > 0`
- `internal/store/store.go` — updated `ListMessages` interface signature

**Usage for Coral dashboard:**
```
# First load — get newest 50 messages
GET /api/board/{project}/messages/all?limit=50

# Scroll up — get 50 messages before the oldest visible one
GET /api/board/{project}/messages/all?limit=50&before_id=123

# Keep paginating backwards
GET /api/board/{project}/messages/all?limit=50&before_id=74
```

- Results always ordered ascending (oldest first)
- `before_id=0` or omitted = no filter (existing behavior)
- Limit capped at 1000 server-side
- Auth: requires `read` scope, same as before

**Tests:** `tests/compat/test_before_id_pagination.py` (9 tests, requires live server)

---

### 2. `subgent_client.py` — Admin API Client (Python)

**File:** `coral/src/coral/messageboard/subgent_client.py`

**Functions:**

```python
# Create a scoped agent key
result = await create_agent_key(
    admin_url="http://subgent.io:8421",
    admin_key="cb_live_...",
    org_id="default",
    board="my-project",
    session_id="agent-1",
    job_title="Lead Developer",
    webhook_url=None,  # optional
)
# Returns: {"api_key": "cb_agent_...", "key_id": 42, ...}

# Revoke a key
await revoke_key(admin_url, admin_key, key_id=42)

# List all keys for an org
keys = await list_keys(admin_url, admin_key, org_id="default")
```

**Security:** SSRF validation on `admin_url` (scheme whitelist, DNS resolution, private IP blocking including IPv6-mapped IPv4). Admin key never logged.

**Dependency:** Uses `httpx` (already in Coral's deps).

**Tests:** `coral/tests/test_subgent_client.py` (28 tests)

---

### 3. `subgent_ws.py` — WebSocket Listener (Python)

**File:** `coral/src/coral/background_tasks/subgent_ws.py`

**Class:** `SubgentWSListener`

```python
listener = SubgentWSListener(session_store)

# Start listening to a board (one connection per board, not per agent)
await listener.start_board(
    subgent_url="http://subgent.io:8421",
    project="my-project",
    api_key="cb_agent_..."  # any valid key for this board
)

# Stop when last agent on board is killed
await listener.stop_board(subgent_url, project)

# Stop all on shutdown
await listener.stop_all()
```

**Behavior:**
- Connects to `ws(s)://{url}/api/board/ws/{project}`
- Authenticates with agent key, session_id `"coral-harness"`, `last_seen_id=-1` (skip backfill)
- On incoming messages, checks @mentions (`@all`, `@session_id`, `@job_title`)
- Sends tmux nudge to mentioned local agents
- Auto-reconnects with exponential backoff (1s → 60s max)
- Auth failures do NOT retry (bad credentials)

**Dependency:** `websockets>=12.0` (added to `coral/pyproject.toml`)

**Tests:** `coral/tests/test_subgent_ws.py` (29 tests)

---

## What the Coral Team Needs to Wire Up

### Phase 2a: `coral/src/coral/api/live_sessions.py`

In `launch_team` endpoint (~line 596), accept optional `subgent` config in request body:

```json
{
  "subgent": {
    "admin_url": "http://subgent.io:8421",
    "admin_key": "cb_live_...",
    "org_id": "default"
  }
}
```

When present:
1. Call `subgent_client.create_agent_key()` for each agent
2. Store `key_id` + plaintext key in `live_sessions` DB row
3. Pass `SUBGENT_API_KEY` and `SUBGENT_URL` as env vars to tmux
4. Call `listener.start_board()` for the board

### Phase 2b: `coral/src/coral/tools/session_manager.py`

In `setup_board_and_prompt` (~line 67), when Subgent mode:
- Skip `coral-board` subscription
- Use subgent prompt template (instructs agents to use `subgent` CLI instead of `coral-board`)

In tmux launch functions, inject env vars before agent command:
```bash
tmux send-keys -t {session} 'export SUBGENT_API_KEY=cb_agent_... SUBGENT_URL=http://...' Enter
```

### Phase 2d: `coral/src/coral/store/sessions.py`

Add columns to `live_sessions`:
- `subgent_key_id TEXT`
- `subgent_api_key TEXT`
- `subgent_admin_url TEXT`
- `subgent_admin_key TEXT`
- `subgent_org_id TEXT`

### Phase 3c: `coral/src/coral/web_server.py`

On startup:
- Create `SubgentWSListener` instance
- Check DB for existing Subgent-backed boards → start listeners
- Store instance for `launch_team` / session-kill handlers

### Phase 4: `coral/src/coral/background_tasks/board_notifier.py`

Skip sessions with `subgent_key_id` set (they get real-time WS notifications, not SQLite polling).

### Phase 6: Session Restart

On agent restart, re-read `subgent_api_key` from DB, re-inject env vars, use subgent prompt template.

### Phase 7: Cleanup on Session Kill

Call `subgent_client.revoke_key()` when session is killed. If last agent on board, call `listener.stop_board()`. Fail silently if Subgent unreachable.

---

## Testing API Key for Coral Team

To create an admin key for integration testing, run once the server is up:

```bash
# Create an admin-scoped key for the Coral team
curl -X POST http://subgent.io:8421/api/board/admin/keys \
  -H "Authorization: Bearer $SUBGENT_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"org_id": "default", "scopes": "admin,read,write", "label": "coral-integration-testing"}'
```

Or use the helper script:
```bash
SUBGENT_ADMIN_KEY=cb_live_... ./scripts/create-test-key.sh coral-integration --scopes=admin,read,write
```

The returned key can create/revoke agent keys and is suitable for the `subgent.admin_key` field in `launch_team` requests.

---

## Security Review Summary

All three deliverables passed security review (0 critical, 0 high):

| Deliverable | Finding | Status |
|---|---|---|
| before_id pagination | Limit cap (max 1000) | Fixed |
| subgent_client.py | IPv6-mapped IPv4 SSRF bypass | Fixed |
| subgent_client.py | DNS rebinding TOCTOU (low, admin-configured URL) | Accepted + documented |
| subgent_ws.py | Dead code (preview var) | Removed |
| subgent_ws.py | stop_all() bug (pop before collect) | Fixed |
| subgent_ws.py | api_key in dataclass repr | Fixed (repr=False) |
