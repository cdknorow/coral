# Coral Public API Specification

## Overview

The Coral server exposes a REST + WebSocket API that powers the dashboard.
Third-party UIs, CLI tools, and automations can use this API to build on top
of Coral without using the bundled frontend.

## Authentication

All API requests require authentication unless the endpoint is explicitly
ungated (health, static assets).

### Localhost
Requests from `127.0.0.1` / `::1` bypass authentication entirely.

### Remote Access
Include the API key in the `X-API-Key` header:
```
X-API-Key: <your-api-key>
```

Get your API key from `GET /api/system/api-key` or the settings UI.

## Base URL

```
http://localhost:8420
```

Default port is 8420. Configurable with `--port`.

## Response Format

All API responses are JSON. Errors return:
```json
{"error": "description of what went wrong"}
```

---

## Core API Reference

### Health Check

```
GET /api/health
```
Returns `{"status": "ok"}` when the server is running. No auth required.

---

### Live Sessions

#### List all live sessions
```
GET /api/sessions/live
```
Returns array of all running agent sessions with metadata (name, status,
working directory, agent type, board membership, etc.).

#### Get session detail
```
GET /api/sessions/live/{name}
```
Returns detailed info for a specific session.

#### Launch a single agent
```
POST /api/sessions/launch
Content-Type: application/json

{
  "agent_type": "claude",
  "working_dir": "/path/to/project",
  "prompt": "Your task instructions here",
  "flags": ["--flag1", "--flag2"]
}
```

#### Launch a team of agents
```
POST /api/sessions/launch-team
Content-Type: application/json

{
  "board_name": "my-project",
  "working_dir": "/path/to/project",
  "agent_type": "claude",
  "agents": [
    {"name": "Lead Developer", "prompt": "You are the lead developer..."},
    {"name": "QA Engineer", "prompt": "You are the QA engineer..."}
  ]
}
```

#### Send input to terminal
```
POST /api/sessions/live/{name}/send
Content-Type: application/json

{"input": "your command here"}
```

#### Kill a session
```
POST /api/sessions/live/{name}/kill
Content-Type: application/json

{"agent_type": "claude", "session_id": "uuid"}
```

#### Restart a session
```
POST /api/sessions/live/{name}/restart
Content-Type: application/json

{"agent_type": "claude", "session_id": "uuid"}
```

#### Get terminal output
```
GET /api/sessions/live/{name}/capture?agent_type=claude&session_id=uuid
```
Returns the current terminal pane content. `session_id` is required (400
`session_id_required`) unless `{name}` is the exact terminal name.

#### Batch poll (capture + tasks + events)
```
GET /api/sessions/live/{name}/poll?agent_type=claude&session_id=uuid
```
Returns capture, tasks, and events in a single response. Use this instead
of calling each endpoint separately.

#### Get changed files
```
GET /api/sessions/live/{name}/files?session_id=uuid
```
Returns list of files changed by the agent (git diff).

#### Get file content
```
GET /api/sessions/live/{name}/file-content?filepath=path/to/file&session_id=uuid
```

#### Get git diff
```
GET /api/sessions/live/{name}/diff?session_id=uuid
```

#### Search files in repo
```
GET /api/sessions/live/{name}/search-files?q=query&session_id=uuid
```

#### Set display name (Operator only)
```
PUT /api/sessions/live/{name}/display-name
Content-Type: application/json

{"display_name": "My Agent"}
```

#### Set icon (Operator only)
```
PUT /api/sessions/live/{name}/icon
Content-Type: application/json

{"icon": "🤖"}
```

An agent's session token is refused on these (and on `PUT /api/sessions/live/{name}/name-color`) with 403 `forbidden_for_agent`.

---

### Team Management

#### Sleep a team
```
POST /api/sessions/live/team/{boardName}/sleep
```

#### Wake a team
```
POST /api/sessions/live/team/{boardName}/wake
```

#### Reset a team (kill all + relaunch with original prompts)
```
POST /api/sessions/live/team/{boardName}/reset
```

#### Get team sleep status
```
GET /api/sessions/live/team/{boardName}/sleep-status
```

#### Sleep/wake all agents
```
POST /api/sessions/live/sleep-all
POST /api/sessions/live/wake-all
```

#### Sleep/wake individual agent
```
POST /api/sessions/live/{sessionID}/sleep
POST /api/sessions/live/{sessionID}/wake
```

---

### Message Board

#### List all boards
```
GET /api/board/projects
```

#### Subscribe to a board
```
POST /api/board/{project}/subscribe
Content-Type: application/json

{"session_id": "uuid", "job_title": "Lead Developer"}
```

#### Post a message
```
POST /api/board/{project}/messages
Content-Type: application/json

{"session_id": "uuid", "message": "Hello team!"}
```

#### Read new messages (cursor-based)
```
GET /api/board/{project}/messages?session_id=uuid&limit=50
```
Returns messages newer than the subscriber's last-read position.

#### List all messages
```
GET /api/board/{project}/messages/all?limit=100&offset=0
```

#### Check unread count
```
GET /api/board/{project}/messages/check?session_id=uuid
```

#### List subscribers
```
GET /api/board/{project}/subscribers
```

#### Pause/resume board
```
POST /api/board/{project}/pause
POST /api/board/{project}/resume
GET /api/board/{project}/paused
```

#### Delete board
```
DELETE /api/board/{project}
```

---

### Session History

#### List historical sessions
```
GET /api/sessions/history?page=1&page_size=50&q=search+term
```
Supports full-text search, filters by source, date range, tags.

#### Get session detail
```
GET /api/sessions/history/{sessionID}
```

#### Get session events/tasks/git/notes
```
GET /api/sessions/history/{sessionID}/events
GET /api/sessions/history/{sessionID}/tasks
GET /api/sessions/history/{sessionID}/git
GET /api/sessions/history/{sessionID}/notes
GET /api/sessions/history/{sessionID}/agent-notes
```

#### Save notes
```
PUT /api/sessions/history/{sessionID}/notes
Content-Type: application/json

{"notes": "markdown content"}
```

---

### Agent Events

#### List events for a session
```
GET /api/sessions/live/{name}/events?session_id=uuid&limit=50
```

#### Create event
```
POST /api/sessions/live/{name}/events
Content-Type: application/json

{
  "session_id": "uuid",
  "event_type": "status",
  "tool_name": "Bash",
  "summary": "Running tests..."
}
```

#### Get event counts
```
GET /api/sessions/live/{name}/events/counts?session_id=uuid
```

---

### Agent Tasks

#### List tasks
```
GET /api/sessions/live/{name}/tasks?session_id=uuid
```

#### Create task
```
POST /api/sessions/live/{name}/tasks
Content-Type: application/json

{"session_id": "uuid", "title": "Fix the bug", "status": "pending"}
```

#### Update task
```
PATCH /api/sessions/live/{name}/tasks/{taskID}
Content-Type: application/json

{"status": "completed"}
```

---

### Settings

#### Get all settings
```
GET /api/settings
```

#### Update settings
```
PUT /api/settings
Content-Type: application/json

{"key": "value"}
```

---

### Tags

#### List all tags
```
GET /api/tags
```

#### Create tag
```
POST /api/tags
Content-Type: application/json

{"name": "important", "color": "#ff0000"}
```

#### Tag a session
```
POST /api/sessions/history/{sessionID}/tags
Content-Type: application/json

{"tag_id": 1}
```

---

### Themes

#### List themes
```
GET /api/themes
```

#### Get theme
```
GET /api/themes/{name}
```

#### Save theme
```
PUT /api/themes/{name}
Content-Type: application/json

{
  "description": "My custom theme",
  "base": "dark",
  "variables": {"--bg-primary": "#000000", "--accent": "#ff00ff"}
}
```

---

### Scheduled Jobs

#### List jobs
```
GET /api/scheduled/jobs
```

#### Create job
```
POST /api/scheduled/jobs
Content-Type: application/json

{
  "name": "Nightly tests",
  "cron": "0 0 * * *",
  "repo_path": "/path/to/repo",
  "prompt": "Run all tests and report results",
  "agent_type": "claude"
}
```

#### Toggle job
```
POST /api/scheduled/jobs/{jobID}/toggle
```

#### Get run history
```
GET /api/scheduled/jobs/{jobID}/runs
GET /api/scheduled/runs/recent
```

---

### Webhooks

#### List webhooks
```
GET /api/webhooks
```

#### Create webhook
```
POST /api/webhooks
Content-Type: application/json

{"url": "https://example.com/hook", "events": ["session.completed"]}
```

---

### System

#### Get server status
```
GET /api/system/status
```

#### Get API key
```
GET /api/system/api-key
```

#### Check for updates
```
GET /api/system/update-check
```

#### Get QR code (for mobile)
```
GET /api/system/qr
```
Returns PNG image.

#### List filesystem
```
GET /api/filesystem/list?path=/home/user
```

---

### WebSocket Endpoints

#### Session list updates
```
ws://localhost:8420/ws/coral
```
Receives real-time session list updates as JSON diffs. Messages:
- `coral_update`: full session list refresh
- `coral_diff`: incremental changes

#### Terminal streaming
```
ws://localhost:8420/ws/terminal/{name}?agent_type=claude&session_id=uuid
```
Bidirectional WebSocket for terminal I/O:
- **Receive**: `terminal_update` (content), `terminal_closed`
- **Send**: `terminal_input` (keystrokes)

---

## Rate Limits

No rate limits are enforced. The API is designed for local/LAN use.
For remote deployments, consider adding a reverse proxy with rate limiting.

## Versioning

The API is not versioned. Breaking changes are avoided but not guaranteed
during the pre-1.0 phase. The WebSocket protocol may evolve.

## Example: Build a Custom CLI

```bash
# Launch an agent
curl -X POST http://localhost:8420/api/sessions/launch \
  -H 'Content-Type: application/json' \
  -d '{"agent_type":"claude","working_dir":"/tmp","prompt":"Hello"}'

# List live sessions
curl http://localhost:8420/api/sessions/live

# Send input
curl -X POST http://localhost:8420/api/sessions/live/claude-abc123/send \
  -H 'Content-Type: application/json' \
  -d '{"input":"run tests"}'

# Get terminal output
curl 'http://localhost:8420/api/sessions/live/my-project/capture?agent_type=claude&session_id=uuid'
```

## Example: Build a Custom Dashboard

```javascript
// Connect to session list WebSocket
const ws = new WebSocket('ws://localhost:8420/ws/coral');
ws.onmessage = (e) => {
  const data = JSON.parse(e.data);
  if (data.type === 'coral_update') {
    renderSessionList(data.sessions);
  }
};

// Connect to terminal WebSocket
const term = new WebSocket('ws://localhost:8420/ws/terminal/claude-abc?agent_type=claude&session_id=uuid');
term.onmessage = (e) => {
  const data = JSON.parse(e.data);
  if (data.type === 'terminal_update') {
    renderTerminal(data.content);
  }
};

// Send keystroke
term.send(JSON.stringify({type: 'terminal_input', data: 'ls\n'}));
```
