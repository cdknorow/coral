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
