# Webhooks API

The Webhooks API provides CRUD operations for webhook configurations, test delivery, and delivery history. Webhooks support Slack, Discord, and generic platforms with automatic retry logic and circuit breaker protection.

## Allowed targets

Coral posts webhooks from your computer, so a webhook URL must not reach this computer or its network. Notification webhooks, job-run callbacks (`webhook_url` on `POST /api/tasks/run`) and board subscriber webhooks (`webhook_url` on subscribe) all follow the same rules:

- The URL is `http://` or `https://`, with no user name or password in it.
- The host must not be (or resolve to) a loopback, private (10/8, 172.16/12, 192.168/16, fc00::/7), link-local (169.254/16, including cloud metadata at 169.254.169.254), CGNAT, multicast or unspecified address.
- Saving a URL that breaks these rules answers `400` and says why. Each delivery checks again when it connects, so a host that later resolves to a private address is not reached either. Redirects are checked the same way, and no proxy from the environment is used.

To send webhooks to your own network (a home-automation box, a local chat bridge), set `webhook_allow_private` to `true` (`PUT /api/settings`) or start Coral with `CORAL_WEBHOOK_ALLOW_PRIVATE=1`. Then only the scheme is checked.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/webhooks` | List all webhooks |
| POST | `/api/webhooks` | Create a webhook |
| PATCH | `/api/webhooks/{webhookID}` | Update a webhook |
| DELETE | `/api/webhooks/{webhookID}` | Delete a webhook |
| POST | `/api/webhooks/{webhookID}/test` | Send test notification |
| GET | `/api/webhooks/{webhookID}/deliveries` | Get delivery history |

---

## List All Webhooks

```
GET /api/webhooks
```

**Response:**
```json
[
  {
    "id": 1,
    "name": "Slack Alert",
    "platform": "slack",
    "url": "https://hooks.slack.com/services/...",
    "enabled": 1,
    "event_filter": "*",
    "idle_threshold_seconds": 0,
    "agent_filter": null,
    "low_confidence_only": 0,
    "consecutive_failures": 0,
    "created_at": "2024-01-15T10:30:00Z",
    "updated_at": "2024-01-15T10:30:00Z"
  }
]
```

---

## Create Webhook

```
POST /api/webhooks
```

**Request Body:**
```json
{
  "name": "Slack Alert",
  "url": "https://hooks.slack.com/services/...",
  "platform": "slack",
  "agent_filter": null
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Webhook display name |
| `url` | string | Yes | Target webhook URL (must be HTTPS) |
| `platform` | string | No | `"slack"`, `"discord"`, or `"generic"` (default) |
| `agent_filter` | string\|null | No | Only trigger for a specific agent |

**Defaults on creation:**
- `enabled`: 1
- `event_filter`: `"*"` (all events)
- `idle_threshold_seconds`: 0
- `low_confidence_only`: 0
- `consecutive_failures`: 0

**Response:** Created webhook config object.

**Errors:**
- `400` — `name` and `url` are required

---

## Update Webhook

```
PATCH /api/webhooks/{webhookID}
```

Partial update — include only the fields you want to change.

**Request Body (any subset):**
```json
{
  "name": "Updated Name",
  "platform": "discord",
  "url": "https://discord.com/api/webhooks/...",
  "enabled": 1,
  "event_filter": "idle,confidence",
  "idle_threshold_seconds": 300,
  "agent_filter": "agent-1",
  "low_confidence_only": 0,
  "consecutive_failures": 0
}
```

**Response:** `{"ok": true}`

---

## Delete Webhook

```
DELETE /api/webhooks/{webhookID}
```

Deletes the webhook config and all its delivery history (cascading, transaction-protected).

**Response:** `{"ok": true}`

---

## Test Webhook

```
POST /api/webhooks/{webhookID}/test
```

Sends a test notification immediately (not queued). No request body required.

**Response:**
```json
{
  "id": 42,
  "webhook_id": 1,
  "agent_name": "coral-test",
  "session_id": null,
  "event_type": "needs_input",
  "event_summary": "Test notification from Coral dashboard",
  "status": "delivered",
  "http_status": 200,
  "error_msg": null,
  "attempt_count": 1,
  "next_retry_at": null,
  "delivered_at": "2024-01-15T10:35:00Z",
  "created_at": "2024-01-15T10:35:00Z"
}
```

**Platform-specific payloads:**

Slack (Block Kit):
```json
{
  "blocks": [{
    "type": "section",
    "text": {
      "type": "mrkdwn",
      "text": ":raising_hand: *Coral — needs_input*\n*Agent:* `coral-test`\n*Message:* Test notification from Coral dashboard"
    }
  }]
}
```

Discord (Embeds):
```json
{
  "embeds": [{
    "title": "Coral — needs_input",
    "description": "Test notification from Coral dashboard",
    "color": 13924130,
    "fields": [{"name": "Agent", "value": "`coral-test`", "inline": true}],
    "footer": {"text": "Coral"}
  }]
}
```

Generic:
```json
{
  "agent_name": "coral-test",
  "session_id": null,
  "event_type": "needs_input",
  "summary": "Test notification from Coral dashboard",
  "timestamp": "2024-01-15T10:35:00Z",
  "source": "coral"
}
```

**Errors:**
- `404` — Webhook not found
- `400` — Webhook URL blocked by SSRF protection

---

## List Deliveries

```
GET /api/webhooks/{webhookID}/deliveries?limit=50
```

Returns recent delivery attempts, ordered by most recent first.

| Parameter | Type | Default | Max | Description |
|-----------|------|---------|-----|-------------|
| `limit` | int | 50 | 200 | Number of deliveries to return |

**Response:**
```json
[
  {
    "id": 42,
    "webhook_id": 1,
    "agent_name": "agent-1",
    "session_id": "session-123",
    "event_type": "needs_input",
    "event_summary": "Agent needs user input",
    "status": "delivered",
    "http_status": 200,
    "error_msg": null,
    "attempt_count": 1,
    "next_retry_at": null,
    "delivered_at": "2024-01-15T10:35:00Z",
    "created_at": "2024-01-15T10:35:00Z"
  }
]
```

**Delivery statuses:**
| Status | Description |
|--------|-------------|
| `pending` | Waiting to be sent or scheduled for retry |
| `delivered` | Successfully sent (HTTP 200-299) |
| `failed` | Failed after all retries |

---

## Background Dispatcher

Webhooks are delivered by a background dispatcher that:

1. Polls for pending deliveries on an interval
2. Validates webhook URL against SSRF rules (blocks localhost, private IPs, metadata endpoints)
3. Builds platform-specific payload (Slack, Discord, or generic)
4. POSTs to the webhook URL with a 10-second timeout
5. On success (HTTP 200-299): marks `"delivered"`, resets failure counter
6. On failure: schedules retry or marks `"failed"` after max attempts

**Retry schedule:** 30 seconds, 2 minutes, 10 minutes (max 3 retries, 4 total attempts).

**Circuit breaker:** After 10 consecutive failures, the webhook is automatically disabled. The failure counter resets on any successful delivery.

**Pruning:** Keeps the 200 most recent non-pending deliveries per webhook.
