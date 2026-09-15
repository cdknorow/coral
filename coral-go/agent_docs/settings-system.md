# Settings & System API

System-level endpoints for settings, health checks, CLI tools, filesystem browsing, tags, network info, license management, authentication, and team operations.

## Settings

### Get All Settings

```
GET /api/settings
```

Returns all user settings as key-value pairs. Sensitive keys are filtered out.

**Response:**
```json
{
  "settings": {
    "key1": "value1",
    "key2": "value2"
  }
}
```

### Update Settings

```
PUT /api/settings
```

Upserts arbitrary key-value settings.

**Request Body:**
```json
{
  "setting_name": "value",
  "another_setting": true
}
```

**Response:** `{"ok": true}`

**Note:** Booleans are converted to Python-style `"True"` / `"False"` strings.

### Get Default Prompt Templates

```
GET /api/settings/default-prompts
```

Returns default prompt and system prompt templates for orchestrator and worker agents.

**Response:**
```json
{
  "default_prompt_orchestrator": "...",
  "default_prompt_worker": "...",
  "default_system_prompt_orchestrator": "...",
  "default_system_prompt_worker": "...",
  "team_reminder_orchestrator": "...",
  "team_reminder_worker": "..."
}
```

### Agent model suggestions

```
GET /api/agent-models
```

Returns the curated model-name suggestions keyed by agent type. These are UI
suggestions; launch requests may still provide a free-form model string.

---

## Health & Status

### API discovery and embedded docs

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/spec.json` | Machine-readable API metadata used by custom views. |
| `GET` | `/api/agent-docs` | List embedded documentation as `{name,title}` entries. |
| `GET` | `/api/agent-docs/{name}` | Return one document as JSON with `name`, `content`, and `path`. |
| `GET` | `/api/agent-docs/all` | Return all documents concatenated as Markdown. |

### Health Check

```
GET /api/health
```

Simple liveness check. Bypasses all middleware (no auth required). Polled by the native app every 5 seconds.

**Response:** `{"status": "ok"}`

### System Status

```
GET /api/system/status
```

Returns server startup status, version, and tier information.

**Response:**
```json
{
  "startup_complete": true,
  "version": "1.0.0",
  "store_url": "https://store.coralai.ai",
  "skip_license": false,
  "tier_name": "prod"
}
```

### Update Check

```
GET /api/system/update-check
```

Checks GitHub releases for available updates (5-second timeout).

**Response:**
```json
{
  "available": true,
  "current": "1.0.0",
  "latest": "1.1.0",
  "release_url": "https://github.com/cdknorow/coral/releases"
}
```

---

## CLI Check

```
GET /api/system/cli-check
```

Verifies whether a CLI tool is installed and returns its path and version.

| Parameter | Type | Description |
|-----------|------|-------------|
| `type` | string | Agent type to check (e.g., `"claude"`) |
| `binary` | string | Specific binary path to check |

One of `type` or `binary` must be provided.

**Response (found):**
```json
{
  "found": true,
  "path": "/usr/local/bin/claude",
  "version": "1.2.3",
  "agent_type": "claude"
}
```

**Response (not found):**
```json
{
  "found": false,
  "binary": "claude",
  "agent_type": "claude",
  "install_command": "npm install -g @anthropic-ai/claude-code"
}
```

---

## Filesystem Browser

```
GET /api/filesystem/list?path=~
```

Lists visible (non-hidden) directories within a path. Restricted to the home directory.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `path` | string | `~` | Directory path (supports `~` expansion) |

**Response:**
```json
{
  "path": "/Users/username",
  "entries": ["Documents", "Projects", "Desktop"]
}
```

### Check for a Git repository

```
GET /api/filesystem/is-git?path=/absolute/path
```

Returns `{"is_git":true}` when the path contains a `.git` entry. A missing or
invalid path returns `false`.

### Upload an image

```
POST /api/upload
```

Send multipart form field `file`. PNG, JPEG, GIF, WebP, BMP, and TIFF images up
to 20 MiB are stored under Coral's uploads directory. SVG is rejected. The
response contains `ok`, absolute `path`, original `filename`, and byte `size`.

---

## Tags

Tags can be applied to sessions and folders for organization.

### List Tags

```
GET /api/tags
```

**Response:**
```json
[
  {"id": 1, "name": "Important", "color": "#58a6ff"},
  {"id": 2, "name": "Bug Fix", "color": "#d1242f"}
]
```

### Create Tag

```
POST /api/tags
```

**Request Body:**
```json
{
  "name": "New Tag",
  "color": "#58a6ff"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Tag name |
| `color` | string | No | Hex color code (default: `"#58a6ff"`) |

**Response:** Created tag object.

**Errors:** `409` if tag already exists.

### Delete Tag

```
DELETE /api/tags/{tagID}
```

**Response:** `{"ok": true}`

### Add Tag to Session

```
POST /api/sessions/history/{sessionID}/tags
```

**Request Body:** `{"tag_id": 1}`

**Response:** `{"ok": true}` (idempotent)

### Remove Tag from Session

```
DELETE /api/sessions/history/{sessionID}/tags/{tagID}
```

**Response:** `{"ok": true}`

### Folder Tags

```
GET    /api/folder-tags                    # All folder-tag mappings
GET    /api/folder-tags/{folderName}       # Tags for a specific folder
POST   /api/folder-tags/{folderName}       # Add tag to folder (body: {"tag_id": 1})
DELETE /api/folder-tags/{folderName}/{tagID}  # Remove tag from folder
```

`GET /api/folder-tags` returns a map of folder names to tag arrays:
```json
{
  "folder1": [{"id": 1, "name": "Tag1", "color": "#58a6ff"}],
  "folder2": [...]
}
```

---

## Network & QR Code

### Network Information

```
GET /api/system/network-info
```

Returns local network addresses. Prefers RFC1918 private addresses as primary.

**Response:**
```json
{
  "ips": ["192.168.1.100", "10.0.0.50"],
  "primary": "192.168.1.100",
  "port": 8000
}
```

### QR Code Generator

```
GET /api/system/qr?url={url}
```

Returns a 256x256 PNG QR code image encoding the given URL.

- **Content-Type:** `image/png`
- **Cache-Control:** `public, max-age=3600`
- **Error correction:** Medium

---

## License & Activation

### Activate License

```
POST /api/license/activate
```

**Request Body:**
```json
{"license_key": "XXXX-XXXX-XXXX-XXXX"}
```

**Response (success):**
```json
{
  "valid": true,
  "customer_name": "John Doe",
  "customer_email": "john@example.com",
  "activated_at": "2024-03-01T10:00:00Z"
}
```

**Response (failure):**
```json
{"valid": false, "error": "Invalid license key"}
```

Locks the license to the machine via hardware fingerprinting. Backend: Lemon Squeezy API (10-second timeout).

### License Status

```
GET /api/license/status
```

**Response (licensed):**
```json
{
  "valid": true,
  "activated": true,
  "customer_name": "John Doe",
  "customer_email": "john@example.com",
  "product_name": "Coral",
  "activated_at": "2024-03-01T10:00:00Z",
  "last_validated": "2024-03-25T15:30:00Z",
  "machine_id": "abc123def456",
  "days_until_revalidation": 5,
  "trial_ends_at": "2024-04-01T00:00:00Z"
}
```

**Response (unlicensed):**
```json
{"valid": false, "activated": false}
```

Revalidation window: 7 days. Offline grace period: 30 days.

### Deactivate License

```
POST /api/license/deactivate
```

Removes the license file and frees the machine slot.

**Response:** `{"ok": true}` or `{"ok": false, "error": "..."}`

### License Webhook

```
POST /api/license/webhook
```

Receives Lemon Squeezy webhook notifications. Authenticated via HMAC-SHA256 signature (`X-Signature` header). Body size limit: 64 KB.

**Handled events:**
- `license_key.revoked` — Revokes license locally
- `subscription_expired` — Marks license invalid
- `subscription_cancelled` — Revokes immediately
- `subscription_created` / `subscription_updated` — Triggers revalidation

**Config:** Set webhook secret via `CORAL_LS_WEBHOOK_SECRET` environment variable.

---

## Authentication

### Auth Page

```
GET /auth
```

Returns the HTML page for API key entry.

### Validate API Key

```
POST /auth
POST /auth/key
```

**Request Body:** `{"key": "coral_sk_..."}`

**Response (success):** `{"ok": true}` (sets secure session cookie)

**Response (failure):** `{"error": "Invalid API key"}`

**Rate limited:** Returns `429` on excessive attempts.

### Get API Key

```
GET /api/system/api-key
```

Returns the current API key. **Localhost only** (returns `403` for remote requests).

**Response:** `{"key": "coral_sk_abc123..."}`

### Regenerate API Key

```
POST /api/system/api-key/regenerate
```

Generates a new random API key and saves to disk. **Localhost only.**

**Response:** `{"key": "coral_sk_newkey..."}`

### Auth Status

```
GET /api/system/auth-status
```

**Response:**
```json
{
  "authenticated": true,
  "method": "localhost"
}
```

**Possible methods:** `"localhost"`, `"key"`, `"session"`, `"none"`

---

## Indexer

### Refresh Index

```
POST /api/indexer/refresh
```

Manually triggers a session index refresh.

**Response:** `{"ok": true}` or `{"error": "Indexer not available"}`

---

## Teams

### Import Team from Folder

```
POST /api/teams/import
```

Imports a team definition from a folder of markdown files.

**Request Body:**
```json
{"path": "/path/to/team-directory"}
```

**Response:**
```json
{
  "name": "team-directory",
  "agents": [
    {
      "name": "Orchestrator",
      "role": "orchestrator",
      "prompt": "...",
      "description": "...",
      "tools": ["tool1"],
      "mcpServers": {}
    }
  ]
}
```

See [Import Team from Folder](../../docs/import-team-folder.md) for the expected directory structure and frontmatter format.

### Generate Team (Async)

```
POST /api/teams/generate
```

Uses Claude CLI to generate a team definition from a directive.

**Request Body:**
```json
{
  "directive": "Create a team to build a web application",
  "composition": "3-4 members with backend and frontend skills"
}
```

| Field | Type | Required | Max Length | Description |
|-------|------|----------|-----------|-------------|
| `directive` | string | Yes | 4000 | What the team should do |
| `composition` | string | No | 4000 | Team structure hints |

**Response:** `202 Accepted`
```json
{"job_id": "gen-12345-abcde-67890", "status": "pending"}
```

Timeout: 120 seconds. Requires Claude CLI to be installed.

### Get Generation Status

```
GET /api/teams/generate/{jobId}
```

**Response (pending):**
```json
{"job_id": "gen-...", "status": "pending"}
```

**Response (complete):**
```json
{
  "job_id": "gen-...",
  "status": "complete",
  "result": {
    "name": "Generated Team",
    "agents": [...],
    "flags": ""
  }
}
```

**Response (error):**
```json
{"job_id": "gen-...", "status": "error", "error": "..."}
```

Jobs expire after 10 minutes (`404` after expiry).

---

## Middleware

### License Middleware

When `config.LicenseRequired()` is true, gates all `/api/*` paths except:
- `/api/license/*`, `/api/health`, `/api/system/status`
- `/static/*`, `/`

Returns `403 Forbidden` if license is invalid.

### API Key Middleware

- Localhost connections bypass authentication
- Remote connections require a valid API key (header) or session cookie
- Ungated paths: `/api/license/*`, `/static/*`
