# Authentication API

Coral uses a layered authentication system:

1. **Localhost bypass** — Requests from `127.0.0.1` / `::1` are always authenticated without credentials.
2. **API key** — An auto-generated key stored in `~/.coral/`. Remote clients pass it via `Authorization: Bearer <key>` header or `X-API-Key` header.
3. **Session cookie** — After validating an API key via `POST /auth/key`, a session cookie is set for browser access. It is `HttpOnly`, `SameSite=Lax`, lasts 24 hours, and is `Secure` when the browser reached Coral over https: over TLS directly, or through a trusted reverse proxy that sends `X-Forwarded-Proto: https`. A proxy on this machine (a loopback peer) is always trusted; list others in `CORAL_TRUSTED_PROXIES` (comma-separated IPs or CIDRs, e.g. `10.0.0.5,192.168.1.0/24`). The header is ignored from any other peer.

## Auth Page

```
GET /auth
```

Serves the HTML authentication page where users can enter their API key.

**Response:** HTML document.

## Validate API Key

```
POST /auth
POST /auth/key
```

Validates an API key and sets a session cookie on success. Both paths are equivalent.

**Request Body:**
```json
{
  "key": "your-api-key"
}
```

**Response (200):**
```json
{
  "ok": true
}
```

A `coral_session` cookie is set in the response.

**Errors:**
- `400` — Invalid JSON
- `401` — Invalid API key
- `429` — Rate limited: too many wrong keys from this IP in the last minute. Only wrong keys count; signing in with the right key never does.

## Sign Out

```
POST /auth/logout
```

Ends the caller's session and clears the `coral_session` cookie. Reachable without a valid session, so an expired one can still clear its cookie. A request whose `Origin` is another site is refused (`403`), so another page can't sign the user out.

**Response (200):** `{"ok": true}`

## Get API Key

```
GET /api/system/api-key
```

Returns the current API key. **Localhost only.**

**Response:**
```json
{
  "key": "abc123..."
}
```

**Errors:**
- `403` — Not accessed from localhost

## Regenerate API Key

```
POST /api/system/api-key/regenerate
```

Generates a new API key, invalidating the previous one, and ends every session (each browser signs in again with the new key): a new key is how you lock out whoever had the old one. **Localhost only.**

**Response:**
```json
{
  "key": "new-key-456..."
}
```

**Errors:**
- `403` — Not accessed from localhost

## Auth Status

```
GET /api/system/auth-status
```

Returns the current authentication status and method used.

**Response:**
```json
{
  "authenticated": true,
  "method": "localhost"
}
```

| Method | Description |
|--------|-------------|
| `"localhost"` | Request came from localhost |
| `"key"` | Authenticated via API key header |
| `"session"` | Authenticated via session cookie |
| `"none"` | Not authenticated |

## Agent identity and sign-in links (phase 1: log only)

Coral is moving from "anything on this computer is trusted" to "every caller says who it is". Phase 1 (#3387) only **observes**: nothing is refused, and the report below shows what a stricter mode would break.

### Per-session agent tokens

Every agent session gets a random token when Coral launches, restarts, resumes or wakes it. Coral's own launch line reads the token from a single-use file (0600, random name, in `<data>/session_tokens/`) into `CORAL_SESSION_TOKEN` and deletes the file before the agent CLI starts. The token is never typed or echoed in the terminal, never in a command line (`ps`), a log or the board, and never in the tmux server's environment; only that pane's shell and what it starts have it. Restarting or resuming mints a new token and the old one stops working.

`coral-board`, `coral-agent` and the `coral-hook-*` tools pick it up from their environment and send it as `X-Coral-Session-Token` with every request, with no action from the agent: nothing in a prompt or a tool call touches it.

The server attributes each request to the token's session and its board subscription. A board request that claims another identity (another `subscriber_id`, `as` or `created_by`, `Operator`, or another board in the path) is logged once per session and kind every 5 minutes as `[identity] mismatch (log only)`, and counted.

**Threat model.** Tokens stop mistakes and prompt-injected impersonation through Coral's own tools. They do not stop a malicious process running as the same user: it can read another process's environment (`/proc/<pid>/environ` on Linux, `ps -E` on macOS). Isolation between users is the OS's; isolation between agents of one user is about honest attribution.

### Dashboard sign-in links

At startup Coral mints a single-use sign-in token, valid 10 minutes, writes it to `<data>/login_token` (0600) and opens the browser with `http://localhost:<port>/?login_token=…`. With no display (`--no-browser`, a Linux box without `DISPLAY`) it prints the link instead. Opening the link trades the token for the dashboard's session cookie (HttpOnly, SameSite=Strict) and redirects to the same page without it. A used or expired link answers 403.

`coral open` asks the running Coral for a fresh link (through the same handshake as `coral cloud`) and opens it, or prints it with `--print` or without a display. In phase 1 the dashboard keeps working on this computer without the cookie.

### Identity report

`GET /api/system/identity-report` (the dashboard on this computer or the coral CLI only; never through Coral Hub, never an agent's plain request) returns counts by caller type (`agent_token`, `agent_bad_token`, `agent_no_token`, `dashboard_cookie`, `dashboard_no_cookie`, `coral_hub`, `api_key`, `local_other`), the subset strict mode would refuse (`would_break_in_strict_mode`), mismatch counts by kind (`impersonation`, `operator`, `cross_board`) and the last 50 mismatches (never a token).

### System messages

Board messages Coral posts itself (the task queue, the health monitor) carry `is_system: true`, set by the server only and never inferred from a sender's name. The chat collapses them by that flag.
