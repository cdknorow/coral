# Authentication API

Coral uses a layered authentication system:

1. **Localhost bypass** — A request that comes straight from a process of Coral's own account on this machine (a `127.0.0.1` / `::1` peer that carries no reverse-proxy header, whose connection the operating system attributes to the account Coral runs as: see [the no-credential pass is only for Coral's own account](#the-no-credential-pass-is-only-for-corals-own-account)) is authenticated without credentials, unless strict local auth (the default) asks for one. A request a same-machine reverse proxy forwarded is **not**: it carries a forwarding header (anything in the `X-Forwarded-*` or `Tailscale-*` families, hyphen or underscore spelling, or `Forwarded`, `Via`, `X-Real-IP`, `True-Client-IP`, `CF-Connecting-IP`, `X-Original-Forwarded-For`), so it needs the key or a session like a remote caller. (Configure the proxy to send the standard hyphenated header names.) The same "direct local client" test gates Coral's local-only powers — the file explorer's relaxed bounds (any file you can read, versus the strict session/workspace roots), connecting or changing Coral Hub from the command line, opening a desktop editor, and deleting the input log — so a proxied request gets the strict rules there too. **While `require_auth_on_loopback` is on, loopback requests become strict-local, not remote:** a direct-local request is admitted only with strict local auth's exact credential set — an agent session token (`X-Coral-Session-Token`), a dashboard session cookie, a signed command-line request, an API key, or an authentic Coral Hub tunnel stamp — and a tokenless one gets 401. So the dashboard (after signing in), `coral open`, the signed `coral` command line, agents, the `coral-hook-*` binaries and `coral-board` keep working, while a **tokenless terminal session and a bare `curl http://localhost:8420/...` with no credential stop working** (they get 401; add the key, e.g. `-H "Authorization: Bearer <key>"`). A request a proxy forwarded stays remote (key or session only), so even a valid agent session token on a proxied request is refused. The file explorer, editor-open and input-log delete use the strict, non-local rules for every request, the Operator's own browser included. The trust keys on the connection's peer address, never on the `Host` header. A proxy that strips those headers still looks direct; for that case turn on **`require_auth_on_loopback`** (Settings; default off; Operator-only, never settable through the Coral Hub), which makes even a plain loopback request authenticate. When it is on, the dashboard login, the signed `coral` command line and agent tokens still work, but a tokenless terminal session (with strict local auth off) stops reaching Coral over HTTP. If it ever locks you out, restart Coral with `CORAL_REQUIRE_AUTH_ON_LOOPBACK=off` (an empty value is ignored; use `off` or `on`) — it overrides the stored setting at startup without any HTTP call — then clear the setting in Settings so it stays off.
2. **API key** — An auto-generated key stored in `~/.coral/`. Remote clients, and scripts on this computer, pass it via `Authorization: Bearer <key>` header or `X-API-Key` header. It is kept in `<data>/api_key` (0600); `coral api-key` prints it (`--home DIR` for another data folder), e.g. `curl -H "Authorization: Bearer $(coral api-key)" http://localhost:8420/api/sessions/live`. The `?api_key=<key>` query parameter still works but is **deprecated**: it puts the key in the URL, browser history and logs; send a header instead. A browser opening a link with it gets a session cookie (once, when it holds none); a script using it is authenticated like the header form and gets no session. Any program running as the user Coral runs as can read the key (`coral api-key`), so with strict local auth the key keeps out other accounts and agents whose CLI is sandboxed or asks before running commands, not a program of that same user acting deliberately.
3. **Session cookie** — After validating an API key via `POST /auth/key`, a session cookie is set for browser access. It is `HttpOnly`, `SameSite=Strict`, lasts 24 hours, and is `Secure` when the browser reached Coral over https: over TLS directly, or through a trusted reverse proxy that sends `X-Forwarded-Proto: https`. A proxy on this machine (a loopback peer) is always trusted; list others in `CORAL_TRUSTED_PROXIES` (comma-separated IPs or CIDRs, e.g. `10.0.0.5,192.168.1.0/24`). The header is ignored from any other peer.

   **Behind a reverse proxy:** a change from a browser (POST, PUT, PATCH, DELETE) must come from Coral's own page: its `Origin` must name the host the browser reached, which is the request's `Host` or, from a trusted proxy, `X-Forwarded-Host`. Coral reads the LAST `X-Forwarded-Host` value (the one your proxy added), so configure the proxy to overwrite the header rather than append to a client's (nginx: `proxy_set_header X-Forwarded-Host $host;`), or to pass the original `Host` (`proxy_set_header Host $host;`). A request with `Origin` or `Sec-Fetch-Site` repeated is refused (403 `foreign_origin`): a browser never sends either twice.

## Auth Page

```
GET /auth
```

Serves the sign-in page. The server decides what it shows: the password sign-in once a password is set; otherwise "Create a password" (from another computer it also asks for the setup code, below). Every state also offers the API key. The page is never framed and is not cached.

**Response:** HTML document.

## Browser password

A browser signs in to Coral with a password (#3949). The Coral app, the tray and `coral open` sign in by themselves with a single-use link and never need it. Any non-empty password is accepted, up to 4 KiB; a strength meter only advises. Only an argon2id hash is kept, in `<data>/password` (0600). It is never logged.

- **Creating it.** When no password is set, the sign-in page offers to create one.
  - From this computer, no code is needed. "This computer" means a direct loopback request with a localhost Host, no proxy header, no Coral Hub stamp, `require_auth_on_loopback` off, and the system confirming that the request comes from the same OS user.
  - From anywhere else, the page also asks for a one-time **setup code**: 8 characters shown as `XXXX-XXXX`, valid 60 minutes, single use. It is replaced when a password is set, on every restart and after 20 wrong codes.
  - `coral setup-code` prints it on the computer that runs Coral. When Coral was started from a terminal, it also prints it there. It never goes to a log file: not `coral.log`, and not `tray.log` when the tray or the app runs Coral.
- **From a script or a headless install:** `coral set-password` (prompts on the terminal; `--stdin` reads one line), or `CORAL_INITIAL_PASSWORD_FILE=<file>`, which Coral reads once at startup when no password is set. The file must be private (0600) and is deleted after reading. **Never pass a password in an environment variable or on the command line:** other programs of the same user can read both.
- **Signing in:** `POST /auth/password {"password", "keep"}`. "Keep me signed in" (off by default) keeps the browser signed in for 30 days; otherwise it lasts until the browser closes, and at most 24 hours. Wrong passwords are slowed, never locked out: five are free from an address, then each waits 1, 2, 4 ... seconds, up to 15 minutes. The app, the tray, `coral open`, the command line and the API key are never slowed. Behind a reverse proxy, every remote browser shares the proxy's address.
- **Changing it, and other browsers:** Settings > Browser sign-in changes the password (every other browser is signed out), lists the signed-in browsers and recent sign-ins, and signs out the others or one of them. A browser that is signed out loses its open terminals and live feed at once, not only its next request.
- **Forgot it:** run `coral reset-password` (or `--clear` to remove it) on the computer that runs Coral, as the user Coral runs as. It works with Coral stopped too, and every browser is signed out. A reset also replaces the command-line token (`<data>/cli-token`, below), so a copy of the old one, or a program that read it, can no longer sign as you; your own command-line tools read the new one by themselves. To replace the token without a reset, restart Coral: every start writes a new one.
- **Sessions** are kept in `sessions.db` (only a hash of each cookie), so a restart doesn't sign anyone out. The cookie is HttpOnly and SameSite=Strict, and Secure over TLS.
- **Coral Hub** never reaches the password or the session routes, not even through a Hub sign-in (below).
- **Exposing Coral through a reverse proxy on the same computer:** set the password first, or turn on `require_auth_on_loopback`. A proxy that adds no forwarding header makes a remote browser look like this computer, and the setup code can't help there.
- **Plain HTTP:** over plain HTTP on a network, the password and the cookie can be read by someone on that network, as the API key can. Use TLS (`--tls-cert`) or a private network for remote access.

### TLS

`--tls-cert` and `--tls-key` (or `CORAL_TLS_CERT` and `CORAL_TLS_KEY`) make Coral serve https and wss on its port. From another computer only https is accepted: a plain http page load is redirected to https, anything else is refused. Programs on this computer (the hooks, `coral-board`, the Coral Hub link) keep using plain http to `localhost` on the same port.

- **No HSTS.** Coral doesn't send `Strict-Transport-Security`, on purpose. HSTS applies to a whole host name, not one port, so it would break the plain http that local tools and other local servers use on that name. And under HSTS a browser offers no way past a certificate warning, so with the usual self-signed or private certificate (a LAN or Tailscale name) you could be locked out of your own dashboard. The case HSTS is for, a first visit typed as `http://`, is already covered: from the network, plain http is redirected or refused. If you put Coral behind a TLS reverse proxy with a publicly trusted certificate, set HSTS there.
- **Renewing the certificate.** Coral reads the certificate and key once, at startup. After renewing them (for example with certbot or `tailscale cert`), write the new files to the same paths and restart Coral. Until then it keeps serving the old certificate, and browsers warn once it expires.

## Sign in with Coral Hub

The sign-in page can also offer **Sign in with Coral Hub** (#3966). **A Coral Hub sign-in gives full control of this computer**, like the password: whoever controls the Coral Hub account that owns this computer can run commands on it. A compromised or phished Coral Hub account is a compromised computer.

- **Off by default.** It is turned on only in Settings > Browser sign-in, from a session on this computer, or by the box (unticked by default) on the "link this computer and sign in" step of the sign-in page. Coral Hub can't turn it on, an agent can't, and `PUT /api/settings` can't. Coral Hub has its own switch per computer too: both must be on.
- **Who:** only the Coral Hub account that linked this computer (its owner, recorded when it was linked). If Coral Hub ever names another account, Hub sign-in stays off until the computer is linked again.
- **How:** the button sends the browser to Coral Hub, which asks the owner to confirm ("Full control of <computer>") and sends it back to `/auth/hub/callback` with a one-time code. Coral redeems the code over its Coral Hub connection (PKCE, single use, 60 seconds) and signs the browser in with the same kind of cookie as the password, listed as "Coral Hub: <name>". It comes back only to an address this computer told Coral Hub: its own `localhost` port, plus the addresses in Settings (https, or http to a private, loopback or Tailscale IP).
- **How long:** Coral checks with Coral Hub every minute. If Coral Hub says no (signed out there, revoked, the computer removed), the browser is signed out at once. **If your computer cannot reach Coral Hub, a Hub sign-in can last up to 10 more minutes.** It never lasts longer than Coral Hub's grant (at most 8 hours) or 24 hours.
- **What it can't do:** change or reset the password, list or sign out browsers, mint a sign-in link, read or rotate the API key, or change the Coral Hub link and its settings (connect, disconnect, the access level, redaction, auto-accept, the Operator switch, Hub sign-in itself). Those answer 403 `hub_session_not_allowed`. Everything it changes is recorded with its name in the Operator actions list.
- **Turned off at once** by: the setting off, "Don't let Coral Hub act as the Operator" on, disconnecting or re-keying the Coral Hub link, a password change, or signing it out in Settings. Its open terminals close with it.
- **Routes:** `POST /auth/hub/start`, `GET /auth/hub/callback`, `POST /auth/hub/link` and `GET /auth/hub/link/status` (the sign-in page); `GET`/`PUT /api/auth/hub-signin`, `POST /api/auth/hub-signin/dismiss`, `GET`/`PUT /api/auth/hub-origins`, and `DELETE /api/auth/sessions/{id}` (Settings: a signed-in session on this computer or the signed command line; never an agent or Coral Hub).

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
  "method": "session",
  "password_set": true,
  "local": false
}
```

| Method | Description |
|--------|-------------|
| `"localhost"` | Request came from localhost (only with strict local auth off) |
| `"key"` | Authenticated via API key header |
| `"session"` | Authenticated via session cookie |
| `"none"` | Not authenticated |

## Agent identity and sign-in links

Coral is moving from "anything on this computer is trusted" to "every caller says who it is". Phase 1 (#3387) only **observes** and reports what a stricter mode would break. Phase 2 (#3418) added that stricter mode, [strict local auth](#strict-local-auth-the-default); since #3936 it is **on by default**. Start Coral with `--strict-local-auth=false` to turn it off (a dev build also honours `CORAL_STRICT_LOCAL_AUTH=0`; a release build ignores the variable — see [strict local auth](#strict-local-auth-the-default)).

### Per-session agent tokens

Every agent session (not a terminal session; see below) gets a random token when Coral launches, restarts, resumes or wakes it. Coral's own launch line reads the token from a single-use file (0600, random name, in `<data>/session_tokens/`) into `CORAL_SESSION_TOKEN` and deletes the file before the agent CLI starts. The token is never typed or echoed in the terminal, never in a command line (`ps`), a log or the board, and never in the tmux server's environment; only that pane's shell and what it starts have it. Restarting or resuming mints a new token and the old one stops working.

`coral-board`, `coral-agent` and the `coral-hook-*` tools pick it up from their environment and send it as `X-Coral-Session-Token` with every request, with no action from the agent: nothing in a prompt or a tool call touches it.

The server attributes each request to the token's session and its board subscription. **A presented token decides the identity, in every mode** (#3495): a board request that claims another subscriber (`subscriber_id`, `as` or `created_by`) or `Operator` is refused with 403 `identity_mismatch`, and so is a `session_id` naming another session on the routes an agent's tools call (personal tasks, artifacts, agent UI panels, clearing events, the hooks' token-usage and model reports). An agent that hasn't joined a board yet is held to the same rules (#3512): it can join under a new name, but never act as `Operator` or as a subscriber another session holds, and a `session_name` naming another session is refused. Acting on another board is refused in strict mode and only logged otherwise (`[identity] mismatch (log only)`). Each mismatch is logged once per session and kind every 5 minutes, and counted.

The routes only an agent's own tools call need its token in every mode (401 `session_token_required` without one): `coral-agent task` (except finishing a task, which the dashboard does too), `coral-agent artifact upload`, writing an agent UI panel, and the hooks' token-usage and model reports. Clearing an agent's events and `coral-agent notify` also accept the Operator (the dashboard, or the command line outside an agent). A token-less script calling the board works only with strict local auth off; by default it needs a credential (the API key).

**Terminal sessions** are plain shells, started with no launch command, so they get no token. With strict local auth off, `coral-agent task`, `artifact upload` and `ui` run in a terminal session work without one, for that terminal session only; an agent session (claude, codex, agy, pi) is never reachable that way. Strict mode refuses them, like any token-less tool.

**A live session's tasks and notes** (#3498): creating, changing, reordering and deleting them (`POST/PATCH/DELETE /api/sessions/live/{name}/tasks…` and `…/notes…`) act only on the caller's own. An agent's token decides whose they are, whatever `{name}` says, and a `session_id` naming another session is 403 `identity_mismatch`; a terminal session with strict local auth off acts for itself; the Operator (the dashboard) acts on `{name}`'s; anyone else gets 401. An id that isn't in the caller's scope answers 404 and changes nothing. Reading the lists (`GET …/tasks`, `GET …/notes`) is not identity-bound: any caller that reaches the server can read any session's list.

**Threat model.** Tokens stop mistakes and prompt-injected impersonation through Coral's own tools. They do not stop a malicious process running as the same user: it can read another process's environment (`/proc/<pid>/environ` on Linux, `ps -E` on macOS). Isolation between users is the OS's; isolation between agents of one user is about honest attribution. A Codex agent's token file is the one place this is wider than the environment: Codex hides `CORAL_SESSION_TOKEN` from the commands it runs, so Coral keeps that agent's token file in `<data>/session_tokens` (private to the user) for the whole session and names it in `CORAL_SESSION_AUTH_FILE`, while every other agent's file is deleted by its shell at once. Any process of the same user, another agent that can read files included, can read a live Codex agent's token from that folder; it is removed with the session and at the next start once its token is revoked. The tools accept the file only from that folder, and a missing or failing file gives no token (the request is refused), never another identity.

**What turning strict local auth off allows** (strict mode closes all three): any local process can act as a terminal session by naming its `session_id` without a token (an agent can too, by dropping its own token); it can pass as the Operator on the routes the dashboard also uses by sending a browser's same-origin headers from this computer; and it can call the Operator's routes above with no token at all (an agent's token is refused on them in every mode, #3929, but a request that leaves the token out is a token-less local caller). A token decides only when it is sent; with strict local auth off, token-less callers on this computer are trusted, as before #3936.

### Dashboard sign-in links

At startup Coral mints a single-use sign-in token, valid 10 minutes, writes it to `<data>/login_token` (0600) and opens the browser with `http://localhost:<port>/?login_token=…`. With no display (`--no-browser`, a Linux box without `DISPLAY`) it prints the link instead. Opening the link trades the token for the dashboard's session cookie (HttpOnly, SameSite=Strict) and redirects to the same page without it. A used or expired link answers 403.

`coral open` asks the running Coral for a fresh link (through the same handshake as `coral cloud`) and opens it, or prints it with `--print` or without a display. `--path` makes the link land on a dashboard page, for example `coral open --path /chats` or `coral open --path /team/<board>`. Without strict local auth the dashboard also works on this computer without the cookie; with it, the cookie is required.

**Who can use a link** (#3970): only the user Coral runs as. A link opened by another user of this computer is refused like a used one, and stays good for its owner (an opener's command line can be read by other users in `ps`). Where the system can't tell which user opened it, links work as before and the log says so once. On Linux that is also the case for a browser on another computer, so that line can appear when you open a link from the network. Two consequences:
- a reverse proxy on this computer that runs as another OS user gets its sign-in links refused: sign in there with the password or the API key instead;
- `coral open --print` warns when it runs in an agent's terminal or a tmux pane, where anything that can read the pane can use the link.

Coral's own apps sign in the same way, with no API key and no prompt (#3394). The tray and `launch-coral` run the server themselves and open every dashboard window or browser tab with a fresh link from it. The desktop window (`coral-app`) gets its first link from the tray in its environment (never on the command line, where other users could read it with `ps`), or, started on its own, asks Coral through the command-line handshake. If its session ends and the dashboard shows the sign-in page, it asks for a new link and signs in again (at most once every 30 seconds). Links are never written to logs; they appear as `login_token=REDACTED`. A Coral without sign-in links (before #3387) is opened at its plain address, as before.

### Identity report

`GET /api/system/identity-report` (the dashboard on this computer or the coral CLI only; never through Coral Hub, never an agent's plain request) returns `mode` (`log_only` or `strict`), counts by caller type (`agent_token`, `agent_bad_token`, `agent_no_token`, `dashboard_cookie`, `dashboard_no_cookie`, `coral_hub`, `api_key`, `local_other`), the subset strict mode would refuse (`would_break_in_strict_mode`), the same broken down as `"caller tool METHOD /route/{pattern}"` (`would_break_by_route`, e.g. `"local_other curl GET /api/sessions/live": 8`; the tool is the User-Agent's product name; never ids, queries or identity values; at most 200 keys), mismatch counts by kind (`impersonation`, `operator`, `cross_board`), agents' calls to the Operator-only routes (`agent_operator_calls`, see below) and the last 50 mismatches (never a token). Read `would_break_by_route` before turning strict mode on: every line is something that will be refused.

### System messages

Board messages Coral posts itself (the task queue, the health monitor) carry `is_system: true`, set by the server only and never inferred from a sender's name. The chat collapses them by that flag.

### Strict local auth (the default)

On by default since #3936, for `coral`, the tray and `launch-coral` alike; on the first start the log says so once. **Turning it off is weaker:** start Coral with `--strict-local-auth=false` (a dev build also reads `CORAL_STRICT_LOCAL_AUTH=0`, or `off`, `false`, `no`; every other build ignores the variable and says so in the log), and any program on this computer can use Coral without a credential, as before. A value that is neither on nor off is taken as on, with a warning. It is a startup switch only: no setting or API turns it on or off, so neither Coral Hub nor an agent can change it. Agents never inherit the variable.

With it on, a request **from this computer** needs one of:

| Caller | Credential |
|---|---|
| An agent's tools (`coral-board`, `coral-agent`, the `coral-hook-*` hooks) | the agent's session token, which Coral gives them automatically (nothing to do) |
| The dashboard in a browser | its sign-in cookie: open Coral from the app or tray, or run `coral open` |
| You at the command line (`coral-board`/`coral-agent` in your own terminal, `coral auto-accept off --all`, `coral open`, `coral cloud`, the tray's Shutdown, `coral-coordination-report`) | a signature made with Coral's command-line token (`<data>/cli-token`, 0600), over the method, path, query, body and `Authorization` header, usable once. Automatic when you run them as the user Coral runs as. They act as the local Operator. The token is new on every start and after `coral reset-password`. |
| A script | the API key (`Authorization: Bearer <key>` or `X-API-Key: <key>`; `coral api-key` prints it), or one of the above. The key is a machine credential, not a person or an agent: it can't call the routes that belong to one agent's session (for example `POST /api/sessions/live/{name}/tasks`, answered 401 `session_token_required`), nor the dashboard-only Operator actions. For those, use the agent's own tools (its session token), or sign in as the Operator: a `coral open` link or the password, which give a dashboard cookie. The Coral Hub link's own routes under `/api/cloud/` (connect, cancel, disconnect, status, the access level, redaction, auto-accept, rekey, the identity report) need more than the key too: a browser on this computer that is signed in to the dashboard (opened from the app, the tray or `coral open`), or the signed command line (`coral cloud connect`). With only the key they answer 403 `local_dashboard_required`, by design: connecting is for someone at this computer. A scripted setup uses `coral cloud connect` (or `--ticket-file`) or a sign-in link. |

Always open: `GET /api/health`, the command line's hello (`POST /api/cloud/cli-hello`), the sign-in page (`/auth`), static files. Coral Hub (the tunnel) and other computers (API key or sign-in) keep their own rules.

What is refused:

| Request | Answer |
|---|---|
| An agent's tool acting as someone else: another member (`subscriber_id`, `as`, `created_by`), the `Operator`, or another board | 403 `identity_mismatch` (also logged and in the report) |
| An agent's tool with an unknown or revoked token, or none (an agent started before v1.4.19, or a token removed) | 401 `session_token_required`: restart the agent from Coral |
| An agent's token on a route that runs code or starts agents on this computer: creating, changing, deleting or triggering a workflow (`/api/workflows…`), scheduling, changing or toggling a job (`/api/scheduled/jobs…`), starting a task run (`POST /api/tasks/run`), stopping a workflow or task run (`POST …/runs/{id}/kill`), and changing Coral's settings (`PUT /api/settings`: it holds `cli_path_*`, auto-accept, remote-access and cloud keys; no agent tool sends it) (#3445), reading the API key (`GET /api/system/api-key`: with it an agent could drop its token and act as the Operator) and renaming or deleting a board (`PATCH` / `DELETE /api/board/{project}`; leaving a board stays allowed) (#3513), reading or typing into an agent's terminal through the terminal stream (`GET /ws/terminal/{name}`), turning every agent's auto-accept off (`POST /api/auto-accept/disable-all`), the dashboard's file upload, its removal and reading one back for a chat chip's thumbnail (`POST /api/upload`, `DELETE /api/upload/{name}`, `GET /api/upload/{name}`) and opening a file in the Operator's editor (`POST /api/sessions/live/{name}/open-in-editor`) (#4512: no agent tool calls them) | 403 `forbidden_for_agent`: these are the Operator's (the dashboard, the API key, or the command line outside an agent). This one applies in **every mode** (#3929): an agent's token is refused on them without strict mode too. So `coral-board workflow` (`create`, `list`, `trigger`, `status`, `runs`, `kill`) and `coral-board delete` are the Operator's. Since #3929 the same holds for what acts on **other** agents, on what every agent is told, and on the machine and the dashboard's own data. That covers:
- typing into, sending keys to, answering the prompt of, stopping, restarting, resuming, attaching to, resizing, sleeping, waking, archiving or setting the goal or auto-accept of an agent or a team (`/api/sessions/live/{name}/send`, `keys`, `answer-prompt` and the rest, and the terminal stream `/ws/terminal/{name}`, whose typing and resize frames are refused to an agent's token along with the stream itself);
- writing a file in an agent's folder;
- a board's settings, groups and working mode, and its presets (`coral-board working-mode` can read them, and only the Operator changes them);
- a team's knowledge, generating, resurrecting or deleting a team;
- pinned folders, themes, custom views, tags, session notes and summaries, the diff mode and the dashboard's notices;
- reads that span every session, board or team (since v1.4.105, #5013, #5041, #5075): the history list and search (`GET /api/sessions/history`, `…/history/search`; an agent searches its own through `coral-agent history`), the token-usage list and by-branch (`GET /api/token-usage`, `…/by-branch`), the goal generator's metrics (`GET /api/goals/metrics`), the identity report, every board's tasks (`GET /api/board/tasks`; an agent reads its own board's), a team's knowledge, and every workflow and scheduled-job read (list, get, runs, a run's output and step files, which hold commands and credentials).

A token also limits what an agent reads about one **session**, in every mode: its own session, its resume ancestors and the sessions on its own board (their chat, terminal capture, files, diffs, git state, events, history, subagents, notes and usage turns), and 403 `forbidden_other_session` for any other. In the live list (`GET /api/sessions/live`, and the `/ws/coral` feed) a session on another board appears as a roster row only (name, type, ids, board, status flags, model): no prompt, summary, branch, working directory or git state. See [Sessions](sessions.md#get-apisessionslive). The Operator, the dashboard and the command line are not limited.

Agents talk to each other through the board. The full list is `operatorOnlyRoutes` (internal/server/routes/identity_routes.go), and every route the server serves has a decision in internal/server/testdata/agent_routes.txt. A request with no token is left to strict mode (see below). Each agent call is listed in the identity report's `agent_operator_calls` (`"<session> METHOD /route"`). Agents launching agents (`POST /api/sessions/launch`, `launch-team`) stays allowed in every mode and is recorded in `agent_operator_calls`. |
| Anything else without a credential (`curl`, a browser with no sign-in) | 401 `dashboard_auth_required`; a page load is sent to `/auth?locked=1&return_to=…` |
| A request from **another account** on this computer, or one whose account Coral can't tell, with no credential (see below) | 401 `other_user_needs_sign_in`, with `detail` `other_user` or `unattributed`; a page load is sent to `/auth?locked=1&return_to=…` |

Inside an agent session (`CORAL_SESSION_NAME` is set) the tools **never** fall back to the command-line signature, even if they could read the token file: an agent whose token is missing is refused, not promoted to Operator.

**Recovering:** run `coral open` (or open Coral from the app or the tray) to sign the browser in again. If an agent is refused with `session_token_required`, restart it. To turn strict mode off (weaker), restart Coral with `--strict-local-auth=false` (a dev build also reads `CORAL_STRICT_LOCAL_AUTH=0`; a release build ignores it and keeps strict mode on).

### The no-credential pass is only for Coral's own account

Loopback isn't per user. On Windows another interactive, Remote Desktop or terminal-server user of the same computer, or a process under WSL2's mirrored networking, reaches Coral from `127.0.0.1` too; on a shared Linux or macOS machine so does every other account. So Coral asks the operating system whose process opened each loopback connection (Windows: the process that owns the socket and its token's user SID; Linux: `/proc/net/tcp`; macOS: the kernel's socket list), once per connection, and only a connection it positively attributes to **the account Coral runs as** gets the loopback pass and the local-only powers (the file explorer's relaxed bounds, the Coral Hub settings, reading the API key, opening an editor, deleting the input log, creating the first password without the setup code).

Any other loopback connection is treated like a request from another computer, **whatever the settings** (strict local auth off included): it needs a credential (the dashboard's password sign-in, the API key, or an agent's session token; a command-line signature already requires Coral's own account) and gets no local-only power. Without one it gets 401 `other_user_needs_sign_in`, with `detail` `other_user` (another account's process) or `unattributed` (Coral couldn't tell: the connection's process had already gone, its token couldn't be read, or a WSL2 mirrored connection). The account is never named in the answer. Coral logs each such connection (at most once a minute per address and reason), so a connection that should have been yours can be found in `coral.log`.

Your own programs are unaffected: the dashboard, the app, the tray, the `coral` command line, agents and the `coral-hook-*` tools run as your account. One exception: Windows doesn't let a normal program read an **elevated** (Run as administrator) process's token, so a request from an elevated PowerShell of yours can be `unattributed`. It still works with a credential: the signed `coral` command line and agents' tools send one already; a script sends the API key. The profiler (`CORAL_PPROF_ADDR`) answers only Coral's own account too. An agent whose commands run as **another account** (for example Codex's elevated Windows sandbox, which runs them as a separate sandbox user; its unelevated restricted token is still your account) is another account here too, and can't read your account's session-token file either, so `coral-board` from there gets 401. That was already the case under strict local auth: give such an agent a sandbox mode that runs as your account, or have it reach Coral through its own tools outside the sandbox. If the system's socket table can't be read at all (an OS version this build doesn't know), Coral can't tell your account from another one: the no-sign-in pass (when strict local auth is off) is per machine again, and the dashboard's security warning says so, but **no request gets a local-only power** (the API key routes, the file explorer's relaxed bounds, Coral Hub settings, opening an editor, deleting the input log) until Coral is updated to read that OS version's table.

`require_auth_on_loopback` is a different setting, for a reverse proxy on this computer (see the top of this page). It stays off by default: another account is already covered by the rule above.

**What it protects against, and what it doesn't.** Strict mode is about **mistakes and prompt-injected agents acting through Coral's tools**: an agent told to post as the Orchestrator or the Operator, to work on another team's board, or a stray local script or web page calling Coral's API without a sign-in. It is **not** a defence against a malicious program running as your user: such a program can read Coral's data folder (the command-line token, the sign-in link file) and act as you. Keep untrusted code in another user account or a sandbox.

## Threat model: what Coral defends and what it does not (v1.4.146 to 151)

Coral is one person's tool on one person's computer. The pieces that can go wrong are the agents (they read untrusted text, so a prompt can steer them), the other accounts and programs on the computer, the other computers that reach Coral, the people you give access through Coral Hub, and any text a machine wrote (an agent's reply, a board post, a saved message). What follows is the whole list, in plain words, including what Coral leaves to you.

### What it defends

- **An agent acting as someone else.** A session token names the agent in every request; an agent that names another member, a team it is not on, or the Operator is refused (see [per-session agent tokens](#per-session-agent-tokens)). Since v1.4.146 an artifact has an owner, and an agent can read only the artifacts owned by the owner of its team; anything else answers "not found", the same as an artifact that does not exist.
- **Other accounts on the same computer** (v1.4.150). Every loopback connection is traced to the operating-system account that made it. Another account, or one Coral cannot trace (an elevated window, some WSL2 setups), needs a credential and gets none of the local-only powers (reading the API key, the file explorer's wider bounds, Coral Hub settings, deleting the input log). If the system's socket table becomes unreadable to this build, nobody gets those powers until you sign in (v1.4.151).
- **Coral Hub users.** Each route has a Coral Hub tier, and some routes are the owner's alone: the artifact list and "who can read this", a team's cross-team settings, and every Save for later route (v1.4.151: reading, saving, marking done and deleting a bookmark). A Coral Hub user with a lower tier gets a refusal, not a smaller answer. The cross-team link table (v1.4.147) is shown to the owner and admits nothing yet.
- **Hostile text.** Text an agent or a board wrote is drawn as text: the Inbox (v1.4.151) shows task and message text through a renderer that builds the page from parts, never from an HTML string, with no images and links only to http, https or mailto (the host is shown). A coral://artifacts link becomes a link only when this viewer is allowed to read that artifact. A saved message is read from the stored bookmark alone, with secrets masked and cut at 64 KiB, and is never cached.
- **Typing into an agent.** Every sender of text or keys says who it is (a person, Coral, or Coral's own keys) and uses one write path per terminal backend; control characters and line breaks are removed from Coral's text; the input debug log is private, hides secrets, keeps what a person types only by length, and deletes itself after 7 days.
- **The merge queue** (v1.4.150). A reviewer approves one exact patch (its digest); a patch changed after that needs a new review; only the orchestrator or the Operator marks a patch landed. A request that names nobody is attributed to the caller's verified identity, never to a name in the request (v1.4.151).
- **Windows** (v1.4.148 to 150). Agents start from a filtered environment; Coral's programs are named by absolute path and load DLLs from the system folder; the data folder is tightened to your account, or Coral refuses to start; the desktop app opens only http and https links and only its own log files; a configured CLI path that cannot run refuses the launch and is never replaced by another CLI; a Public-network firewall rule for Coral is reported.

### What it does not defend

- **A malicious program running as you.** It can read Coral's data folder (the command-line token, the sign-in link, the API key), another process's environment, and a Codex agent's token file, and then act as you. Keep untrusted code in another account or a sandbox. Coral does not sandbox agents: an agent can do whatever your account can.
- **The API key.** It is a machine credential for a script you run yourself. Anything that holds it acts as you, so never give it to an agent.
- **Windows auto-accept.** It waits on commands it knows to be dangerous. A command it does not recognise passes, and the folder check reads the path text, so junctions and mapped drives are not seen.
- **A firewall.** Coral reads the Windows Firewall rules and warns; it never changes them.
- **Artifact owners are a Coral control, not an operating-system boundary.** Two teams with the same owner read each other's artifacts, and a program running as you reads the files directly.
- **Secrets you type.** A token typed into a reply or a board post is stored as typed; it is masked when the text is read through Coral Hub and in a saved message the Inbox reads, not where it is kept. Task text on your own computer is shown as stored, token included.
- **The Coral Hub owner account.** "Coral Hub can turn on auto-accept" is on by default, and the account this computer is linked to can turn it on or off from Coral Hub (`POST /api/hub-auto-accept`, v1.4.151). With Launch access that account can start agents that skip permission prompts, so it is trusted as much as the person at this computer: protect it with the Coral Hub's own sign-in controls. Turning the switch off, here or from Coral Hub, ends the auto-accepts the Hub had started.
- **Coral Hub's stamp.** Who the Coral Hub user is, and whether a route is the owner's, comes from what the Coral Hub connector sends; Coral cannot check it beyond the tunnel it arrives on. Turning strict local auth off, or `require_auth_on_loopback` off behind a proxy, weakens the rules above as described on this page.
- **Cross-team links** are not enforced yet: the table changes nothing about who can message or send files to whom.
