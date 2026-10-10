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

`/api/remote/{server}/{path}` forwards to `{remote url}/{path}` for `/api/**` paths and WebSockets (for example `/api/remote/ws1/ws/terminal/...`). The hub strips the browser's `Authorization`, `Cookie` and `api_key`, and adds `Authorization: Bearer <remote key>`.

| Result | Meaning |
|---|---|
| 404 | Unknown server id |
| 502 `{"error":"remote unreachable"}` | Connect or dial failure |
| 502 `{"error":"remote rejected API key"}` | The remote answered 401. The server is marked `unauthorized`. |

Requests are never handled locally as a fallback. Targets are always registered servers, and private addresses need `allow_private`.

## Merged session list

`GET /api/sessions/live` returns a plain array when no remotes are registered. Otherwise it returns `{"sessions": [...], "servers": [{id, label, status}]}`. Each session has `server` (`local` or the remote id). Sessions from an offline remote carry `stale: true`.

The `/ws/coral` feed tags updates with `server` and sends `removed_remote` when a remote's sessions disappear.

## Identity

An agent or team is identified by `(server, name)`. In the UI a remote key is `@{server}/{name}` and a popout is `/agent/{server}/{id}`. Names are unique only within one server.

## Launching

Send `POST /api/remote/{server}/api/sessions/launch` or `/launch-team` to start an agent or team on a remote. Paths in the body (such as `working_dir`) are interpreted on that remote. Use `/api/remote/{server}/api/filesystem/list` to browse the remote's folders.
