# Coral API Reference

Coral exposes a REST API over HTTP. All endpoints are prefixed with `/api/` unless noted otherwise.

**Authentication:** Every request needs a credential, including from this computer ([strict local auth](auth.md#strict-local-auth-the-default), the default since #3936): agents' tools send their session token automatically, the dashboard signs in (open it from the app or tray, or run `coral open`), the `coral` command line signs its requests, and a script sends the API key (`Authorization: Bearer <key>` or `X-API-Key` header; `coral api-key` prints it). Start Coral with `--strict-local-auth=false` to turn this off for requests from this computer (weaker); a dev build also honours `CORAL_STRICT_LOCAL_AUTH=0`, but a release build ignores that variable. See [auth.md](auth.md).


## Reading these docs as an agent

Run `coral-agent docs` to list the topics, `coral-agent docs <topic>` to read one (for example `coral-agent docs board`), and `coral-agent docs all` to print everything. The command sends your session token, so it works under strict local auth and from inside a sandbox that can run Coral's own tools; a plain `curl` of `/api/agent-docs` has no credential and is refused. The Docs tab in the dashboard has a **Copy docs line for agents** button that copies this instruction for a user to paste to an agent.

## API Documentation

### Sessions & Real-Time
- [Sessions](sessions.md) — Live session management, sleep / wake / archive, input (`/send`, `/keys`), file browsing, git info
- [Session History](session-history.md) — Historical session data, notes, events, tags, and the live chat transcript (paged by `before`)
- [WebSockets](websockets.md) — Real-time terminal and event streams, immediate updates on state changes, notifications

### Getting Started
- [Quickstart](quickstart.md) — Install, launch your first agent, and get a committed change
- [Worked Demos](worked-demos.md) — Three multi-agent scenarios run end to end, with real output
- [Using the Dashboard](using-the-dashboard.md) — Task-oriented: model and thinking level and the chat-bar chip, Cost by thinking level, the team health monitor, the agent list's Display panel, the sectioned dialogs, image thumbnails
- [Troubleshooting](troubleshooting.md) — Observed failure modes and their causes, profiling, isolated test servers

### Automation
- [Workflow Quickstart](workflow-quickstart.md) — Build your first workflow in 5 minutes
- [Workflows](workflows.md) — Multi-step workflow definitions and execution (the Operator's: an agent's token is refused, reads included)
- [Scheduled Jobs](scheduled-jobs.md) — Cron-based scheduled job management (the Operator's: an agent's token is refused, reads included)
- [Jobs](jobs.md) — One-shot agent execution

### Collaboration
- [Agent UI](agent-ui.md) — publish interactive sidebar panels and read user responses
- [Personal Tasks](agent-tasks.md) — `coral-agent task` commands, HTTP API, and migration
- [Artifacts](artifacts.md) — Publish files and reports to the Artifacts tabs; show the user files as `coral://` links, never local paths
- [Task Workflows](task-workflows.md) — Dependencies, Build → Test → Release, artifacts, retries, and default instructions
- [Merge queue](merge-queue.md) — Attach patches to tasks, approval of exact digests, `coral-board queue`, send-back, mark-landed
- [Message Board](board.md) — Multi-agent message board, subscriptions, groups, board status/routing, board tasks
- [Cross-team Messaging](cross-team.md) — Orchestrator-to-Orchestrator messages between teams (`coral-board send`): Coral Hub team links grant permission; messaging and file switches are on by default as kill switches, while local and legacy paths need explicit opt-ins
- [Notifications](notifications.md) — Push toast and alert notifications to the Coral UI
- [Saved for later](saved-messages.md) — The Operator's bookmarks on chat and board messages (`/api/saved`), the stable message key, jump back to a message

### Configuration
- [Multi-Server Hub](hub.md) — Register remote Coral servers, merged agent list, proxy and remote launch, Coral Hub tunnel refusals, versions
- [Settings & System](settings-system.md) — App settings, status, CLI checks, network info
- [What Coral Stores](what-coral-stores.md) — What Coral keeps on this computer, for how long, whether it is cleaned of secrets, where to change it, what reaches Coral Hub
- [Teams](teams.md) — Team lifecycle management, members, and resurrection
- [Team Configuration](team-config.md) — Agent team configuration (agent.json schema)
- [Hooks](hooks.md) — Per-agent and per-step hook configuration (lifecycle events, cross-agent support)

### Customization
- [Themes](themes.md) — Theme CRUD, import/export, LLM-powered generation
- [Templates](templates.md) — Agent and command templates from GitHub
- [Views](views.md) — Custom dashboard views/tabs

### Privacy
- [Telemetry](telemetry.md) — Every event Coral sends, what it never collects, and why there is no opt-out
- [What the redactor recognises](redaction.md) — The rules that hide tokens from Coral Hub viewers, the keyword rules, and every entropy threshold

### Observability
- [Token Usage](token-usage.md) — Recorded usage, summaries, turns, time series, teams, and branches

### Auth
- [Authentication](auth.md) — API key management, session auth, auth status

## Quick Reference

### Health & System
| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/health` | Health check: exactly `{"status":"ok","version":"…","hub":false}` |
| `GET` | `/api/system/status` | System status (version, uptime, sessions) |
| `GET` | `/api/system/update-check` | Check for new Coral versions (on the update channel set by `update_channel`) |
| `GET` | `/api/system/cli-check` | Check installed CLI tools |
| `GET` | `/api/system/qr` | QR code for remote access URL |
| `GET` | `/api/system/network-info` | Network interfaces and IPs |

### Themes (7 endpoints)
| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/themes` | List all themes |
| `GET` | `/api/themes/variables` | Get CSS variable definitions |
| `GET` | `/api/themes/{name}` | Get a theme |
| `PUT` | `/api/themes/{name}` | Create/update a theme |
| `DELETE` | `/api/themes/{name}` | Delete a theme |
| `POST` | `/api/themes/import` | Import theme from JSON file |
| `POST` | `/api/themes/generate` | Generate theme with LLM |

### Templates (6 endpoints)
| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/templates/agents` | List agent categories |
| `GET` | `/api/templates/agents/{category}` | List agents in category |
| `GET` | `/api/templates/agents/{category}/{name}` | Get agent template |
| `GET` | `/api/templates/commands` | List command categories |
| `GET` | `/api/templates/commands/{category}` | List commands in category |
| `GET` | `/api/templates/commands/{category}/{name}` | Get command template |

### Views (5 endpoints)
| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/views` | List all views |
| `POST` | `/api/views` | Create a view |
| `GET` | `/api/views/{id}` | Get a view |
| `PUT` | `/api/views/{id}` | Update a view |
| `DELETE` | `/api/views/{id}` | Delete a view |

### Authentication (6 endpoints)
| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/auth` | Auth page (HTML) |
| `POST` | `/auth`, `/auth/key` | Validate API key |
| `POST` | `/auth/logout` | Sign out (end this session, clear the cookie) |
| `GET` | `/api/system/api-key` | Get API key (localhost only) |
| `POST` | `/api/system/api-key/regenerate` | Regenerate API key, ending every session (localhost only) |
| `GET` | `/api/system/auth-status` | Get auth status |
