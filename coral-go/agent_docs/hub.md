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

A remote's own 403 (a forbidden path, an access refusal, ...) is passed through unchanged. Requests are never handled locally as a fallback. Targets are always registered servers, and private addresses need `allow_private`.

Connections are reused: all remotes share one pooled transport (idle connections kept 5 minutes), and the background `/api/health` poll uses the same pool, so a request after a quiet spell usually skips a new TCP/TLS handshake.

Compression: the browser's `Accept-Encoding` is passed through, and a gzip response is relayed unchanged (the hub neither adds nor decodes compression).

### Coral Hub tunnel refusals

Through a Coral Hub tunnel the machine's connector can refuse a request. These are answers, not outages: none marks the machine `unreachable`.

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

`version` in `/api/health` (and in the Coral Hub tunnel Hello) is the release version, or for a `make dev` / `make build` build `<next patch>-dev+<commit>[.dirty]`; a plain `go build` reports `0.0.0-dev+<commit>`, which hubs treat as very old.

The Coral Hub may add `coral_version`, `latest_coral` (no `v`) and `update` (`current`, `available`, `required` or `unknown`) to a `servers` entry. The dashboard shows nothing for `current` or `unknown`. For `available` and `required` it shows an **Update** / **Update required** badge on the server row, a line in its tooltip and in the Servers dialog, all linking to the latest GitHub release. Nothing is disabled.

Archive needs v1.4.2 on the machine: the dashboard offers **Archive** only for rows that carry the `archived` field, so it is hidden for older remotes. A machine lists archived agents last and never reports them as waiting for input; the hub's per-machine `agents` count excludes them.

## Connecting a server to Coral Hub

Coral connects to Coral Hub only when it is started with `--cloud-url <hub origin>` (or `CORAL_CLOUD_URL`). Connecting links this server to your Coral Hub account; only someone at this computer, as the user Coral runs as, can do it. The dashboard's **Settings > Coral Hub** does it from a browser on this computer. Opened from another computer, every `/api/cloud` route answers `403` with `code: local_dashboard_required` and `command: coral cloud connect`.

### A headless server (SSH)

On the server, in a terminal (for example over SSH), as the user Coral runs as:

```
coral cloud connect
```

It prints `Open https://app.coralai.ai/enroll?code=ABCD-EFGH`; the code is filled in on that page, and you approve it from any computer. The command waits, then asks the most Coral Hub may do on this server: **View** (the default: read everything, including every agent's terminal output and chat and all board chat on every team, and change nothing), **Interact** or **Launch**, and for Launch whether Coral Hub may turn on auto-accept. Ctrl-C cancels the enrollment (it never undoes a connection). An expired code is offered again, with Coral Hub's note that it is in closed beta (an account outside the beta can't sign in, so its code just expires). A request denied in Coral Hub ends at once (`Not connected: the request was denied in Coral Hub`); a Coral Hub that is busy (`429`) says how long to wait, from its `Retry-After`, and nothing is retried automatically. A server that is already connected (or was revoked) is left as it is (exit `5`); `coral cloud connect --force` forgets its enrollment on this computer and connects again: the old server entry stays in Coral Hub until you revoke it there. `coral cloud status` shows the state; `coral cloud disconnect` unlinks the server. All three take `--url` (this computer only) and `--home` (if Coral runs with one).

Exit codes: `0` connected, `2` a permanent problem (no Coral Hub configured, a bad file, a credential the Hub refused), `3` the terms must be accepted again, `4` try again (the Hub or this Coral could not be reached, the Hub is busy or had a server error, an expired code, a denied request), `5` already enrolled (nothing was changed), `130` cancelled, `1` anything else.

Alternatively, forward the dashboard and use it as if you were on the server: `ssh -L 8420:localhost:8420 server`, then open `http://localhost:8420` (Settings > Coral Hub).

### At startup

When Coral is started with a Coral Hub URL **given explicitly** (`--cloud-url` or `CORAL_CLOUD_URL`) and is not connected yet, it runs the same flow once the dashboard is up, with `--cloud-connect`:

- `prompt` (the default): on a terminal, it asks "Connect this server to Coral Hub now?", shows the link and code, waits, then asks the access mode. `s`, or Ctrl-C while it waits, skips it; the server keeps running on its own. Without a terminal (a service, a pipe, `&`) it never waits for input: it does what `auto` does.
- `auto`: it logs `[CLOUD] To connect this server to Coral Hub, open <url> and enter code XXXX-XXXX` (stderr and `coral.log`), and a new code when one expires, for an hour. Approve it from the journal; the access stays View.
- `off`: nothing; use `coral cloud connect` or the dashboard.

A server that is already connected skips the step and logs `[CLOUD] Coral Hub: this server is connected as …`.

### How the command line proves who it is

Loopback requests skip the API key, so the `/api/cloud` routes check who is asking. On Linux Coral refuses another local user outright (`403`, `code: other_user`; see below). Then `coral cloud` must prove it can read Coral's command-line token, which Coral writes at each start to `<data folder>/cli-token` (mode `0600`). The token itself is never sent:

1. `POST /api/cloud/cli-hello {"nonce": c}`: Coral answers `{"nonce": s, "proof": HMAC(token, "server", c, s)}`. The command checks the proof before it sends or reads anything else, so a program squatting on the Coral port gets neither the token nor a credential, and a credential file is not even read (exit `4`).
2. Every request then carries `X-Coral-CLI-Auth: s.HMAC(token, "client", c, s, method, path, sha256(body))`, accepted once, within 30 seconds of the hello.

## Enrolling with a one-time credential (Cloud Hub)

A machine can join a Coral Hub without a browser, with a one-time credential the Hub vends (a launch-anywhere bootstrap credential `chb_…`, or a Coder workspace ticket `cht_…`). Coral needs `--cloud-url <hub origin>` (https only) and redeems the credential once; the result is shown in **Settings > Connect to Coral Hub** (`GET /api/cloud/status` → `bootstrap: {status, detail, at}`).

Where the credential comes from (never from an environment variable or the command line):

- **Linux, at startup:** `/run/coral/bootstrap.d/credential`. `/run` and `/run/coral` must be owned by root and not writable by group or others; `bootstrap.d` must be owned by the user Coral runs as, mode `0700`, on a **tmpfs**; the file owned by that user, mode exactly `0600`, one link, at most 4096 bytes, no symlink anywhere on the way. Other names in the directory are ignored.
- **`coral --bootstrap-file <path>`** (any platform with Unix permissions; macOS has no `/run`): the same file checks; its folder must be yours (or root's) and not writable by others. A warning is logged when it is not on a tmpfs.
- **`coral cloud connect --ticket-file <path|->`** against the running server on this computer (it redeems, so it is the only writer of the identity). The command reads the file itself, as the user who runs it, with the same checks, and hands the credential over in an `Authorization` header (and a proof's contents inline), only once Coral has proved who it is (see above): the server never opens a path a caller names. It first asks the server whether it could proceed and reads nothing when it could not. The file stays on disk until the Hub has answered: it is removed once the server enrolled or the Hub refused the credential, and kept when the Hub could not be reached, so running the same command again retries (exit `4`). `-` reads the credential from stdin; `--url` must be this computer. Exit codes as above.

Rules Coral keeps:

- A startup file is read once and **removed right after the read**; if it cannot be removed, the credential is not used. (`coral cloud connect --ticket-file` removes its file once the Hub has answered, above.) A file that does not contain a credential is left as it is.
- **No network call before the file is validated.** A machine that is **already enrolled** never sends the credential (`ignored_existing_identity`): the startup file is removed (a `--bootstrap-file` only if it holds a credential), and `coral cloud connect` leaves its file alone.
- The Hub's refusals are one generic answer (`credential not valid: … ask for a new one`), never retried; after an attempt whose answer was lost it reads `the credential is no longer usable here: ask the automation for a new one`. `429` (honouring `Retry-After`), `5xx` and lost connections are retried a few times with **one redeem id**, so a retry after a lost answer gets the same identity; the credential is kept only in memory.
- The new identity is stored only if the Hub names the configured origin, mode `view`, and accepted **exactly this Coral's terms version**; that acceptance is recorded locally (`source=hub`). The local access cap is never changed: it stays View until you raise it.
- **Only Coral's own user can change the connector.** Loopback requests skip the API key, so the `/api/cloud` routes check who is asking. On Linux, Coral refuses cloud changes (and the status, which shows an enrollment's code) from any other local user, including a pod sidecar running as another uid (`403`, `code: other_user`). A program running as Coral's own user cannot be told apart, and it could read Coral's files anyway. Beyond that, the dashboard keeps its browser rules, and `coral cloud` proves it can read `<data folder>/cli-token` (mode `0600`, so only Coral's user can read it; use `--home` if Coral runs with one) without sending it (see "How the command line proves who it is").
- `--proof-file <path>` (bootstrap credentials): a JSON object of at most 16 KiB written by a provider helper, one of `{"kind":"aws","document","signature"}`, `{"kind":"gcp","token"}`, `{"kind":"kubernetes","token"}` (Azure is not available yet). Coral checks only that shape, sends it unchanged and never logs it.

## Identity

An agent or team is identified by `(server, name)`. In the UI a remote key is `@{server}/{name}` and a popout is `/agent/{server}/{id}`. Names are unique only within one server.

## Launching

Send `POST /api/remote/{server}/api/sessions/launch` or `/launch-team` to start an agent or team on a remote. Paths in the body (such as `working_dir`) are interpreted on that remote. Use `/api/remote/{server}/api/filesystem/list` to browse the remote's folders.
