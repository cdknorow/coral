# Spec: Multi-Server Hub

## Problem

Each Coral server shows only its own agents, teams and boards. Anyone running Coral on several machines (laptop, workstation, cloud boxes) has to open each server separately, and cannot see or start work across them from one place.

## Goal

Any Coral server can act as a **hub**: it shows the agents and teams from every registered remote Coral server in the normal dashboard, lets you open their chat and terminal views, and lets you launch agents and teams on them.

## Non-goals (v1)

- Cost, analytics and other aggregate dashboards across servers. They stay local-only. Per-server fetch-and-sum is a later follow-up.
- Teams or boards that span servers. A team, its board and its agents all live on one server.
- Central storage or replication. The hub keeps no copy of remote chats, boards or tasks; it reads them live. If a remote is down its agents show as unreachable.
- Changing how remotes behave. A remote stays a standalone Coral server and needs no knowledge of the hub.
- Replacing the existing `board_remotes.go` subscription feature (agent on server A subscribed to a board on server B). It is unrelated and stays as is.

## Terminology

- **Hub**: the Coral server whose UI the user has open.
- **Remote**: a registered standalone Coral server the hub talks to.
- **Server id**: a short stable slug for a registered remote (e.g. `workstation`). `local` is reserved for the hub itself and is the implicit default.
- **Identity**: an agent or terminal is identified by `(server, name)`. `name` is the existing session name.

## Design

### 1. Server registry

New table in the hub's `sessions.db`:

```sql
CREATE TABLE IF NOT EXISTS remote_servers (
    id         TEXT PRIMARY KEY,      -- slug, [a-z0-9-]{1,32}, not "local"
    label      TEXT NOT NULL,         -- display name
    url        TEXT NOT NULL,         -- base URL, scheme+host+port, no trailing slash
    api_key    TEXT NOT NULL,         -- the remote's API key, encrypted (see "Key encryption at rest"); never plaintext
    created_at TEXT NOT NULL,
    last_seen  TEXT,                  -- last successful contact
    last_error TEXT                   -- last failure, cleared on success
);
```

Routes (hub-local, never proxied):

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/servers` | List servers with `id,label,url,status,last_seen,last_error`. **Never returns `api_key`.** Includes the implicit `local` entry. |
| POST | `/api/servers` | Add `{id,label,url,api_key}`. Validates the URL, then calls the remote's `GET /api/health` with the key before saving. |
| PATCH | `/api/servers/{id}` | Edit label, url or key (key write-only). |
| DELETE | `/api/servers/{id}` | Remove. |
| POST | `/api/servers/{id}/test` | Re-run the connectivity and auth check. |

Status values: `online`, `unreachable`, `unauthorized` (key rejected, HTTP 401/403), `key_unreadable` (stored key cannot be decrypted, see 1a), `version_mismatch` (see Compatibility).

### 1a. Key encryption at rest

Remote API keys grant full control of a remote, so they are never stored in plaintext. This is field-level encryption inside the hub's DB and must work in every build, independent of the experimental SQLCipher mode in `internal/dbcrypt`.

- **Cipher:** AES-256-GCM from the standard library. A fresh random 12-byte nonce per encryption. The stored value is a versioned envelope, `v1:` + base64(nonce || ciphertext || tag), so the scheme can change later without a flag day.
- **Binding:** the server `id` is passed as GCM additional data, so a ciphertext copied onto another row fails to decrypt.
- **Master key:** 32 random bytes generated on first use and stored in `<CoralDir>/.remote_secret_key`, mode `0600`, created atomically (`O_EXCL`) in a `0700` directory. Reuse the permission and ownership checks used for `.db_key` (see `internal/dbcrypt/keyfile_unix.go` and `keyfile_other.go`) and refuse to start the registry if the key file is group- or world-readable. The master key lives outside `sessions.db`, so a copy of the database alone does not expose the keys.
- **Override:** if `CORAL_SECRET_KEY` is set (64 hex characters, or `rawhex:` form as in `dbcrypt`) it is used instead of the file, for deployments that inject secrets. It is never logged or written to disk.
- **Do not derive the key from the machine fingerprint** as the licence code does: it changes with hardware and network changes and would silently lock users out of every remote.
- **Where decryption happens:** only in the proxy and poller, at the moment of dialing a remote, into a local variable. Plaintext is never cached on the struct returned by the store, never put in a log line, error message or debug dump, and zeroed from byte slices where practical.
- **Failure modes:** a missing or changed master key, or an authentication-tag failure, marks that server `key_unreadable`. The UI asks the user to re-enter the key, which re-encrypts it. This must not crash the hub or affect other servers. A missing key file with existing encrypted rows does not generate a new key silently.
- **Backups and moves:** document that the key file is required to read the keys. Copying `sessions.db` to another machine without `.remote_secret_key` leaves the servers needing their keys re-entered, which is the intended outcome.
- **Rotation:** `coral rotate-remote-key` (or an equivalent maintenance command) generates a new master key and re-encrypts all rows in one transaction. If it fails midway, the old key and rows remain valid.
- **Not covered:** this protects against theft of the database file or its backups. It does not protect against an attacker who already has the hub's user account or running process.

### 2. Proxy

`/api/remote/{server}/*` forwards to `{remote.url}/*`.

- **HTTP:** streaming reverse proxy (`httputil.ReverseProxy` or equivalent). Sets `Authorization: Bearer <api_key>` on the upstream request. Strips any `Authorization`, `Cookie` and `api_key` query parameter supplied by the browser before forwarding; the browser's credentials never reach a remote and the remote key never reaches the browser. Does not buffer or parse bodies. Preserves status codes, `Content-Type`, and SSE/streaming responses.
- **WebSocket:** `/api/remote/{server}/ws/...` and any other upgraded path is bridged using `nhooyr.io/websocket` (already a dependency): accept the browser socket, dial the remote with the key, then pump frames both ways, propagating close codes and reasons. Cover both the terminal socket and the dashboard feed socket.
- **Errors:** unknown server id returns 404. Dial or connect failure returns 502 with `{"error":"remote unreachable","server":"<id>"}`. A remote 401/403 returns 502 with `{"error":"remote rejected API key","server":"<id>"}` and marks the server `unauthorized`. The proxy must never fall back to local handling.
- **Timeouts:** connect timeout 5s; no overall timeout on streaming and WebSocket requests; 30s response-header timeout on ordinary requests.
- **SSRF:** validate the registered URL with the existing `httputil.ResolveAndValidateURL`, which blocks private and reserved addresses. Add an explicit per-server `allow_private` flag (default false, settable only through the registry API) so LAN and tailnet servers can be registered deliberately. Re-check the resolved IP at dial time, not only at registration, to avoid DNS rebinding.
- **Localhost remotes:** a remote on `127.0.0.1` skips key auth on its side. Allow it only through the same `allow_private` flag.
- **Path safety:** reject `..` segments and anything that would make the upstream path escape `/api` or `/static`. Only `/api/**` is proxied.

### 3. Merged agent list and live feed

- The hub polls each online remote's live-session list (the same endpoint the sidebar uses) every 5s with jitter, and keeps the latest result in memory. Slow or failing remotes must not block others (per-remote goroutine, per-request timeout).
- The hub's own dashboard WebSocket additionally opens one upstream feed socket per online remote (the same bridge as above, hub-initiated) and rebroadcasts the events to browsers tagged with `server`. If the feed cannot be established it falls back to polling.
- The list API for the frontend returns each entry with `server` set (`local` for hub agents). Unreachable remotes contribute a single placeholder row per server with `status` and the last known agents marked `stale: true` (greyed out, not actionable).
- Reconnect with backoff (1s to 30s) after failures. Reflect status changes to the UI within one poll interval.

### 4. Frontend

- **Identity:** state, DOM ids, selection, URL routing (`/agent/{name}`) and local storage keys become `(server, name)`. Routes for remote agents use `/agent/{server}/{name}`. The legacy `/agent/{name}` keeps meaning `local`. Popout windows follow the same shape.
- **Routing helper:** add `serverBase(server)` returning `""` for `local` and `/api/remote/{server}` otherwise. `apiFetch` takes an optional `server` option and prefixes the URL. The terminal WebSocket (`xterm_renderer.js`) and the dashboard feed (`websocket.js`) use the same helper.
- **Audit raw `fetch(` calls:** about 30 files call `fetch` directly. Every call that targets a per-agent or per-team resource must go through the helper with the right server. Calls that are intentionally hub-local (settings, licence, the servers registry itself) stay local and are listed in a comment at the top of `api.js`.
- **Sidebar:** group or badge by server. Local is shown first and unlabeled when there are no remotes, so a hub with zero remotes looks identical to today. Show status dots for each server and a "unreachable" state.
- **Servers settings page:** add, edit, test and remove servers. The API key field is write-only.
- **Boards, tasks, notes, files, history, git:** all per-agent and per-team views work through the helper once routing is correct.
- **Aggregate views** (cost dashboard, analytics) show local data only, with a short "this server only" note when remotes exist.

### 5. Launching on a remote

- The launch modal (and every launch entry point in `modals.js`: agent, team, terminal, default agent, board launch, workflows) gets a **Server** dropdown, default `local`. Launches initiated from inside a remote agent, team or board inherit that server and hide the dropdown.
- Launch requests go through the proxy to the remote's `POST /api/sessions/launch` and `/api/sessions/launch-team`. The remote does everything else (tmux, board subscription, prompts).
- **Remote filesystem:** the working-directory picker calls `/api/filesystem/list` and `/api/filesystem/is-git` through the proxy for the selected server. Paths are always interpreted on the target server. Never send a local path to a remote.
- **Per-server options:** agent types, installed CLIs, prerequisites, presets and worktree options are fetched from the selected server and reloaded when the dropdown changes.
- **Failure:** if the remote is unreachable or rejects the key, show the error and do not launch anything. Never fall back to local.

## Compatibility

- Remotes need no changes. Hub-to-remote calls use the same API the browser uses today.
- The remote's `/api/health` response does not carry a version. Add a `version` field to it (additive and backward compatible). The hub treats a remote with no version as supported and surfaces a `version_mismatch` warning only when a remote reports a version older than the hub's declared minimum.
- Hubs with no registered remotes behave exactly as today.

## Security

- Remote API keys are stored encrypted (see 1a). They are never returned by any API, never logged, and never sent to the browser. Redact them in debug logs, including `DebugRequestLogger`.
- The proxy is an authenticated hub endpoint behind the hub's normal auth. A remote-facing client must not be able to use `/api/remote/*` to reach arbitrary hosts: the target is always a registered server, never a URL taken from the request.
- Apply SSRF checks at registration and at dial time (see Proxy).
- Hub operations that would be dangerous if proxied blindly (launching, killing, sending keys) are only reachable by whoever can already use the hub's own UI.
- Document that the hub holds keys that grant full control of each remote.

## Work packages

Dependencies: WP1 and WP2 first. WP3 needs WP1 and WP2. WP4 and WP5 need WP2. WP6 needs WP4 and WP5. WP7 needs WP2 and WP4.

| WP | Scope | Owner area |
|---|---|---|
| WP1 | Registry table, store layer, `/api/servers` routes, health check, key redaction, version field in `/api/health`, key encryption at rest (1a) and its tests | `internal/store`, `internal/server/routes`, new `internal/secretbox` package |
| WP2 | HTTP and WebSocket proxy, SSRF and dial-time checks, error mapping, tests with httptest remotes | `internal/server/routes`, `internal/httputil` |
| WP3 | Background poller and cache of remote live sessions, per-remote feed sockets, rebroadcast with `server` tag, stale handling | `internal/background`, `internal/server` |
| WP4 | Frontend routing: `(server, name)` identity, `serverBase`, `apiFetch`/WebSocket changes, URL routing, audit of raw `fetch` calls | `frontend/static` |
| WP5 | Sidebar and Servers settings UI, server badges, unreachable state | `frontend/static`, templates |
| WP6 | Launch modal server picker, remote filesystem browsing, per-server options, launch error handling | `frontend/static/modals.js` |
| WP7 | End-to-end tests: hub plus two real remote servers (isolated data dirs), covering list merge, chat and terminal via proxy, launch on a remote, offline remote, key rejection | `coral-go` tests |

## Acceptance criteria

1. With no remotes registered, the UI and APIs behave exactly as before.
2. A remote can be added with a valid key; a wrong key shows `unauthorized`; an unreachable URL shows `unreachable`; the key is never visible in any API response or log.
3. The stored `api_key` column contains only `v1:` ciphertext. A raw dump of `sessions.db` contains no plaintext key. Swapping ciphertexts between rows, a wrong master key, and a missing key file each produce `key_unreadable` for the affected server without crashing the hub.
4. A remote's agents appear in the hub sidebar with a server badge and update live (state changes within 5s).
5. Opening a remote agent shows its chat history and a working interactive terminal, entirely through `/api/remote/{server}`.
6. Two agents with the same name on different servers are separate selectable items.
7. An agent or team can be launched on a remote from the hub, in a directory chosen by browsing that remote's filesystem, and runs there.
8. Taking a remote offline marks its agents stale without breaking the sidebar or local agents; bringing it back recovers without a reload.
9. The proxy refuses unregistered servers, path traversal, and private or reserved addresses unless `allow_private` is set on that server.
10. `cd coral-go && go test ./...` passes, including new proxy and registry tests.

## Testing notes

- Follow CLAUDE.md: tests and manual servers use a separate `CORAL_DATA_DIR` per server (for example `/tmp/coral-hub`, `/tmp/coral-remote-a`, `/tmp/coral-remote-b`) and non-default ports. Never touch `~/.coral/`.
- Build with `-tags dev` for test servers so EULA and licence checks are skipped.
- Remotes in tests must require an API key, so bind them to a non-loopback address or call them through a test harness that does not hit the localhost auth bypass. Add a test proving the bypass is not what makes the proxy work.
- Encryption tests: round-trip, tamper detection (flip a byte), AAD binding (move ciphertext between ids), wrong key, missing key file with existing rows, key-file permission refusal, `CORAL_SECRET_KEY` override, and rotation including a simulated mid-rotation failure. Assert no plaintext key appears in the DB file bytes, API responses or captured logs.
- Cover WebSocket bridging with real sockets, including close propagation and a remote that drops mid-stream.

## Open questions

1. Should `allow_private` be per server, as specified, or a global setting?
2. Should the hub hide its own local agents when it is a pure hub (a "hub only" display setting), or always show them?
3. Do we want a short-lived per-hub token on remotes later, instead of handing out the full API key?
4. What minimum remote version do we declare once `/api/health` carries a version?
5. Should the master key optionally live in the OS keychain (macOS Keychain, Windows Credential Manager) instead of a `0600` file? The file is the portable default for v1.
