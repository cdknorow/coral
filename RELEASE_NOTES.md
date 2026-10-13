# Release Notes

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
