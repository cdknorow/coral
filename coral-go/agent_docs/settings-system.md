# Settings & System API

System-level endpoints for settings, health checks, CLI tools, filesystem browsing, tags, network info, authentication, and team operations.

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

**Note:** Booleans are converted to Python-style `"True"` / `"False"` strings. Coral reads its
boolean settings in any case, so `true` and `"true"` mean the same.

**Settings with no control in the dashboard** (set them with this route):

| Key | Default | Effect |
|---|---|---|
| `auto_summarize` | off | `true` runs the batch summarizer over finished sessions. Read at startup: restart Coral after changing it. |
| `auto_goals` | on | `false` stops the short goal line Coral keeps for each live agent (and `POST /api/sessions/live/{name}/goal` answers 409). Read on every pass, so it applies within a minute. |
| `terminal_replay_bytes` | 262144 | How much recent terminal output a newly opened terminal is sent first (see websockets.md), clamped to 4 KiB to 16 MiB. Applies to terminals opened from then on. |

**CLI paths:** `cli_path_<type>` (Settings › CLI Paths: `claude`, `codex`, `agy`,
`antigravity`, `gemini`, `pi`) is the program Coral runs for that agent type. A new value
must be an absolute path to an executable regular file (a symlink to one is fine) with no
shell metacharacters (`' " \` $ ; & | < > * ? !`) or control characters; `""` clears it.
Otherwise the whole request is refused with `400` and the reason, and nothing in it is
saved. A value equal to the saved one is not checked again, so a CLI uninstalled since
doesn't block saving other settings.

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

### Models each agent CLI offers

```
GET  /api/agents/models?type=claude|codex|agy|antigravity|gemini|pi
POST /api/agents/models/refresh?type=...
```

The models the agent's own CLI lists for the user's login (Coral never asks for a vendor key):

| Type | Where the list comes from |
|---|---|
| Claude Code | the CLI's initialize reply (the `/model` picker's list); nothing else from that reply is kept |
| Codex | `codex debug models`, or the catalog shipped with codex when that fails |
| agy, antigravity, gemini | `agy models` |
| pi | `pi --list-models` |

The GET answers at once from a cache and never runs a CLI itself:
`{agent_type, models: [{value, display_name, description?, efforts?}], fetched_at, refreshing, source, error?}`.

- `value` is what to send as a launch's `model`.
- While the CLI hasn't listed its own models (or can't), `models` is the curated list above, with `source: "builtin"`.
- A missing or day-old list is fetched in the background: `refreshing: true` means ask again shortly.
- `error` is a short reason, never the CLI's output.

The refresh fetches now (202). It is the Operator's (not an agent token, not Coral Hub), and a second refresh of the same CLI within 30 seconds answers 429 `models_refresh_limit` (`X-Coral-Limit: action`). Lists are cached per CLI build (path, modification time, size), so updating a CLI refreshes its list.

A launch's `model` must be a model id: letters, digits and `. _ : / @ [ ] + -`, at most 128 characters, not starting with `-`. Anything else is answered 400.

The session list and `GET /api/sessions/live/{name}/info` carry `model`, the model the agent actually uses as its own transcript records it (Claude Code's assistant entries, Codex's `turn_context`), so it follows a `/model` switch: `model_source: "transcript"`. Until a turn has been read, and for agy, whose transcript doesn't name the model, it is the launch's model (`model_source: "launch"`).

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
| `binary` | string | A CLI name (`claude`, `codex`, `agy`, `pi`) or the absolute path of one |

One of `type` or `binary` must be provided.

`binary` is the Operator's (the dashboard or the command line outside an agent): with an agent's token, or through Coral Hub, it returns 403 `operator_only`. A path must be the CLI path configured in Settings or a file named like the CLI's binary (for `type`'s CLI when `type` is given), and it is refused (400, nothing runs) when it or what it links to is in Coral's data folder, a live agent's working folder or a temp folder. Only live agents' folders are known to Coral: a path inside any other project folder is the Operator's own responsibility, the same as a CLI path typed in Settings. `type` alone needs no permission.

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

Uploads are kept for `uploads_retention_days` (default 30, 1-365): a background job
removes older ones hourly. The folder is capped at `uploads_max_mb` (default 1024,
50-102400): a new upload evicts the oldest uploads to fit, never one from the last hour;
if it still doesn't fit the answer is `507` with the reason.

### Delete an upload

```
DELETE /api/upload/{name}
```

`name` is the file name (the last element of the upload's `path`). Only a regular file
directly in the uploads directory: `400` for anything else, `404` if it's gone. Local
only: never through Coral Hub.

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

Generates a new random API key and saves to disk; every session ends (browsers sign in again). **Localhost only.**

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

Imports a team definition from a folder of markdown files: `SKILL.md` (the orchestrator) and `agents/*.md`.

The Operator's only: an agent's token gets 403 `operator_only`. The path must be an absolute folder that is not the file system's root, the home folder, a dot-folder or a sensitive location (`.ssh`, `.aws`, `.git` and the like). Only regular files are read, never a symlink, a dot-file or a sensitive name; each file is at most 256 KiB and `agents/` at most 50 files (400 otherwise).

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
      "tools": ["tool1"]
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

### API Key Middleware

- Localhost connections bypass authentication
- Remote connections require a valid API key (header) or session cookie
- Ungated paths: `/static/*`
