# Release Notes

## v1.4.22 — Safety: automated messages never answer a prompt; workflow and agent security fixes

**Safety and security fixes — please upgrade.** Coral no longer types its own messages into an agent that is waiting on you at a permission or selection prompt, where they could answer it. This release also closes workflow injection and file-escape holes, keeps agents from inheriting Coral's own settings or acting as the Operator, and fixes the terminal width bug.

### Safety

- **Automated messages never answer a prompt.** While an agent waits on you (a permission or selection menu, an approval question, the trust-this-folder screen, a login or model picker), Coral doesn't type board messages, nudges, task notifications, wait results or a new agent's first prompt into it: typed there, the text and its Enter could answer the prompt for you. Coral checks the agent's screen just before typing, holds the message, and sends it once, in order, after you answer. A new agent's first prompt waits for its normal input box. If a message has been held for two minutes, the dashboard shows that the agent needs your input. Held nudges are dropped after an hour, and everything held for an agent is dropped when it is killed, restarted or resumed.
- Scripts and agents that type through the API (`/send`, `/keys`) are held the same way (they get **202** `{"held": true}`); the dashboard, Coral Hub at Interact and your own `coral` command line type at once.
- Auto-accept (when you turn it on) remains the one automated way to answer an approval, and each answer is logged in the input debug log.

### Security

- **Workflows can't be hijacked through step output.** Output and artifacts substituted into a shell step (`{{prev_stdout_content}}` and similar) are quoted as one word, so their text can't change the command; trailing newlines are dropped as `$(...)` does. A step's `output_artifact` stays inside its own folder (no `..`, absolute paths or symlinks), and content over 64 KiB fails the step with a clear message.
- **Agents never inherit Coral's identity or location settings**, and the command line never sends an unsigned request: aimed at a different Coral than its data folder, it says so and sends nothing.
- **A wrong credential is refused in every mode:** a wrong API key or a forged Coral Hub stamp gets **401** `invalid_credential` (it used to be ignored on this computer). Send the current key from Settings, or none.
- **Operator-only actions (strict mode):** with `--strict-local-auth`, an agent can't create, change or trigger workflows, scheduled jobs or task runs, or change sensitive settings (agent CLI paths, auto-accept, remote access, Coral Hub) or kill runs (**403** `forbidden_for_agent`), and can't obtain a dashboard sign-in link (forged browser headers no longer count). Without strict mode such calls are recorded in the identity report.
- **Coral Hub:** board writes and working-mode changes through Coral Hub follow the "act as the Operator" rules (attributed, and refused with the opt-out); deleting a whole board or killing a whole team needs Launch, and the dashboard shows those actions disabled below Launch.
- At most 5 workflow runs at once; a 6th trigger gets **429** with `Retry-After`.
- Resuming an agent with an old session id is refused (409) instead of starting a duplicate agent.

### Fixes

- **Terminals render at the pane's real width**, so lines no longer wrap and spill in the dashboard.
- **Linux:** if Coral's data folder is writable by group or others (e.g. Ubuntu's default umask 002), Coral removes that write permission when it writes its command-line token, as the token's safety check requires.
- Jobs and workflows: a timeout kills the agent and its whole process group (a background child no longer survives); deleting a workflow removes its run folders, and each workflow keeps its newest 50 runs; hooks run in the workflow's folder.
- The dashboard backs off on its first load while the server is busy, and a per-action limit (`X-Coral-Limit: action`) shows its own message without pausing the rest of the page.
- Scheduled jobs: an unknown job answers 404 `job not found` (get, toggle, update).
- Accessibility fixes in the dashboard.

### Diagnostics

- **Stall dumps:** if the server stops answering, Coral writes every goroutine's stack to `<data folder>/stall-<time>.txt` (`CORAL_STALL_WATCHDOG=off` turns it off). `CORAL_PPROF_ADDR` (loopback only) serves Go's profiler on its own port.

### For integrators (Coral Hub, scripts)

- `/send` and `/keys` from a non-person caller can answer **202** `{"ok": true, "held": true}`.
- Wrong credentials: **401** `invalid_credential` / `cli_signature_required`, **403** `other_user`; strict mode adds **403** `forbidden_for_agent`.
- Workflow triggers: **429** `too_many_runs` with `Retry-After: 30`.

## v1.4.21 — Optional strict local sign-in

**New, off by default: optional strict local sign-in.** Start Coral with `--strict-local-auth` (or `CORAL_STRICT_LOCAL_AUTH=1`) and every request on this computer has to say who it is: the dashboard needs to be signed in, each agent uses its own session token, and the `coral` command line signs its requests. Nothing changes unless you turn it on.

### Security

- **Strict local sign-in (`--strict-local-auth`).** With it on:
  - **The dashboard is locked until you sign in.** The desktop app and the tray sign in by themselves. In a browser, run `coral open` in a terminal on this computer; opening the dashboard any other way shows a "Coral is locked" page that explains this. It never asks for or shows a key. Signing in uses a one-time link.
  - **Agents act only as themselves.** An agent's request must carry its own session token. A request that claims to be another agent, the Operator or another board is refused, as is one with a missing, old or wrong token.
  - **Nothing inside an agent session can act as the Operator.** `coral-board`, `coral-agent` and `coral` sign their requests as you only in your own terminal, never inside an agent's session.
  - Health checks keep working, and Coral Hub's own access rules are unchanged. Strict mode is a startup option only: it can't be changed through settings or Coral Hub, and agents don't inherit it.
- **What it doesn't do:** it stops mistakes and agents acting as someone else through Coral's tools, not a malicious program running as your own user. See "Agent identity and sign-in links" in the auth docs for how to turn it on and how to recover.
- **Sign-in redirects stay on this Coral.** A `return_to` address with a backslash, a control character or a second slash is refused, so a crafted sign-in link can't send the browser to another site.

### Changes

- **`coral-agent notify`** posts a notification from a hook or a workflow step (they used `curl`, which strict mode refuses).
- The identity report lists the routes each caller would lose in strict mode, so you can check before turning it on.

### Testing

- `tests/stress/run_identity.sh --strict` runs a whole team (three teams of four agents, restarts, wake and resume) under strict mode and checks that only deliberate probes are refused.
- A flaky dashboard test (agent_ui) is fixed.

### For integrators (Coral Hub, scripts)

- Strict mode answers **401** `dashboard_auth_required` (dashboard and `/ws/` without a sign-in), **401** `session_token_required` (agent tool without a valid token) and **403** `identity_mismatch` (a token used as someone else). A page load is redirected (307) to `/auth?locked=1&return_to=…`.
- Scripts on this computer: use `coral-board` / `coral-agent` from your own terminal (they sign requests), or the API key. `GET /api/health` and `POST /api/cloud/cli-hello` stay open.
- Requests through the Coral Hub tunnel are unaffected.

## v1.4.20 — Coral Hub tunnel hardening, phone View mode, lighter live updates

**Security fixes for Coral Hub users — please upgrade.** This release closes the remaining findings of an audit of the Coral Hub tunnel and hub proxy, makes Coral Hub's View mode usable on phones, and makes the dashboard's live agent updates smaller.

### Security (Coral Hub)

- **The tunnel can't pass cookies or security headers under another spelling.** Header names in responses coming back through the tunnel are normalized before filtering, so a `set-cookie`, `refresh`, `content-security-policy` or `access-control-*` header in unusual capitalization can no longer slip past the filter and reach the browser on the Coral Hub site.
- **WebSockets only for WebSocket routes.** Coral Hub and the machine's tunnel accept a WebSocket only on Coral's WebSocket paths (`/ws/…`); a WebSocket opened against an API path is refused before it reaches Coral.
- **Peeking at an agent through Coral Hub can't choose who's asking.** Through Coral Hub, reading an agent's terminal from the board ignores the caller-supplied subscriber name, and never changes the task's activity time. On this computer nothing changes.

### Coral Hub

- **View mode works on phones.** The "View only" label fits the header on narrow screens (tap it for the full reason), and the message box and Send are visibly read-only while you're in View mode.
- **Links to your Coral Hub machines pages.** On a dashboard opened through Coral Hub, the Servers tab can show links that Coral Hub provides to its machines pages. Only plain links to `/machines…` pages are shown.

### Changes

- **Lighter live updates.** The dashboard on this computer now receives only the fields of an agent that changed, instead of the whole agent, every time something updates. Coral Hub's own feed still gets whole rows.

### Testing

- The browser test runner removes its temporary folder after a passing run (about 250 MB each), and keeps only the results and logs after a failure. The troubleshooting guide explains what to do when test runs fill the disk.
- Several timing-sensitive tests no longer fail on a busy machine, and a data race in the analytics tests is fixed.

### For integrators (Coral Hub, scripts)

- **Re-pin `pkg/hubproxy` and `pkg/tunnel` from v1.4.20.** A WebSocket to `/api/remote/{id}/api/…` now gets **403** at the hub. Tunnel response header names are canonicalized. No API changes.
- `GET /ws/coral?diff=fields` (opt-in; ignored with `feed=hub`) sends a changed agent as `{_partial: true, server, session_id, name, agent_type, …changed fields}`, with `null` for a field that was removed. Through the tunnel only the exact value `diff=fields` is accepted.
- `GET /hub/nav.json` (served by Coral Hub): a list of `{path, label}`; only paths matching `^/machines(/[a-z0-9-]+)*$`, labels of 1-40 characters, at most 8 items and 4 KiB are used.
- Through Coral Hub, `GET /api/board/{project}/peek` ignores `subscriber_id`.

## v1.4.19 — Coral Hub acts as the Operator again, with attribution; sign-in links; fewer requests when busy

**Coral Hub users at Interact or Launch act as the Operator again, by default, and every such action now records which Hub user did it.** You can opt out. This release also gives each agent its own identity (logged only for now), lets the desktop apps sign in by themselves, and makes the dashboard and Coral Hub enrollment back off instead of retrying hard.

### Coral Hub

- **Coral Hub acts as the Operator by default, with attribution.** Through Coral Hub at Interact or Launch, a Hub user can post as the Operator and create, assign, amend, reassign and complete tasks, as before v1.4.18. Each such message shows **Operator via Coral Hub · hub-user-…** in the dashboard, and agents see the same in what they receive: the text starts with `[Operator via Coral Hub, hub-user-…]`. Every action is written to coral.log.
- **Opt out:** Settings > Coral Hub > **Don't let Coral Hub act as the Operator**. Then chat messages from Coral Hub go out as the Hub user, and Operator task actions are refused (as in v1.4.18). The choice is kept if you disconnect and reconnect, and it can only be changed at this computer.
- **Recent actions as the Operator through Coral Hub** are listed in Settings > Coral Hub (time, action, board, Hub user, result).
- Still never through Coral Hub: changing board settings, making yourself an Orchestrator, reading every message, or acting as the Operator without saying which Hub user you are. View can't post.
- **Coral Hub enrollment backs off.** Restarting Coral while a Coral Hub sign-in code is pending reuses the same code instead of asking for a new one. When Coral Hub is down or busy, Coral waits longer between attempts (30 s, doubling up to 15 minutes, and as long as Coral Hub asks), and the wait survives a restart.

### Sign-in and agent identity

- **Each agent gets its own identity.** Every agent Coral starts gets a private session token, and Coral records which agent made each request. In this release that's for the log only: a request claiming to be someone else is written to coral.log, nothing is refused, and nothing changes for you or your agents.
- **`coral open`** opens the dashboard with a one-time sign-in link (`--print` prints it).
- **The desktop app, the tray and `launch-coral` sign in by themselves** with a fresh one-time link, never a key or a prompt.

### Fixes

- **The dashboard backs off when Coral or a machine is busy.** When the server answers "too busy" (429 or 503), the dashboard's refreshes pause and retry later instead of adding load; on Coral Hub, a busy machine pauses only that machine's views. A change that a busy or unreachable machine refused says so plainly.
- **"Terminal gone":** an agent whose terminal session has disappeared shows as asleep, offering Restart only.
- **Unread-message checks work for roles with spaces.** For roles such as "Backend Dev" the agent hook's unread check built an invalid address and never ran; it does now.

### Testing

- Every dashboard test suite now has exactly one runner, and the test run refuses to start if one has none, so a suite can't be silently skipped. Five suites that had never run now run and pass.

### For integrators (Coral Hub, scripts)

- **Pin v1.4.19's hubproxy.** `X-Coral-Hub-User` is now passed on every board write (`/api/board/{project}/…`, not GET or HEAD), as decided by `tunnel.ForwardsHubUser`, which the hub proxy and the tunnel share. A hub on v1.4.18's hubproxy gets **403** `hub_user_required` on Operator task actions; its chat posts keep working.
- Message `origin`: `hub_operator` (the Operator through Coral Hub, with `hub_user`), `hub_user` (posted as the Hub user), `hub_user_unknown` (no header; posted as `Coral Hub`). Operator posts through Coral Hub start with `[Operator via Coral Hub, hub-user-…] `.
- `GET /api/system/hub-operator` is readable through Coral Hub at View and returns `{enabled, disabled}`; `PUT` is local only. A missing header on a task action gives **403** `hub_user_required`; a malformed or repeated one **400**; a name collision **409**; with the opt-out on, **403** `operator_identity_required`.
- Overload: a 429/503 on `/api/remote/<id>/` pauses only that machine unless the response carries `X-Coral-Hub-Overload: 1`.
- Agents get `CORAL_SESSION_TOKEN` in their own shell only. `GET /api/system/identity-report` is local only.

## v1.4.18 — Security fixes: Coral Hub identities, agent environments

**Security fixes — please upgrade.** Through Coral Hub, a request can no longer post or act as the Operator or an Orchestrator, and every chat message from Coral Hub shows the Hub user who sent it. Agents no longer inherit Coral's own server settings, such as its secret key or Coral Hub address.

### Security

- **Coral Hub can't act as the Operator or an Orchestrator.** A board write that arrives through Coral Hub and names the Operator, names an Orchestrator (or a member with orchestrator rights), or asks to receive every message is refused (403). Turning on auto-accept through Coral Hub is refused too. Requests made at this computer are unchanged.
- **New, off by default: "Let Coral Hub act as the Operator"** (Settings > Coral Hub). When on, people using this Coral through Coral Hub can do what you do as the Operator here. Each use is written to coral.log, and it turns off when you disconnect from Coral Hub.
- **Chat messages from Coral Hub show who sent them.** A chat message sent through Coral Hub is posted as the Hub user (`hub-user-…`) and gets a "Coral Hub user" badge; the sender name in the request is ignored. Names starting with `hub-user-`, and the name **Coral Hub**, are reserved. A message from an older Coral Hub that doesn't say which user sent it is posted as **Coral Hub**, marked "Coral Hub, user not identified", never as the Operator or an agent.
- **Agents don't inherit Coral's server settings.** Agents, workflow steps and hooks no longer receive Coral's own `CORAL_…` settings, such as `CORAL_SECRET_KEY`, `CORAL_CLOUD_URL`, `CORAL_TLS_*` and any added later. They still get what agent tools use (`CORAL_URL`, `CORAL_SESSION_NAME`, `CORAL_SUBSCRIBER_ID` and similar) and every non-Coral variable (`PATH`, API keys). An already-running tmux server is cleaned before each new agent starts.
- The Dashboard explains why an Operator-only or auto-accept action is refused through Coral Hub.

### For integrators (Coral Hub, scripts)

- Coral Hub's proxy and the tunnel pass `X-Coral-Hub-User` only on `POST /api/board/{project}/messages`; it is dropped on every other route and method, and honoured only on requests that came through the tunnel.
- Tunnel chat posts: the sender is the `X-Coral-Hub-User` value (`hub-user-` + 8 hex characters), returned with `origin: "hub_user"`. A missing header posts as `Coral Hub` with `origin: "hub_user_unknown"`; a malformed one gets **400** `hub_user_required`; a name that matches a member or role gets **409**.
- Tunnel board writes naming the Operator get **403** `operator_identity_required`, and ones naming an Orchestrator get **403** `orchestrator_identity`.
- `GET`/`PUT /api/system/hub-operator` (local only; never through Coral Hub).
- Test servers: run them with `--cloud-url off` (or without `CORAL_CLOUD_URL`) so they don't connect to Coral Hub.

## v1.4.17 — Security fixes: terminal agents, identities, webhooks, sign-in; Linux ARM64 downloads

**Security fixes — please upgrade.** This release closes a way for a board message to run commands in a terminal agent's shell, validates the names agents and people use on boards, guards outbound webhooks, hardens sign-in, and warns plainly when remote access isn't encrypted. Linux downloads now come for ARM64 too.

### Security

- **Nothing automated is typed into a terminal agent anymore.** A terminal agent is a plain shell, so text Coral typed into it, such as a "[Wait resolved] …" notice quoting a board message, could run as a command. A message or sender name containing `$( )`, backticks or a line break could run commands in that shell. Coral now never types board notifications, nudges or other automated text into a terminal agent; LLM agents still get them, always as one line.
- **Board identities are validated.** Names, subscriber ids, job titles and the like can't contain control or invisible formatting characters, line breaks, or text-direction overrides (they're refused with the character named). Coral's own senders, such as **Coral Task Queue**, can't be imitated with look-alike spacing, invisible characters, fullwidth letters or different case, and `hub-user-…` names are reserved for Coral Hub.
- **Outbound webhooks are guarded.** Job-run webhooks, board subscriber webhooks and webhook targets you create are checked when saved and again when sent, including after DNS resolution and redirects. Loopback, link-local (cloud metadata) and private addresses are refused by default; set `webhook_allow_private` if you send webhooks to machines on your own network.
- **Sign-in hardening.** `POST /auth/logout` ends a session, and regenerating the API key signs out every session. The session cookie is marked Secure behind an HTTPS reverse proxy (only when the proxy is trusted). The API key no longer appears in logs, even in part. Only failed sign-ins count toward the rate limit. The hub proxy drops `api_key` from websocket URLs.
- **Remote access says when it isn't encrypted.** The remote-access prompt, the startup log and Settings now say that remote access is plain HTTP unless Coral runs with a certificate. New: `--tls-cert` and `--tls-key` (or `CORAL_TLS_CERT` / `CORAL_TLS_KEY`) serve HTTPS and secure websockets directly. Coral Hub or a TLS reverse proxy are the other secure options.
- **Agent CLI paths are checked.** A `cli_path_<type>` setting must be an absolute path to an executable file.

### New

- **Linux ARM64.** `coral-linux-arm64-<version>.tar.gz` (Graviton, Ampere, Raspberry Pi 5, Docker on Apple silicon) is built and tested on native ARM64, signed and attested like the x86-64 tarball. Pick the one for your machine (`uname -m`: `x86_64` → amd64, `aarch64` → arm64).
- **Uploads are cleaned up.** Files uploaded to Coral are kept for 30 days (`uploads_retention_days`, up to 365). When the uploads folder passes 1 GB (`uploads_max_mb`), the oldest uploads are removed first; one less than an hour old is never removed.

### Changes

- **Board chat stays readable.** The task queue's own lines (new, claimed, completed) collapse into one row, so people's messages aren't buried.
- **Coral Hub "View" says what it reads.** When connecting, the View level is described plainly: Coral Hub can read every agent's terminal output and chat, all board chat, files, tasks and boards, and can't type, change or start anything.
- A Coral Hub that is down or answers with an error is reported as unreachable ("try again in a minute"), with exit code 4.

### For integrators (Coral Hub, scripts)

- Identity fields (`subscriber_id`, `as`, `assignee`, job titles and similar, in bodies and queries) are refused with **400** when they contain control or formatting characters, are too long, or use a reserved name or the `hub-user-` prefix.
- Automated text is never typed into `terminal` agents.
- `POST /auth/logout`; regenerating the key revokes all sessions.
- Webhook create/update answers **400** for a disallowed target.
- Downloads: `coral-linux-arm64-<version>.tar.gz` and its `.sigstore.json` bundle; `SHA256SUMS.txt` lists both Linux tarballs. Verify as in the README.

## v1.4.16 — Security fix in agent launch; connect any Coral to Coral Hub; no more licence

**Please upgrade:** this release fixes a way for a crafted launch setting or imported definition to run shell commands. Coral is also now free to use with no licence key or demo limits; only the terms of service remain. A Coral running on a headless server, over SSH or in a container can connect to **Coral Hub**. The board task list is paged and much lighter, terminal input over the websocket is confirmed, and a batch of fixes landed.

### Security

- **Agent launch commands now quote every user- or file-supplied value.** A crafted flag, resume id, or imported team, template or workflow definition could previously run shell commands when an agent launched. Thanks to our Security Reviewer.

### New

- **Connect to Coral Hub from anywhere Coral runs.** On first launch, after the terms, Coral offers **Connect to Coral Hub** (app.coralai.ai), **Run your own hub** (`coral --hub`) or **Skip for now** to keep Coral local; Settings > Coral Hub connects later. For a server without a browser:
  - `coral cloud connect` in a terminal on that machine (over SSH, for example) prints a code to approve in Coral Hub. It only talks to the Coral running on that machine, and checks it really is that Coral before sending anything.
  - For automated setups (containers, provisioning), a one-time credential file can be handed to `coral cloud connect --ticket-file` or placed where Coral reads it at startup; it's removed after use.
  - If the code expires before it's approved, Coral explains that Coral Hub is in closed beta and links to the access request. A busy hub, a denied request and an already-connected server each get a clear message (`--force` reconnects).
  - Opening a server's Coral Hub settings from another computer now explains that connecting has to happen on that computer, with the command to run.
  - While a connected server's link to Coral Hub reconnects, it shows **Connected to Coral Hub as <name> (reconnecting…)** instead of "Not connected", and Coral keeps working locally meanwhile.
- **`coral db compact`.** Run it with Coral stopped to shrink `sessions.db` once; it writes a backup first. API call metrics are now kept for 14 days by default (`call_metrics_retention_days`, 1 to 90), the dashboard's own refresh polls are no longer counted, and two unused indexes are dropped. On a busy server this took the database from 2 GB to 1.2 GB.
- **Log terminal input (debug).** A Settings toggle (or `CORAL_DEBUG_INPUT=1` / `--debug-input`) that records everything Coral sends to agent terminals, with where it came from, for troubleshooting. It's off by default, readable only by you, and size-capped. It can contain secrets you type, so turn it off when you're done; **Delete the log** removes the file.

### Changes

- **No licence and no demo limits.** The developer-licence purchase, the reminder on the 25th launch and the beta tier's 2-team / 8-agent limit are gone. An old `license.dat` is ignored.
- **The board task list is paged and light.** A board shows its open tasks and the most recent completed ones without their full text, and loads the rest as you scroll ("Load more"). The full task loads when you open it. Each refresh of a 2,000-task board used to download 17.5 MB; in testing, a 70-task board's default page is 28 KB against 1.1 MB for the full list. The dashboard also no longer starts a refresh while one is running, or refreshes a hidden tab.
- **Terminal input is confirmed.** Text sent from the composer over the terminal connection is cleared only once Coral confirms it reached the agent; if the terminal is gone, it comes back in the composer.
- **Knowledge distillation** keeps running when you leave the team and shows its progress when you come back. If Coral restarts mid-run, it says the run was interrupted. If some agents' knowledge can't be extracted, their earlier knowledge is kept unchanged and the tab says which agents failed; before, the error text could end up in an agent's knowledge.

### Fixes

- **`--backend pty` works again on macOS and Linux.** Every PTY agent failed to start ("operation not permitted"). Thanks to QA for finding it behind skipped tests.
- A line typed in the dashboard terminal now runs in zsh (it used to stay on the line, unsubmitted).
- Resume of a sleeping agent wakes it; its tags move to the new session, and history keeps its own.
- A background job left in an exited agent's shell no longer hides that the agent exited.
- Scheduled jobs: a job paused after three failures runs again when you re-enable it; unknown time zones and folders that aren't git repositories are refused; one-off runs work in a folder under Coral's data folder, not next to your repository.
- `launch-coral` no longer deletes other Coral instances' agent logs from the shared temp folder; a server started with its own data folder keeps agent logs in `<data folder>/logs`.
- Launching a plain terminal agent no longer logs an "unknown agent type" warning.
- A data race when stopping a PTY agent is fixed (QA).

### For integrators (Coral Hub, scripts)

- `GET /api/board/{project}/tasks` returns open tasks plus recent completed ones, without bodies, by default. Use `full=1` for the full list and `cursor` for the next page; a cursor only works for the board it came from.
- `/ws/terminal`: send `terminal_input` with an integer `id` to get `terminal_input_ack {id}`, `terminal_input_failed {id, not_sent}` or `terminal_gone {id, not_sent}`. Without an `id` (or with a non-integer one) input is typed as before, with no answer. A gone terminal at attach now gets `terminal_closed`, not "attach failed".
- The Coral Hub can opt in to per-input read-only notices with `StreamPolicy.ReadOnlyNotice` (pkg/hubproxy).
- New cloud routes are local-only and never reachable through the hub: `POST /api/cloud/cli-hello`, `/api/cloud/redeem`, `/api/cloud/connect/cancel`, and `DELETE /api/system/debug-input-log`. Auto-accept enable refuses a credential in the query string through the hub.
- The connector status has `last_error_code` (`code_expired`, `access_denied`, `rate_limited`), so a UI never matches on wording.
- Releases keep the v1.4.15 signing: verify with `gh attestation verify` or `cosign verify-blob` as in the README.

## v1.4.15 — The server no longer freezes; nothing is typed into a shell after an agent exits; signed Linux downloads

**Please upgrade.** A Coral started from a terminal window could freeze completely if that window stopped showing output. This release fixes that, stops Coral from typing into a plain shell after an agent's CLI has exited, and makes recovering an agent whose terminal is gone a one-click action. Linux downloads are now signed, and every release can be verified.

### Fixes

- **The server no longer freezes when its terminal stops reading.** Coral copies its log to the terminal it was started from. If that window stopped taking output (for example after Ctrl-S, or a terminal app that stops reading a background window), the next log line blocked and the whole server froze: the dashboard stopped responding, agents got no messages, and Coral could not be stopped normally. Coral now writes `coral.log` first and never waits for the terminal; if the terminal falls behind, it shows how many lines it skipped (they're all in `coral.log`). Messages that explain why Coral stopped, such as "another Coral server is already running", still reach the terminal.
- **Nothing is typed into a shell after an agent's CLI exits.** When an agent's CLI exited and left its terminal at a shell prompt, Coral could still type board messages, task prompts and other automated input into that shell, where it ran as shell commands. Coral now checks first and sends nothing to an agent whose CLI has exited. About a minute later the agent shows as asleep, and **Wake** starts it again in the same terminal. Anything you type yourself is still sent.
- **Restart and Resume work when an agent's terminal is gone.** Before, for about a minute after a terminal closed, Restart and Resume were refused with "The agent's terminal is gone". They now start the agent in a new terminal in its own folder, without touching any other agent.
- **Resume keeps the agent's identity.** A resumed agent keeps its display name, tags and board subscription (job title, receive mode), as Restart already did.
- **"Terminal gone" is clear, and drafts are never lost.** Typing into an agent whose terminal has closed now shows the same message whether you use the terminal view or the composer: a line under the composer with a **Restart** button, a chip on the agent's row and a badge in the header. The text you were sending comes back in the composer and the draft is kept; it's cleared only once Coral confirms the send.
- **Starting without a terminal never hangs.** Started in the background or as a service, Coral used to wait forever for an answer to a first-launch question nobody could see. Now remote access stays off and nothing is saved (you're asked on the next start from a terminal), a password-protected database explains that it needs a terminal, and the terms of service prompt says to run Coral once from a terminal. `--remote` no longer asks the remote-access question.
- **The board task list no longer piles up requests.** On a board with many tasks, a slow tab could start a new full task-list fetch every 10 seconds before the last one finished. The task list now waits for the previous fetch and pauses while the tab is hidden.
- **Sidebar: yellow means it needs you.** "Task assigned · idle" is now a muted ring with a neutral label. Yellow is kept for states that need your attention, such as Needs input.
- **Input reaches only the agent it's meant for.** Coral matches an agent's terminal by its exact session id. Tools that still address an agent by name only are logged as deprecated (see the note for integrators below).

### Security: signed, verifiable downloads

Every package in this release is built and signed by Coral's release workflow on GitHub, with no long-lived signing keys (Sigstore keyless signing). The release adds `SHA256SUMS.txt.sigstore.json` and `coral-linux-amd64-1.4.15.tar.gz.sigstore.json`, and every package has a build-provenance attestation. To check a download:

```bash
# With the GitHub CLI (any package)
gh attestation verify coral-linux-amd64-1.4.15.tar.gz --repo cdknorow/coral \
  --signer-workflow cdknorow/coral/.github/workflows/release.yml

# Offline, with cosign v2.4 or later
cosign verify-blob SHA256SUMS.txt --bundle SHA256SUMS.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/cdknorow/coral/\.github/workflows/release\.yml@refs/tags/v'
sha256sum --check --ignore-missing SHA256SUMS.txt
```

If either check fails, the file isn't an official build: don't run it.

### Other changes

- **Cloud Hub is now called Coral Hub** in the dashboard and the docs.
- Coral records which version of the terms of service you accepted.
- When a tmux command fails, `coral.log` now includes tmux's own error message.

### For integrators (Coral Hub, scripts)

- **Restart and Resume on a gone terminal** now return **200** and recover the agent in a new terminal, instead of **409** `terminal_gone`.
- **The terminal websocket** (`/ws/terminal/...`) sends `{"type":"terminal_gone","code":"terminal_gone","not_sent":true}` when input or a resize can't be delivered, then `terminal_closed` when the stream ends. Before, such input was dropped silently.
- **Name-only writes are deprecated.** Sending messages or keys, or answering a prompt, without `session_id` still works but is logged as deprecated; a later release will require `session_id`.
- **`pkg/license`** exports the terms text, version and digest.

## v1.4.14 — Security fix: input could reach the wrong agent; auto-accept permission prompts

**Security fix — please upgrade.** Input meant for an agent whose terminal had closed could reach another agent's terminal. This release also adds auto-accept for agents' permission prompts, fixes a hub's remote server that stayed Unreachable until you clicked Test, makes Coral use the agent CLI path you set everywhere it starts one, and fixes a batch of smaller problems.

### Security

- **Input for an agent whose terminal had closed could reach another agent's terminal.** When an agent's terminal had gone (for example it was closed or crashed) but Coral still listed the agent, Coral looked for the terminal by a loose name match. A message, a key press or an answer to a permission prompt meant for that agent could then go to another agent's terminal: one working in the same folder, one whose name started with the same letters, or, for a short name, an unrelated agent. Answering a permission prompt this way could approve the other agent's pending action. Coral reported success, and this could last up to about a minute, until the agent was marked asleep.
  - **Affected:** earlier versions, when one agent's terminal closed while other agents were running.
  - **Fixed:** Coral now reaches an agent only through that agent's own terminal. If it's gone, sending a message or keys, answering a prompt, resizing, restarting or resuming is refused with "The agent's terminal is gone" and nothing is sent anywhere; the agent's prompt and screen read as empty instead of showing another agent's. Kill, Kill All and Sleep end only that agent's own session (before, they could end another agent's). About a minute later the agent shows as asleep, and **Wake** brings it back.
  - Write routes require `session_id`; input is never routed by name. Routes that read or act on one agent (its screen, kill, restart, the terminal socket) also require `session_id` or the agent's exact terminal name.
  - Auto-accept (new in this release) already checked for the agent's own terminal.

### New: auto-accept permission prompts

For an agent you trust with its task, Coral can answer its permission prompts for you. Open the agent's menu (⋮) and turn on **Auto-accept prompts**. Claude and Codex agents are supported.

**Risky actions can be auto-approved.** Coral answers "Yes" to the agent's permission prompts, and that can include deleting files, force-pushing, running `sudo` or changing files outside the agent's folder. Coral warns you about this before turning it on. Only use it for a task you trust, ideally in a sandbox or a disposable worktree.

- **Only "approve once":** Coral answers only prompts asking to approve a tool call, and never picks "always allow" or an option that changes settings.
- **What still waits for you:** questions, plan approvals and anything Coral doesn't recognise.
- **A best-effort safety net, not a guarantee:** Coral holds back some high-risk actions it recognises, such as recursive deletes outside the agent's folder, force pushes, `sudo`, and writes outside the folder. It says why in the agent's **Activity**. It is not a full shell parser and doesn't follow scripts, so risky actions may still be approved.
- **Easy to see and stop:**
  - An amber **AUTO** chip marks the agent; click it to turn auto-accept off. In a popped-out agent, the header button is the switch.
  - **Stop all auto-accept** in the top-bar menu (or `coral auto-accept off --all`) turns it off everywhere.
  - Every approval is listed in the agent's Activity.
- **Turns itself off:**
  - when the agent restarts, resumes or wakes, or when the time limit you chose (optional, 15 minutes to 8 hours) runs out;
  - when the same prompt keeps coming back, or there are more than 30 approvals in a minute.
- **Runs on the Coral server,** so it keeps working with the dashboard closed. It answers only in the agent's own terminal: if that terminal is gone, Coral sends nothing, rather than risk typing into another agent's.
- **Only you can turn it on, from a dashboard:** on this computer, or on another device after logging in. Coral refuses it from agents, from their tools and CLI, from API keys, and from pages on other sites.
- **From the Cloud Hub:** needs Launch access on this computer and always has a time limit (1 hour unless you choose, at most 8 hours). It also ends when Coral restarts, the Cloud link is removed, or the hub's access drops below Launch. The new Settings switch **Cloud Hub can turn on auto-accept** (on by default) can only be changed on this computer; turning it off ends any auto-accept the hub turned on.

### Fixes

- **A remote server no longer stays Unreachable until you click Test.** On a hub, if one request to a remote failed during a short network blip (for example while you had one of its agents open), the Servers page could keep showing the server as Unreachable for up to a minute after it was back, or until you clicked **Test**. If that failure was an unreadable key, the hub stopped checking the server altogether until Test. Now a failed request makes the hub check the server again at once, so it shows Online within about a second when it's fine, and a key that can be read again is picked up without Test. The hub's log records each server status change on one line.
- **Workflows, Distill and summaries find the Claude CLI you configured.** Coral now looks for an agent's CLI the same way everywhere it starts one: the path in **Settings > CLI Paths** first, then `PATH`, then the usual install locations (`/usr/local/bin`, `/opt/homebrew/bin`, `~/.local/bin`, nvm). Before, agent workflow steps, Team Knowledge Distill, session summaries and **Generate Team** (Generate a team with AI) ignored the setting. In the macOS app started from Finder, which has no Homebrew `PATH`, they failed with `agent binary "claude" not found`, and summaries quietly fell back to a plain extract. When no CLI is found, the error now says to install it or set its path in Settings > CLI Paths. The launch-time "CLI not found" warning also checks the configured path, so it no longer shows when a custom path is set.
- **Scheduled jobs, one-off task runs, Restart and Resume use the agent CLI you configured** in **Settings > CLI Paths**. Before, they ran whichever `claude` (or other agent CLI) came first on `PATH`.
- **Launching in a directory that doesn't exist** returns a clear error that names the directory field (400, not 500). The launch dialog marks the field and puts focus on it.
- **A failed workflow step shows why.** The run view shows the step's error output (the last 40 lines it wrote to stderr), and the run header quotes its last line, for example `step 1 (two) failed: exit code 3: boom`. A collapsed output no longer shows a clipped strip, and the Output toggle is a real button.
- **Team view:**
  - The team-view button is always shown and reachable by keyboard (Enter opens it).
  - The group menu shows on keyboard focus.
  - Opening the team view scrolls the team chat to the newest message.
  - Closing the team view from its Tasks tab no longer leaves the team chat stuck under the task list.
- **Tasks panel:**
  - In a narrow panel each task wraps, with its full title on its own line, so there's no sideways scroll.
  - Priority reads High, Medium, Low or Critical, and a blocked task reads Blocked.
  - The task id is a button you can reach by keyboard.
  - Dependency reasons are in plain words, for example "not finished yet (in progress); this task starts when it succeeds".
- **Agent panels:**
  - Clicking a panel in an agent's panel list shows it at once, even while the panels are refreshing; before, it waited for that refresh to finish. Closing a panel returns to the list at once, too.
  - Deleting a panel removes it from view right away; before, its card stayed visible until you switched agents.
- **Message board:** a long unbroken word wraps instead of widening the feed.
- **Docs tab:** if the docs fail to load, the tab says so with a Retry button instead of staying blank.
- **Chats tab:** with no chat open, it shows an empty state for chats instead of the Agents home.
- **Analytics, Tool & API Calls:** shows agents' tool calls only. The dashboard's own internal requests (`/api/health`, `/api/tracking/event`, `/api/system/status`, `/api/remote/…`) are no longer recorded, and rows already stored for them are left out of the summary.
- **Durations:** minutes carry into the hour, so a duration never reads "60m" or "69h 60m".
- **Settings, API key:** the key is masked, has its own full-width line, and has Show/Hide and Copy buttons. Copy also works over http. At desktop width the field had been squeezed to almost nothing.
- **Servers tab:**
  - "Last contact" everywhere, shown only for a server that isn't online.
  - An unreachable server adds "Is Coral running on that machine? Check the URL."
  - The Local row shows "This hub · Coral <version> · N agents".
- **Phone:**
  - The tab strip has no scrollbar and fades at the edge where more tabs are hidden.
  - The "+T" button is now a team icon, labelled "Send with team reminder".
- **Desktop:** the agent search box says "Search", so the placeholder is no longer cut off.
- **Links to a view:** a link to a view restores only the view the page was opened with. A view you open before the agent list first loads is no longer reopened, and the list no longer loads twice.

### Upgrade notes

- **Cloud Hub:** auto-accept through the Cloud Hub works once the hub updates to Coral 1.4.14 (Coral's team coordinates that). Until then, turn it on from Coral's own dashboard.
- **Reverse proxy:** if you run Coral behind a reverse proxy on the same machine that rewrites the `Host` header, the dashboard behind it can't turn auto-accept on (the request's origin no longer matches). Turning it off still works.
- API users:
  - New routes `POST /api/sessions/live/{session_id}/auto-accept/enable` and `…/disable`, and `POST /api/auto-accept/disable-all`. Live sessions gain the `auto_accept*` fields; see the agent docs.
  - When an agent's own terminal is gone, `send`, `keys`, `resize`, `answer-prompt`, `restart` and `resume` return 409 `{error, code: "terminal_gone", not_sent: true}` (`send` returned 500 "session not found" before, or wrongly succeeded).
  - Write routes (`send`, `keys`, `answer-prompt`, `resize`) require `session_id` (and `agent_type`). `answer-prompt` and `resize` are new to this; without them they return 400 `{code: "session_id_required"}`. The dashboard always sends both.
  - **May break scripts and custom views:** the routes that read or act on one agent now also need `session_id` unless `{name}` is the agent's exact terminal name (`claude-<uuid>` and the like). These are `GET /api/sessions/live/{name}`, `/capture`, `/poll`, `/info`, `/prompt-options`, `/kill`, `/restart`, `/attach`, `/resume` (its `current_session_id`) and the `/ws/terminal` socket. Called with a bare agent or folder name, they return 400 `{code: "session_id_required"}` (the socket closes with 1008). Add `?session_id=` (it's in the live list), or use the exact terminal name. Custom views that read an agent by name need the same change.
  - A launch with a missing working directory now returns 400 `{error, field: "working_dir"}` instead of 500.
  - Workflow step results gain an `error_tail` field on failed steps.
  - A task's dependency reason is now plain words ("not finished yet (in review)") instead of the raw status ("review_pending").
- **Database:** Coral adds four auto-accept columns to its sessions table the first time it starts after the upgrade; there's nothing to run by hand.

### For contributors

- Test runners (`tests/frontend/run.sh`, `run_cdp_suites.mjs`, `tests/stress/*.sh`) and Go test binaries drop an agent shell's `CORAL_*` identity, its production links (Cloud Hub URL, hub mode, tmux socket) and its workflow/step variables, so a test started from an agent shell can't pick them up.
- All Go code is gofmt-clean, and a new `gofmt` CI workflow keeps it that way.
- Flakes fixed:
  - Two tmux tests (`TestSendPromptCancelledAfterPasteStillSubmits`, `TestClient_ListPanes_WithSessions`) wait on state instead of fixed timing.
  - `artifact_preview_safety` counts only artifact reads.
  - `agent_ui`, `mobile_connect`, `sleeping_agent_view`, `agent_popout` and `papercuts_browser` wait for the state they check.
- New browser suites: `auto_accept_browser`, `terminal_gone_browser`, `papercuts_browser`, `board_team_ux_browser`.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.13.

## v1.4.13 — Artifact links work through the Cloud Hub again

**No new security fixes in this release.** v1.4.12 remains the current security release; this release fixes a problem it introduced for Cloud Hub users.

### Fixes

- **Artifact links work through the Cloud Hub.** In 1.4.12, new artifact links gained a `?v=2` suffix (`/api/artifacts/<id>?v=2`), and the Cloud Hub refused them because the hub only passes query parameters it knows for each route. Opening an artifact's link or its preview through the Cloud Hub failed. The hub now passes exactly `v=2` on that route; any other value or parameter is still refused, both by the hub and by Coral itself. Links opened directly on Coral, and older links without `?v=2`, were not affected.

### Upgrade notes

- The Cloud Hub picks this up when it updates to Coral 1.4.13 (Coral's team coordinates that), and machines connected to it should be upgraded to 1.4.13 as well.
- Nothing to migrate; there are no database changes.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.12.

## v1.4.12 — Security fix: artifact links could run script on the dashboard

**Security fix — please upgrade.** This release fixes a way for a file an agent uploaded to run script with the dashboard's access.

### Security

- **Artifact links could run script on the dashboard.** Before 1.4.12, an HTML or SVG file that an agent uploaded as an artifact opened inline from Coral's own address when you followed its link (**Copy link**, or the `/api/artifacts/<id>` URL), so a script in it ran with the dashboard's access. The same applied to an HTML file a workflow step wrote into its run's files.
  - **Affected:** anyone who opened such a link on an earlier version, in a dashboard connected to Coral directly (on the same machine or over the network) rather than through the Cloud Hub. Artifact links opened through the Cloud Hub were not affected (the hub already served them sandboxed); previews inside Coral were already sandboxed.
  - **Fixed:** artifact and workflow-file responses are now sandboxed (`Content-Security-Policy: sandbox`, `nosniff`), and HTML, SVG, XML, JavaScript, PDF, Markdown and unknown types download instead of opening. Plain text, JSON, images, audio and video still open in the browser. Preview inside Coral works as before.
  - **Cached copies:** Coral clears the browser's cached copies once, the next time you open the dashboard over https or on the same machine. New artifact links have a fresh form (`/api/artifacts/<id>?v=2`) so they never match an old cached copy; older links keep working with the same protection.
  - **If you use Coral over plain http on a LAN address** (for example `http://192.168.x.x:8420`), clear your browser's cache for that site once after upgrading.

### Upgrade notes

- **Opening an artifact link for HTML, SVG, Markdown or PDF now downloads the file** instead of displaying it. Use **Preview** in Coral to view it.
- API users: the artifact `url` returned by uploads and the team artifacts `preview_url` now end in `?v=2`. `coral-agent artifact download` accepts both forms.
- Nothing to migrate; there are no database changes.

### For contributors

- Five more browser suites wait on the app's state instead of fixed sleeps (`sleeping_agent_view`, `vendor_lazy`, `hub_readonly`, `lazy_panels`, `team_workspace_activation`); the sleep ratchet is down to 82.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.11.

## v1.4.11 — Unreachable servers explained, access changes that apply at once, and lighter startup

**No security fixes in this release.** v1.3.43 remains the current security release. This release explains an agent whose server can't be reached instead of failing quietly, recovers it without a reload when the server returns, unlocks an agent as soon as you raise its server's access in the Cloud Hub, and trims what the dashboard loads before it starts.

### Fixes

- **An agent on an unreachable server says so.** On a hub, when an agent's server stops answering, a banner above the agent says which server is unreachable and that the agent shows its last known state. Sending is refused with that explanation (your draft is kept, nothing is sent), and the dashboard stops polling that agent. When the server is back, the banner clears and polling resumes within about a second, without reloading the page.
- **Hub errors in plain words.** Errors from a remote server (unreachable, unavailable, rejected key, address not allowed, too busy and so on) show as a sentence naming the server, never as raw JSON.
- **A recovered server's agents look normal again.** After a remote server came back, its agents could keep the "stale" styling until the page was reloaded.
- **Raising access unlocks the open agent right away.** When a Cloud Hub agent is read-only and you raise its server's access in the Servers tab, the agent you have open is unlocked as soon as the change is saved, with no need to select it again. It's unlocked only if the server's effective access now allows sending. If the machine's own setting still limits it, the agent stays read-only and the message explains why, for example "Read-only: This machine: View" with "Change it in Coral on that machine".

### Faster

- **Remote agents update promptly on a busy hub.** Activity on the hub's own machine could delay remote agents' updates in the dashboard by up to a second; they now go out with the next update. A hub also writes a remote server's unchanged status to its database at most once a minute instead of every 30 seconds.
- **Dialogs load when you first open them.** The code for the launch, settings, team and other dialogs no longer loads at startup; it loads the first time you open one. If that load fails, Coral says so ("Could not load this dialog") and tries again on the next click. Keyboard handling (Escape and Tab) and your theme and settings still work from the first moment. On a throttled phone connecting directly to Coral, the first load finished 6% sooner (5.17 s → 4.84 s), with 12% less JavaScript to run.

### For contributors

- Test servers get their own home directory, so they never index this machine's real Claude, Codex, Gemini or Pi chat history. The Kill all stress test works again: it had broken when static files became build-versioned.
- More browser suites wait on the app's state instead of fixed sleeps (`agent_ui`, `subagent_tasks`): the sleep ratchet is down to 124.
- The `agent_popout` browser suite waits on the popout's state instead of 31 fixed sleeps, and runs in 21 s instead of 31 s. 
- The agent-list parity suite now also compares the phone list at phone width, and `import_once` checks for the list at the moment the failure is reported, which removes a flake in full sharded runs.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.10.

### Upgrade notes

- Nothing to migrate; there are no database changes.
- On a hub, the Servers tab's last-seen time for an online server is now accurate to about a minute (status changes are still recorded at once).

## v1.4.10 — A much faster agent list, and scheduled jobs that don't fire the moment you create them

**No security fixes in this release.** v1.3.43 remains the current security release. This release makes the agent list update far more cheaply on busy servers, stops new scheduled jobs from running immediately, and makes the dashboard more resilient to a flaky connection.

### Faster

- **The agent list keeps up with busy servers.** Live updates are now drawn at most once per animation frame, so a burst of changes collapses into one update. Only the rows that changed are touched; the rest keep their elements. On a 250-agent list with the CPU slowed 4×, one agent's change went from 176 ms to 39 ms of main-thread time, a burst of updates from 98 ms to 5 ms per message, and the longest freeze from about 200 ms to under 50 ms.
- **Syntax highlighting loads when first needed.** The dashboard no longer loads any third-party script before it draws. Code in chat, the docs and file previews is coloured as soon as the highlighter has loaded, and stays plain if it can't load.

### Fixes

- **A new scheduled job no longer runs the moment you create it.** A job that had never run looked back a full day, so a "weekdays at 9:00" job created at 10:00 ran straight away. On a fresh install after 9:00 on a weekday, the built-in demo job did the same, starting a real agent and showing its "Daily Motivation" message. A job now only runs at times after it was created or last changed. An unchanged job still catches up on a time it missed while Coral was off.
- **Parts of the dashboard recover from a failed load.** If a panel or feature fails to load (for example on a flaky connection), Coral says so ("Could not load …. Check the connection to Coral, then try again.") and loads it properly next time, instead of failing until you reload the page.
- **Bigger touch targets, round three.** On touch screens: agent list rows, the agent search box, the sidebar's collapse and summary buttons, menu items, the list display options and the welcome screen's links and buttons.

### For contributors

- **Test readiness signals:** `window.__coralReady` lets tests wait for the app instead of sleeping. It resolves after startup, and offers `.liveListRendered()` and `.agentSelected(name or session id)`. The browser suites now use it; fixed sleeps over 100 ms are capped per file by a ratchet (`sleep_hygiene`), which only goes down.
- **Isolated browser tests:** before every suite the runner opens a fresh tab and clears the Coral site's browser storage, and test servers set `CORAL_NO_DEMO_SEED=1`. Under `npm run test:cdp:stress` (CPU 4×, 3 runs) the whole browser suite now passes every run.
- **Agent list kill switch:** setting `localStorage['coral-no-morph'] = '1'` (or opening the dashboard with `?nomorph=1`) turns off the incremental list updates and redraws the whole list each time, as before.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.9.

### Upgrade notes

- Nothing to migrate; there are no database changes.
- **Scheduled jobs:** editing, enabling or disabling a job now counts as changing it, so it won't run a time that passed before the change; it runs at its next time. A job you leave alone behaves as before.
- **`CORAL_NO_DEMO_SEED=1`** (also `true`/`yes`) starts Coral without the demo workflow and its scheduled job. It's meant for test and automation setups.

## v1.4.9 — View-only servers stay view-only, and raising a server to Launch asks first

**No security fixes in this release.** v1.3.43 remains the current security release. This release fixes a race that could make a Cloud Hub View-only agent look writable again, and asks for confirmation before a server is allowed to launch agents from the Cloud Hub.

### Fixes

- **A View-only agent stays read-only.** When the Cloud Hub tells the dashboard that an agent's terminal is read-only, the dashboard locks the message box and shows the read-only chip. If the server's access lookup finished just after that message, it could undo it: the chip disappeared and the agent looked writable again, while the hub still dropped anything you sent (the "Sent" problem fixed in v1.4.5). The hub's read-only message now holds until you select the agent again, however long the lookup takes.
- **Access changes apply at once.** After you raise or lower a server's hub mode in the Servers tab, the next message you send uses the new mode straight away, instead of up to 15 seconds later.

### New

- **Confirm before allowing Launch.** Raising a server's hub mode to Launch, from **Raise hub mode** or the Access menu, now asks "Allow <server> to launch agents from the Cloud Hub?" first, because Launch lets the hub start processes on that machine. Cancel is the default. If you cancel, nothing changes and focus returns to the control you used. Lower modes change at once, as before.

### For contributors

- The browser test runner (`tests/frontend/run_cdp_suites.mjs`) is sturdier. A suite that hangs is killed after its time limit and its server and Chrome are replaced. A failed suite is retried once on a fresh server and Chrome; if it passes only then, it's reported as FLAKY rather than passed (exit status 3; `--allow-flaky` treats that as success). `results.json` keeps every attempt's complete output.
- `npm run test:cdp:stress` (`--stress`) runs the suites three times on three shards with every page's CPU slowed 4×, and prints how often each unstable suite passed. Its first run found the read-only race fixed above.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.8.

### Upgrade notes

- Nothing to migrate; there are no database changes.

## v1.4.8 — A first load about 37% smaller, and Resume that never guesses the agent

**No security fixes in this release.** v1.3.43 remains the current security release. This release makes the dashboard's first load much lighter by loading the code editor, the terminal and rarely used panels only when you open them. It also stops Resume from guessing an agent's type, and enlarges more touch targets.

### Faster

- **About 37% less to download on the first load.** The code editor and the terminal now load the first time a view needs them, as do the Workflows, Cost, Docs, theme editor, template browser and chat search panels. On a throttled phone connecting directly to Coral, the first load went from 1.27 MB to 800 KB on the wire and finishes about 2 seconds sooner (7.4 s → 5.4 s), with half as much JavaScript to run. Revisits stay as fast as in v1.4.6.
- Opening one of those panels for the first time loads it once; later opens are instant. If it can't load, a message says so ("Could not load Docs. Check the connection to Coral, then try again.") and the next try loads it again.
- **The last unversioned file is versioned.** The app icon and the web manifest now load from the build-specific `/static/b/<build>/` address too, so a revisit asks the server for no static files at all.

### Fixes

- **Resume uses the session's real agent type.** When a past session was opened from a link before the history list had loaded it, and Coral couldn't look the session up, Resume could start a Codex session as Claude. The agent type now comes from the history list, else from the session's own transcript. If it's still unknown, Coral says so and doesn't open the Resume dialog.
- **Bigger touch targets, round two.** On touch screens: the side panel's tabs and close button, favourite stars, Browse icons and row actions (now visible without hovering), file explorer rows, the file filter, team artifact actions and the Servers tab's access selector. On the Cloud Hub sign-in and enroll pages, inputs and buttons are larger on touch too.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.7.

### Upgrade notes

- Nothing to migrate; there are no database changes.
- Code that calls the dashboard's panel functions from outside the page (devtools snippets, test harnesses), such as `selectWorkflow`, still works: each is a stub that loads its module on first use.
- `/manifest.json` at the site root still works; its icon paths now point under `/static/`.

## v1.4.7 — A compact server button in the agent list, and the phone side panel fixed

**No security fixes in this release.** v1.3.43 remains the current security release. This release replaces the agent list's server pill and chips with a compact server button, shows each machine's version, agent count and update status in the Servers tab, and fixes the phone top bar covering the side panel's tabs (a v1.4.6 regression).

### New

- **A compact server button in the agent list.** The large server pill above the list and the server chips on each row are replaced by one button in the search row: `● <name> ▾` for one server, `● N servers ▾` for several. It opens a details panel you can tap or reach by keyboard: status, version, last contact, number of agents, any update notice, and **Open in Servers**. Server names are shortened to the hub's label for the server, or its hostname without the domain; when two servers would get the same short name, both keep their full name.
- **Show server and Group by server.** The agent list's ≡ menu adds **Show server**, which puts each agent's short server name on its row (on by default when you have two or more servers), and **Group by server**, which groups agents into collapsible server sections. **Show server** is saved with your settings; **Group by server** and which sections are collapsed are remembered in this browser.

### Fixes

- **Servers tab rows show version, agents and updates.** Each machine's row reads like "Online · Coral 1.4.2 · 7 agents · last seen …" (the version is left out when unknown). On the Cloud Hub, a machine that needs an update shows the **Update** / **Update required** badge in its row, linking to the latest release.
- **Servers tab (Cloud Hub):** the stray word "null" under the introduction is gone, and the screen-reader-only "(online)/(offline)" text no longer shows up visibly or breaks long machine names.
- **Phone: the top bar no longer covers the side panel's tabs.** In v1.4.6 the larger touch targets made the phone top bar taller, but the side panel (Browse, Artifacts) still opened at the old height, so the bar covered its tabs. The panels now open below the top bar's measured height, whatever it is at your screen width, and follow it if it changes. The tablet sidebar and the team settings workspace get the same fix.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.6.

### Upgrade notes

- Nothing to migrate; there are no database changes.
- Cloud Hub: the new **Show server** setting is saved as `show_session_server` (`true`/`false`; unset means automatic). Add it to the account settings allowlist so it persists in cloud mode; without it the setting lasts only for the session.

## v1.4.6 — A much faster dashboard on slow connections, a Servers tab, and phone-sized touch targets

**No security fixes in this release.** v1.3.43 remains the current security release. This release makes the dashboard load several times faster on a slow phone connection and almost instantly on a revisit, turns Servers into a top-level tab, and makes the dashboard comfortable to use by touch.

> **Embedders (Cloud Hub), action needed:** `templates/cloud/signin.html` and `templates/cloud/enroll.html` are now Go templates and must be executed with `frontend.PageData` (or any value with a `StaticBase` field) instead of being served as files. An empty `StaticBase` renders the previous `/static/` paths. See **Upgrade notes**.

### Faster

- **Much faster first load.** Scripts, stylesheets and other text files are sent gzip-compressed (text assets 3.97 MB → 1.16 MB), and the code editor, syntax highlighter and terminal libraries no longer block the page from drawing. On a throttled phone at 390 px connecting directly to Coral (not through the Cloud Hub, which already compressed and cached), the first paint went from 11.9 s to 1.24 s.
- **Revisits download almost nothing.** The dashboard's files are served from a build-specific address, `/static/b/<build>/`, that the browser keeps for good, so a revisit loads them from its cache without asking the server again. The page itself is checked on every visit, so after an upgrade it always loads the new build. On the same throttled phone connecting directly to Coral (not through the Cloud Hub), a revisit now finishes loading in about 0.3 s, with 139 bytes of static files on the wire (v1.4.5 transferred 3.9 MB).

### New

- **Servers is a top-level tab** (on the Cloud Hub, or on a hub with remote machines), replacing the Servers dialogs. Each machine's row shows its Hub, Machine and Effective access, with a one-click **Raise hub mode**. Opening the tab puts focus on its heading, or on the machine's row when you arrive from a link. The server chips in the agent list are now buttons that open that machine's row, as does **Open Servers** on an access refusal. Status, version, last contact and access are shown on the row, not only in a hover tooltip. On a phone the rows stack (520 px and narrower), and Revoke and Remove use the error colour so they no longer look disabled (Revoke measured 4.99:1).

### Fixes

- **Touch-friendly controls.** On touch screens, the dashboard's buttons and links have hit areas of at least 44 × 44 px, including the Agent UI panel list. Mouse and trackpad layouts are unchanged.
- **Agent UI panel dialog.** The expanded panel is a proper dialog named after the panel, with a 44 px **Close** button. Focus moves to Close when it opens and back to **Expand** when it closes (Close, Escape or a click outside).

### Hub (self-hosted `--hub` and Cloud Hub)

- **Cloud Hub sign-in and enroll pages are templates now** (see the note at the top). They load their files through `StaticBase`, so the Cloud Hub can serve them from its own versioned prefix (`/static/v/…`); Coral's own prefix is `/static/b/…`.
- Through the Cloud Hub tunnel, files are still sent uncompressed (the connector never forwards `Accept-Encoding`).

### For contributors

- New tests pin that the compressed, cached dashboard and popout pages carry no secrets: the page data's fields must each be reviewed, and two different visitors get byte-identical pages.
- Code that imports dashboard modules from outside the page (devtools snippets, test harnesses) must import through `window.__coralStatic`, or it gets a second copy of each module. The browser suites do this.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.5.

### Upgrade notes

- **Embedders (Cloud Hub):** execute `templates/cloud/signin.html` and `templates/cloud/enroll.html` as Go templates with `frontend.PageData` (or any value with a `StaticBase` field), rather than serving them raw. An empty `StaticBase` renders the previous `/static/` paths.
- **Static files:** the standalone server serves the dashboard's scripts, stylesheets, fonts and icons from `/static/b/<build>/` with `Cache-Control: public, max-age=31536000, immutable`. Plain `/static/` paths still work for one release, with `no-cache` and an `ETag`. Text files are gzip-compressed when the browser accepts it.
- **Service worker:** its cache is renamed to `coral-v4`, which clears what earlier versions stored, and it no longer stores the build-specific files (the browser cache keeps them). On the Cloud Hub the service worker is not used.
- Nothing to migrate; there are no database changes.

## v1.4.5 — View-only Cloud Hub servers say so instead of pretending to send

**No security fixes in this release.** v1.3.43 remains the current security release. This release makes the dashboard tell you, before anything is sent, when a Cloud Hub server only allows View, and explains every access refusal in plain words.

### Fixes

- **In a Cloud Hub View-mode server the dashboard no longer says Sent when nothing was sent.** A server limited to View drops terminal input without an error, so the dashboard used to show "Sent" while the agent never got the message. Now every way of typing into an agent checks the server's access first: the message box, keys, quick commands, macros, team and board prompts, and typing in the terminal. Nothing is sent and your draft is kept. A toast explains what blocked it, for example "Blocked: my-mac needs Interact (Hub: View · This machine allows: Interact · Effective: View). Raise it in Servers.", with an **Open Servers** button. When the machine's own setting is the limit, the toast says to change it in Coral on that machine instead. The terminal header shows a read-only chip while the server is View-only.
- **Access refusals are explained, not shown as codes.** A launch or action that a server refuses for access (`access_mode`, `launch_disabled`, `local_mode`) shows the same "Blocked: …" explanation, naming the server, the access it needs and which setting is the limit.
- **Toasts are easier to use.** Every toast has a Dismiss button and closes with Escape. Errors are announced to screen readers right away, other toasts politely. A toast with an action button stays for at least 10 seconds.
- **Settings › Coral Hub** explains that the hub has its own access setting for each server (View by default), and the lower of the two applies.

### Hub (Cloud Hub)

- The dashboard reads the server's access from the Servers list, and also from a `{"type": "hub_read_only"}` message the Cloud Hub can send on an agent's terminal connection. Once received, the terminal stays read-only for that connection. Other non-JSON text messages on the terminal connection are ignored.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.4.

### Upgrade notes

- Nothing to migrate; there are no database changes.
- Cloud Hub: a hub may send `{"type": "hub_read_only"}` on a terminal WebSocket to mark that terminal read-only. Older dashboards ignore it.

## v1.4.4 — Coral Hub tells you when a machine needs an update, and history links always offer Resume

**No security fixes in this release.** v1.3.43 remains the current security release. This release shows Coral Hub's update notice for machines running an older Coral, and fixes the Resume button missing when a past session is opened from a link.

### New

- **Update notices from Coral Hub.** When the Cloud Hub reports that a machine's Coral is out of date, the server row shows an **Update** badge, or **Update required** in the warning colour, when the Cloud Hub needs a newer Coral for some of its features. The badge appears in the sidebar and phone server list, the row's tooltip, and the Cloud Servers dialog. The notice names the version to update to and links to the latest release on GitHub. Nothing is disabled, and machines that are current (or whose status is unknown) show nothing.

### Fixes

- **A session opened from a link shows Resume** even before the history list has loaded it. This covers `#session/<id>` links, the popout's history link, and sessions beyond the loaded page. Previously the transcript opened without its Resume button.

### For contributors

- `troubleshooting.md`: a plain `go build` reports `0.0.0-dev`, which a hub treats as an old version (it gets the plain feed, which takes that machine's dashboard notifications); use `make build` or `make dev`.
- The `history_resume_ui` browser suite no longer flakes; it waits for the page's own history load before injecting its fixtures.
- `tests/stress/run.sh --kill-all` waits for its own server to report it bound the port before trusting `/api/health`.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.3.

### Upgrade notes

- Nothing to migrate; there are no database changes.
- Cloud Hub: the dashboard reads the optional `coral_version`, `latest_coral` and `update` (`current`, `available`, `required` or `unknown`) fields on a `servers` entry. It shows a notice only for `available` and `required`; any other or missing value shows nothing. Older dashboards ignore these fields.

## v1.4.3 — Kill all no longer kills team agents, a live feed that keeps up, and team artifacts that fit any panel

**No security fixes in this release.** v1.3.43 remains the current security release. This release fixes a folder group's **Kill all** killing agents the group doesn't show, makes the dashboard react to agent changes at once, lists uploaded files with a team's task results, reworks the team artifacts panel for narrow windows, and brings the agent docs up to date with v1.4.x.

### Faster

- **Changes show up at once.** Launching, restarting, resuming, renaming, killing, sleeping, waking, archiving or unarchiving agents (one agent, a team, or all) now reaches every open dashboard within about 50 ms, instead of on the next poll of the live feed. A burst of changes, such as Sleep All, still costs one update.

### New

- **Uploaded files appear in Team Artifacts.** A team's artifact list now includes files its agents uploaded with `coral-agent artifact upload`, next to task results, newest first, including uploads from a member's earlier sessions. They show "No task" and source "upload" in Details, with Preview and Download.

### Fixes

- **Kill all on a folder group kills only the agents that group shows.** It used to kill every agent in that directory, including team members (listed under their team), archived agents and workflow-run agents, none of which the folder group lists. The confirmation now counts exactly the agents it will stop, and an active agent search narrows it the same way it narrows the list. A workflow group's **Kill all**, which did nothing before, now stops that workflow's agents.
- **Team artifacts fit narrow panels.** The table shows Name, Type, Created and Actions, with Size and Task moved into each row's Details. There is no horizontal scrolling at any panel width or at 200% zoom. Below about 520 px only Name and Actions show, long names wrap to two lines (the full name is in the tooltip), and the actions stay on one row.
- **Producer is shown in artifact Details.** It was blank for every artifact.
- An untasked artifact with no readable name is called "Uploaded artifact" instead of "Task #0 artifact".
- **API:** `GET /api/sessions/live/{name}/chat` (`after=0`) on an agent with no messages yet returns an empty page with `start: 0` instead of the poll shape, and rejects a bad `before=` there too. `POST /api/views` and `PUT /api/views/{id}` return the saved view with its real `created_at`/`updated_at` (they were empty), and `PUT` of a view that doesn't exist answers 404 `view not found`.

### Docs

- The agent docs (`coral-go/agent_docs`, also served in the app) are updated for v1.4.x and checked against the code: sleeping and archived agents, the `/send` and `/keys` exact-target rules and their error codes, chat history paging, the `/ws/coral` feed, hub and Cloud Hub behaviour, the board CLI's `--help` and option checks, cross-team messaging limits, artifacts and uploads, and dev-build versions. `GET /api/sessions/{id}/resolve` is now documented. Stale examples are fixed: artifact URIs use the bare hex id (`coral://artifacts/<hex>`), not the `sha256:` digest.

### For contributors

- `bash tests/stress/run.sh --kill-all` runs the folder Kill all check with real agents and the real dashboard on an isolated server. It fails on v1.4.2.
- Pull requests that touch code the Cloud Hub depends on are labelled `cloud-hub-impact` and get one comment listing the watched files (`.github/workflows/hub-impact.yml`).

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.2.

### Upgrade notes

- Nothing to migrate; there are no database changes.
- API users: `GET /api/board/{project}/artifacts` can now return upload rows: `source: "upload"`, `task_id: 0`, an empty `task_title`, `uri: "coral://artifacts/<hex>"`, and `produced_by` when known (otherwise `null`). Rows are deduplicated by id, and a task result wins over the same file as an upload.
- API users: see **Fixes** for the `/chat` empty-transcript page and the views `PUT` 404.

## v1.4.2 — Sleeping agents keep their history, an Archived state for agents you are done with, and input that always names its agent

**No security fixes in this release.** v1.3.43 remains the current security release. This release changes what you see for a sleeping agent, adds an Archived state that Wake All and team wake leave alone, and makes the input endpoints refuse anything that does not name its exact agent.

### New

- **Archived agents.** Archive an agent you are done with but want to keep: from the agent's menu choose **Archive** (a running agent is stopped first). Archived agents leave the main list, team groups, sleeping summaries and agent counts and appear in one collapsed **Archived (N)** section at the bottom of the agent list, captioned by team. Selecting one shows its history read-only, with **Unarchive** (back to plain sleeping) and **Wake** (wakes and unarchives it). **Wake All** and **team Wake** leave archived agents asleep, and an archived agent is never counted against the live-agent limit.
- **Tasks are never given to an archived agent.** `coral-board status` reports it as `archived` (not available), creating, editing or reassigning a task to it is refused with "unarchive it before assigning it work", and the task queue never nudges it, including the "tasks available" nudge for unassigned work, which now always goes to an agent that can claim it.
- **Sleeping agents show their chat history.** Selecting a sleeping agent shows its conversation as usual, scrollable and with older history loading as you scroll up, instead of an overlay. The composer says "This agent is sleeping" with **Wake** (and **Restart…**; the popout shows Wake only) and the input is disabled; nothing can be sent to a sleeping agent from any control.

### Fixes

- **Switching agents after scrolling up opens the next chat at the bottom** again, instead of at the top, and no longer fetches an older page it does not need (a regression in v1.4.1's scroll loading; through a hub that was a wasted round trip to the machine). Older-history loading can no longer stall after a page lands.

### Changed

- **`/send` and `/keys` require the exact agent.** `POST /api/sessions/live/{name}/send` and `/keys` now always need `session_id` and `agent_type` matching the agent in the path. A call by bare name used to reach whatever terminal carried that name, including a sleeping or archived agent's leftover pane; it is now refused with 400 `{error, code: "session_id_required"}` and nothing is typed. A sleeping agent answers 409 `session is sleeping`, an archived one 409 `session is archived`. The dashboard, popout, mobile view, macros and quick actions already send both (the `coral-board` and `coral-agent` CLIs do not call these endpoints); scripts calling them by name must add both fields (the curl examples in the Troubleshooting and Quickstart docs are updated).

### Hub (self-hosted `--hub` and Cloud Hub)

- **Archive needs v1.4.2 on the machine.** The dashboard offers Archive only for machines that report the `archived` field (v1.4.2, server and connector); on older machines the menu item is hidden. Archived agents are listed last in a machine's feed and session list, are not counted in the hub's per-machine agent count, and never report "waiting for input".

### For contributors

- Dev and local builds report a version in `/api/health`, the startup log and to Coral Hub: `<next>-dev+<commit>` (for example `1.4.2-dev+337c80bc`, `.dirty` with uncommitted changes) instead of an empty string. Use `make dev` or `make build` to get that base; a plain `go build` reports `0.0.0-dev+<commit>`, which hubs treat as very old. Release builds are unchanged, and dev builds still never offer themselves an update.
- CI's Linux jobs are pinned to `ubuntu-24.04` ahead of GitHub moving `ubuntu-latest` to Ubuntu 26; the Linux release build was verified on `ubuntu-26.04` with the new manual **Linux runner dry run** workflow.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.1.

### Upgrade notes

- **Archive needs v1.4.2 on the machine** (server and connector). Through a hub, older machines don't offer Archive.
- **`/send` and `/keys` refuse calls that don't name the exact agent:** 400 `session_id_required` without `session_id` and `agent_type`, 409 `session is sleeping` or `session is archived`. Scripts calling them by name must add both fields; see **Changed**.
- **Database:** adds one column, `live_sessions.is_archived`, migrated automatically on first start. Nothing else to do. An older Coral started on the same data directory ignores the column, so archived agents appear there as plain sleeping agents; any it wakes are unarchived automatically when this release starts again.
- **API users:**
  - `POST /api/sessions/live/{session_id}/archive` and `/unarchive` return `{ok, sleeping, archived}`; `POST .../wake` on an archived agent also unarchives it. Both new routes are `interact` through the Cloud Hub tunnel, like sleep.
  - `/api/sessions/live` rows and `/ws/coral` entries (including `feed=hub`) carry `archived: true|false` on every row, and an unarchive diff carries `archived: false` explicitly.
  - `GET /api/board/{project}/status` reports `availability: "archived"` and counts it in `summary.archived`.
  - Task create, edit (`assigned_to`) and reassign naming an archived agent answer 409 `{error, code: "assignee_archived"}`.
  - `/send` and `/keys`: see **Changed** above.

## v1.4.1 — Faster agent switching through the hub, chat history that loads as you scroll, and steadier hub connections

**No security fixes in this release.** v1.3.43 remains the current security release. This release makes switching between agents fast on a hub, even over a slow link, and stops the hub from calling a busy machine unreachable.

### Faster

- **Switching agents.** Opening an agent's chat loads its last 100 messages instead of 400, and large chat responses are gzip-compressed when the browser accepts it. Through a self-hosted hub on a slow link (80 ms round trip, 20 Mbit/s) the first chat paint went from 271 ms to 116 ms, and the first response from 386 KB to 34 KB.
- **Older history loads as you scroll up**, 200 messages at a time, keeping your place, with no "Load more" click (the button stays for find-in-chat and retries). Messages that arrive while you read are not duplicated, and switching agents mid-load never shows the previous agent's messages.
- A self-hosted hub keeps idle connections to each machine for 5 minutes instead of 60 seconds, so switching agents reuses a warm connection.

### Hub (self-hosted `--hub` and Cloud Hub)

- **Older history works through the hub and the Cloud Hub tunnel**, including with machines that have not been upgraded: if a machine does not support the new paging, the dashboard falls back to the old paging for that server after one retry.
- **A busy or refused machine is no longer marked unreachable.** A machine at its concurrent-request limit answers 503 with `Retry-After: 1`. A request the machine's connector rejects answers 400, and a path or query its route table does not allow answers 403. All three carry `{error, code}` (`too_many_streams`, `bad_request`, `path_not_allowed`), and the machine keeps its status. Previously the first two were a 502 that marked the machine unreachable. A real outage is still a 502 that marks it.
- **The Cloud Hub tunnel never relays compressed responses.** The connector does not forward `Accept-Encoding` to the local Coral, and a gzip response from it is decoded before relaying, with the size limit applied to the decoded bytes. Any other encoding is refused with a 502 JSON error. This is hardening: Coral's own handlers do not compress unless asked, and this ensures they never do so through the tunnel.

### Fixes

- **Fixed duplicated messages** when an agent posted while you were loading older history.
- **Clearer Interact access.** In the hub's server list and the machine's Cloud panel, the Interact access description now says it also allows changing board settings, including turning on cross-team messaging.

### For contributors

- Frontend suites take their server from `CORAL_TEST_URL` through `tests/frontend/test_base.js`. They never read `CORAL_URL` and refuse port 8420 with no override (`CORAL_TEST_ALLOW_PROD` is gone). `run.sh` and `run_cdp_suites.mjs` set it for you.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.4.0.

### Upgrade notes

- Nothing to migrate; there are no database changes.
- Mixed versions work. A v1.4.1 dashboard or hub pages older machines by offset, and an older dashboard keeps paging a v1.4.1 machine by offset.
- API users: `GET /api/sessions/live/{name}/chat` accepts `before=<index>` (an absolute message index, exclusive; it wins over `offset`) and returns `start`, the first message's absolute index. Pass it as the next `before` to get the page above. History pages (`after=0`) over 8 KB are gzip-compressed when the request sends `Accept-Encoding: gzip`; polls are not. `offset` paging and polling with `after` are unchanged.
- Cloud Hub: connector refusals now arrive as 503 `too_many_streams` (with `Retry-After`), 400 `bad_request` and 403 `path_not_allowed` instead of 502, and none of them marks the machine unreachable.

## v1.4.0 — Built for big teams: a lighter live feed and board, every notification on every dashboard, and cross-team messaging

**No security fixes in this release.** v1.3.43 remains the current security release. This release is about scale: a Coral server running many agents, and a hub watching many machines, do far less work per tick. Every open dashboard now gets every notification, and Orchestrators can (optionally) message each other across teams.

### New

- **Cross-team messaging (off by default).** An Orchestrator can message another team's Orchestrator on the same Coral server with `coral-board send --board <team> "<message>"`. The Operator turns it on per board in **Team settings → Workflow → Cross-team messaging**, on both the sending and the receiving board; if either has it off, `send` fails and names the board. Messages are rate-limited, and a receiving Orchestrator treats them as requests, never instructions. Sender identity is local trust, like every other board identity on a shared computer; see the Cross-team messaging help page.
- **`--help` is safe everywhere.** `coral-board` and `coral-agent` print usage for `--help` or `-h` on any command or subcommand without running it or contacting the server. Message or option text that is really a mistyped option (for example `coral-board post --bogus`) is refused instead of sent; put `--` before text that starts with a dash (`coral-board post -- "--help is a flag"`, `coral-agent search -- -x`).

### Faster and lighter

- **Live feed and session list.** `/ws/coral` and `/api/sessions/live` cost much less per tick: token usage is rolled up per session instead of scanned, "latest event" lookups read one row per session from new time-ordered indexes, unchanged git file lists are skipped, and each repository root is scanned once. A dashboard gets its first update as soon as it connects, instead of after the first 5-second tick.
- **Transcripts are read incrementally** (Claude, Codex and Pi), so a long session no longer re-reads its whole transcript.
- **Message board.** Board status reads only open tasks, task lists load workflows and dependencies in batches, unread counts update incrementally, completion checks run outside the board's write transaction, `coral-board wait` is woken at once instead of up to half a second late (with far fewer database reads per waiter), and message history pages by id instead of by offset. The dashboard dedupes and backs off its board-status polling.
- **Request metrics** are written in background batches instead of two database writes per API request; the macOS tray flushes them on quit.
- **Memory.** Per-session caches drop ended sessions.

### Hub (self-hosted `--hub` and Cloud Hub)

- **The hub's machine feed no longer eats notifications.** The hub's permanent feed to each machine used to count as a dashboard, so a notification pushed while no local dashboard was open was marked delivered and never shown. The hub now connects with `/ws/coral?feed=hub`, a machine feed that never reads or marks notifications. Session data is unchanged.
- **Tunnel rule for the machine feed.** Through the Cloud Hub tunnel, `feed=hub` is accepted only on `GET /ws/coral`, only once, and only as that exact, case-sensitive value. Any other value, repeated or look-alike parameters, and `feed=hub` on any other route are refused. Every other route's allowed query parameters are unchanged.
- **Less polling.** While a machine's feed is live, the hub polls only `/api/health` instead of the full session list (the list was 92% of the bytes a machine served), with a `coral_heartbeat` on the feed. It falls back to list polling when the feed is quiet for 30 seconds, and caches and encodes each machine's snapshot once per change.
- **Richer feed.** `/ws/coral` sessions now carry `branch`, `git_state`, `repo_name`, `capabilities`, `context_pct` and `context_window`, with the same values as `/api/sessions/live`; diffs include them only when they change.
- `GET /api/health` returns exactly `{status, version, hub}`, a fixed contract the hub relies on.
- Remote-agent polling starts only in hub mode.

### Fixes

- **Every dashboard gets every notification.** Each open dashboard (tab or window) used to race for notifications, so only one of them showed each one. Now each connection has its own cursor. A notification counts as delivered only after it was actually sent. A dashboard that opens later gets the notifications no dashboard has shown yet (from the last 5 minutes), and a reload never replays ones already shown.
- **Live feed fields match the session list.** On `/ws/coral`, `summary` and `status` are `null` when empty and `display_name` is `""` when unset, exactly as on `/api/sessions/live` (the feed used to send `summary: ""` and `display_name: null`). Both now come from the same code, so they cannot drift again. The dashboard is unaffected; scripts that read the feed directly may notice.
- **Board health report.** It now says "#N is blocked but has no prerequisites" instead of claiming all of that task's prerequisites completed, and it warns when a blocked task depends on canceled work (canceled tasks are stored as "skipped").
- **Quieter logs.** Routine per-subscriber notifier lines moved to DEBUG; real failures log at WARN.

### For contributors

- Test binaries cannot reach the live Coral server, the live tmux server or the real `~/.coral`; a test that tries fails at once.
- `CORAL_PPROF_ADDR` (loopback addresses only, off by default) serves Go's profiler, for example `CORAL_PPROF_ADDR=127.0.0.1:6060 coral`.
- Opt-in load tests: `CORAL_LOADTEST=1` (board status under load) and `CORAL_HUBSCALE=1` (hub with many machines).
- The test suites run much faster: `go test -race ./...` takes 86 s instead of 279 s, `go test ./...` 37 s instead of 59 s, and the browser (CDP) suites 227 s instead of 519 s. `node tests/frontend/run_cdp_suites.mjs --shards N` runs the browser suites on N isolated server and Chrome pairs (about 117 s with 2 shards).

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.3.45.

### Upgrade notes

- The database gains new indexes, a `token_usage_rollup` table, a `board_settings` table and a `board_messages.external_json` column. They are created automatically on first start; there is nothing to do, and existing data is not changed.
- Cross-team messaging stays off until the Operator turns it on for each board.
- Self-hosted hub: a v1.4.0 hub works with older machines (they treat its feed as an ordinary connection, as before, and the hub falls back to list polling when a machine has no `/api/health`). Upgrade the machines too, so the hub's feed stops consuming their notifications.
- Cloud Hub: a machine's tunnel accepts the Cloud Hub's `feed=hub` connection from v1.4.0 on. The Cloud Hub sends `feed=hub` only to connectors newer than 1.3.45, and falls back to the plain feed URL if a machine refuses it (`path_not_allowed`), so older machines keep working.
- API users: `staleness_seconds` in list and feed payloads is rounded down to the unit the dashboard shows (detail endpoints keep exact values). On `GET /api/board/{project}/messages/all`, `before_id=N` now returns the newest page below N (oldest first), and `after_id` is new; offset paging still works.

## v1.3.45 — More of Coral on the Cloud Hub: Agent UI panels, desktop alerts, and lighter browser load

**No security fixes in this release.** v1.3.43 remains the current security release. This release is about the Cloud Hub (the hosted dashboard that shows your machines): it closes the gaps found in the last parity audit, adds Agent UI panels and optional desktop alerts there, and makes the dashboard lighter on the network. On your own computer the visible changes are the browser-load fixes and the new Desktop alerts switch below; Agent UI panels there work as before, with a per-panel rate limit on responses.

### New

- **Agent UI panels on the Cloud Hub.** Panels an agent publishes now show in the hub's Agent UI tab and can send responses back to the agent. A panel is agent-authored content, so it is labelled "agent-authored panel" and runs in a locked frame: no network, no storage, no way to reach the dashboard or your hub session, and one kind of message only (a response to the agent, rate-limited per panel). The hub only reads and relays; publishing and deleting panels stay on the machine. Known limits of any browser sandbox, documented in the Agent UI help: a panel can cause a DNS lookup and, through WebRTC, send UDP packets to an address it chooses. Open panels only from agents you trust.
- **Desktop alerts (off by default).** Settings has a new "Desktop alerts" switch. When it is on, this browser shows a desktop notification while the tab is in the background and an agent needs input. It is per browser (not saved on the hub), needs your browser's permission, uses no service worker, and shows only the agent's name and the server's name, never what the agent said. Your operating system may keep those names in its notification history.
- **Cloud Hub parity.** The changed-files view has its diff-mode selector, Sleep All / Wake All work per server, On the hub, changing the diff mode needs at least Interact access to that machine. Branch and dirty badges, repository name and context use on hub-connected machines come from the matching hub release.

### Fixes

- **Less browser load.** The terminal poll runs every 3 seconds instead of every 500 ms while the terminal socket is open; the live feed reconnects with backoff and jitter instead of retrying at a fixed pace; a feed update that only changes the idle counter no longer redraws the whole agent list; and the page no longer fetches the session list twice at start-up. When the hub ends your session the dashboard re-checks the session before giving up on the feed.

### Release process

- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** Same as v1.3.44.

### Upgrade notes

- No database or configuration changes.
- Agent UI panels, the hub's git badges and the hub-side diff-mode route need the matching hub release (v1.3.45); until a hub is updated those parts keep working as before or are not offered.

## v1.3.44 — Phone fixes for the Cloud Hub, and signed-release hardening

**No security fixes in this release.** It fixes tapping an agent at phone width when Coral is opened through the Cloud Hub, and tightens how macOS releases are signed and published. v1.3.43 remains the current security release.

### Fixes

- **Agents now open at phone width on the Cloud Hub.** The phone-width agent list re-ran each row's click handler through `eval()`, which the hub's content security policy refuses, so tapping an agent did nothing. The rows now keep their click handlers, and the list uses classes instead of inline styles. Local Coral was unaffected. The settings menu now opens above the side panel.
- **Phone-width polish.** The panel's close button no longer covers the Tasks tab at 390 px, the artifacts table scrollbar is thinner, and the command box placeholder is a short "Send to <agent>…" instead of a sentence cut off mid-word. Desktop is unchanged.
- "Check for Updates" is hidden when Coral is shown through the Cloud Hub.

### Release process (first in this release; v1.3.43 did not have these)

- **macOS:** `Coral.app` itself is now notarized and stapled, not only the DMG, so it still passes Gatekeeper offline after being dragged out of the DMG. The release build now verifies the DMG and the app inside it (`codesign --verify --strict`, `spctl`, `stapler validate`, hardened runtime on every executable) and fails if any check fails. The v1.3.43 DMG was already signed with a Developer ID certificate and notarized.
- **`SHA256SUMS.txt`** is now published with every release, covering all four packages. It sits in the same release as the packages, so it detects a corrupted download, not a compromised release.
- **Windows build is unsigned (beta); verify with SHA256SUMS.txt.** This is the same as v1.3.43. Windows signing is prepared in the build and stays off until it is enabled.

### Upgrade notes

- No database or configuration changes.

## v1.3.43 — Security fix: custom views

**Custom views stored by an agent or script could run with the dashboard's rights; fixed (severity: High, local). Update recommended for everyone who uses custom views or runs agents with untrusted input.**

### Security

- **Custom views no longer run with the dashboard's rights.** A view's HTML used to run inside the dashboard's own origin, so a view stored by an agent, a prompt-injected tool call or any local script could call every local API (including settings that run commands) the next time you opened its tab. Views now run in an isolated sandbox that cannot make web requests or navigate away from Coral, and read data only through a small read-only list of endpoints. (Browsers' content-security policies cannot block WebRTC, so this is not a promise of absolutely no network access.) Views that called `fetch('/api/...')` or `window.coral.get(path)` keep working for the listed read endpoints; any other endpoint, or any write, no longer works.
- **Views not written in the dashboard ask first.** Every existing view is treated as "created by an automated caller" after the upgrade: it shows its source and runs only after you press "Run this view" (once per page load). Open and save it in the editor to mark it as yours. Views created by an automated caller can read only the list of agents, their tasks, tags, board names and the chat-history list, and cannot read terminal output, files, git state, notes or events even after you press Run; views you create in the dashboard keep the full read list.
- **Only the dashboard can change views.** Creating, editing or deleting a view now requires a request that comes from the Coral dashboard in a browser on this computer. This stops other websites, proxies, the Cloud Hub and agents calling the API with curl. It does not stop a program on this computer that deliberately forges browser headers and can then mint a view marked as yours; the sandbox, the read-only allowlist and the click-to-run gate are the protections against that. Opening the dashboard through remote access from another device can no longer create, edit or delete views. A per-install dashboard token is a possible later hardening.
- **Also security-relevant, first released in v1.3.42 (published 2026-10-11) and repeated here for anyone still on an older version:** (a) *File reads through the local API:* absolute paths, symlinks, `.git` and credential files could be read; file endpoints are now bounded to the session's repository by default (this affected the self-hosted `--hub` from v1.3.41); (b) *Cross-site scripting in inline handlers* from quote-unsafe escaping is fixed with quote-safe sinks and a fail-closed allowlist; (c) the build toolchain moved to Go 1.26.9, which carries the standard-library security fixes. Items (b) and (c) were not described in the v1.3.42 notes.

### Fixes

- Preview-pane tabs, the team dialog, the dependency picker, the agent configuration form and workflow step output regained their working element ids (a regression from the XSS hardening pass above, which had turned string ids into `NaN`).
- Artifact downloads sanitise file names (trailing dots and spaces, reserved Windows names, `: * ? " < > |`) and no longer navigate the page. SVG and HTML artifacts render only as images or inside a script-free sandbox.
- Looking is not writing: opening a session through the Cloud Hub no longer sends a background terminal resize or shows a toast for background refusals.
- The dashboard header no longer overflows at 1024 px wide.

### New

- **Cloud Hub dashboard:** per-user settings, themes, tags and saved views, and artifact viewing. Artifacts open at the View tier, are never cached, and are capped at 25 MiB. The Cloud Hub connector stays off by default. It gains one read-only route (an agent artifact by its exact digest, View tier) and reports response lengths to the hub; nothing else changes in what the hub can reach.

### Upgrade notes

- The database gains a `created_via` column for views automatically; existing rows become `api` (hence the one-time "Run this view"). Older Coral versions ignore the column.
- A view that uses `fetch('/api/...')` or `window.coral.get(path)` works only for the listed read endpoints.
- A dashboard opened through remote access from another device can no longer create, edit or delete views.
- Self-hosted `--hub` spokes whose project folders are outside the user's home directory must set `CORAL_PROJECT_ROOTS` (a path list), as in v1.3.42.

## v1.3.42 — Multi-server hub, Cloud Hub connector and bounded file endpoints

- **Multi-server hub (opt-in).** Start one Coral with `coral --hub` (or `CORAL_HUB=1`), then add other Coral servers under Settings > Servers. Their agents and teams appear in the hub's dashboard, you can open their chat and terminal, and you can launch agents and teams on them, including browsing the remote's folders. Remote API keys are stored encrypted. See the Multi-server hub guide.
- Artifacts: an upload now appears in the agent's own Artifacts tab, including for agents without a team. Chat opens absolute and `~/` file paths, and the sidebar no longer groups agents by folder. The agent header shows the git branch, or the folder when there is none.
- **Cloud Hub connector (opt-in).** `coral --cloud-url https://your-hub` (or `CORAL_CLOUD_URL`) lets this server connect outbound to a Coral Cloud Hub so you can reach it from the web. Off by default: with no URL set, Coral opens no connection to the cloud. Enroll from Settings > Coral Hub with a short code; the server token is stored encrypted with the same key as remote API keys. The hub can read everything you view through it.
- **Access modes, set on this computer.** Settings > Coral Hub has a maximum mode: View (default: read the terminal, chat, files, tasks and board), Interact (adds typing into running agents and changing tasks and the board; typing can run commands) and Launch (adds starting agents, writing files and browsing folders). The hub's per-server setting can only lower it. Only the Coral dashboard on this computer can change it, never the hub.
- **Fixed route list.** Through the hub only routes on a reviewed list are reachable; settings, API-key, remote-server, webhook, licence and workflow-definition endpoints are not. A test walks the real router and fails when a new route has not been classified.
- **File endpoints are bounded** (also hardens the self-hosted `--hub`): reads and listings are limited to the session's repository, `.git` and credential locations are never served, and sizes are capped. Requests from the dashboard on this computer keep opening absolute paths an agent printed.
- **Action needed for the self-hosted `--hub`:** spokes whose project folders are outside the user's home directory must set `CORAL_PROJECT_ROOTS` (a path list) so the hub's folder browser and launch dialog can reach them.
- The Coral frontend moved to `coral-go/pkg/frontend` so the Cloud Hub can embed the same dashboard.

## v1.3.26 — Consistent dashboard styling, artifact tables and HTML preview

- Repository HTML and HTM files open in Preview as rendered webpages inside an empty sandbox with a restrictive CSP. Scripts, same-origin access and parent requests stay blocked; Edit and diff fallback still show source.
- The Files source picker uses text tabs with an active underline that stay visible during inline previews. Tabs follow standard keyboard navigation (arrows, Home, End) and return focus when a preview closes.
- Team artifacts are listed in a table with Name, Type, Size, Task, Created and Actions columns. Narrow screens scroll the table horizontally with Preview and Download pinned; full MIME type and references remain in Details.
- Shared type, icon, spacing and control-size roles are applied across navigation, chat, tasks, board, workflows, Agent UI, settings, dialogs, scheduler, docs and analytics. Narrow task footers and workflow headers no longer overflow, and close and icon buttons have accessible labels. Workspace layout, terminal and code fonts, and custom prose settings are unchanged.
- The chat composer's Cancel button is now a Stop icon. It still sends Escape; the terminal Esc button is unchanged.
- Added a contributor UI style guide at `coral-go/docs/ui-style-guide.md`.

### Verification and limits

The full Go suite passed. Isolated headless-browser suites passed for chat search, team artifacts, repository HTML preview, and cross-surface UI consistency in light and dark themes at desktop and mobile widths. The operator reviewed the rebuilt dashboard. No backend or database changes are included.

## v1.3.25 — SQLite cancellation recovery and stable agent lists

- Updated the Standard SQLite driver to modernc.org/sqlite v1.43.0. The previous pinned driver could leave Coral's single database connection returning `interrupted (9)` after a request cancellation raced query completion, until Coral restarted. The updated driver fixes that cancellation race.
- When essential live-session identities or board subscriptions cannot be read, the agent list returns a temporary 503 instead of an incomplete list. The dashboard keeps its last good agent and team display until a healthy response arrives.
- An exact-target send blocked by unavailable session metadata now reports that no input was sent. Delivery errors after the terminal call remain uncertain and are not automatically retried.

### Verification and limits

The full Go suite passed with bounded parallelism. A 20,000-iteration cancellation and data-persistence regression passed on macOS and in an isolated static Linux arm64 container. Focused route tests covered metadata failures and pre-delivery send behavior; an isolated browser test confirmed that repeated 503 responses preserve the visible agent list and that the next healthy response updates it.

The old-driver failure was reproduced locally and matched the remote report, and the user reported that restarting Coral restored service. The updated build has not been run on the affected host, so recovery there is not yet confirmed. This release does not resend a failed prompt automatically. Regular packages remain Standard builds without SQLCipher/OpenSSL; macOS includes bundled tmux. No Windows package is requested. The macOS deployment target is checked in binaries; an actual macOS 13 host runtime test remains outside verification.

## v1.3.24 — Faster Browse and reliable task assignments

- Browse avoids launching Git for every ordinary subdirectory when resolving a workspace. Large directory listings sort names once, reuse normalized sort keys, and build response paths only for the requested page. The tree reuses existing rows and updates keyboard focus without scanning every rendered row.
- Task edits preserve the current assignee when subscriber discovery is missing, fails, or returns malformed data. Delayed responses cannot replace a newer edit form or save changes to another board.
- The board CLI rejects bare names and unknown options in task reassignment instead of silently returning the task to the unassigned pool. Explicit `--to` reassignment and intentional unassignment remain available.
- Workers cannot complete tasks assigned to someone else. The assignee, Operator, and active registered orchestrators retain their completion permissions. Rejected completions leave task state and artifacts unchanged and do not post completion notifications.
- Markdown artifacts are recognized reliably when the host MIME database lacks `.md`. Uploads preserve useful filename extensions, and previews use the stored response filename independently of an extensionless display label. Existing Markdown uploads with a generic binary media type can render without changing their stored bytes.
- Clarified the Agent UI request text to describe content displayed in the panel. Replaced the legacy public documentation with current installation, teams, artifacts, Agent UI, privacy, and troubleshooting guides.

### Verification and limits

The expanded isolated agent_tasks stress suite passed **167 checks with zero failures**, including real CLI/API requests, mock-agent notification recipients, concurrent claims, rejected completion integrity, reassignment, browser assignment edits, and restart recovery. Focused Go and browser regressions and independent reviews also passed.

Local diagnostic benchmarks measured non-repository root discovery with 200 subdirectories at about 2.2 seconds before and 12 milliseconds after; listing 50,000 entries at 84 versus 26 milliseconds; and 11 expand/collapse operations in a 5,000-entry browser tree at 810 versus 39 milliseconds. These are local fixture measurements, not guarantees for every machine or filesystem. Directory sorting and visible-tree reconciliation still grow with the number of entries.

Board API identities remain caller-asserted under the existing local trust model. Regular packages retain the Standard build without SQLCipher/OpenSSL; macOS includes bundled tmux. No Windows package is requested for this release. The macOS deployment target is verified in binaries; an actual macOS 13 host runtime test remains outside local verification.

## v1.3.23 — Bundled tmux and clearer setup checks

- The macOS app includes tmux 3.7c, terminal definitions, and its statically linked libevent/ncurses dependencies. Homebrew is no longer required for tmux when using this app bundle. Claude, Codex, and other agent CLIs still need separate installation and authentication.
- Coral discovers bundled tmux automatically, including when started through a symlink. Explicit `CORAL_TMUX_BIN` overrides remain available. Companion hooks and terminal attach commands use the selected executable and handle paths with spaces. Existing tmux servers are never automatically killed to resolve version mismatches; status and logs report recovery guidance.
- Added privacy-preserving prerequisite observations for tmux, Claude, and Codex: available, missing, failed version check, or timed-out version check. Events contain controlled tool/status/source fields, honor telemetry preferences, and deduplicate repeated observations within a server run. They do not measure external installer outcomes or authentication success.
- Fixed missing CLI warnings and added explicit prerequisite rechecks. Failed checks and network errors have distinct messages, stale responses cannot overwrite a different form/provider, and PTY users are not incorrectly blocked by missing tmux.
- macOS packaging builds tmux natively for Apple Silicon and Intel from checksum-pinned sources, includes source/license notices, checks system-only dynamic dependencies and the macOS 13 deployment target, and requires signing and notarization for release publication.
- Updated the README demo to the new Loom video.

### Verification and limits

Focused backend and browser tests passed for prerequisite checks, telemetry filtering, and setup warnings. The real Apple Silicon tmux payload passed isolated session creation, capture, resizing, helper discovery, and reconnection tests with Homebrew absent from PATH. An isolated Coral startup selected the bundled executable, and the local Apple Silicon test DMG was reported working by the user on a new machine.

Those local checks used a development build with ad-hoc signing. Release CI separately builds and tests both native architectures before universal assembly and signing. The macOS 13 deployment target is checked in binaries; an actual macOS 13 host test has not been performed. Linux retains its regular static package and system-tmux requirement. No Windows package is requested for this regular release.

## v1.3.22 — Activation and launch diagnostics

- Added dashboard readiness, controlled startup/fetch failure codes, and daily visible-dashboard activity. Server starts and dashboard loads are measured separately.
- Added correlated agent/team launch request and result events with provider, backend, duration, and controlled failure categories. Team metrics distinguish requested, started, failed, and partially successful launches; failed members no longer inflate successful launch counts.
- Added dashboard composer submission intent and a separate milestone for terminal-accepted HTTP sends. Task completion events include success/failed outcomes, with a separate first-success milestone.
- Analytics schema version 2 adds random process/attempt/page identifiers and executable entrypoint attribution. The `launch-coral` server startup path now honors the same telemetry initialization and privacy settings as the main server.
- One-time analytics milestones are marked delivered only after acceptance. Bounded retries preserve event identity and the original milestone properties; opt-out invalidates queued work and clears pending snapshots. Snapshot timestamps retain subsecond precision.
- Added `CORAL_TELEMETRY_DISABLED=1` for automated execution, overriding saved opt-in. Release verification and frontend test runners explicitly suppress telemetry. The environment override is not baked into downloaded packages.
- Updated telemetry documentation and added the activation measurement plan. Usage analytics remains configurable in Settings, and keyless builds remain silent. Payloads exclude prompts, source code, transcripts, paths, credentials, and raw errors.

### Verification and limits

Focused Go tests with fake collectors and isolated browser tests passed for launch counts/categories, task outcomes, browser readiness and failure recovery, retry identity, opt-out, and strict event properties. No real PostHog events or live agent prompts were sent during testing.

A successful launch records process creation/submission, not provider authentication or model readiness. Confirmed-send tracking covers HTTP only; WebSocket composer events measure intent. First-response tracking is not implemented because reliable turn correlation is unavailable. Failures before the executable or dashboard code starts remain outside these events, and ordinary events have no durable outbox. The browser plugin document is a proposal, not an implemented browser feature.

## v1.3.21 — Files viewer and Agent UI requests

- The Files viewer now has four sources: Files, Browse, Artifacts for the selected agent, and Team Artifacts. Browse lists the selected agent's repository one directory at a time and opens text and image files using paths relative to the resolved repository root. Agent artifacts use stored task ownership for attribution.
- Artifact entries have an explicit Preview action alongside download or Open link. Coral-managed and inline content can be viewed in Coral, including Markdown and recognized JSON reports with a raw view. HTML and external link previews use restricted frames; sites that forbid embedding still need Open link.
- The Agent UI panel request form lets a user ask the selected agent for a panel. Requests retain a stable identity across retries, are saved before delivery, and report delivery failures. Terminal delivery requires a known safe input state; the form never sends a draft automatically.
- Agent guidance now describes Coral-managed artifact upload and Agent UI panels as the default meaning of publishing or sharing within Coral.

Focused Go checks and isolated browser suites passed for the changed routes, storage, terminal delivery, viewer sources, artifact previews and reports, and Agent UI behavior. The Browse regression was reproduced against the old path handling and passed with the corrected root-relative path. No live agent prompt was sent during verification. The agent browser Review popup remains a specification and is not part of this release.

## v1.3.20 — Mobile access requires opt-in

- Remote access defaults to off when no explicit preference is saved. Startup binds to loopback when the setting is absent, disabled, or unreadable; an existing saved opt-in is preserved.
- Opening the mobile QR view while access is off asks **“Mobile access is disabled. Enable it?”** Opening or cancelling does not change settings. Only **Enable** saves the preference.
- Enabling remote access requires restarting Coral. The dialog shows the pending state and withholds the QR code and API key until both saved and running access are enabled. Pending disable also hides connection details.
- Privacy status now reports saved and effective access separately, including pending restart states. Explicit loopback host restrictions remain in effect.
- Linux release CI now starts the shipped Standard server and runs real board commands inside a native scratch container without shared libraries before publishing the tarball.

## v1.3.19 — Standard package compatibility

- Regular Linux builds use pure-Go SQLite and produce static executables without a host OpenSSL or SQLCipher dependency. The package verifier rejects a dynamic loader, dynamic section, or shared-library dependency in any shipped Linux executable.
- Regular macOS bundles keep their native GUI, omit the SQLCipher/OpenSSL payload, and target the advertised macOS 13.0 minimum. The package verifier checks both architecture slices and external library imports; older macOS runtime behavior still needs a host-level smoke test.
- Standard startup and database APIs reject encryption requests and existing encrypted databases without migration or replacement. The settings API reports the build capability and cannot enable encryption in a Standard build.
- The regular Linux server and board CLI passed an isolated scratch-container smoke test with no glibc or crypto libraries: startup, join, post, explicit message read, and ordinary `coral-board read`.
- Database encryption remains an experimental source-build option behind the `sqlcipher` build tag with CGO. It is not included in the regular packages. The separate encryption verification workflow explicitly opts into that tag; this release has no encrypted package.

The v1.3.18 candidate was not published: its macOS verifier used an invalid `lipo` argument order. This release corrects the verifier; the v1.3.18 tag remains unchanged.

### Verification status

An unreleased eight-command Linux Standard package passed the static dependency verifier and a clean scratch-container startup plus `coral-board` join/post/read smoke without shared libraries in the image. The full default-build Go suite, focused no-CGO CLI/API tests, and installer upload regression passed. The macOS arm64 server and board CLI import only system libraries and encode a minimum below the advertised macOS 13.0. Full signed universal macOS and older-OS package checks remain for the release workflow; no older macOS runtime smoke has been completed.

## v1.3.17 — team artifact browser and release upload fix

### Added

- The Files viewer has an optional **Team artifacts** view alongside the default Files view. Files remains the default, and the artifact list is loaded only when you select it, with no polling while it is inactive.
- The team artifact list covers the selected team's task results and completion-review submissions. Each entry shows its name, type, size, source task and time, with the existing preview or download. Inline results open as text, and external links open as explicit links that Coral does not fetch.
- Listing is team-scoped and bounded: it reads the 500 most recent artifact-bearing tasks of a team, pages with `limit`/`offset`, and reports when older tasks were not included. It does not scan the artifact directory or load artifact contents.
- Repeated references to the same stored object or link collapse into one entry with a reference count. Stored objects missing from disk are marked unavailable rather than linked.
- New API: `GET /api/board/{project}/artifacts` and `GET /api/board/{project}/tasks/{taskID}/artifact-content`. Inline content is always served as plain text with `nosniff`.

### Fixed

- The release workflow no longer reports failure when the optional Windows packages are intentionally not built. Absent Windows packages are skipped explicitly, a missing Linux or macOS package is an error, and a failed upload still fails the step. v1.3.16 published its Linux and macOS packages correctly but its release job ended with a false failure from this loop; that tag is unchanged.

### Unchanged

- Production release policy is unchanged: Linux and universal macOS packages are published, and the Windows build stays opt-in by tag.
- Completion gates remain disabled by default.

### Verification status

Independent acceptance (#2177) passed:

- Focused backend tests cover team isolation, ordering, deduplication, pagination bounds, missing stored objects and inline-content scoping, and the neighbouring board route tests pass.
- Browser fixtures for the new view (lazy loading, previews, stale responses, errors) and an isolated-server smoke test against the real routes passed, with another team's artifact excluded.
- A shell regression runs the release upload step with a fake `gh`: absent Windows packages succeed, and failed or missing required uploads exit nonzero. It fails against the v1.3.16 workflow.

Acceptance included direct inspection of four rendered screenshots, a fresh isolated browser run, and five focused Go tests. The release workflow verifies packages and signing during publication. Artifacts older than the 500-task window are not listed, and personal (non-team) tasks are not covered.

## v1.3.16 — agent recovery, task controls, and live-refresh fixes

### Fixed

- Codex chat can explicitly bind a Coral session to its verified native thread, replace a cached old transcript, and retain the association across server restarts. Resume uses the mapped native identity.
- Agent restart preserves board membership, subscriber identity, and notification preferences even when membership was established after launch.
- Live refresh scopes database aggregation to active agents, skips sleeping-agent transcript discovery, and incrementally reads lifecycle records with replacement/truncation recovery.
- Reminder deletion preserves the running reminder when persistence fails. Cancellation and replacement wait for in-flight delivery; old generations cannot remove replacements.
- Blocked tasks can be reassigned without dropping their prerequisites or sending premature claim instructions.
- Amended-task completion errors explain the required task revision separately from the source/build revision.

### Added

- `coral-agent artifact download <uri> [--output FILE]` retrieves and SHA-256-verifies shared artifacts, giving screenshots an image extension usable by local viewers.
- `coral-board task unblock <id> --blocker <upstream-id>` removes one obsolete prerequisite; `--all` clears prerequisites. Readiness is recalculated atomically.
- Isolated stress coverage for transcript pickup across real server restarts and board membership across agent restart.

### Experimental behavior

- Completion gates and registered checks remain disabled by default. Stored gates are explicitly inactive and do not execute runners; existing required artifact outputs remain enforced.
- These optimizations reduce measured fixture work; they do not establish the cause of the earlier system-memory incident.

## v1.3.15 — security and encryption hardening

### Removed

- Retired the runtime LLM proxy/MITM path, provider-key loading, Connected Apps/OAuth routes and registries, and legacy workflow token injection. Historical proxy, cost, OAuth, and connection rows remain preserved for compatibility; no credentials or user data are deleted automatically.

### Added

- Optional SQLCipher encryption for the sessions and message-board databases. Encryption is disabled by default and can be enabled with startup password or owner-protected key-file unlock modes.
- Explicit plaintext-to-encrypted migration with paired-database preparation, rollback markers, recovery guidance, and retained plaintext backups.
- SQLCipher/SQLite/OpenSSL notices, reviewed dependency policy, exact native-library identity attestations, encrypted package self-tests, and non-publishing Linux/Windows/macOS package verification workflows.

### Compatibility and boundaries

- Encryption covers only the sessions and message-board databases. Transcripts, logs, artifacts, uploads, screenshots, API keys, shell history, CA files, and other files remain outside this database-encryption scope.
- Encrypted key rotation and encrypted-to-plaintext disable/decrypt are unsupported until a dedicated offline operation exists. Lost password/key material requires a user-managed backup or data discard.
- Encrypted builds require CGO, the fts5 build tag, a compiler, OpenSSL development/runtime libraries, and the reviewed SQLCipher driver. Plaintext-compatible builds remain supported when the codec is unavailable.
- Release packaging now fails closed on unreviewed or mismatched native-library identity, versions, hashes, or macOS slices.

### Verification status

Local arm64 encryption, migration, dependency-policy, package self-tests, and failure controls pass. Universal macOS, Linux, and Windows extracted-package execution, native UI password-entry behavior on every platform, signing, and runner-resolved advisory/backport evidence still require external CI. This change does not claim a fully production-validated encrypted release.
