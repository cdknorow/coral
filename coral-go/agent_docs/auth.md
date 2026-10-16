# Authentication API

Coral uses a layered authentication system:

1. **Localhost bypass** — A request that comes straight from a process on this machine (a `127.0.0.1` / `::1` peer that carries no reverse-proxy header) is authenticated without credentials. A request a same-machine reverse proxy forwarded is **not**: it carries a forwarding header (anything in the `X-Forwarded-*` or `Tailscale-*` families, hyphen or underscore spelling, or `Forwarded`, `Via`, `X-Real-IP`, `True-Client-IP`, `CF-Connecting-IP`, `X-Original-Forwarded-For`), so it needs the key or a session like a remote caller. (Configure the proxy to send the standard hyphenated header names.) The same "direct local client" test gates Coral's local-only powers — the file explorer's relaxed bounds (any file you can read, versus the strict session/workspace roots), connecting or changing Coral Hub from the command line, opening a desktop editor, and deleting the input log — so a proxied request gets the strict rules there too. **While `require_auth_on_loopback` is on, loopback requests become strict-local, not remote:** a direct-local request is admitted only with strict local auth's exact credential set — an agent session token (`X-Coral-Session-Token`), a dashboard session cookie, a signed command-line request, an API key, or an authentic Coral Hub tunnel stamp — and a tokenless one gets 401. So the dashboard (after signing in), `coral open`, the signed `coral` command line, agents, the `coral-hook-*` binaries and `coral-board` keep working, while a **tokenless terminal session and a bare `curl http://localhost:8420/...` with no credential stop working** (they get 401; add the key, e.g. `-H "Authorization: Bearer <key>"`). A request a proxy forwarded stays remote (key or session only), so even a valid agent session token on a proxied request is refused. The file explorer, editor-open and input-log delete use the strict, non-local rules for every request, the Operator's own browser included. The trust keys on the connection's peer address, never on the `Host` header. A proxy that strips those headers still looks direct; for that case turn on **`require_auth_on_loopback`** (Settings; default off; Operator-only, never settable through the Coral Hub), which makes even a plain loopback request authenticate. When it is on, the dashboard login, the signed `coral` command line and agent tokens still work, but a tokenless terminal session (default mode) stops reaching Coral over HTTP. If it ever locks you out, restart Coral with `CORAL_REQUIRE_AUTH_ON_LOOPBACK=off` (an empty value is ignored; use `off` or `on`) — it overrides the stored setting at startup without any HTTP call — then clear the setting in Settings so it stays off.
2. **API key** — An auto-generated key stored in `~/.coral/`. Remote clients pass it via `Authorization: Bearer <key>` header or `X-API-Key` header.
3. **Session cookie** — After validating an API key via `POST /auth/key`, a session cookie is set for browser access. It is `HttpOnly`, `SameSite=Lax`, lasts 24 hours, and is `Secure` when the browser reached Coral over https: over TLS directly, or through a trusted reverse proxy that sends `X-Forwarded-Proto: https`. A proxy on this machine (a loopback peer) is always trusted; list others in `CORAL_TRUSTED_PROXIES` (comma-separated IPs or CIDRs, e.g. `10.0.0.5,192.168.1.0/24`). The header is ignored from any other peer.

   **Behind a reverse proxy:** a change from a browser (POST, PUT, PATCH, DELETE) must come from Coral's own page: its `Origin` must name the host the browser reached, which is the request's `Host` or, from a trusted proxy, `X-Forwarded-Host`. Coral reads the LAST `X-Forwarded-Host` value (the one your proxy added), so configure the proxy to overwrite the header rather than append to a client's (nginx: `proxy_set_header X-Forwarded-Host $host;`), or to pass the original `Host` (`proxy_set_header Host $host;`). A request with `Origin` or `Sec-Fetch-Site` repeated is refused (403 `foreign_origin`): a browser never sends either twice.

## Auth Page

```
GET /auth
```

Serves the HTML authentication page where users can enter their API key.

**Response:** HTML document.

## Validate API Key

```
POST /auth/key
```

Validates an API key and sets a session cookie on success.

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

## Agent identity and sign-in links

Coral is moving from "anything on this computer is trusted" to "every caller says who it is". Phase 1 (#3387) only **observes** and reports what a stricter mode would break. Phase 2 (#3418) adds that stricter mode as an **opt-in**: [strict local auth](#strict-local-auth-opt-in), off by default.

### Per-session agent tokens

Every agent session (not a terminal session; see below) gets a random token when Coral launches, restarts, resumes or wakes it. Coral's own launch line reads the token from a single-use file (0600, random name, in `<data>/session_tokens/`) into `CORAL_SESSION_TOKEN` and deletes the file before the agent CLI starts. The token is never typed or echoed in the terminal, never in a command line (`ps`), a log or the board, and never in the tmux server's environment; only that pane's shell and what it starts have it. Restarting or resuming mints a new token and the old one stops working.

`coral-board`, `coral-agent` and the `coral-hook-*` tools pick it up from their environment and send it as `X-Coral-Session-Token` with every request, with no action from the agent: nothing in a prompt or a tool call touches it.

The server attributes each request to the token's session and its board subscription. **A presented token decides the identity, in every mode** (#3495): a board request that claims another subscriber (`subscriber_id`, `as` or `created_by`) or `Operator` is refused with 403 `identity_mismatch`, and so is a `session_id` naming another session on the routes an agent's tools call (personal tasks, artifacts, agent UI panels, clearing events, the hooks' token-usage and model reports). An agent that hasn't joined a board yet is held to the same rules (#3512): it can join under a new name, but never act as `Operator` or as a subscriber another session holds, and a `session_name` naming another session is refused. Acting on another board is refused in strict mode and only logged otherwise (`[identity] mismatch (log only)`). Each mismatch is logged once per session and kind every 5 minutes, and counted.

The routes only an agent's own tools call need its token in every mode (401 `session_token_required` without one): `coral-agent task` (except finishing a task, which the dashboard does too), `coral-agent artifact upload`, writing an agent UI panel, and the hooks' token-usage and model reports. Clearing an agent's events and `coral-agent notify` also accept the Operator (the dashboard, or the command line outside an agent). A token-less script calling the board keeps working in default mode; strict mode needs a credential.

**Terminal sessions** are plain shells, started with no launch command, so they get no token. In default mode, `coral-agent task`, `artifact upload` and `ui` run in a terminal session work without one, for that terminal session only; an agent session (claude, codex, agy, pi) is never reachable that way. Strict mode refuses them, like any token-less tool.

**A live session's tasks and notes** (#3498): creating, changing, reordering and deleting them (`POST/PATCH/DELETE /api/sessions/live/{name}/tasks…` and `…/notes…`) act only on the caller's own. An agent's token decides whose they are, whatever `{name}` says, and a `session_id` naming another session is 403 `identity_mismatch`; a terminal session in default mode acts for itself; the Operator (the dashboard) acts on `{name}`'s; anyone else gets 401. An id that isn't in the caller's scope answers 404 and changes nothing. Reading the lists (`GET …/tasks`, `GET …/notes`) is not identity-bound: any caller that reaches the server can read any session's list.

**Threat model.** Tokens stop mistakes and prompt-injected impersonation through Coral's own tools. They do not stop a malicious process running as the same user: it can read another process's environment (`/proc/<pid>/environ` on Linux, `ps -E` on macOS). Isolation between users is the OS's; isolation between agents of one user is about honest attribution.

**What default mode still allows** (strict mode closes all three): any local process can act as a terminal session by naming its `session_id` without a token (an agent can too, by dropping its own token); it can pass as the Operator on the routes the dashboard also uses by sending a browser's same-origin headers from this computer; and it can call the Operator's routes above with no token at all (an agent's token is refused on them in every mode, #3929, but a request that leaves the token out is a token-less local caller). A token decides only when it is sent; default mode trusts token-less callers on this computer as it always has.

### Dashboard sign-in links

At startup Coral mints a single-use sign-in token, valid 10 minutes, writes it to `<data>/login_token` (0600) and opens the browser with `http://localhost:<port>/?login_token=…`. With no display (`--no-browser`, a Linux box without `DISPLAY`) it prints the link instead. Opening the link trades the token for the dashboard's session cookie (HttpOnly, SameSite=Strict) and redirects to the same page without it. A used or expired link answers 403.

`coral open` asks the running Coral for a fresh link (through the same handshake as `coral cloud`) and opens it, or prints it with `--print` or without a display. `--path` makes the link land on a dashboard page, for example `coral open --path /chats` or `coral open --path /team/<board>`. Without strict local auth the dashboard also works on this computer without the cookie; with it, the cookie is required.

Coral's own apps sign in the same way, with no API key and no prompt (#3394). The tray and `launch-coral` run the server themselves and open every dashboard window or browser tab with a fresh link from it. The desktop window (`coral-app`) gets its first link from the tray in its environment (never on the command line, where other users could read it with `ps`), or, started on its own, asks Coral through the command-line handshake. If its session ends and the dashboard shows the sign-in page, it asks for a new link and signs in again (at most once every 30 seconds). Links are never written to logs; they appear as `login_token=REDACTED`. A Coral without sign-in links (before #3387) is opened at its plain address, as before.

### Identity report

`GET /api/system/identity-report` (the dashboard on this computer or the coral CLI only; never through Coral Hub, never an agent's plain request) returns `mode` (`log_only` or `strict`), counts by caller type (`agent_token`, `agent_bad_token`, `agent_no_token`, `dashboard_cookie`, `dashboard_no_cookie`, `coral_hub`, `api_key`, `local_other`), the subset strict mode would refuse (`would_break_in_strict_mode`), the same broken down as `"caller tool METHOD /route/{pattern}"` (`would_break_by_route`, e.g. `"local_other curl GET /api/sessions/live": 8`; the tool is the User-Agent's product name; never ids, queries or identity values; at most 200 keys), mismatch counts by kind (`impersonation`, `operator`, `cross_board`), agents' calls to the Operator-only routes (`agent_operator_calls`, see below) and the last 50 mismatches (never a token). Read `would_break_by_route` before turning strict mode on: every line is something that will be refused.

### System messages

Board messages Coral posts itself (the task queue, the health monitor) carry `is_system: true`, set by the server only and never inferred from a sender's name. The chat collapses them by that flag.

### Strict local auth (opt-in)

Start Coral with `--strict-local-auth` (or `CORAL_STRICT_LOCAL_AUTH=1`). It is a startup switch only: no setting or API turns it on or off, so neither Coral Hub nor an agent can change it. Agents never inherit the variable.

With it on, a request **from this computer** needs one of:

| Caller | Credential |
|---|---|
| An agent's tools (`coral-board`, `coral-agent`, the `coral-hook-*` hooks) | the agent's session token, which Coral gives them automatically (nothing to do) |
| The dashboard in a browser | its sign-in cookie: open Coral from the app or tray, or run `coral open` |
| You at the command line (`coral-board`/`coral-agent` in your own terminal, `coral auto-accept off --all`, `coral open`, `coral cloud`, the tray's Shutdown, `coral-coordination-report`) | a signature made with Coral's command-line token (`<data>/cli-token`, 0600): automatic when you run them as the user Coral runs as. They act as the local Operator. |
| A script | the API key (`Authorization: Bearer <key>`), or one of the above |

Always open: `GET /api/health`, the command line's hello (`POST /api/cloud/cli-hello`), the sign-in page (`/auth`), static files. Coral Hub (the tunnel) and other computers (API key or sign-in) keep their own rules.

What is refused:

| Request | Answer |
|---|---|
| An agent's tool acting as someone else: another member (`subscriber_id`, `as`, `created_by`), the `Operator`, or another board | 403 `identity_mismatch` (also logged and in the report) |
| An agent's tool with an unknown or revoked token, or none (an agent started before v1.4.19, or a token removed) | 401 `session_token_required`: restart the agent from Coral |
| An agent's token on a route that runs code or starts agents on this computer: creating, changing, deleting or triggering a workflow (`/api/workflows…`), scheduling, changing or toggling a job (`/api/scheduled/jobs…`), starting a task run (`POST /api/tasks/run`), stopping a workflow or task run (`POST …/runs/{id}/kill`), and changing Coral's settings (`PUT /api/settings`: it holds `cli_path_*`, auto-accept, remote-access and cloud keys; no agent tool sends it) (#3445), reading the API key (`GET /api/system/api-key`: with it an agent could drop its token and act as the Operator) and deleting a board (`DELETE /api/board/{project}`; leaving a board stays allowed) (#3513) | 403 `forbidden_for_agent`: these are the Operator's (the dashboard, the API key, or the command line outside an agent). This one applies in **every mode** (#3929): an agent's token is refused on them without strict mode too. So `coral-board workflow create`, `workflow trigger`, `workflow kill` and `coral-board delete` are the Operator's. Since #3929 the same holds for what acts on **other** agents, on what every agent is told, and on the machine and the dashboard's own data. That covers:
- typing into, sending keys to, answering the prompt of, stopping, restarting, resuming, attaching to, resizing, sleeping, waking, archiving or setting the goal or auto-accept of an agent or a team (`/api/sessions/live/{name}/send`, `keys`, `answer-prompt` and the rest);
- writing a file in an agent's folder;
- a board's settings, groups and working mode, and its presets (`coral-board working-mode` can read them, and only the Operator changes them);
- a team's knowledge, generating, resurrecting or deleting a team;
- pinned folders, themes, custom views, tags, session notes and summaries, the diff mode and the dashboard's notices.

Agents talk to each other through the board. The full list is `operatorOnlyRoutes` (internal/server/routes/identity_observer.go), and every route the server serves has a decision in internal/server/testdata/agent_routes.txt. A request with no token is left to strict mode (see below). Each agent call is listed in the identity report's `agent_operator_calls` (`"<session> METHOD /route"`). Agents launching agents (`POST /api/sessions/launch`, `launch-team`) stays allowed in every mode and is recorded in `agent_operator_calls`. |
| Anything else without a credential (`curl`, a browser with no sign-in) | 401 `dashboard_auth_required`; a page load is sent to `/auth?locked=1&return_to=…` |

Inside an agent session (`CORAL_SESSION_NAME` is set) the tools **never** fall back to the command-line signature, even if they could read the token file: an agent whose token is missing is refused, not promoted to Operator.

**Recovering:** run `coral open` (or open Coral from the app or the tray) to sign the browser in again. If an agent is refused with `session_token_required`, restart it. To turn strict mode off, restart Coral without the flag.

**What it protects against, and what it doesn't.** Strict mode is about **mistakes and prompt-injected agents acting through Coral's tools**: an agent told to post as the Orchestrator or the Operator, to work on another team's board, or a stray local script or web page calling Coral's API without a sign-in. It is **not** a defence against a malicious program running as your user: such a program can read Coral's data folder (the command-line token, the sign-in link file) and act as you. Keep untrusted code in another user account or a sandbox.
