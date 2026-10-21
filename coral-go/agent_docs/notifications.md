# Notifications API

Push notifications to the Coral UI from workflows, scripts, or any HTTP client. Notifications appear in real-time via WebSocket.

An agent sends one with `coral-agent notify --title "…" [--message "…"] [--level info|success|warning|error] [--alert]`, which carries its session token. A script sends the API key, as in the examples below (`coral api-key` prints it): a request with no credential is refused.

## Create Notification

```
POST /api/notifications
```

**Request Body:**
```json
{
  "title": "Build Complete",
  "message": "All 42 tests passed",
  "type": "toast",
  "level": "success",
  "link": {
    "label": "View docs",
    "url": "/api/agent-docs/workflow-quickstart"
  }
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `title` | string | One of title/message required | — | Bold heading text |
| `message` | string | One of title/message required | — | Body text |
| `type` | string | No | `"toast"` | `"toast"` (top-right, auto-dismiss) or `"alert"` (centered modal, requires OK click) |
| `level` | string | No | `"info"` | `"info"`, `"success"`, `"warning"`, `"error"` — controls icon and styling |
| `link` | object | No | — | Optional link shown in the notification |
| `link.label` | string | Yes (if link) | — | Link text |
| `link.url` | string | Yes (if link) | — | Where the link goes: `docs://name` opens a doc in the Docs tab, an `http(s)://` address opens in a new tab, and a dashboard path (for example `/board/my-team`) opens that page. Anything else (`javascript:`, `data:`, `file:`, `//host`, `/api/...`) is refused with 400. At most 2048 characters; `link.label` at most 200. |

**Response:**
```json
{
  "id": 1,
  "title": "Build Complete",
  "message": "All 42 tests passed",
  "type": "toast",
  "level": "success",
  "link": { "label": "View docs", "url": "/api/agent-docs/workflow-quickstart" },
  "created_at": "2026-04-02T12:00:00Z"
}
```

## Notification Types

### Toast (`type: "toast"`)

Appears in the top-right corner and auto-dismisses after 8 seconds. User can click ✕ to dismiss early. Best for status updates and non-critical messages.

```bash
curl -H "Authorization: Bearer $(coral api-key)" -X POST http://localhost:${CORAL_PORT}/api/notifications \
  -H 'Content-Type: application/json' \
  -d '{"title": "Deploy Complete", "message": "v1.2.0 is live", "level": "success"}'
```

### Alert (`type: "alert"`)

Centered modal overlay that requires the user to click OK. Supports an optional link button. Best for important messages that require acknowledgment.

```bash
curl -H "Authorization: Bearer $(coral api-key)" -X POST http://localhost:${CORAL_PORT}/api/notifications \
  -H 'Content-Type: application/json' \
  -d '{
    "title": "Welcome to Coral Workflows",
    "message": "Automate multi-step tasks with shell commands and AI agents.",
    "type": "alert",
    "level": "info",
    "link": {
      "label": "Read the Workflow Quickstart →",
      "url": "/api/agent-docs/workflow-quickstart"
    }
  }'
```

## Levels

| Level | Icon | Use for |
|-------|------|---------|
| `info` | 💬 | General information |
| `success` | ✅ | Completed tasks, passing tests |
| `warning` | ⚠️ | Non-critical issues, deprecations |
| `error` | ❌ | Failures, critical issues |

## Delivery

Notifications are delivered via the existing `/ws/coral` WebSocket connection. They are:
- **Ephemeral** — stored in memory only, not persisted to database
- **Drained on delivery** — each notification is sent once to the next WebSocket poll, then removed
- **Expired after 5 minutes** — undelivered notifications are automatically pruned

## Usage in Workflows

Notifications are especially useful in workflow shell steps for reporting results:

```json
{
  "name": "notify-result",
  "type": "shell",
  "command": "coral-agent notify --title 'Tests Complete' --message 'All passed' --level success"
}
```

Use `StepFailed` hooks to send error notifications:

```json
{
  "hooks": {
    "StepFailed": [{
      "hooks": [{
        "type": "command",
        "command": "coral-agent notify --title 'Step Failed' --message 'Check workflow run logs' --level error"
      }]
    }]
  }
}
```

## From a hook, a workflow step or an agent: `coral-agent notify`

```bash
coral-agent notify --title "Deploy Complete" --message "v1.2.0 is live" --level success
coral-agent notify --title "Review needed" --message "PR #42 is ready" --alert
```

It sends the same request as the `curl` examples, with the caller's identity (an agent's session token, or the command-line signature in your own terminal or a workflow step), so it keeps working with [strict local auth](auth.md#strict-local-auth-the-default), where a plain `curl` is refused (401 `dashboard_auth_required`). A script that has to use `curl` can send the API key: `-H "Authorization: Bearer $(coral api-key)"`.

## Environment

The `CORAL_PORT` environment variable is available in all workflow steps and agent sessions, making it easy to construct the notification URL.

## Limits (#4602)

A notification's `title` is at most **200 bytes** and its `message` **4096 bytes**; over either is a `400` that names the limits. The server keeps the newest **200** notifications (older ones are dropped, delivered or not).
