# Settings & System API

System-level endpoints for settings, health checks, CLI tools, filesystem browsing, tags, network info, authentication, and team operations.

## Settings

### Get All Settings

```
GET /api/settings
```

Returns all user settings as key-value pairs. Sensitive keys are filtered out.

**Access:** Operator only — an agent's token gets 403 `forbidden_for_agent`, and the route is not reachable through Coral Hub.

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

**Access:** Operator only — an agent's token gets 403 `forbidden_for_agent`, and the route is not reachable through Coral Hub. A value is a string, `true`/`false` or a whole number (`null` clears it); an array, an object or a fraction is refused with 400 and nothing is saved. Keys that have their own route (terminal backend, the Hub link's trust switches, Sign in with Coral Hub, the identity mode, Run as a hub) are refused here and named in the error.

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

**One registry (#5147):** every setting is declared once in `internal/settings` (`specs.go`): its type (on/off, whole number with its limits, one of a list, text), its default, whether a change applies live, at restart or only in the dashboard, and who writes it. `PUT /api/settings` checks a value against that declaration, and the server reads a setting only through its typed readers (`settings.Bool`, `Int`, `Enum`, `Text`, `Raw`), so the words for on and off (`true`, `1`, `yes`, `on` / `false`, `0`, `no`, `off`, any case), the default and the clamp are decided in one place. Two switches that widen exposure, `remote_access_enabled` and `hub_mode`, are on only for the word `true`. A test (`internal/settings/census_test.go`) fails on code that indexes a settings map directly instead of using the readers. To add a setting, declare it in `specs.go`; a key not declared there is refused by the PUT and panics in a reader.

**Settings with no control in the dashboard** (set them with this route):

| Key | Default | Effect |
|---|---|---|
| `auto_summarize` | off | `true` runs the batch summarizer over finished sessions. Read at startup: restart Coral after changing it. |
| `auto_goals` | on | `false` stops the short goal line Coral keeps for each live agent (and `POST /api/sessions/live/{name}/goal` answers 409). Read on every pass, so it applies within a minute. Each goal is one short call to the Claude Code CLI on the user's account (about 1,300 input tokens, Haiku): after the first goal an agent's goal is asked for again only when the user's requests changed, at most every 30 minutes (60 in a turn that never goes quiet), with a failure backoff of 30 minutes doubling to 4 hours. The sparkle refresh (`POST .../goal`) runs at once, at most once a minute per session. A boolean only (`true` or `false`). |
| `update_channel` | `approved` | Which releases the update notice, the tray and the Updates pane tell you about: `approved` (the release marked latest) or `agentic` (every published release, approved or not, plain `vX.Y.Z` tags only). Any other value is refused when saved and reads as `approved`. It changes the NOTICE only: the updater (Hub relaunch into a staged update) installs approved releases whatever this says. Applies on the next check. |
| `terminal_replay_bytes` | 262144 | How much recent terminal output a newly opened terminal is sent first (see websockets.md), clamped to 4 KiB to 16 MiB. Applies to terminals opened from then on. |

**CLI paths:** `cli_path_<type>` (Settings › CLI paths: `claude`, `codex`, `agy`,
`antigravity`, `gemini`, `pi`) is the program Coral runs for that agent type. The `gemini` type is Antigravity compatibility: it runs agy, with agy's flags and model list (`agy` and `gemini` share one path field in Settings), so a path that is the real Gemini CLI is refused when a launch or the CLI check reaches it, with a setup error naming the setting. A new value
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

### Custom models (Settings > Custom models, #5480)

```
GET /api/custom-models
PUT /api/custom-models   {"models": [{"family", "value", "display_name"?, "efforts"?}]}
```

Model ids the Operator adds per provider (`family`: `claude`, `codex`, `agy` (also antigravity and gemini) or `pi`), with the thinking levels each takes. Both routes are the Operator's (an agent token and Coral Hub are refused). The PUT replaces the whole set: it is checked as a whole, stored in one transaction, and an error (400) names the `row`, `family` and `field` and changes nothing. The GET answers `{models, limits}` (the PUT answers the same): `limits` holds the caps (50 per provider, 200 in all, 128 characters for an id, 80 for a name) and `efforts`, the levels each provider's CLI takes: Claude and agy `low`, `medium`, `high`, `xhigh`, `max` (`--effort`); Codex `minimal`, `low`, `medium`, `high`, `xhigh` (`-c model_reasoning_effort=...`); Pi none (its CLI is not checked yet), and a provider with no levels takes none. Gemini has none.

An id is a model id as above (never flag-shaped), is checked again whenever it is read, and reaches a CLI only through one function that checks it (a test holds that no code builds `--model <value>` itself). The customs are added at read time, after the CLI's own models, to both catalogs the pickers read: `GET /api/agent-models` (ids) and `GET /api/agents/models?type=` (each with `custom: true` and the levels the Operator set as `efforts`). A custom id that the CLI lists too keeps its place and takes the Operator's name and levels.

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

An agent reads them with `coral-agent docs [topic|all]`, which sends its session token; the routes need a credential like any other. The `path` in a document's reply (`agent_docs/<name>.md`) names the file in Coral's source repository, not a file on this computer: the docs are embedded in the program.

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

Checks GitHub releases for available updates (5-second timeout). The answer is cached for an hour (a failure for five minutes) and concurrent calls share one request, so repeated calls, an agent's included, make one request to GitHub. `?refresh=1` (the dashboard's "Check for Updates") asks again when the cached answer is over a minute old.

The channel is the `update_channel` setting (`approved` by default). On `approved` Coral asks for the release marked latest; on `agentic` it asks for the ten newest releases of the pinned repository and offers the highest plain `vX.Y.Z` (no `-rc`, `-beta`, `-dev` or `-forDropbox` tag, no draft), whether or not it is approved yet. A build that is already newer than what the channel offers is never told to move back. The two channels are cached separately.

**Response:**
```json
{
  "available": true,
  "current": "1.0.0",
  "latest": "1.1.0",
  "release_url": "https://github.com/subgentic/coral/releases",
  "channel": "approved",
  "approved": true
}
```

`channel` is `approved` or `agentic`. `approved` is false when the offered release has not been approved yet (it is still a pre-release); it is always true on the approved channel. On the agentic channel `release_url` is that release's own page.

### Update status (channel facts)

```
GET /api/system/update
```

The updater's status (it answers 404 while the update feature is off) also carries, additive and each only when known: `channel`; `approved` `{"version", "approved_at"}` (the newest approved release; `approved_at` is "" when its notes carry no `Approved: <time>` line, and is for display only: what is approved is decided by the release's pre-release flag and by `releases/latest`, never by that line); `past_approved: true` when the running build is newer than the newest approved release; and, on the agentic channel and never through Coral Hub, `waiting`: up to five `{"version", "published_at", "notes"}` of unapproved releases newer than the newest approved one, newest first, with the notes as plain text of at most 2000 characters (control and format characters removed). The route never waits for GitHub: until the first answer is cached only `channel` is present, and the next poll has the rest. Coral never approves anything: approving is the Operator's act on GitHub.

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

`binary` is the Operator's (the dashboard or the command line outside an agent): with an agent's token it returns 403 `operator_only`, and through Coral Hub it is never carried (the connector's query table does not list it, and the handler refuses it as well). A path must be the CLI path configured in Settings or a file named like the CLI's binary (for `type`'s CLI when `type` is given), and it is refused (400, nothing runs) when it or what it links to is in Coral's data folder, a live agent's working folder or a temp folder. Only live agents' folders are known to Coral: a path inside any other project folder is the Operator's own responsibility, the same as a CLI path typed in Settings. `type` alone needs no permission.

**Through Coral Hub** (for the New Agent dialog's check, #4579) `GET /api/system/cli-check?type=<agent type>` is carried at the Launch tier, never View, because it runs `<cli> --version` on the computer. The connector accepts only `type` (exactly one of `claude`, `codex`, `agy`, `antigravity`, `gemini`, `pi`: an unknown type would be answered as found) and `source=cli_recheck`; any other query key, a repeated key and a viewer below Launch mode are refused. The reply names nothing of the computer's account: no `path`, `binary` is the CLI's own name even when a configured path cannot be run (`configured: true` and the `message` say so without the path), and `version` is only the version number (`2.1.300`, not the CLI's whole first line). A local caller's reply is unchanged.

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

**Through Coral Hub** the home directory is shown as `~` and a folder under it as `~/Projects`, never as an absolute path (it would name the computer's account), and Browse asks for `~/Projects` when a folder is clicked: the connector accepts exactly `~` and `~/<relative path>` for `path` on this route and on `is-git` (the relative part is checked like any file reference: no `..`, no absolute path, no `~user`), and nowhere else. The folder must still lie inside the home directory or a project root after links are resolved and must not be a credential folder; anything else answers like a missing folder (#4527).

**What Browse and file reads refuse (#4531).** Whoever asks (the local dashboard too), a path is refused when any component matches the sensitive list in `internal/server/routes/file_bounds.go` (`sensitivePath`; case-insensitive, so `NTUSER.DAT` and `Library/Keychains` match on any OS). A refused folder is not listed, `is-git` answers `false` for it, and a refused file reads as missing. The list covers:

- **Credential folders:** `.ssh`, `.gnupg`, `.aws`, `.azure`, `.azure-devops`, `.gcloud`, `.config` (gh, gcloud and the rest), `.kube`, `.minikube`, `.helm`, `.docker`, `.oci`, `.ibmcloud`, `.bluemix`, `.aliyun`, `.vault`, `.pulumi`, `.cloudflared`, `.wrangler`, `.heroku`, `.stripe`, `.ngrok`, `.subversion`, `.nuget`, `.composer`, `.kaggle`, `.huggingface`, `.npm`, `.cargo`, `.m2`, `.gradle`, `.terraform`, and similar platform CLIs' folders.
- **Password managers, mail and keyrings:** `.password-store`, `.1password`, `.op`, `.bitwarden`, `.lastpass`, `.thunderbird`, `.mutt`, `.local/share/keyrings`, `.local/share/gnome-keyring`, `.local/share/kwalletd`, `.local/share/python_keyring`, and files `.kdbx`, `.kdb`, `.keychain`, `.keychain-db`.
- **Browser profiles:** `.mozilla`, `.cache/huggingface`, `snap/firefox|chromium|brave`, macOS `Library/Application Support`, `Library/Cookies`, `Library/Safari`, `Library/Keychains`, `Library/Mail`, `Library/Messages`, `Library/Containers`, `Library/Group Containers`, `Library/Preferences`, `Library/Accounts`, and Windows' `AppData/Roaming` and `AppData/LocalLow` whole, and the named folders of `AppData/Local` (`Google`, `Microsoft`, `Packages`, `Mozilla`, `BraveSoftware`, `Vivaldi`, `Opera Software`, `1Password`, `Comms`: browser profiles, credential stores, password managers); `AppData/Local` as a whole is not refused, since the temp folder (`AppData/Local/Temp`) and tools (`Programs`) live there.
- **Credential files by name:** `.env*`, `.netrc`/`_netrc`, `.npmrc`, `.pypirc`, `.pgpass`, `.git-credentials`, `.terraformrc`, `.vault-token`, `.databrickscfg`, `.boto`, `.dockercfg`, `.yarnrc`, `.condarc`, `.msmtprc`, `.authinfo`, `.hgrc`, `.wgetrc`, `.curlrc`, `kaggle.json`, `NTUSER.DAT`, SSH keys, and by extension `.pem`, `.key`, `.p12`, `.pfx`, `.p8`, `.ovpn`, `.tfvars`, `.tfstate`, `.jks`, `.jceks`, shell history.
- **Agent CLIs' state, in the home folder only:** `.claude`, `.codex`, `.gemini`, `.pi`, `.agy`, `.antigravity`, `.cursor`, `.continue`, `.codeium`, `.windsurf`, `.opencode`, `.qwen`, `.copilot`, `.augment`, `.goose`, `.aider` (a project's own `.claude` folder stays readable).
- **Coral's own data folder** (`~/.coral`, `CORAL_DATA_DIR`, or the `--home` folder the server runs with).

A folder with an ordinary name (`projects`, a checkout called `huggingface`) is not refused; add a new location to the lists and to `sensitive_path_audit_test.go`.

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

Send multipart form field `file`, and `session_id` (the agent the image is for). PNG, JPEG, GIF, WebP, BMP, TIFF and SVG
images up to 20 MiB are stored under Coral's uploads directory. A JPEG, PNG or GIF is saved without its metadata
(GPS, camera, thumbnail; no re-encoding; a JPEG keeps its Orientation tag so a portrait photo is not on its side).
HEIC is not converted or stripped: a file named `.heic` is refused as an unsupported type, and HEIC bytes that arrive under an
allowed name are saved for the agent unchanged, metadata included, and get no artifact. A WebP, TIFF or BMP is also saved for the
agent unchanged; the artifact step refuses a WebP or TIFF that carries metadata (`artifact_error`).
The response contains `ok`, absolute `path`, original `filename`, and byte `size`.

The Operator's only (an agent's token is refused). The image is also stored as an artifact on that agent's session
(`uploaded_by` owner, `source` paste), which Coral Hub serves through the existing `GET /api/artifacts/{id}`; the reply
then has `artifact` (`id`, `uri` as `coral://artifacts/<id>`, `media_type`, `size`, `name`, `inline`), and the id is the
SHA-256 of the file at `path`. Otherwise it has `artifact_error`, a sentence saying why, and the upload still succeeded:
SVG (kept for the agent only, never shared), a WebP or TIFF that carries metadata, a file that is not a recognised picture,
a `session_id` that is not a live session of this Coral, the store's size cap, the disk guard, or the dashboard's own hourly
window for these (100 a hour, 512 MiB, separate from every agent's). BMP and TIFF are artifacts that the chat shows as a
chip, not a thumbnail. The text the dashboard types into the agent for each image is `<path> coral://artifacts/<id>`.

**Who sees it.** An image cannot be redacted. Through Coral Hub every artifact, a pasted image included, is for this
computer's OWNER only (the account it is linked to; #5575): a request through the tunnel for an artifact, its metadata, its
comments or a list of them is answered `403 owner_only` unless the connector's stamp, the owner marker and the enrolled owner
label all match; a grantee, a view-only user and an agent get nothing. There is no switch for it and no notice before a paste:
the setting `hub_share_pasted_images` and `PUT /api/cloud/share-pasted-images` are gone, and a stored value is ignored. This
computer's dashboard and the command line are not affected.

```
DELETE /api/shared-images/{id}?session_id=<agent session>
```

Removes that session's shared copy of a pasted image (the Operator's, never through Coral Hub). The object itself goes only
when no other session or board lists it and it is not kept; the agent's own file is not touched. `404` for anything that is
not a pasted image of that session. A pasted image's record also follows `uploads_retention_days` (below), and a task result
cannot name one (it is the Operator's, not the agent's upload).

Uploads are kept for `uploads_retention_days` (default 30, 1-365): a background job
removes older ones hourly. The folder is capped at `uploads_max_mb` (default 1024,
50-102400): a new upload evicts the oldest uploads to fit, never one from the last hour;
if it still doesn't fit the answer is `507` with the reason.

### Delete an upload

```
GET /api/upload/{name}
```

Serves one uploaded image to the dashboard (the thumbnail on a chat image chip). `name` is the file name
(the last element of the upload's `path`), a base name only: `400` for a separator, a dot segment, a hidden or
device name, a folder or a link; `404` if it is gone. The type is what the bytes are: PNG, JPEG, GIF or WebP
(`415` for anything else, SVG included, whatever the name says). The reply is `X-Content-Type-Options: nosniff`,
`Content-Disposition: inline` with a plain file name, a sandboxing `Content-Security-Policy` and a `private, no-cache` header, up to
the 20 MiB upload cap. The Operator's (the dashboard, the command line); an agent's token is refused. Local
only: never through Coral Hub (the chip there keeps its name).

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

The Operator's only: an agent's token gets 403 `forbidden_for_agent` (the identity layer answers before the handler). Through Coral Hub it is Launch tier. The path must be an absolute folder that is not the file system's root, the home folder, a dot-folder or a sensitive location (`.ssh`, `.aws`, `.git` and the like). Only regular files are read, never a symlink, a dot-file or a sensitive name; each file is at most 256 KiB and `agents/` at most 50 files (400 otherwise).

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

### Install update (Windows)

```
POST /api/system/update/install
```

On Windows an update installs from its MSI, and Windows Installer asks the person at the computer (UAC) to allow it. So only that person starts it: the dashboard on this computer, or the signed command line, as the user Coral runs as. Never through Coral Hub (403; the route is not in the tunnel table), never an agent (its session token is refused with 403 `operator_only`), never another local user. The request has no body (`{}` is accepted): it names no version, path or URL, and the update is the one Coral staged and verified, which the updater verifies again.

What happens: the server writes a one-time marker (`<data>/updates/install-trigger.json`, private, good for 2 minutes), copies the installed `coral-update.exe` into `<data>/updates/apply` (hash-checked against the installed program, so a running updater never holds the install folder open), starts that copy as `apply` detached from Coral (no console, its own process group, out of Coral's job objects where the job allows breakaway, no inherited handle), and answers 202 `{"ok":true,"version":...,"quitting":true}`. The server then quits with exit code 5; the tray's supervisor takes 5 as "the updater has Coral" and ends itself and the dashboard window instead of starting the server again. `coral-update apply` consumes the marker first: run by hand, from a script or stale, it prints `not_requested` (or `request_expired`, `request_invalid`) and asks Windows nothing.

409 answers (the server keeps running, nothing changed): `unsupported_platform` (not Windows: those platforms apply an update without asking anyone), `install_unavailable` (this Coral is not under the tray), `nothing_staged`, `busy` (one is already under way); 500 `updater_not_started` when the copy or the start failed.

Coral Hub's relaunch (`POST /api/system/update/relaunch`, and the status's `relaunch` view) is refused on Windows with 409 `local_approval_needed`: it would leave Coral stopped behind a prompt nobody sees. macOS and Linux keep it. The apply itself stays off in releases (`applyEnabledWindows` in `cmd/coral-update/apply_switch.go`) until the coral-go-team has reviewed it.

### API Key Middleware

- Localhost connections bypass authentication
- Remote connections require a valid API key (header) or session cookie
- Ungated paths: `/static/*`
