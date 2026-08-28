# Coral API Reference

Coral exposes a REST API over HTTP. All endpoints are prefixed with `/api/` unless noted otherwise.

**Authentication:** Localhost requests bypass auth. Remote requests require an API key (`Authorization: Bearer <key>` or `X-API-Key` header) or a valid session cookie. See [auth.md](auth.md).


## API Documentation

### Sessions & Real-Time
- [Sessions](sessions.md) — Live session management, PTY interaction, file browsing, git info
- [Session History](session-history.md) — Historical session data, notes, events, tags
- [WebSockets](websockets.md) — Real-time terminal and event streams

### Automation
- [Workflow Quickstart](workflow-quickstart.md) — Build your first workflow in 5 minutes
- [Workflows](workflows.md) — Multi-step workflow definitions and execution
- [Scheduled Jobs](scheduled-jobs.md) — Cron-based scheduled job management
- [Tasks](jobs.md) — One-shot task execution

### Collaboration
- [Message Board](board.md) — Multi-agent message board, subscriptions, groups, board tasks
- [Notifications](notifications.md) — Push toast and alert notifications to the Coral UI
- [Webhooks](webhooks.md) — Webhook management and delivery tracking

### Configuration
- [Settings & System](settings-system.md) — App settings, status, CLI checks, network info
- [Teams](teams.md) — Team lifecycle management, members, resurrect/relaunch
- [Team Configuration](team-config.md) — Agent team configuration (agent.json schema)
- [Hooks](hooks.md) — Per-agent and per-step hook configuration (lifecycle events, cross-agent support)
- [Connected Apps](connected-apps.md) — OAuth connections to external services

### Customization
- [Themes](themes.md) — Theme CRUD, import/export, LLM-powered generation
- [Templates](templates.md) — Agent and command templates from GitHub
- [Views](views.md) — Custom dashboard views/tabs

### Privacy
- [Telemetry](telemetry.md) — Every event Coral sends, what it never collects, and why there is no opt-out

### Observability
- [LLM Proxy](proxy.md) — Proxy passthrough, cost tracking, dashboard API, WebSocket events

### Auth
- [Authentication](auth.md) — API key management, session auth, auth status

## Quick Reference

### Health & System
| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/health` | Health check (always `{"status":"ok"}`) |
| `GET` | `/api/system/status` | System status (version, uptime, sessions) |
| `GET` | `/api/system/update-check` | Check for new Coral versions |
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

### LLM Proxy (10 endpoints)
| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/proxy/{sessionID}/v1/messages` | Forward Anthropic request |
| `POST` | `/proxy/{sessionID}/v1/chat/completions` | Forward OpenAI request |
| `GET` | `/proxy/health` | Proxy health check |
| `GET` | `/api/proxy/stats` | Aggregated cost stats |
| `GET` | `/api/proxy/requests` | List proxy requests |
| `GET` | `/api/proxy/requests/{requestID}` | Get single request |
| `GET` | `/api/proxy/session/{sessionID}/cost` | Session cost summary |
| `GET` | `/api/proxy/tasks/runs/{runID}/cost` | Task run cost summary |
| `GET` | `/api/proxy/pricing` | Model pricing table |
| `GET` | `/ws/proxy` | Real-time proxy event stream (WebSocket) |

### Authentication (5 endpoints)
| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/auth` | Auth page (HTML) |
| `POST` | `/auth`, `/auth/key` | Validate API key |
| `GET` | `/api/system/api-key` | Get API key (localhost only) |
| `POST` | `/api/system/api-key/regenerate` | Regenerate API key (localhost only) |
| `GET` | `/api/system/auth-status` | Get auth status |

