# LLM Proxy API

The LLM proxy intercepts agent API calls, forwards them to upstream providers, and tracks token usage and cost per request. It supports both JSON and SSE streaming for Anthropic and OpenAI APIs.

The proxy runs on the same server as the Coral API. Agents are automatically wired to use it when `proxy_enabled=true` in settings.

---

## Proxy Passthrough Routes

These routes forward requests to the upstream LLM provider. They live outside `/api/` to avoid auth middleware collision with agent requests.

### POST `/proxy/{sessionID}/v1/messages`

Forward an Anthropic Messages API request. Supports both JSON and SSE streaming.

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `sessionID` | string | The agent's session ID (embedded in the proxy base URL) |

**Headers forwarded to upstream:**
| Header | Description |
|--------|-------------|
| `x-api-key` | Anthropic API key (pass-through if proxy has no configured key) |
| `anthropic-version` | API version (defaults to `2023-06-01`) |
| `anthropic-beta` | Beta features (e.g., extended thinking) |

**Behavior:**
- Reads `model` and `stream` from request body JSON
- Creates a tracked proxy request record
- Forwards to `{ANTHROPIC_BASE_URL}/v1/messages`
- For streaming: passes SSE lines through line-by-line, extracts usage from `message_start` and `message_delta` events
- For JSON: reads full response, extracts usage from `usage` field
- Computes cost breakdown and records completion

**Response headers added:**
| Header | Description |
|--------|-------------|
| `X-Coral-Proxy` | Always `"true"` |
| `X-Coral-Request-Id` | UUID for this proxy request |
| `X-Coral-Session-Id` | The session ID |
| `X-Coral-Cost-Usd` | Computed cost (e.g., `"0.003450"`) — non-streaming only |

---

### POST `/proxy/{sessionID}/v1/chat/completions`

Forward an OpenAI Chat Completions API request. Supports both JSON and SSE streaming.

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `sessionID` | string | The agent's session ID |

**Headers forwarded to upstream:**
| Header | Description |
|--------|-------------|
| `Authorization` | `Bearer <key>` — pass-through if proxy has no configured key |

**Behavior:**
- Same tracking flow as Anthropic endpoint
- Forwards to `{OPENAI_BASE_URL}/v1/chat/completions`
- For streaming: extracts usage from final chunk (when `stream_options.include_usage` is true)
- Same response headers as Anthropic endpoint

---

### GET `/proxy/health`

Proxy health check.

**Response:**
```json
{
  "status": "ok",
  "uptime_seconds": 3600
}
```

---

## Dashboard API

All dashboard endpoints are under `/api/proxy/` and require standard Coral authentication.

### GET `/api/proxy/stats`

Aggregated proxy cost statistics.

**Query Parameters:**
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `period` | string | `"day"` | Time window: `"hour"`, `"day"`, `"week"`, `"month"` |
| `session_id` | string | | Filter to a single session |

**Response:**
```json
{
  "period": "day",
  "total_requests": 142,
  "total_cost_usd": 1.234567,
  "total_input_tokens": 450000,
  "total_output_tokens": 125000,
  "by_model": [
    { "model": "claude-sonnet-4-20250514", "requests": 100, "cost_usd": 0.95 },
    { "model": "gpt-4o", "requests": 42, "cost_usd": 0.28 }
  ],
  "by_agent": [
    { "session_id": "abc-123", "agent_name": "Backend Dev", "requests": 50, "cost_usd": 0.65 }
  ]
}
```

`by_agent` is only included when `session_id` is not specified.

---

### GET `/api/proxy/requests`

List recent proxy requests with pagination.

**Query Parameters:**
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `session_id` | string | | Filter to a single session |
| `limit` | int | `50` | Max results |
| `offset` | int | `0` | Pagination offset |

**Response:**
```json
{
  "requests": [
    {
      "id": 1,
      "request_id": "uuid-string",
      "session_id": "session-uuid",
      "agent_name": "Backend Dev",
      "agent_type": "claude",
      "board_name": "my-board",
      "provider": "anthropic",
      "model_requested": "claude-sonnet-4-20250514",
      "model_used": "claude-sonnet-4-20250514",
      "is_streaming": 1,
      "input_tokens": 5000,
      "output_tokens": 1200,
      "cache_read_tokens": 3000,
      "cache_write_tokens": 0,
      "total_tokens": 9200,
      "input_cost_usd": 0.015,
      "output_cost_usd": 0.018,
      "cache_read_cost_usd": 0.003,
      "cache_write_cost_usd": 0.0,
      "pricing_input_per_mtok": 3.0,
      "pricing_output_per_mtok": 15.0,
      "pricing_cache_read_per_mtok": 0.3,
      "pricing_cache_write_per_mtok": 3.75,
      "cost_usd": 0.036,
      "started_at": "2026-04-03T10:00:00Z",
      "completed_at": "2026-04-03T10:00:05Z",
      "latency_ms": 5000,
      "status": "success",
      "error_message": null,
      "http_status": 200,
      "cache_hit": 0
    }
  ],
  "total": 142
}
```

---

### GET `/api/proxy/requests/{requestID}`

Get a single proxy request by its UUID.

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `requestID` | string | The proxy request UUID |

**Response:** Single request object (same schema as items in list endpoint).

**Errors:**
- `404` — Request not found

---

### GET `/api/proxy/session/{sessionID}/cost`

Cost summary for a single session (all time).

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `sessionID` | string | Session UUID |

**Response:**
```json
{
  "session_id": "abc-123",
  "total_requests": 50,
  "total_cost_usd": 0.65,
  "total_input_tokens": 150000,
  "total_output_tokens": 45000,
  "by_model": [
    { "model": "claude-sonnet-4-20250514", "requests": 50, "cost_usd": 0.65 }
  ]
}
```

---

### GET `/api/proxy/tasks/runs/{runID}/cost`

Cost summary for a scheduled/one-shot task run. Joins `scheduled_runs` with `proxy_requests` by session ID.

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `runID` | int | The task run ID |

**Response:**
```json
{
  "run_id": 42,
  "session_id": "abc-123",
  "total_requests": 10,
  "total_cost_usd": 0.125,
  "total_input_tokens": 50000,
  "total_output_tokens": 15000
}
```

**Errors:**
- `400` — Invalid run ID
- `404` — Run not found

---

### GET `/api/proxy/pricing`

Current model pricing table used for cost calculations.

**Response:**
```json
{
  "models": [
    {
      "model": "claude-opus-4-20250514",
      "InputPerMTok": 15.0,
      "OutputPerMTok": 75.0,
      "CacheReadPerMTok": 1.5,
      "CacheWritePerMTok": 18.75
    },
    {
      "model": "claude-sonnet-4-20250514",
      "InputPerMTok": 3.0,
      "OutputPerMTok": 15.0,
      "CacheReadPerMTok": 0.3,
      "CacheWritePerMTok": 3.75
    },
    {
      "model": "gpt-4o",
      "InputPerMTok": 2.5,
      "OutputPerMTok": 10.0,
      "CacheReadPerMTok": 0.0,
      "CacheWritePerMTok": 0.0
    }
  ]
}
```

Prices are in USD per million tokens.

---

## WebSocket

### GET `/ws/proxy`

Real-time proxy event stream. Uses the same WebSocket library (`nhooyr.io/websocket`) as `/ws/coral`.

**Event types:**

#### `request_started`
```json
{
  "type": "request_started",
  "request_id": "uuid",
  "session_id": "session-uuid",
  "provider": "anthropic",
  "model": "claude-sonnet-4-20250514",
  "streaming": true,
  "timestamp": "2026-04-03T10:00:00Z"
}
```

#### `request_completed`
```json
{
  "type": "request_completed",
  "request_id": "uuid",
  "session_id": "session-uuid",
  "model": "claude-sonnet-4-20250514",
  "input_tokens": 5000,
  "output_tokens": 1200,
  "cost_usd": 0.033,
  "latency_ms": 5000,
  "status": "success",
  "http_status": 200,
  "timestamp": "2026-04-03T10:00:05Z"
}
```

#### `request_error`
```json
{
  "type": "request_error",
  "request_id": "uuid",
  "session_id": "session-uuid",
  "model": "claude-sonnet-4-20250514",
  "status": "error",
  "error": "HTTP 429",
  "http_status": 429,
  "timestamp": "2026-04-03T10:00:01Z"
}
```

Events are non-blocking — slow consumers may miss events (buffer size: 64).

---

## Configuration

### Enabling the Proxy

Set `proxy_enabled=true` in Coral settings (`PUT /api/settings`). When enabled, Coral auto-injects the proxy base URL into agent launch environment variables:

| Agent Type | Environment Variable | Value |
|------------|---------------------|-------|
| Claude | `ANTHROPIC_BASE_URL` | `http://127.0.0.1:{port}/proxy/{sessionID}` |
| Antigravity (`agy`) | `GOOGLE_GEMINI_BASE_URL`, `GEMINI_API_BASE` | `http://127.0.0.1:{port}/proxy/{sessionID}` |
| Codex | `OPENAI_BASE_URL` | `http://127.0.0.1:{port}/proxy/{sessionID}` |

Each agent gets a unique URL with its session ID embedded in the path.

### Provider API Keys

The proxy uses the agent's own API key (pass-through from request headers) by default. Optional server-side keys can be configured via environment variables:

| Variable | Provider |
|----------|----------|
| `ANTHROPIC_API_KEY` | Anthropic |
| `OPENAI_API_KEY` | OpenAI |
| `GOOGLE_API_KEY` | Google |

### Custom Base URLs

Override upstream provider URLs:

| Variable | Default |
|----------|---------|
| `CORAL_ANTHROPIC_BASE_URL` | `https://api.anthropic.com` |
| `CORAL_OPENAI_BASE_URL` | `https://api.openai.com` |
| `CORAL_GOOGLE_BASE_URL` | `https://generativelanguage.googleapis.com` |

---

## Cost Calculation

Costs are calculated using a built-in pricing table with prefix-based model matching (e.g., `claude-sonnet-4` matches `claude-sonnet-4-20250514`).

Token types tracked:
- **Input tokens** — prompt/input tokens
- **Output tokens** — completion/output tokens
- **Cache read tokens** — Anthropic prompt caching reads
- **Cache write tokens** — Anthropic prompt caching writes

Each request stores both the per-token pricing applied and the computed cost breakdown, so historical costs remain accurate even if the pricing table is updated.

---

## Storage

Proxy requests are stored in the `proxy_requests` table in the main SQLite database. Indexed on `session_id`, `started_at`, and `model_used`.
