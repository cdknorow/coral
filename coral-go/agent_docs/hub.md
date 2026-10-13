# Multi-Server Hub API

A Coral server started with `--hub` (or `CORAL_HUB=1`) can register other standalone Coral servers (remotes), show their agents in its dashboard and proxy calls to them. Without hub mode none of these routes exist (404). `GET /api/health` reports `"hub": true|false`.

User guide: [Multi-server hub](https://cdknorow.github.io/coral/multi-server-hub/).

## Registry (hub only)

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/servers` | List servers: `id`, `label`, `url`, `status`, `last_seen`, `last_error`, `allow_private`. Never returns the API key. |
| POST | `/api/servers` | Add `{id, label, url, api_key, allow_private?}`. The hub calls the remote's `/api/health` with the key before saving. |
| PATCH | `/api/servers/{id}` | Edit label, URL, `allow_private` or key (the key is write-only; omit to keep it). |
| DELETE | `/api/servers/{id}` | Remove. |
| POST | `/api/servers/{id}/test` | Re-run the connectivity and key check. |

`id` is a slug (`[a-z0-9-]`, up to 32 characters, not `local`).

Status values: `online`, `unreachable`, `unauthorized` (the remote rejected the key), `key_unreadable` (the stored key cannot be decrypted).

API keys are encrypted at rest (AES-256-GCM, bound to the server id). The master key is `<data dir>/.remote_secret_key` (mode 0600) or `CORAL_SECRET_KEY`.

## Proxy

`/api/remote/{server}/{path}` forwards to `{remote url}/{path}` for `/api/**` paths and WebSockets (for example `/api/remote/ws1/ws/terminal/...`). The hub strips the browser's `Authorization`, `Cookie`, `Origin`, `Referer`, forwarding headers (`Forwarded`, `X-Forwarded-*`, `X-Real-Ip`) and `api_key`, and adds `Authorization: Bearer <remote key>`.

| Result | Meaning |
|---|---|
| 404 | Unknown server id |
| 403 `{"error":"path not allowed"}` | The path is not proxyable |
| 502 `{"error":"remote unreachable"}` | Connect or dial failure. The server is marked `unreachable`. |
| 502 `{"error":"remote rejected API key"}` | The remote answered 401. The server is marked `unauthorized`. |
| 502 `{"error":"remote address not allowed"}` | Private address without `allow_private` |

A remote's own 403 (demo limit, forbidden path, ...) is passed through unchanged. Requests are never handled locally as a fallback. Targets are always registered servers, and private addresses need `allow_private`.

Connections are reused: all remotes share one pooled transport (idle connections kept 5 minutes), and the background `/api/health` poll uses the same pool, so a request after a quiet spell usually skips a new TCP/TLS handshake.

Compression: the browser's `Accept-Encoding` is passed through, and a gzip response is relayed unchanged (the hub neither adds nor decodes compression).

### Cloud Hub tunnel refusals

Through a Cloud Hub tunnel the machine's connector can refuse a request. These are answers, not outages: none marks the machine `unreachable`.

| Result | Meaning |
|---|---|
| 403 `{"error":"path not allowed","code":"path_not_allowed"}` | The connector's route table refused the path or a query parameter (e.g. an older machine) |
| 503 `{"error":"remote busy","code":"too_many_streams"}` + `Retry-After: 1` | Concurrent-stream cap reached; retry |
| 400 `{"error":"bad request","code":"bad_request"}` | The connector could not accept the request as sent (header, method or body) |
| 403 `{"error":"local_mode","mode":"view"\|"interact"}` | The machine's own access setting is below what the hub allows |

A real transport failure still answers 502 `remote unreachable` and marks the machine. The tunnel forwards only an allowlist of request headers, never `Accept-Encoding`; the connector decodes a gzip response from the machine before relaying it and answers 502 `{"error":"unsupported content encoding"}` for any other encoding.

## Merged session list

`GET /api/sessions/live` returns a plain array when no remotes are registered. Otherwise it returns `{"sessions": [...], "servers": [...]}`; each `servers` entry has `id`, `label`, `status`, `agents` and, when known, `url`, `last_seen`, `last_error`, `version`. Each session has `server` (`local` or the remote id). Sessions from an offline remote carry `stale: true`.

The `/ws/coral` feed tags updates with `server` and sends `removed_remote` when a remote's sessions disappear.

### How the hub follows a remote (hub mode only)

Remote-agent tracking runs only in hub mode. For each remote the hub holds a `/ws/coral?feed=hub` socket (a machine feed: it does not consume the remote's UI notifications). While that feed is live, the ~5s background poll only calls `GET /api/health`, which returns exactly `{status, version, hub}`. The full `GET /api/sessions/live` list is polled again when the feed drops, or when no feed message (including `coral_heartbeat`, sent after 15s of silence) arrived for 30s. A remote whose `/api/health` answers 404 is always list-polled. The merged view is cached and rebuilt once per change.

### Versions and archived agents

`version` in `/api/health` (and in the Cloud Hub tunnel Hello) is the release version, or for a `make dev` / `make build` build `<next patch>-dev+<commit>[.dirty]`; a plain `go build` reports `0.0.0-dev+<commit>`, which hubs treat as very old.

Archive needs v1.4.2 on the machine: the dashboard offers **Archive** only for rows that carry the `archived` field, so it is hidden for older remotes. A machine lists archived agents last and never reports them as waiting for input; the hub's per-machine `agents` count excludes them.

## Identity

An agent or team is identified by `(server, name)`. In the UI a remote key is `@{server}/{name}` and a popout is `/agent/{server}/{id}`. Names are unique only within one server.

## Launching

Send `POST /api/remote/{server}/api/sessions/launch` or `/launch-team` to start an agent or team on a remote. Paths in the body (such as `working_dir`) are interpreted on that remote. Use `/api/remote/{server}/api/filesystem/list` to browse the remote's folders.
