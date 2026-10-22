# Multi-Server Hub API

A Coral server started with `--hub` (or `CORAL_HUB=1`) can register other standalone Coral servers (remotes), show their agents in its dashboard and proxy calls to them. Without hub mode none of these routes exist (404). `GET /api/health` reports `"hub": true|false`.

### Turning hub mode on from Settings

**Settings > Run as a hub** (#3460) does the same as `--hub` for people who start Coral from the macOS or Windows app. It saves `hub_mode` and applies at the next start: `--hub` or `CORAL_HUB=1` turn hub mode on, and so does the saved setting; nothing else does. With `--hub` or `CORAL_HUB=1` the toggle shows as on and locked: to turn hub mode off, start Coral without them (and clear the setting). The desktop apps show a **Restart Coral** button, which starts a new instance and stops this one (agents keep running in tmux and are found again); a server started from a terminal says to quit and start it again.

- `GET /api/system/hub-mode` → `{saved, running, forced, restart_required, can_restart}`; `PUT` with `{"enabled": bool}` saves it. `POST /api/system/restart` restarts where the process can (`202`), otherwise `409 restart_unavailable`. All three answer only the dashboard on this computer, as the user Coral runs as: never through Coral Hub (`403`), never another local user (`403 other_user`). `PUT /api/settings` refuses `hub_mode` (`400`).
- Hub mode adds the routes below on Coral's own listener: the same host, port, TLS and sign-in (the API key from other computers; Remote Access decides whether other computers can connect at all). Nothing new listens.
- Turning it off only unroutes them: the registered servers and their keys stay in the database and are back when hub mode is.

User guide: [Multi-server hub](https://subgentic.github.io/coral/multi-server-hub/).

## Registry (hub only)

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/servers` | List servers: `id`, `label`, `url`, `status`, `last_seen`, `last_error`, `allow_private`, and `warning` (only for a server reached over plain http at an address that is not this computer: "the key crosses the network unencrypted; use https or a tunnel"). Never returns the API key. The Operator's: an agent's session token is refused. |
| POST | `/api/servers` | Add `{id, label, url, api_key, allow_private?}`. The hub calls the remote's `/api/health` with the key before saving. A server not marked `allow_private` must be `https`. |
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

### Identity through the tunnel

The tunnel carries no proof of who the Hub user is, so on boards (#3377, #3404), for requests the connector executed:

| Request | Answer |
|---|---|
| A chat post, `POST /api/board/{p}/messages` | The sender is the `X-Coral-Hub-User` header (`hub-user-` + 8 hex), never a name from the body, stored with `origin: "hub_user"` and `hub_user`. If the post asks to be the `Operator` (`subscriber_id` or `as`), it's sent as the **Operator** (see the next row) with `origin: "hub_operator"` and `hub_user`, and its text starts `[Operator via Coral Hub, hub-user-…]` so agents can tell it from the local Operator. A malformed or repeated header gets 400 `hub_user_required`; a name that's already a member or role on the board gets 409. No auto-subscribe, Operator included. Names starting `hub-user-` are reserved for this. A post with **no** header gets 400 `hub_user_required` (the allowance for an older `pkg/hubproxy` ended with #3743). |
| `Operator` in any identity field of a board write (task create and amend, with or without an `assignee`, complete others' tasks, cross-team posts; reassign is never reachable through the tunnel) | Allowed by default at Interact or Launch, as before v1.4.18, with the Hub user: no `X-Coral-Hub-User` gets 403 `hub_user_required`, a malformed or repeated one 400, a member's name 409. Each use (and, since #3428, every board write through the tunnel other than a chat post) is logged in coral.log and recorded with the Hub user and the outcome (`recent_actions` in `GET /api/system/hub-operator`, from this computer). A cross-team post's text starts `[Operator via Coral Hub, hub-user-…]`. The owner can opt out with **Don't let Coral Hub act as the Operator** (Settings › Coral Hub; `PUT /api/system/hub-operator {"disabled": true}`, this computer only; kept when the Hub link is removed): then 403 `operator_identity_required`, and chat posts are sent as the Hub user. Honoured only on a request the connector stamped, while linked. The hub can read the setting (`GET /api/system/hub-operator`, view tier: `{"disabled": bool}`) but never change it. The connector and `pkg/hubproxy` forward `X-Coral-Hub-User` on board writes only (`/api/board/{p}/…`, not `GET` or `HEAD`). |
| Any other board write (everything except a chat post) naming an actor: `subscriber_id`, `session_id`, `as`, `created_by` or `job_title` (#3428) | The actor must be the `Operator`. A name that is an existing member or role of the board (compared with case, spacing and invisible characters ignored) gets 409 `member_identity`, the same as a chat post's; any other name gets 403 `hub_identity_required`. So a Coral Hub user can't claim, complete, rename, unsubscribe or wait as a member, with the opt-out on or off. `assignee` and `assigned_to` (who a task is for) may name anyone but the Operator rule above. In `POST` or `DELETE …/reminder`, `subscriber_id` is the reminded subscriber, a target, not an actor. |
| A board write that names no actor (pause and resume, which need **Launch**; a reminder, at Interact) | Is the Operator's: allowed by default, 403 `operator_identity_required` with the opt-out, 403 `hub_user_required` with no `X-Coral-Hub-User`. |
| Acting as an orchestrator (an identity with "orchestrator" in it, or a subscriber with orchestrator or peek rights), or `receive_mode: "all"` | 403 `orchestrator_identity` |
| `PUT /api/board/{p}/settings` | needs **Launch** and is this computer's owner's alone (#5307): the connector stamps the owner marker (`X-Coral-Hub-Role: owner`, set by the hub for the account this computer is linked to) and the machine checks it, the enrolled owner label (it must have been recorded at enrolment: a machine linked before that refuses until it is linked again) and that the Hub may act as the Operator; anyone else, a share grantee at any tier included, gets 403 `owner_only`. The body is strict JSON with known keys only (400 otherwise), at most 16 KB (413 `too_large`), and the reply is `Cache-Control: no-store`. Saving the health setup types nothing and stops nothing by itself: it changes what the health monitor and review auto-delegation do afterwards (automated nudges typed into the team's agents, review tasks handed to the named reviewers); it never kills, restarts, sleeps or reassigns an agent. Only the `health` part may be saved: the cross-team fields (`cross_team_messaging`, `cross_team_servers`, `cross_team_artifacts`) stay on this computer (403 `local_only`, nothing saved). `GET /api/board/{p}/settings` (view) leaves `health` out for everyone but the owner, who keeps it. |
| `GET /api/settings/prompt-inspection` (the global prompts, read only; the `board` query only) | needs **Launch** and is the owner's read alone (403 `owner_only` for anyone else, the same owner-label rule as above); read only, `Cache-Control: no-store`, `board` at most 128 characters, reply at most 1 MiB (#5307) |
| `DELETE /api/board/{p}` (delete the whole board: its messages, tasks and members; can't be undone) | needs **Launch** (Interact gets 403 `local_mode` or `path_not_allowed`), and is the Operator's: attributed to the Hub user, recorded, refused with the opt-out (#3433) |
| `POST /api/sessions/live/team/{board}/kill` (stop every agent of a team at once) | needs **Launch** (#3433). So does stopping one agent (`…/{name}/kill`) and putting one to sleep (`…/{sessionID}/sleep`) since #4274, and waking, restarting or resuming one, and the team's sleep, wake and reset. Archive and unarchive (`…/{sessionID}/archive`, `…/unarchive`) stay at Interact |
| `PUT /api/board/{p}/working-mode` and `POST /api/board/{p}/working-mode/presets` (what every agent of the team is told); editing a preset (`PUT …/presets/{id}`) and resetting one are never reachable through the tunnel | Interact, the Operator's: attributed to the Hub user and recorded in Coral Hub actions, refused with the opt-out |
| `POST /api/hub-auto-accept` with the closed body `{"allow": true}` or `{"allow": false}` (Coral Hub's owner turning **Coral Hub can turn on auto-accept** on or off) | needs **Launch**, and is the Hub owner's alone (the account this computer is linked to; the machine refuses anyone else, and a request from this computer, with 403 `owner_only`). Turning it off ends every auto-accept the hub had enabled. The change is recorded on this computer (who and when, in the status as `allow_hub_auto_accept_changed`) and in the Operator-actions log. The switch itself is on by default and an owner's off stays off. |
| `Idempotency-Key` on `POST /api/sessions/launch`, `POST /api/sessions/launch-team`, `POST /api/sessions/live/{name}/resume`, `POST /api/sessions/live/{id}/wake` (one agent's, not a team's) and `POST /api/tasks/run` (#5841) | A request through Coral Hub that carries the header (16 to 128 characters of letters, digits, `-` and `_`, one per action, reused on every retry of it) **and** the hub user's label (`X-Coral-Hub-User`, forwarded on these five routes only) runs once per route, hub user and key. A repeat while the first still runs gets 409 `request_in_progress` (with `Retry-After`); a repeat after the first succeeded gets the first answer again (same status and body, header `Idempotent-Replay: true`) and starts nothing (an answer over 1 MiB is not kept: the repeat is 409 `request_already_done`, and still starts nothing); the same key with another body or another query string is 422 `idempotency_key_reuse`; a malformed key is 400 `invalid_idempotency_key`. Only a 2xx answer is kept, so a retry after a refusal or a failure runs again. A key without a valid hub-user label is ignored (the request runs, never deduplicated, never against a shared caller), and a request that did not come through Coral Hub is not touched. The records are in memory for 10 minutes (at most 200 per hub user, 2000 in all, and 32 MiB of answers: the oldest finished ones go first): a restart forgets them. |
| An agent that skips permission prompts: `POST /api/sessions/launch`, `POST /api/sessions/launch-team` (any member, with its own flags or the team's), `POST /api/sessions/live/{name}/restart` (`extra_flags`, `capabilities`) and `POST /api/tasks/run` (`auto_accept`, `flags`) (#126 F4) | needs the Hub's provenance, the local switch **Coral Hub can turn on auto-accept** (on unless the owner turned it off; Settings > Network and Hub) and Launch mode (HTTP 403 with the body `{"code": "...", "error": "..."}`, the code `no_dashboard_provenance`, `hub_auto_accept_disabled` or `hub_mode_too_low` in the order of the checks, and nothing starts; the `hub_auto_accept_disabled` message says where on this computer to turn the switch on, and the codes are what Coral Hub shows the person). It counts as skipping when the request has capability `allow` rules, `tools`, `hooks`, or any flag other than these: `--model`/`-m`, `--permission-mode default\|plan`, Claude's and agy's `--effort`, agy's and Gemini's `--mode plan` and `--approval-mode default\|plan`, Codex's `-a`/`--ask-for-approval untrusted\|on-request`, `-s`/`--sandbox read-only\|workspace-write` and `-c model_reasoning_effort=<level>`. It's an allowlist, so a flag nobody listed counts too, and so does a flag whose agent type isn't known: a team launch whose member and team name no `agent_type` is checked as having none, so only `--model`/`-m` and `--permission-mode default\|plan` pass; name the type to use its own flags above. A team member that is exactly an agent of a team template you saved on this computer (every field: name, prompt, type, model, flags, capabilities, tools, hooks and knowledge) may keep its allow rules, tools and hooks without the switch; its flags are still checked, and a member that differs in any field refuses the whole team. The built-in templates (Coding Team and the others) give their members `allow` rules and aren't your choice, so through the Hub they need the switch; save one as your own template to launch it from the Hub with the switch off. Only what the request asks for is checked: an agent restarted (with no extra flags or capabilities), woken, resumed or resurrected with its stored settings isn't, and neither is the local default permission mode. So an agent started while the switch was on keeps its mode after the switch is turned off; stop it, or restart it from this computer with the mode you want. Requests from this computer are unchanged. |
| `GET /api/saved` and `POST`, `PATCH`, `DELETE /api/saved…` (the Operator's Save for later bookmarks, [saved-messages.md](saved-messages.md)) | The list and `GET /api/saved/{id}/message` (the whole text of a bookmark, redacted, at most 64 KiB, #5753) are View and **the owner's alone**: the connector stamps the owner marker and a hub user that is not the account this computer is linked to gets `403 owner_only`. Saving, marking done and removing are Interact for the tier and the owner's alone too (#5763): a grantee can neither read nor change the bookmarks. They are the Operator's own notes and change no work; an agent's token is refused. |

A read never changes anything (#3428): `GET /api/board/{p}/messages` through the tunnel ignores `subscriber_id` and `session_id` and is served like `messages/all`, so it never moves a member\'s read position (`messages/check` and the other reads change nothing). Requests from the computer itself are unchanged.

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

## What Coral Hub viewers see

**View access is the whole session.** A Coral Hub user at View reads everything the dashboard shows: every agent's terminal output (capture and the live terminal), chat and transcripts, tool events and pending tool calls, subagents, history and search, every board's messages, tasks and peeks, team knowledge, and workflow and scheduled-run output. Anything an agent printed or a person pasted (a `cat .env`, an environment dump, a token in a command) is in those replies. Grant View only to people who may see that.

**Tokens are hidden from viewers who can only look.** Before a reply leaves this computer for a Coral Hub viewer at View, the connector replaces what the betterleaks rules recognise (tokens and API keys in their known formats, credentials assigned to a secret-named variable, private keys) with `[redacted token]`. Viewers at Interact or Launch get every reply as it is: they can already run commands as this computer's user. The hub sends each request's viewer tier with the stream; it can't raise anything above this computer's own mode (the lower of the two counts), and a request without a tier counts as View. The tier changes only what is hidden: a credential file stays refused and a route that is never allowed stays refused at every tier. Every View reply is scanned except artifact downloads (byte-exact, their id is their hash: a token in an uploaded `.txt`, `.md`, `.log` or `.env` artifact is shown whole, so redact before you upload) and health, and binary types (images, audio, video, fonts, PDF, archives). On this computer nothing is hidden. Turn it off in **Settings > Coral Hub** ("Hide tokens from Coral Hub viewers who can only look"); only someone at this computer can.

**Paths are shown from the home folder.** Through Coral Hub a folder under this computer's home is shown as `~/...` in paths, in error messages and in commands (a workflow step's command and arguments, a script); logs and other local files' paths are left out. Prompts, chat and terminal output are shown as written.

Colour and other terminal codes around a token don't stop it from being found, in the live terminal and in captured output alike.

**Live streams too.** For a View viewer, the live session list (`/ws/coral`) is redacted message by message, and the live terminal (`/ws/terminal`) piece by piece, with nothing held back, so typing echoes at once. Each piece of terminal output is scanned with what was shown before it: a token that arrives in one piece is hidden, also at the end of a piece, and the rest of a token completed by a later piece is hidden. A token split across pieces shows its first piece, and a token typed key by key shows in full as it is typed (the operator's decision: the terminal is never delayed). If this computer's mode is lowered to View while a terminal is open, its next piece is redacted.

What it doesn't cover:
- **Tokens broken up in the terminal's output:** a token passes as it is when a colour code or cursor movement sits inside it, when a program wraps it onto a second line itself (a full-screen app drawing it in a box), or when the screen is redrawn a few characters at a time. A token the terminal window wraps at its edge is caught. A private key whose `END` line is broken up the same way shows as it is, like any other split token (nothing on the terminal is held back).
- **Other formats:** a token in a format not listed above passes as it is.
- A false match (a word that looks like a token, such as a package name starting with `sk-` and 20 or more characters long, like `sk-learn-contrib-…`) shows `[redacted token]` on the hub only. Turning redaction off or on applies within a second.

## Connecting a server to Coral Hub

Coral connects to Coral Hub only when it has a Coral Hub URL: `--cloud-url <hub origin>`, `CORAL_CLOUD_URL`, or a release build's default (`https://app.coralhub.dev`; `--cloud-url off` turns it off). The release build's default URL never connects, prompts or pops anything up by itself: a fresh install offers Coral Hub only on the home screen, in **Settings > Coral Hub** and through `coral cloud connect`, unless a URL is passed explicitly or Coral is started with `--cloud-connect` (see [At startup](#at-startup)). Connecting links this server to your Coral Hub account; only someone at this computer, as the user Coral runs as, can do it. The dashboard's **Settings > Coral Hub** does it from a browser on this computer. Opened from another computer, every `/api/cloud` route answers `403` with `code: local_dashboard_required` and `command: coral cloud connect`.

### A headless server (SSH)

On the server, in a terminal (for example over SSH), as the user Coral runs as:

```
coral cloud connect
```

It prints `Open https://app.coralhub.dev/enroll?code=ABCD-EFGH`; the code is filled in on that page, and you approve it from any computer. The command waits, then asks the most Coral Hub may do on this server: **View** (the default: read everything, including every agent's terminal output and chat and all board chat on every team, and change nothing), **Interact** or **Launch**, and for Launch whether Coral Hub may turn on auto-accept. Ctrl-C cancels the enrollment (it never undoes a connection). An expired code is offered again, with Coral Hub's note that it is in closed beta (an account outside the beta can't sign in, so its code just expires). A request denied in Coral Hub ends at once (`Not connected: the request was denied in Coral Hub`); a Coral Hub that is busy (`429`) says how long to wait, from its `Retry-After`, and nothing is retried automatically. A server that is already connected (or was revoked) is left as it is (exit `5`); `coral cloud connect --force` forgets its enrollment on this computer and connects again: the old server entry stays in Coral Hub until you revoke it there. `coral cloud status` shows the state; `coral cloud disconnect` unlinks the server. All three take `--url` (this computer only) and `--home` (if Coral runs with one).

**From a script:** `coral cloud connect` is the scripted way. The same actions over HTTP (`POST /api/cloud/connect` and the other `/api/cloud/` routes: cancel, disconnect, status, the access level, redaction, auto-accept) need the local dashboard (a browser on this computer, signed in) or the command line's signature; the API key alone answers 403 `local_dashboard_required`, by design, because connecting is for someone at this computer. Redeeming a one-time credential (`--ticket-file`) accepts only the command line.

Exit codes: `0` connected, `2` a permanent problem (no Coral Hub configured, a bad file, a credential the Hub refused), `3` the terms must be accepted again, `4` try again (the Hub or this Coral could not be reached, the Hub is busy or had a server error, an expired code, a denied request), `5` already enrolled (nothing was changed), `130` cancelled, `1` anything else.

Alternatively, forward the dashboard and use it as if you were on the server: `ssh -L 8420:localhost:8420 server`, then open `http://localhost:8420` (Settings > Coral Hub).

### At startup

With only the build's default URL, Coral does not offer Coral Hub on its own (#3459): no **Connect to Coral Hub** screen in the dashboard (or the macOS and Windows apps, which show the dashboard) after the terms, no prompt in the terminal and no enrollment code in the log. The offer is on, for a server that has a Coral Hub URL and is not connected yet, when a URL is **given explicitly** (`--cloud-url <url>` or `CORAL_CLOUD_URL=<url>`: like `prompt`) or with `--cloud-connect` (or `CORAL_CLOUD_CONNECT`):

- `prompt`: the dashboard opens its one-time first-launch **Connect to Coral Hub** screen, and on a terminal Coral asks "Connect this server to Coral Hub now?", shows the link and code, waits, then asks the access mode. `s`, or Ctrl-C while it waits, skips it; the server keeps running on its own. Without a terminal (a service, a pipe, `&`) it never waits for input: it does what `auto` does.
- `auto`: the first-launch screen, and Coral logs `[CLOUD] To connect this server to Coral Hub, open <url> and enter code XXXX-XXXX` (stderr and `coral.log`), and a new code when one expires, for an hour. Approve it from the journal; the access stays View.
- `off`: nothing, even with an explicit URL; use the home screen, **Settings > Coral Hub** or `coral cloud connect`.
- not set: `prompt` with an explicit URL, otherwise `off`.
- `CORAL_CLOUD_CONNECT` also takes `1`, `true`, `yes` or `on` (= `prompt`) and `0`, `false` or `no` (= `off`); any other value logs a warning and means `off` (a wrong `--cloud-connect` stops startup with an error).

**Setting the hub's access cap at launch.** `--cloud-max-mode view|interact|launch` (or `CORAL_CLOUD_MAX_MODE`) saves the same `cloud_max_mode` cap that Settings > Coral Hub shows ("Most the hub may do on this computer"), at startup, for a machine nobody sits at, such as a container or CI runner. It is the machine owner's statement, made by whoever starts the process: the hub, a tunneled request and `PUT /api/cloud/max-mode` (which still needs the local dashboard) cannot change the cap. Unset leaves the saved setting (view by default). A value that is not `view`, `interact` or `launch` stops Coral from starting (exit 2), so a cap is never silently left lower or made higher. Setting a cap below `launch` also ends any hub-enabled auto-accept, as lowering it in the dashboard does. It is applied at every start, so while the flag or `CORAL_CLOUD_MAX_MODE` is set, a cap changed in the dashboard is replaced by it at the next start (the log says so); remove the flag or variable to manage the cap from the dashboard instead. This does not change the hub side: a server still starts in View on the hub and its owner raises it there.

`GET /api/cloud/status` reports the result as `offer_at_launch`; the dashboard opens the first-launch screen only when it is true.

A server that is already connected reconnects on its own whatever the setting, skips the step and logs `[CLOUD] Coral Hub: this server is connected as …`.

### How the command line proves who it is

Loopback requests skip the API key, so the `/api/cloud` routes check who is asking. On Linux, macOS and Windows, Coral refuses another local user outright (`403`, `code: other_user`; see below). Then `coral cloud` must prove it can read Coral's command-line token, which Coral writes at each start to `<data folder>/cli-token` (mode `0600`). The token itself is never sent:

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
- **Only Coral's own user can change the connector.** Loopback requests skip the API key, so the `/api/cloud` routes check who is asking. On Linux, macOS and Windows, Coral refuses cloud changes (and the status, which shows an enrollment's code) from any other local user, including a pod sidecar running as another uid (`403`, `code: other_user`). A program running as Coral's own user cannot be told apart, and it could read Coral's files anyway. Beyond that, the dashboard keeps its browser rules, and `coral cloud` proves it can read `<data folder>/cli-token` (mode `0600`, so only Coral's user can read it; use `--home` if Coral runs with one) without sending it (see "How the command line proves who it is").
- `--proof-file <path>` (bootstrap credentials): a JSON object of at most 16 KiB written by a provider helper, one of `{"kind":"aws","document","signature"}`, `{"kind":"gcp","token"}`, `{"kind":"kubernetes","token"}` (Azure is not available yet). Coral checks only that shape, sends it unchanged and never logs it.

## Identity

An agent or team is identified by `(server, name)`. In the UI a remote key is `@{server}/{name}` and a popout is `/agent/{server}/{id}`. Names are unique only within one server.

## Launching

Send `POST /api/remote/{server}/api/sessions/launch` or `/launch-team` to start an agent or team on a remote. Paths in the body (such as `working_dir`) are interpreted on that remote. Use `/api/remote/{server}/api/filesystem/list` to browse the remote's folders. The New Agent dialog's prerequisite check goes through `/api/remote/{server}/api/system/cli-check?type=<agent type>` (Launch tier; the reply has no path, see [CLI Check](settings-system.md#cli-check)).
