# Coral → Subgent API Update Plan

The Subgent server has been restructured with a new database schema, flat UUID routing, and new key types. This document covers every change the Coral codebase needs.

## Summary of Breaking Changes

| What Changed | Old | New |
|---|---|---|
| Board operations URL | `/api/board/{project-name}/messages` | `/api/board/{board-uuid}/messages` |
| WebSocket URL | `/api/board/ws/{project-name}` | `/api/board/ws/{board-uuid}` |
| Agent key creation | `POST /api/v1/admin/keys/agent` | `POST /api/board/admin/agent-keys` |
| Key listing | `GET /api/v1/admin/keys` | `GET /api/board/admin/keys` |
| Key revocation | `DELETE /api/v1/admin/keys/{key_id}` | `DELETE /api/board/admin/keys/{key_id}` |
| Agent key `board` field | `board: "project-name"` (string) | `board: "board-uuid"` (UUID) |
| Agent key response `key_id` | integer | UUID string |
| Org IDs | text string (`"default"`) | UUID |
| New required setup | None | Must create project + board first to get UUIDs |
| Agent key new field | N/A | `check_mode: "all\|mentions\|group\|none"` |
| Message new field | N/A | `target_group_id: "uuid"` (optional) |

---

## File-by-File Changes

### 1. `src/coral/messageboard/subgent_client.py`

**Priority: HIGH — all API calls go through this file**

#### Endpoint URL fixes

```python
# OLD
f"{base}/api/v1/admin/keys/agent"
f"{base}/api/v1/admin/keys/{key_id}"
f"{base}/api/v1/admin/keys"

# NEW
f"{base}/api/board/admin/agent-keys"
f"{base}/api/board/admin/keys/{key_id}"
f"{base}/api/board/admin/keys"
```

#### create_agent_key() signature change

The `board` parameter now requires a **board UUID**, not a project name string. The function needs to either:
- Accept a `board_id` (UUID) parameter directly, OR
- Accept project name + board name, resolve to UUID via API calls first

**Recommended approach:** Add a setup flow that creates/resolves project + board UUIDs before creating agent keys.

```python
# NEW functions needed:

async def ensure_project(admin_url: str, admin_key: str, project_name: str) -> str:
    """Create project if needed, return project UUID."""
    # GET /api/board/projects — find by name
    # If not found: POST /api/board/projects {"name": project_name}
    # Return UUID

async def ensure_board(admin_url: str, admin_key: str, project_id: str, board_name: str) -> str:
    """Create board if needed, return board UUID."""
    # GET /api/board/projects/{project_id}/boards — find by name
    # If not found: POST /api/board/projects/{project_id}/boards {"name": board_name}
    # Return UUID
```

#### create_agent_key() body changes

```python
# OLD body
{
    "org_id": org_id,
    "board": board_name,       # string name
    "session_id": session_id,
    "job_title": job_title,
    "scopes": "read,write",
    "ttl_hours": ttl_hours,
    "label": label,
    "webhook_url": webhook_url,
}

# NEW body
{
    "org_id": org_id,          # now a UUID
    "board": board_id,         # UUID, not name
    "session_id": session_id,
    "job_title": job_title,
    "scopes": "read,write",
    "ttl_hours": ttl_hours,
    "label": label,
    "webhook_url": webhook_url,
    "check_mode": check_mode,  # NEW: "all", "mentions", "group", or "none"
}
```

#### Response shape changes

```python
# OLD response
{"api_key": "cb_agent_...", "key_id": 42}          # key_id is int

# NEW response
{"key": "cb_agent_...", "id": "uuid-string"}        # id is UUID string
# Field names may differ — verify: "key" vs "api_key", "id" vs "key_id"
```

**Action:** Update response parsing. The `key_id` stored in the DB is now a UUID string, not an integer.

#### revoke_key() changes

```python
# key_id is now a UUID string, but the URL pattern is the same
# Just verify the URL path prefix change (/api/v1/ → /api/board/)
```

---

### 2. `src/coral/background_tasks/subgent_ws.py`

**Priority: HIGH — WebSocket URL changed**

#### WebSocket URL

```python
# OLD
ws_url = f"wss://{host}/api/board/ws/{project_name}"

# NEW
ws_url = f"wss://{host}/api/board/ws/{board_uuid}"
```

#### start_board() signature

```python
# OLD
async def start_board(self, ws_url, project, api_key, session_id, job_title)

# NEW — needs board_uuid instead of project name
async def start_board(self, ws_url, board_uuid, api_key, session_id, job_title)
```

#### Internal tracking

The listener tracks connections by `(ws_url, project)`. Change to track by `board_uuid` since project names are no longer in the URL.

#### Auth message — verify format

```python
# Current auth message
{"type": "auth", "token": api_key, "session_id": session_id, "last_seen_id": 0}

# Verify this still works with the new server. The WS handler may expect
# different field names or additional fields.
```

---

### 3. `src/coral/api/board_proxy.py`

**Priority: HIGH — proxy URLs changed**

#### All proxy endpoints change from project name to board UUID

```python
# OLD
GET  /api/board-proxy/{project}/messages/all  → GET  {base}/api/board/{project}/messages/all
POST /api/board-proxy/{project}/messages      → POST {base}/api/board/{project}/messages
GET  /api/board-proxy/{project}/subscribers   → GET  {base}/api/board/{project}/subscribers

# NEW
GET  /api/board-proxy/{boardID}/messages/all  → GET  {base}/api/board/{boardID}/messages/all
POST /api/board-proxy/{boardID}/messages      → POST {base}/api/board/{boardID}/messages
GET  /api/board-proxy/{boardID}/subscribers   → GET  {base}/api/board/{boardID}/subscribers
```

The proxy needs to receive board UUIDs from the Coral frontend, not project names.

---

### 4. `src/coral/api/live_sessions.py`

**Priority: HIGH — launch_team flow changes**

#### launch_team() changes

The launch flow currently:
1. Creates agent keys with `board=project_name` (string)
2. Stores `subgent_key_id` as integer

New flow must:
1. **Create/resolve project UUID** via `ensure_project()`
2. **Create/resolve board UUID** via `ensure_board()`
3. Create agent keys with `board=board_uuid` (UUID)
4. Pass `check_mode` parameter (default: `"all"`)
5. Store `subgent_key_id` as UUID string (not int)
6. Store `board_uuid` for WS listener startup

```python
# NEW launch flow (pseudocode):
subgent_cfg = body.get("subgent", {})
project_id = await ensure_project(admin_url, admin_key, board_name)
board_id = await ensure_board(admin_url, admin_key, project_id, board_name)

for agent in agents:
    key_data = await create_agent_key(
        admin_url, admin_key, org_id,
        board_id=board_id,           # UUID, not name
        session_id=session_id,
        job_title=agent["job_title"],
        check_mode="all",            # NEW parameter
    )
    # key_data["id"] is UUID string, not int
    # key_data["key"] is the API key
```

#### WebSocket listener startup

```python
# OLD
await ws_listener.start_board(ws_url, project_name, api_key, session_id, job_title)

# NEW
await ws_listener.start_board(ws_url, board_uuid, api_key, session_id, job_title)
```

---

### 5. `src/coral/tools/session_manager.py`

**Priority: MEDIUM**

#### setup_board_and_prompt()

The board state file and env vars may need updating:
- `SUBGENT_URL` stays the same
- `SUBGENT_API_KEY` stays the same
- Consider adding `SUBGENT_BOARD_ID` env var so the CLI can skip name resolution

#### Board state file

If the state file includes a `project` field, consider adding `board_id` (UUID) so the CLI can use it directly.

---

### 6. `src/coral/api/system.py`

**Priority: MEDIUM**

#### Subgent config storage

The `org_id` setting was a text string (e.g., `"default"`). It's now a UUID. The test connectivity endpoint (`POST /api/settings/subgent/test`) should be updated to handle UUID org IDs.

#### Test connectivity

```python
# Update the test endpoint to call the new API paths
# OLD: GET {base}/api/v1/admin/keys
# NEW: GET {base}/api/board/admin/keys
```

---

### 7. `src/coral/background_tasks/board_notifier.py`

**Priority: LOW — no URL changes needed**

The notifier skips Subgent-backed sessions (checks `subgent_key_id IS NOT NULL`). Since `subgent_key_id` is now a UUID string instead of an integer, verify the `IS NOT NULL` check still works (it should — SQLite doesn't enforce types).

---

### 8. `src/coral/web_server.py`

**Priority: MEDIUM**

#### Deferred startup — WS listener

The startup code queries `get_subgent_live_sessions()` and starts WS listeners. The WS URL construction needs updating:

```python
# OLD
ws_url = f"wss://{host}/api/board/ws/{project_name}"

# NEW — needs board_uuid from the session record
ws_url = f"wss://{host}/api/board/ws/{board_uuid}"
```

This means `get_subgent_live_sessions()` must return the board UUID. Either:
- Store `board_uuid` in the live_sessions table, OR
- Resolve it from the project name on startup (extra API call)

**Recommendation:** Store `board_uuid` in the live_sessions table (new column: `subgent_board_id TEXT`).

---

### 9. `src/coral/store/connection.py`

**Priority: MEDIUM**

#### Schema update

Add column to live_sessions table:

```python
# Add to migration/schema
"subgent_board_id TEXT"   # Board UUID for WS connections and API calls
```

---

### 10. `src/coral/store/sessions.py`

**Priority: LOW**

#### get_subgent_live_sessions()

Include `subgent_board_id` in the SELECT query so it's available for WS listener startup.

---

### 11. `src/coral/background_tasks/remote_board_poller.py`

**Priority: LOW**

If remote boards point to Subgent servers, the URL pattern needs updating:

```python
# OLD
f"{remote_server}/api/board/{project}/messages/check?session_id=..."

# NEW
f"{remote_server}/api/board/{board_uuid}/messages/check?session_id=..."
```

---

## New Setup Flow

Before any agent keys can be created, the Coral team launch flow must:

```
1. GET /api/board/projects → find existing project by name
2. If not found: POST /api/board/projects {"name": "my-project"} → get project UUID
3. GET /api/board/projects/{project-uuid}/boards → find existing board by name
4. If not found: POST /api/board/projects/{project-uuid}/boards {"name": "general"} → get board UUID
5. Now create agent keys with board=board-uuid
6. Store board-uuid for WS connections and API calls
```

This replaces the old flow where the project string was used directly in URLs.

---

## Database Column Changes (Coral SQLite)

| Column | Old Type | New Type | Notes |
|---|---|---|---|
| subgent_key_id | TEXT (int as string) | TEXT (UUID) | Key ID is now UUID |
| subgent_board_id | N/A (new) | TEXT (UUID) | Board UUID for API calls + WS |
| subgent_org_id | TEXT ("default") | TEXT (UUID) | Org is now UUID |

---

## Migration Checklist

- [ ] Update `subgent_client.py` — endpoint URLs, request/response shapes, add ensure_project/ensure_board
- [ ] Update `subgent_ws.py` — WS URL uses board UUID, tracking by board UUID
- [ ] Update `board_proxy.py` — proxy URLs use board UUID
- [ ] Update `live_sessions.py` — launch_team creates project+board first, passes check_mode, stores board_uuid
- [ ] Update `session_manager.py` — board state includes board_uuid
- [ ] Update `system.py` — test connectivity uses new API paths
- [ ] Update `web_server.py` — WS listener startup uses board_uuid
- [ ] Update `connection.py` — add subgent_board_id column
- [ ] Update `sessions.py` — include subgent_board_id in queries
- [ ] Update `remote_board_poller.py` — URL pattern if pointing at Subgent
- [ ] Update `board_notifier.py` — verify IS NOT NULL check works with UUID strings
- [ ] Test end-to-end: launch_team → agents post/read → WS notifications
