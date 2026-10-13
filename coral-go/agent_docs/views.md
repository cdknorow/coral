# Views API

Custom views allow users to create additional dashboard tabs with custom HTML content and system prompts. Views are stored in the SQLite database.

**Reads are open; writes are dashboard-only.** `POST`, `PUT` and `DELETE` are accepted only from the Coral dashboard in a browser on this computer (loopback peer, localhost `Host`, same-origin fetch, no Cloud Hub tunnel, no `Authorization: Bearer`). Anything else, including an agent or script calling with curl, gets `403 {"error": "Custom views can only be changed from the Coral dashboard on this computer"}`. `POST` and `PUT` also need `Content-Type: application/json` (else 400).

A view runs in an isolated sandbox with no network and reads data only through a read-only endpoint allowlist. `created_via` records who wrote it: `"ui"` (the dashboard) or `"api"` (any other caller or unknown provenance). The dashboard shows an `"api"` view's source and runs it only after **Run this view**; open and save it in the editor to make it `"ui"`.

## Reading Coral data from a view

A view's HTML runs in a sandbox and reads Coral through `coral.get(path)` (or `fetch('/api/...')`), GET only. A view created in the dashboard may read the agent list (`/api/sessions/live`), an agent's detail, capture and info, files, git, notes, events, tasks and board messages; one created by an API caller gets the agent list, tasks, tags, board names and history only.

**Name the agent's session.** Per-agent reads that touch its terminal (`/api/sessions/live/{name}`, `/capture`, `/info`, `/poll`) need `?session_id=` (with `agent_type`), else they answer 400 `session_id_required`: a bare agent or folder name is shared by teammates and would read whichever terminal matched. Take both from `/api/sessions/live`:

```js
const agents = await coral.get('/api/sessions/live');
for (const a of agents) {
  const q = `?agent_type=${encodeURIComponent(a.agent_type)}&session_id=${encodeURIComponent(a.session_id)}`;
  const { capture } = await coral.get(`/api/sessions/live/${encodeURIComponent(a.name)}/capture${q}`);
}
```

## List Views

```
GET /api/views
```

Returns all custom views as a JSON array.

**Response:**
```json
[
  {
    "id": 1,
    "name": "Metrics Dashboard",
    "prompt": "You are a metrics assistant...",
    "html": "<div>...</div>",
    "tab_order": 0,
    "scope": "global",
    "created_via": "ui",
    "created_at": "2026-10-01T12:00:00.000000+00:00",
    "updated_at": "2026-10-01T12:00:00.000000+00:00"
  }
]
```

Returns `[]` when no views exist.

## Get View

```
GET /api/views/{id}
```

Returns a single view by ID.

**Parameters:**
| Name | In | Description |
|------|-----|-------------|
| `id` | path | Numeric view ID |

**Response:**
```json
{
  "id": 1,
  "name": "Metrics Dashboard",
  "prompt": "You are a metrics assistant...",
  "html": "<div>...</div>",
  "tab_order": 0,
  "scope": "global"
}
```

**Errors:**
- `400` — Invalid ID (not a number)
- `404` — View not found

## Create View

```
POST /api/views
```

Creates a new custom view (dashboard only, see above). Returns HTTP 201 on success.

**Request Body:**
```json
{
  "name": "Metrics Dashboard",
  "prompt": "You are a metrics assistant...",
  "html": "<div>Custom view content</div>",
  "tab_order": 0,
  "scope": "global"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Display name for the view tab |
| `prompt` | string | no | System prompt for AI interactions within this view |
| `html` | string | no | Custom HTML content for the view |
| `tab_order` | int | no | Sort order for the tab (default: 0) |
| `scope` | string | no | Scope of the view: `"global"` (default) |

**Response (201):** the stored view, with its real timestamps.
```json
{
  "id": 1,
  "name": "Metrics Dashboard",
  "prompt": "You are a metrics assistant...",
  "html": "<div>Custom view content</div>",
  "tab_order": 0,
  "scope": "global",
  "created_via": "ui",
  "created_at": "2026-10-01T12:00:00.000000+00:00",
  "updated_at": "2026-10-01T12:00:00.000000+00:00"
}
```

**Errors:**
- `400` — Invalid JSON, missing `name`, or not `application/json`
- `403` — Not from the local dashboard

## Update View

```
PUT /api/views/{id}
```

Updates an existing view (dashboard only). All fields are replaced, and `created_via` becomes `"ui"`.

**Parameters:**
| Name | In | Description |
|------|-----|-------------|
| `id` | path | Numeric view ID |

**Request Body:** Same structure as Create.

**Response:** the stored view (`created_at` is kept, `updated_at` is the update time).
```json
{
  "id": 1,
  "name": "Updated Dashboard",
  "prompt": "Updated prompt...",
  "html": "<div>Updated content</div>",
  "tab_order": 1,
  "scope": "global"
}
```

**Errors:**
- `400` — Invalid ID, invalid JSON, or not `application/json`
- `403` — Not from the local dashboard
- `404` — View not found

## Delete View

```
DELETE /api/views/{id}
```

Deletes a view (dashboard only).

**Parameters:**
| Name | In | Description |
|------|-----|-------------|
| `id` | path | Numeric view ID |

**Response:**
```json
{
  "ok": "true"
}
```

**Errors:**
- `400` — Invalid ID
- `403` — Not from the local dashboard
