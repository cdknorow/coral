# Release Notes

## v1.4.159 — Switching an agent's model or thinking level works, helpers end with their agent, and a first run that is easier to start

DRAFT (DevOps, 23 Oct), against ci/v159 46ad12e62: every bullet comes from the commit messages in v1.4.158..46ad12e62 (list: /tmp/list159.txt); #6213, #6226 and #6208 are 160 and are not here.

### Changes

- **Switching an agent's model or thinking level works:** Claude Code 2.1.309 writes a slash command and its output in a new transcript format, so no /model or /effort switch was ever confirmed ("Claude Code did not confirm the model change"). Coral now reads both formats and confirms a switch from Claude's own reply at once; /effort no longer waits for the next reply (the wait is 30 seconds, not 30 minutes). A second choice replaces the first instead of answering "switch in progress", and two are never typed at once; a quick second choice waits for the first to settle. Sleep, Kill and Restart end a pending switch with a message. The chosen model and level survive sleep, wake and a restart of Coral (they are stored, not kept in memory); Default works, including after a wake; a project folder that is a symlink finds its live transcript. The chat shows the command as one short line. Checked against Claude Code 2.1.309's records for /effort; /model uses the same format. The thinking chip shows as soon as a Codex agent on Default is selected, without opening the model menu first.
- **An agent's helpers end with it:**
  - On tmux, Kill, Sleep and Restart now end the agent's whole process tree, including helpers that detached from it: every tmux agent carries a random run tag in its environment, which everything it starts inherits.
  - If Coral crashes or is force-killed, a small guardian process (named `coral pty-guardian` in the process list) ends the process tree of each built-in-terminal agent. A kill of the guardian itself (`pkill coral`) only ends the guardian; Coral starts another.
  - Limit, plainly: on macOS a helper that detached and is one of Apple's own programs (such as perl or sleep) is not found, because macOS does not show its environment; an ordinary program is. Agents started before 159 have no run tag, so for them only helpers still attached are found; restart them once to get it. The run tag is typed into the agent's pane like the other exported variables, so it shows on the agent's screen and in its terminal log; it names processes and is not a credential.
- **Confirm owner is stricter and says when to retry:** it adopts the owner label only on a read (GET or HEAD), refuses when several Coral Hub accounts were stamped as the owner within the hour, and then says when to try again, as a time, from a `retry_after` the answer now carries.
- **History summaries no longer keep the start of a secret:** a summary is redacted before it is shortened, and stored summaries are rewritten once (the first start after updating re-reads History once; a few seconds on a large history).
- **A shipped Coral has no profiler:** `CORAL_PPROF_ADDR` is ignored outside dev builds, and a release build says it ignored the variable.
- **Add image is 20 MB here and through Coral Hub** (the 16 MB Hub cap is gone). A stalled upload says so ("The upload stalled. Try again." or "Upload timed out. Try a smaller image.") and no longer marks the computer unreachable. A pending message with images shows their thumbnails and the person's words, not links and download instructions.
- **Launching an agent, first run:** New Agent chooses the agent (Claude, Codex, Antigravity, Pi) in its first step, the installed ones first and the others marked "not installed"; every folder list has "Use this folder", and a recent folder closes the list so More options has room; Codex's hooks question holds a first send back with one short sentence that points at the card above it, and answering it says the message is still in the box; the "Claude is not signed in" line names what waits (scheduled jobs and workflows that use Claude), can be dismissed for a day, and shows only on Agents, Jobs and Workflows; a History chat is titled by what it says, with its id behind Details.
- **A new conversation is in History within seconds:** a finished turn, or an empty History list, now asks the indexer for a pass (at most one per 15 seconds) instead of waiting up to two minutes.
- **The agent list no longer reports your display choices** (provider, model or thinking level hidden, branches shown) as a summary line; only filters and sort appear there, with Clear (named "Clear filters and sort"); a display preference is changed only in the Display panel.
- **On a phone, an expanded file or artifact preview fills the screen again;** a preview height saved on a desktop no longer cuts it to half.
- **The sidebar's server rows show their agent count right after the name.**
- **Notices and toasts on a phone:** the update notice, warnings and short toasts are one stack at the top, below the top bar, one line each until opened, so they no longer cover Send, Wake or a dialog's buttons; the notices are a region named "Notifications" (not drawn while empty); a toast with a button (Retry, Undo, View saved, Open Servers) stays until used or dismissed (Escape dismisses it when focused); a plain toast keeps its 4 seconds; attaching several images raises no toast per file ("Uploading 2 images…" shows in the chip row).
- **On a phone and by keyboard:** the Jobs list is in the Jobs page, so a job can be picked, read and edited; "+ New Agent" is pinned at the bottom of the sidebar (in view with 20 or more agents) and a row the keyboard focuses is scrolled clear of it; the workflow editor is a real dialog (focus in and back, a standard close); the terminal's way out is announced and no longer dimmed; "Remove shared copy" says what happened (gone from Coral Hub, or still shared elsewhere, so it stays on Coral Hub) and its button names its image; buttons and fields are named by their row or agent (board Save and Delete, the workflow editor's steps, Settings' default-model and CLI-path fields, the Files panel's star, Copy path, Preview and Edit); red and green text, white on the dark accent and the toasts hold AA contrast in both themes.
- **Windows:** the agent tools folder warning in Settings > Terminal is one line with "How to fix it" behind it; on a pty-only system Settings > Terminal shows one short line up front and the rest behind Learn more; Windows CI now runs the dashboard's `test:cloud` and its CDP suites.

### Fixes

- **Kill in a git repository with no commits** saves its diff (it used to answer "Killed, but changes.diff could not be saved").
- **Windows and CI:** a Windows `./...` no-cgo build of `cmd/coral-app` no longer has a main package without `main` (the WebView2 profile file is built only with the webview tag).

### For integrators (Coral Hub, API)

- The launch response (and the team launch, reset and restart results) says `launch_path` where it said `backend` (the request's `backend` and the stored column are unchanged); the update relaunch's `restarts[]` rows no longer carry `backend`.
- Every row of `GET /api/sessions/live`, awake or sleeping, carries `terminal` (`tmux` or `pty`), where the agent really runs.
- An upload the Coral Hub proxy ends answers 408 with the codes `upload_stalled` or `upload_too_slow`, not 502 "remote unreachable".
- Confirm owner's 409 `several_hub_users` carries `retry_after` (seconds, rounded up) and a `Retry-After` header; `GET /api/cloud/owner-label` adds `retry_after` while several accounts are stamped.

### Internal

- Release verification reads a `-trimpath` build by its `vcs.revision`: the tag's commit, with `vcs.modified` false (Windows programs may say true: the release rewrites four tracked `.syso` files), instead of an ldflags line that is no longer in the build info; `cross-build` also runs on `ci/` branches; the Windows runner dry run follows the latest published release.
- Test and lint additions: a lint that nothing is hidden under `(hover: none)`; phone, contrast and keyboard censuses of every dashboard scene; a census that watches the Windows known-reds list; the `terminal_switch` timing check is measured in the page, and a pasted-image test pins both removal cases.

## v1.4.158 — Quitting Coral ends its agents on the built-in terminal, Add image on a phone, and a Hub that says what to do when it cannot confirm you own a computer

DRAFT (DevOps, 22 Oct), against ci/v158 38e175f4e: every bullet is taken from a landed commit's own message and is to be checked against the final batch list.

### Features

- **Add image in the message box:** an icon in the corner of the text box opens the file picker (a phone offers Photo Library, Take Photo and Browse); files go up one after another. A photo over 2 MB is made smaller in the browser, a PNG is not; an image over 20 MB (16 MB through Coral Hub) is refused before any request; a HEIC is refused with what to do. An upload that fails, or that Coral Hub cut because it was slow ("Upload timed out. Try a smaller image."), shows Retry. An image sent through Coral Hub keeps the Hub account on its upload record, and a Hub account has half of the owner's hourly allowance in its own window, so it cannot use up the owner's. On a phone the attached images sit in a row above the message box, which keeps its width; the Sent toast is one short line; a sent image's remove control is a trash icon labelled "Remove shared copy".
- **Coral Hub can say what to do when it cannot confirm you own a computer:** a computer linked to Coral Hub before Coral recorded the owner label used to refuse the owner's own requests on the owner-only routes (artifacts, image thumbnails, saved messages, custom models) with the same answer a stranger gets. Now the answer is "Coral Hub can't confirm you own this computer yet. On the computer, open Settings > Network and Hub and choose Confirm owner.", and Settings > Network and Hub has a Confirm owner button, shown only while the label is missing. Pressing it allows one adoption for 60 seconds (a restart cancels it; at most three presses an hour): the first owner-only request in that minute that comes through the connector from a Hub account becomes the owner label, and nothing is ever replaced. The Servers list marks such a computer "Owner not confirmed" with the same step.
- **Artifact documents open before Comments on a phone:** the Comments sheet starts closed below the phone breakpoint unless a choice is saved, an artifact you open takes the whole screen, a docked or expanded preview can hide Comments, and the side panel's tab strip shows a shadow where more tabs are hidden.
- **History and Board on a phone through Coral Hub:** the nav opens a linked computer's Chats and Board on a phone; the nav menu, the Chats list and the boards list are 44 px targets.

### Changes

- **On the built-in terminal, quitting Coral, or killing, restarting or putting an agent to sleep, ends the agent's whole process tree:** the agent and what it started (a Codex's code-mode host, programs that detached themselves) are stopped, then killed after a short grace period; a program the agent started on purpose in the background ends with its terminal too. Before, a real Codex could keep running after Coral quit (reparented to PID 1), which broke "agents stop when you quit" from 157. Limits: on macOS, a helper that had already detached from the agent is found only if it is an ordinary program; one of Apple's own (such as perl or sleep) is not. If Coral itself crashes or is force-killed, its agents keep running. tmux is unchanged.
- **Quit Coral in the tray no longer removes your agents on the built-in terminal (pty):** they stop, and wake the next time you open Coral when "Wake agents after a restart" is on (the item says so, with the count); otherwise the item says they stop and you wake them from Coral. In 157, Quit ended them for good. tmux is unchanged.
- **The tools prove Coral is Coral before they send a credential (macOS and Linux):** before the agent's tools or the command line send a credential to a Coral on this computer, the program answering the port proves it is Coral, on the very connection the credential then travels on, so a program that took the port, or a relay in front of the real Coral, gets nothing. A refusal exits "refused" (5) with its own sentence, and the troubleshooting guide explains each one. Only this computer is answered. Windows already checked who owns the port. Agents started before this release keep working with their old token, and are verified once restarted.
- **Windows: the Coral app's WebView2 profile is under `%LOCALAPPDATA%\Coral`,** set before the window is created, so the sign-in cookie does not roam: sign in once after the update, and you can delete the old `%APPDATA%\coral-app.exe` folder (optional; Coral never deletes it). The "what Coral stores" page says where it lives.
- **Settings no longer flips a light phone to Dark:** with nothing stored, the theme control shows System (on Coral Hub; Dark on this computer) and a Save writes no theme you did not pick.
- **Coral Hub sign-in and connect pages follow the system's light or dark theme when nothing is saved.**
- **The Sessions page names the Coral app's sign-in:** "Coral app on <system>" for a session the app opened.
- **Small touch and keyboard fixes:** the launch dialog's vendor install link is a 44 px row on touch; the sidebar search shows a visible focus ring; the toast's Dismiss and the "Wake agents after a restart" row in Settings > Terminal are 44 px on touch; a test pins that Tab stays inside Team settings on a phone; on touch, the chat find bar, History's Chat tab, the board's First filter, History's Edit & Resubmit, the History chat's tab bar, a board message's Save and Delete, and the Workflows editor and run controls are 44 px, and a test now fails on any new control under 44 px at phone width.

### Fixes

- **A message to an agent whose hooks are gone:** when the app an agent started from is replaced while it runs, its hook programs are gone; the agent took the message but sent no event, and Chat said "may not have been sent" for a message that arrived. Coral now records where each agent's tools live; a message is confirmed by the agent's transcript gaining it, then by its echo on the screen, and with no signal the agent gets "Coral can't reach this agent's hooks, so it can't tell whether your message arrived… reopen Coral or restart the agent". A running agent whose hooks are gone is flagged once on its row, "Hooks can't run". Resume refreshes the hooks of any agent type.
- **A message could be reported as possibly not sent when the computer was busy,** even though the agent's transcript had it.
- **History search upkeep:** exclusions of Codex rollouts that are gone are forgotten; a Codex rollout that never parses is left alone after three passes at the same modification time, and tried again only when it changes.
- **Uploads through Coral Hub are bounded:** the header wait starts after the upload; an upload that sends nothing for 30 seconds is cut, and the total is capped at 10 minutes (a slow but moving upload passes).
- **Lists that were ordered by a time that could tie are now stable:** artifact comments, saved bookmarks, pinned folders and every other time-ordered list come back in the same order every time, with a test that fails a new one without it. (Fixes a rare out-of-order comment thread and a flaky test.)
- **Windows:** when an agent's terminal ends on its own, the dashboard now says it closed instead of staying on a dead terminal (a ConPTY pipe never reported the end).

### Internal

- Tooling: store lints for time-ordered lists and the RFC3339Nano writers; release provenance judges the gated commit named in the notes commit by name; every release and installer build uses `-trimpath` so a shipped stack trace holds no path of the machine that built it; the Windows runner dry run follows the latest published release.

### For the notes' reviewers (not for publication)

- Coral Hub items to send through the Orchestrator: the owner-label routes (never through the Hub), the proof route (never through the Hub), POST /api/upload's Hub-account stamp, the 503 text, and the Servers row's needs_relink.

## v1.4.157 — The built-in terminal is the default, a first run that says what is wrong, and less text in more places

### Features

- **The built-in terminal is the default on macOS and Linux:** quitting Coral now ends its agents, which is what people expect (Windows always had only this). tmux stays available and keeps agents running across a quit, a restart and an update. Who is affected: new installs and installs with no saved choice; an explicit choice in Settings or `--backend` is always kept; an upgrade that still has agents running in Coral's private tmux server runs tmux for that one start so no running agent is hidden. An install that ran on the old default sees one line, once: "Agents now stop when you quit Coral. To keep them running, choose tmux in Settings > Terminal." On the built-in terminal the tray has one exit that stops the agents ("Quit and stop all agents"); with tmux it keeps its two exits ("Quit and stop all agents" and "Quit Coral"). Agents on the built-in terminal wake again after a restart through the "Wake agents after a restart" setting, which is now shown there.
- **A first run that says what is wrong:**
  - tmux missing (only when tmux is the chosen backend): one line under the launch dialog with the operating system's install command and "Check again"; Launch is off with that as its reason; a remote server chosen in the dialog is not checked against this computer's tmux; a failed start on the built-in terminal is no longer blamed on tmux.
  - An agent that is installed but not signed in (Claude and Codex): one plain card, "<CLI> is not signed in", with a way to sign in and "Check again", read from the CLI's own status command at once (before, up to five minutes late, and nothing for Codex); the "Claude is not signed in: background jobs are waiting" line shows only on a Claude agent's page.
  - The first reply: a quiet "Working · 12 s" from 5 seconds, "Still working. Open the terminal to see what it is doing." from 30 seconds, "<name> stopped before answering" with Restart when the CLI exits, and "Waiting for <CLI> to start" in the agent's first 25 seconds.
  - The create-a-password page says what the password is for, in one sentence.
  - The Changes panel in a folder that is not a git repository says "Not a git folder" instead of "0 files changed vs HEAD~1".
- **Hover a message to see when it was sent:** in agent chat and on the board, a message's local sent time is shown next to its actions (always on a touch screen, on hover or focus with a mouse or keyboard), with the full date in the tooltip.
- **History and Search work on a phone, and Clear filters works:** the chat history list and Search are reachable on a phone, clearing a search brings the list back and ignores a stale result, and the return control is named "Back to chats".
- **Chat History no longer lists Codex's own auto-review messages:** Codex's guardian review rollouts are excluded from History and search, and Codex 0.163 chats are checked once and remembered, so later passes skip unchanged rollout files.

### Changes

- **Less text, screen by screen** (visible words before and after, as each task measured them): Cost dashboard, empty period 84 to 66; History search desktop 10 to 9 (filters open 34 to 32, a partial-result example 49 to 47); Scheduled jobs: no workflows 20 to 15, a new prompt job 74 to 70, a new workflow job 56 to 53, a job's detail with the example prompt folded 63 to 38 (the full prompt is one click away); Create Task with its optional workflow section closed 86 to 28; notifications: agent input 13 to 12, the received-files limit 23 to 20; Git, empty changed-files panel 10 to 8; Notes, empty prompt 4 to 2.
  - The agent bar, its menus and the right-hand panels: the command box no longer prints the board's generated id ("Message <name>, or paste an image"; "Type a command in <name>" for a terminal), the Files panel draws its comparison choice once, the Team view's legend is behind a "?", empty chat, Tasks, Artifacts, Saved and agent panels say less, the board shows its message count only while messages are hidden and the date only when it is not today, and the artifact viewer loses its "Who can read this" line.
  - The tray menu: "Open in App" is "Open Coral", the exits are "Quit and stop all agents" and "Quit Coral" (Windows has the first only), and an update notice is one line, "Update available: vX (you have vY)". The Coral Hub enroll page keeps "Only approve a machine you control." with the rest behind a "?", and the sign-in page loses its tagline.
  - The launch chooser, New Agent, Team launch and Add agent lose their sub-titles and restated defaults, Team settings > Messaging and Workflow get shorter labels with their notes behind an (i), and the Working mode list names its four modes the same way (no "built in" on one).
  - A new Home makes "Launch agent" the obvious start and quiets the Coral Hub cards; Save is the primary button in Team settings; the persona buttons are rectangles, not pills; the (i) is one shape everywhere; Settings shows the four default models two by two; a Team launch's three top fields are one style and every disclosure keeps its arrow on a phone.
- **Keyboard and touch:** a dock tab the keyboard reaches shows its name; Enter or Space on the Settings button moves the focus into the menu and Escape closes it; the empty-folder refusal is said once; the Create Task checkboxes are 44 px on a touch screen; an agent called by its folder carries its CLI in the list.
- **The updater is switched off in this release too:** the tray's "Update to vX available" item and the dashboard's Updates pane exist, and with the feature off (the status route answers 404) or no newer approved release there is no item.
- **Coral Hub:** `GET /api/system/cli-check` accepts `signin` (rate limited, a boolean reply); the changes list and refresh answers carry an optional `is_git`; the live-session list rows carry a start time, `created_at`; `POST /api/system/terminal-backend/notice-seen` is never carried through the Hub.

### Fixes

- **Themes from Coral Hub are checked:** a theme answer that is empty or malformed can no longer empty the page's colours, and with no theme chosen the page follows the system's light or dark setting. A theme from Coral Hub also no longer replaces your own saved light or dark choice when the page loads.
- **Windows, the first-time walk:** the MSI is a real x64 package built the same way everywhere, so the install moves from Program Files (x86) to Program Files (an update changes shortcuts and scripts that name the old folder); a refused installer launch (an installer run as administrator) says why in a message box; the Coral window opens inside the screen's work area.
- **Windows, the Windows team's fixes:** every console child Coral starts gets no window; a working folder of 259 characters or more starts through its short path or is refused with a plain reason; a subagent id that is not a plain id is refused and the Tasks table escapes the label; Coral's listeners hold their port exclusively, so no other program can bind a more specific address on Coral's port while it runs; hooks wait 0.8 seconds for Coral when nothing reads their post (a created task keeps 3 seconds); agy's slash command Enter goes through agy's own completion menu and only when the highlighted command is the typed one.
- **Codex and agy agents:** the input gate never types into a working Codex, recognises its own text left in the box and says when sends wait behind it, does not take a past prompt in Codex's history for its input box, and forgets its own text once its Enter has emptied the box; a typed send is remembered as Coral's for two minutes only.
- **The command-line tools:** unknown options and unknown commands are refused (exit 2), `post` and `send` read options before the message and treat anything after it that is not an option as text (a misspelt `--to` or `--file` after it is refused), `task list` has `--json`, `task current --json` prints JSON in both tools, each way a request fails has its own exit code with plain "cannot reach" text and `CORAL_TIMEOUT` (capped at 10 minutes; `coral-board wait` keeps its own timing), `coral-board read` and `check` give up on a server that never answers, `artifact download --output` copies the verified bytes where the file system has no hard links, and help topics `windows`, `exit-codes`, `json` and `timeouts` explain them. A task that does not exist answers 404 "Task not found" from complete, submit-review, approve, reject and the merge-queue routes.
- **The agent token is guarded better:** it is never sent over plain http to an address that is not this computer; on Windows it is sent to a Coral on this computer only after the listener's executable and account are checked to be Coral's (verified again after 30 seconds, 2 after a refusal), and a refusal says in whole sentences what was refused, why and what to do (exit code 5, "refused").
- **Board and diffs:** completing or cancelling a task answers once it is committed and its `changes.diff` is written after the reply, one at a time; the diff lists tracked changes first and then new files whole while they fit, and says how many it left out and where they are; the Health "held draft" line says whose messages wait and that Coral never sends the text.

### Privacy and security

- **Logs hide more:** your home folder is written as `~`, your user name and computer name are hidden (also in their short forms and after `:`, `|`, `>` or `file://`), project folders appear by their last name only, the PATH is written once at server start, secrets are scanned out of log lines, and hook crash traces are redacted.
- **Settings > Usage analytics shows your install ID** with Copy, so a deletion request can name it; the telemetry documentation now says the analytics service derives an approximate location from the IP address of incoming events, while Coral's own payloads never carry one.
- **Telemetry:** `provider_unavailable` is replaced by `provider_missing` and `provider_unrunnable`, and `prerequisite_check` gains `signed_in` / `signed_out` from the launch probe (one boolean, the CLI's output is dropped).

### Internal

- The browser test run uses the default (built-in terminal) backend and a separate tmux job runs the suites that drive tmux; `npm run test:cloud` runs on Windows, with Windows process helpers for the browser runner; the dead quick-launch team dialog is removed; test fixes for the startup golden, the live history golden, the hover-time check, the goal-generator tests (a signed-in stand-in `claude`), the listener-cache and Windows log tests, and the proxy-failure burst test.
- On macOS and Linux nothing checks who owns the default port while Coral is stopped; the Windows check does. That is unchanged.

## v1.4.156 — Less text in Settings, team dialogs and the Inbox, first-time fixes, and the updater ships (switched off)

### Features

- **Settings says less, one group at a time:** the long explanations moved behind each row's (i), the sub-titles that repeated the tab name went, and the visible words of each group came down: Network and Hub 413 to 74, Agents 200 to 73 (the four per-provider Custom models groups are now one list with one "Add a model" form that has a Provider box, and its hint sits behind the row's (i)), Storage and cleanup 118 to 47 (the breakdown behind a disclosure), Terminal 122 to 53 (the security notes behind Learn more), General 53 to 38, Notifications 10 to 4, Files and Git 19 to 15, Security and privacy 86 to 31. In Network and Hub an empty status line takes no room, "Run as a hub" is label left and switch right like every other row, and Disconnect lines up with the row's left edge. A test now holds each group to a word budget.
- **Team settings > Health check says less:** 118 visible words down to about 50 (354 with its folded rules open down to about 190, the explanations behind (i) buttons). One short label per control and at most one hint line; while the check is off, Every, Send it to and Post when all is well are dimmed and disabled; ranges and units sit in the control ("Every [10] min"); "Only the Operator on this computer can change these." is said once, in the Team settings header; opened from the health icon, the first focus is on that tab.
- **Prompt inspection and the template browser say less:** the controls around Prompt inspection went from 95 visible words to 28, with the full prompts folded until asked (copying a whole role still works); a template's source attribution went from 10 words to 3.
- **The Inbox and the Hub Servers page say less:** the Inbox headings and empty states are shorter (an empty Inbox's Needs you 27 words down to 13, Tasks 29 to 12, All saved 50 to 27), and the optional Hub Servers setup and the access detail are disclosed on request instead of always shown (a computer's expanded row on the Servers page 164 words down to 35, an empty account's page with setup open 114 to 90).
- **A first Launch Team works the way a first agent does:** after Launch Team the team opens (its board with the Team view beside it); an optional "First task" field posts your first message to the Orchestrator (or the first member), and an empty board says "Tell @<lead> what to build" with the compose box focused; clicking a team's name or pressing Enter on its header opens it, and the chevron before the name is the one control that collapses it; the dialog opens on the template choice with no members picked for you; the folder is above the sections on every tab, required, and empty unless Settings has a default folder.
- **A first single agent starts without surprises:** New Agent no longer starts on your home folder (the folder box says "Choose a folder" and Launch asks for one; choosing the home folder shows one line that the agent can change any file in it); a missing CLI, or a configured path that cannot run, is said once under the folder (the vendor's install page first, the terminal command second), with Launch off and that message as its reason, and Check again turns it back on; an unnamed agent is called by its folder or its CLI ("my-app", "Claude") instead of "Agent"; the create-a-password page offers one primary path (the Coral Hub way is a quiet link); a phone's Home with nothing running says what Coral is for and offers the same Launch agent and Launch team cards as the desktop.
- **Custom models through Coral Hub, for the account that owns the machine only:** the account a computer is linked to can read and replace the custom models list through the Hub; any other Hub account is refused (403), a computer with no recorded owner refuses everyone, this computer's own requests are unchanged, and "Add a model…" is hidden from a person the machine refuses.

### Changes

- **The updater (A, B and C) ships built and switched off:** the real relaunch, the install classifier (a Homebrew cask is never checked, staged or applied), the automatic check job, the local relaunch route and the dashboard's update UI are in this release, and every switch is off on every platform. What you can see today: nothing. The new routes answer 404 on this computer, and Coral Hub refuses them; the dashboard's update UI stays off (it turns on only when the server says so). `coral-update status --json` now reports what this install is and whether the updater could replace it.
- **Update telemetry, exactly three things:** `upgrade` now carries `from_version` (the release that last ran on this install; a release number only, left out otherwise); `update_notice_shown` (the dashboard's update toast or the tray's notification showed that a newer release exists; once per install, release and place, only for the release this server's own update check offered); and `update_offer_opened` (the person opened the release page the notice offers, which includes the tray's Check for Updates opening it; once per install, release and place). The schema version is 4, all values are closed sets, an install keeps at most 20 update keys, and nothing is sent when usage analytics are off.
- **A node folder that is group-writable is judged more carefully on a Mac:** a group with a nested group, or with more members than the names accepted, is refused, and so is any system account other than root and the Setup Assistant's own.
- **A bare artifact link reads as its visible words to a screen reader** ("artifact 81b837f8"); the full id stays in the tooltip.

### Internal

- Windows: the updater's install answer is one object on every platform (`status.Install`) and `trigger.Preflight` reads it; a signature can be required on a Windows package and the signer pinned to the whole certificate, switched off until the first signed release; a Linuxbrew path test no longer depends on the machine's separator.
- Docs: the cross-team documentation tells linked-team defaults from explicit local opt-ins.
- Tests: the startup request golden gains the updater's one status read; a suite pins the server's answer that turns the update UI on; the static budget for the embedded front end is 6 MiB + 128 KiB; the feed-reconnect stability suite takes its node baseline after the page's one-time interface is built, and the first-time agent suite waits for the New Agent dialog's template to exist (both failed now and then under load).

## v1.4.155 — A Mode button that reads Claude's real modes, @mentions on the board, and the Windows updater built (switched off)

### Features

- **The Mode button reads Claude's own modes:** an agent started in Auto used to be told "Accept Edits" (the footer's "auto mode on" was not read, and the button's "auto" was Accept Edits). The button now names each state as Claude does (Manual, Accept Edits, Plan, Auto, Bypass Permissions, Don't Ask) from the CLI's current mode line, and cycles in Claude's own order (checked on Claude Code 2.1.307). The mode the agent reports is only a label: presses are counted from the screen. A permission mode that says "auto" is labelled "Auto" everywhere, and Manual's tooltip says it is the Default in Settings.
- **@mentions on the board are highlighted:** a mention of a current board member ("@Security Reviewer Windows": names can have spaces), or of the Operator, the Orchestrator or all, is shown with weight and a tint (not colour alone, 4.5:1 in both themes, a border and an underline in forced-colors mode); the viewer's own name is stronger. Nothing inside code, a code block or a link, and an email address is not a mention. A bare `coral://artifacts/<id>` in a post is a link to that artifact.
- **"Add a model…" where you pick a model:** the model menu, and the model field in the launch and restart forms, end with "Add a model…": a small form (the model id, an optional display name) writes it to your custom models, so you do not have to go to Settings. An empty, flag-shaped or existing id is refused in words.
- **The artifact viewer's Comments panel is closed unless you ask for it:** the document has the full width; a "Comments (N)" button in the header opens it (a column beside the document on a desktop, a sheet on a phone), and selecting text still offers Comment. A desktop remembers open or closed.
- **The Windows updater is built, and still switched off:** the install route, the updater copy, the one-time install marker, the verified MSI major upgrade (a click for exactly the staged version, the whole chain re-verified, Authenticode pinned) and the verified rollback package are in this release, and a real-Windows-Installer test passes on a GitHub runner. The apply switch stays off on every platform: nothing updates a Coral yet, and the install route refuses before it quits anything. Windows packages are not Authenticode-signed yet; until they are, an update is accepted on its checksums and Sigstore signature.
- **Windows: opening Coral again shows the running tray's window** (before, a second start did not hand over), through an event only this user can set.

### Changes

- **Board posts are short and scannable:** the agents' prompts and the `coral-board` help teach a one-line summary first, a numbered list, and long evidence as a linked artifact; the help fits 80 columns.
- **The tunnel tells Coral Hub of a mode change with one message at a time**, and a failed message does not reconnect on a connection record that is gone.
- **Auto-accept holds more Git commands:** a Git setting that runs a program (core.hooksPath, core.fsmonitor, core.sshCommand, credential.helper, the pager and protocol settings, and more), and a write into a place Git runs programs from (.git/hooks, .git/config, hook folders, archive and sync destinations), are held even after the shell's quote removal. A Coral Hub account at Launch can still run a program that is simply run; the documentation says what is left.
- **A file relayed to a board that does not exist is refused with the same 403 as every other refusal** (before, an unknown board got its own 404).
- **An agent CLI installed with Homebrew's node runs on a Mac:** a group-writable folder that is yours (the group is admin or your own, and no one else is in it) passes; the refusal names the folder and group and gives the chmod, or says to start Coral from a shell whose PATH has node's folder.
- **A Google API key with a low entropy is masked in what Coral Hub sees:** the redactor's rule has no entropy filter (the library's placeholder filter could let a rare real key through) and still skips documented example keys.
- **Settings is clearer for a computer that never chose a Hub access:** "Not chosen yet (View)" with an explanation, a sentence when the Hub cannot be reached ("Coral could not reach Coral Hub. Check your connection and try again.") with the seconds until Connect may ask again, and no "until you connect" wording for a computer connected from the command line. The Messaging tab says what its box covers: "Message teams on other computers (through Coral Hub links)" and "Share files with teams on other computers (through Coral Hub links)", with "Message teams on this computer: on/off" as its own line (the refusal text for files names the new label), and the team settings dialog opens on its first tab, not on Rename board. The seconds until Connect may ask again count down in plain text (not announced every second), and Connect is off meanwhile.

### Fixes

- **Auto-accept no longer panics on an empty argument on Windows**, and `-DestinationPath` (Expand-Archive, Compress-Archive) is judged as a path parameter, so an extract to an outside folder is not let through.
- **Windows:** a fixture build of Coral refuses to start, saying why.

### Internal

- Tests: the sign-in probe tests find a hold against a signed-in control, not by seconds; the Codex timeline, the testguard PID, the CLI-help and the Coral burst-debounce tests no longer depend on timing or on a reused PID; a browser test pins that a send between two teams on one computer needs an explicit on; the first-launch `editions_browser` step waits for the screen to be shown; the redactor's miss-rate harness is seeded and can be rerun from its seed.

## v1.4.154 — Coral Hub connects with one question, cross-team relay is on by default, and agents launch quietly

### Features

- **Coral Hub asks one question when you connect:** the Connect screen asks "What may Coral Hub do on this computer?" (View, Interact or Launch; Launch is preselected, or what an install already saved), with the auto-accept switch shown beside Launch, and says once that nothing is sent until you connect. The first focus is on the question, so Enter cannot connect before it was reached. Settings > Network and Hub asks the same question in the same words. The answer goes with the connect call (it is saved before the enrollment starts, and nothing is saved when any part is invalid); the command line's plain connect is unchanged.
- **Coral Hub's Servers page (in the shared front end) is a table:** sortable, with rows that open, "Add a machine" on top with the download page, one lower-only "Hub access" control (View, Interact, Launch) that follows the computer, Rename and Revoke in the row, and an online computer with no health reading yet says "Waiting for a health reading" (never "not connected"). Revoke asks in the words Coral Hub wants, and Cancel and Escape give the keyboard back.
- **Coral Hub is told when this computer's access mode changes:** to a hub that advertises it, the change is a short message on a new control stream instead of a reconnect (a reconnect drops every stream and looks like a network failure, and an open terminal a viewer of Interact could type into stayed open until its first keystroke); a hub without it still gets the reconnect.
- **A new agent's terminal starts quiet:** it no longer shows the long launch command; it stays empty until the agent draws (the command is in the server log; `CORAL_KEEP_LAUNCH_SCRIPTS` keeps the script for debugging).

### Changes

- **Cross-team relay is on by default:** a board's cross-team messaging and file switches are now on for a board that never set them (an owner's explicit off stays off), and the two switches are kill switches: the Coral Hub team link is the permission. Teams on one computer still need an explicit on to reach each other, and the Messaging tab now says so, with the two reaches told apart ("Teams on other computers: on (needs a Coral Hub link)" and "Teams on this computer: on or off", with a button, Turn on for teams on this computer).
- **Codex starts in the workspace-write sandbox, with no approval prompts and tool networking on** (before: it bypassed the sandbox); a default mode saved in Settings wins, and `-c sandbox_workspace_write.network_access=false` turns networking off.

### Fixes

- **In Coral Hub mode the Settings dialog keeps its layout:** the hub note sits inside the content column; before, the dialog collapsed to a sliver at every width.
- **An agent CLI installed with npm runs from the macOS app:** `claude`, `codex` and `pi` installed with npm are scripts that start with `#!/usr/bin/env node`, and the app started from Finder has a PATH with no node, so every run of the CLI failed (the launch, the version probe, the sign-in check, the model list). Coral now finds the node that installed the CLI and adds its folder to the end of the PATH for that one child only, and only from a folder no other user can write (each link and parent checked, macOS access lists included) (the process PATH is never changed, and nothing is put first).
- **Windows: every program Coral starts itself runs without the current-folder search**, so a `claude.cmd` shim started in a repository does not run a planted `node.exe` from that folder; and Coral's window marker no longer follows a link.

### Internal

- Windows builds: every Windows test run now builds the package and runs the release's own smoke test (including the MSI when the installer changes), the Windows CI pins MinGW, and a census keeps the test build and the release build the same.
- Tests: the live-PTY restart tests count a real launch of the stand-in agent (a quiet launch erases the screen); an opt-in test checks the Codex hook-trust key against a real Codex 0.163.0; the PTY identity stress run works on Windows and with lower- and mixed-case Coral variable names; the release verifier allows 1.4.153's larger packages.

## v1.4.153 — Claude always carries a permission mode, Pi and Plan need the Hub switch, and the updater ships (switched off)

### Features

- **The updater program ships in every package, switched off:** `coral-update` is in the Linux and macOS packages, the Windows ZIP and the MSI (with its command-line link), and `coral-update status` says so (`apply_enabled` is false on every platform: nothing is updated by it yet). The release check that runs on every package now includes it. Its trust anchors (the Apple team and the promote repository) are pinned in the build. The packages are larger: the third-party licence texts of its new libraries add about 175 entries to each Linux tarball and about 110 to the Windows ZIP, plus the `coral-update` program itself.
- **The first-run screens are finished off:** a sign-in is said once (the card in the chat, or the pill when the card is not on screen, never both), the terminal tab is not covered, the toast names the agent, and the waiting pill over a long chat comes back when you scroll away from the card and goes when you scroll to it. The Shared Inbox keeps only the message body in an inset panel. The Hub setup screen answers Escape only while it is the topmost thing.
- **Antigravity models in the launch dialog:** the `antigravity` alias, and every agent type of one CLI family, now take that family's model list.
- **Windows: Coral trusts its own hooks for each Codex it starts**, when only an administrator can change them; and a person's send into Codex's hooks-review dialog is refused with 409 `codex_hooks_review_pending` instead of being typed into it (checked with Codex 0.161 and 0.163).

### Changes

- **Claude launches always carry a permission mode:** an interactive Claude launch now passes `--permission-mode` explicitly (`default` where nothing chose a mode). This fixes the 1.4.152 known issue: with Claude Code 2.1.283 or later, a launch Coral meant to keep asking (a Hub launch with the auto-accept switch off, or an agent started by another agent) could start in Claude's own default, Auto. Claude's Plan mode no longer counts as keeping the prompts (it runs the commands its classifier approves): a Hub request for Plan needs the same owner switch, Launch mode and provenance as Auto or Bypass. Only `default` (also spelled `manual`) asks.
- **Pi counts as skipping the prompts:** Pi has no mode that asks, so a Pi launch through Coral Hub needs the owner's "Coral Hub can turn on auto-accept" switch, Launch access and the dashboard's provenance. An agent cannot start Pi (nor a team with a Pi member, nor a team type that takes Pi); you can.
- **Antigravity and Gemini:** the old `--approval-mode` spelling is translated to Antigravity's own `--mode` (an unknown value is refused with a message); the `gemini` type is Antigravity compatibility, so a path that is the real Gemini CLI is refused with a setup message naming the setting; Coral's help probe of the Gemini CLI now runs with a limited environment and output.
- **Codex 0.163:** the old `--approval-mode` and `--ask-for-approval untrusted` spellings are emitted in the form Codex 0.163 accepts (before, it refused to start).
- **Updater switch per platform (internal to the switch):** the apply's off switch is now one per platform; all three are still off.

### Fixes

- **The model picker loads instead of staying empty:** a model list that is still refreshing is not kept as the answer; the menu says "Loading models…" and asks again.
- **Install lines:** the missing-CLI help gives Windows the vendor's PowerShell line for Antigravity (with a Windows note), and Coral looks for `agy` in `%LOCALAPPDATA%\agy\bin`; Pi's install command names its new package, `@earendil-works/pi-coding-agent`; the other install commands were checked against their vendors' pages.
- **Accessibility:** the first-run card's Open terminal link is underlined and full size, the missing-CLI dialog's link meets contrast in the light theme, Check again keeps focus, and a view-only message box is named "View only through Coral Hub" for screen readers.
- **A person's message to an agent is not retyped by mistake (Windows round 5):** it is retyped only when the screen is as it was before the typing, never as a slash command; it counts as taken only on evidence of its text; the echo is matched ignoring all whitespace, so a wrapped line or an indented continuation cannot hide it.
- **The Windows installer's Launch Coral does nothing when the installer was run as administrator:** `coral-tray` starts nothing when it is launched from the installer with an elevated token (a normal install is unchanged), so Coral does not start with the installer's rights.
- **Workflow steps on Windows use only Git for Windows' own shell layouts**, never a shell found above the install folder.

### Internal

- Test fixes: Codex trust off-Windows reports the switch before the OS; `human_send_test.go` formatted; the settings golden gains the Codex trust-hooks control; `perf_budgets` pins "Fit pane width" off; `first_run_card` waits without raw animation-frame waits; a test that no XML comment in `tools/coral.wxs` has a double hyphen.

## v1.4.152 — First run that guides you, Claude starts in Auto mode, and safer launches through Coral Hub

### Features

- **A first run that guides you:** after Launch the new agent opens (before, the page stayed on the home screen and the agent was a row to find in the list); a missing CLI is said under the working folder, with **Check again** (and the Not Found dialog has it too); an agent that has not started shows a readable card that says what to do, and "Claude is not signed in" when it is not (Coral checks Claude's sign-in once, in the background, after the first Claude launch). A launch whose CLI is installed nowhere answers `422 cli_not_found`, naming the setting that points at an existing install.
- **Claude agents start in Auto permission mode:** where nothing chose a mode, a Claude launch is `--permission-mode auto` instead of skipping every prompt, so Coral does not scare new users. Codex, Antigravity and Pi keep the mode they always had (Codex's Auto cannot reach the board on localhost, Antigravity has no mode between edits-only and skip-permissions, Pi has none). A saved default, or a mode you pick, wins; Known issue: with Claude Code 2.1.283 or later, a Claude launch that Coral means to keep asking (a launch through Coral Hub with the auto-accept switch off, or an agent started by another agent) can start in Claude's own default, Auto mode. 1.4.153 passes an explicit mode to fix this. If you saved a default permission mode (for example Bypass) in Settings, it still wins; choose Auto there to switch.
- **Idempotency-Key through Coral Hub:** a launch, launch-team, resume, wake or task run that reaches Coral through the Hub with an `Idempotency-Key` (and a Hub user label) runs once per route, user and key: a repeat while the first runs is 409 `request_in_progress`, a repeat after it succeeded gets the first answer and starts nothing, the same key with a different body is 422. Coral's hello to the Hub now carries a boot id and a start time, so the Hub can tell a restart from a reconnect.
- **The Windows installer offers Launch Coral:** its last screen has a ticked "Launch Coral" box that starts Coral when you click Finish (a silent install does not start it).
- **Settings shows who last changed "Coral Hub can turn on auto-accept":** "Turned on from Coral Hub by <user>, <time>" or "Turned off on this computer, <time>", under the switch.

### Changes

- **Cross-team messages and files follow the Hub's link snapshot:** from this release a relayed message or file is accepted only when Coral Hub's current links say so. With a stale or missing snapshot nothing is admitted, and the Messaging tab says "Waiting for Coral Hub: messages from other teams are refused until this computer reconnects" with when the Hub was last heard.
- **A saved team template matches by the type the member will run as:** a saved member with no agent type, sent unchanged to a team of another CLI, no longer matches its saved key, so its allow rules cannot translate into that CLI's bypass without the Hub auto-accept switch. A team saved under the Auto or Bypass default needs the switch through the Hub.
- **The publisher is Subgentic LLC:** the Windows installer's publisher, the four Windows programs' version information and the LICENSE copyright holder say Subgentic LLC (the installer's UpgradeCode is unchanged, so an installed Coral still upgrades).
- **Workflows on Windows:** local step files are served, steps run an absolute `sh`, Git's folders join a step's and a hook's PATH, and a step's children join the kill job before they run.
- **Escape on the first-launch Coral Hub screen answers "Skip for now"** wherever the focus is.
- **A view-only agent keeps its words:** the composer keeps "View only through Coral Hub" when you select the agent again and on every live update.

### Fixes

- **The Coral Dropbox edition ships no installer package:** its release has no MSI, its checksums name none and its notes link none; a production release still builds, signs and links the MSI.

### Internal

- A test names the stop gate's retry loop and leak checks print every goroutine when they fail; the first-run flow has a browser suite that walks a brand-new install; `settings_switch_names` waits for the Hub block's status read.

## v1.4.151 — A shared Inbox, Coral Hub controls its auto-accept, Windows round 4, and faster Saved

### Features

- **The shared Inbox:** the Inbox tab shows Tasks for you (expand one and reply) and "Read the whole message", drawn by the same Markdown renderer Coral Hub uses (vendored byte for byte and pinned by hash), so a message reads the same in both places. A refresh draws an open task again without a read or link lookups when it did not change.
- **Working mode descriptions:** Team settings explains the chosen working mode in one line under the list (None, Shared checkout, Worktrees and Merge queue each have a short description).
- **Edited tasks record when:** changing a task's body, title, priority or assignee records `edited_at`, so a client can tell an edit from a new task.
- **Mac app:** opening Coral.app again while it runs brings its window forward, and the relaunch after an update opens the window only if it was open when the update began (a tray-only Coral stays in the background).

### Changes

- **Coral Hub can turn on auto-accept stays on by default, and the Hub owner can change it:** the switch now lives in Settings > Network and Hub, and the account this computer is linked to can turn it on or off from Coral Hub. Turning it off ends every auto-accept that was started through the Hub (local ones stay). With it on, an agent that skips permission prompts can be started from Coral Hub at Launch access: the Hub owner account is trusted as much as the person at this computer, so protect it with the Hub's own sign-in controls (the threat model page says so).
- **Hub launches that skip permission prompts are checked everywhere:** launch, launch team (each member, when its turn comes, with the settings read then), restart and task runs check the flags against an allowlist (anything unknown counts as skipping), and refuse with `no_dashboard_provenance`, `hub_auto_accept_disabled`, `hub_mode_too_low` or `hub_settings_unreadable`. A team member that is exactly one of the owner's saved templates keeps its allow rules, tools and hooks.
- **Saved messages are for the Hub owner only:** through Coral Hub the Saved list and its messages belong to the account this computer is linked to.
- **The Saved list is fast with a large history:** the server marks bookmarks that are gone or not reachable itself, within a time budget and from the bookmark's own agent store, and keeps a small cache; a first list with 10,000 Codex rollouts takes 1.5 s, the next 167 ms.
- **Merge queue from the dashboard:** attach, send-back and mark-landed take the verified actor when the body names nobody (in the default identity mode too), and mark-landed accepts a bare or `sha256:`-prefixed digest. A task reply from the dashboard works in the default identity mode.
- **Windows:** the updater's folders and staged files are private; a task run refuses a network `repo_path` before any stat; the tools folder joins PATH with the platform separator; Hub-visible paths hide the home folder's 8.3 form; Git Bash gets `cygpath -u`; and a configured CLI path that cannot be run is a setup problem. A team launch that started nobody keeps a board that something else used meanwhile.
- **Accessibility:** the Board and Team views (task rows are no longer buttons holding buttons; the message list is a list; Team cards are named), Create task and task detail (a spoken refusal, a keyboard dependency picker, a named dialog), the Inbox (focus after send, one announcement, a turning chevron, the selected tab in forced colors), the light theme (the warning toast's title holds 4.5:1; toasts, the hover card and pasted-image chips are held to it).
- **Startup:** the shared Inbox renderer and the Coral Hub connect screen load when first used, not at startup; if the connect screen cannot load after an update, a toast says to reload the page.
- **Docs:** user docs for the thinking level, comments and the agent line (with fresh screenshots), and the threat model refreshed for 146 to 151.

### Internal

- The health check form's fields come from one shared field builder (no visible change).
- Watchdogs in the Windows stress runners signal only the process they started; tmux capture and four terminal tests wait for what the shell prints; CDP suites that look at the light theme clear the custom colour variables first, and the "really light" check runs in every gate.

## v1.4.150 — A merge queue for teams, Windows round 3, comments redesigned, and high-contrast fixes

### Features

- **Merge queue (a working mode for teams that land through one person):** choose **Merge queue** in Team settings and every agent is told how to work: in a worktree, with the patch attached to the task. `coral-board task attach <id> --artifacts manifest.json` adds the next revision of a patch while a task is in review; reviewers approve the exact patch (its digest), so a patch changed after approval, or after a reviewer read it, needs a new review; `coral-board queue` lists the approved patches that have not landed, oldest first; `coral-board task send-back <id> --note "why"` (orchestrator) returns an approved task to its author; `coral-board task mark-landed` (orchestrator or Operator) records what landed. Coral does not apply or push anything itself, and these routes never go through Coral Hub. The guide is `agent_docs/merge-queue.md`.
- **Comments look like a document's comments:** a thread is one card with avatars, a name and time line, a thin thread line, Resolve at the top right and a reply box at the foot; resolved threads are behind Show resolved. The Edit and Delete row takes no room until you hover or focus the card.
- **Agents show their thinking level:** a Thinking row in the hover card (the level the newest turn ran at, or "(as started)"), and the sidebar's agent line is built from Show provider, Show model and Show thinking level. **Show thinking level is on by default**, so the line reads like "Claude · Opus 4.1 · High".
- **A pasted image is one compact chip:** the thumbnail with its remove button on its right edge, no "Pasted image N" pill; several wrap as a row of equal chips. A screen reader hears "Remove image 2" and "Image 2" by the image's place, not a generated file name.

### Changes

- **A command Coral cannot run now refuses the launch (Windows and every OS):** a configured CLI path that cannot be run refuses launch, restart, resume and wake with a 422 naming the setting (before, the CLI on PATH ran, or a bare 500). Through Coral Hub the message names the setting only, never the path.
- **A team in which no member started is an error:** launching a team now answers with each member's reason (422 when all are setup problems, otherwise 500) and takes back what the request made (the team record, the worktree, and the branch and board only when this request created them); before, it answered 200 over an empty team.
- **Other accounts on this computer need a credential (loopback):** every loopback connection is attributed to its operating-system user once; another user, or one Coral cannot attribute, needs a credential and gets no local-only power. An elevated browser or PowerShell of the owner counts as unattributed (the docs say so), as can WSL2 mirrored networking.
- **The webview's system DLLs load from the system folder (Windows):** the desktop app loads the DLLs the webview looks up by bare name from the system folder, and the agent docs now say that a command using `coral api-key` is a script you run yourself, never an agent.
- **The terminal input path is one path:** every sender of text or keys to an agent names who is typing (a person, Coral, or Coral's internal keys), one write function per backend records it, and the stop's Ctrl-C keys are recorded. A person's text sent through an answer prompt or `/send` and `/keys` is now logged by its length, as a typed line is; a character typed through `/keys` is hidden.
- **Windows warnings:** the security, firewall and database warnings no longer draw over each other, each × is a button named "Dismiss: ..." with a 24 px target, and the firewall and tools-folder messages are in plainer words.
- **High contrast and forced colors (Windows):** Settings switches, the selected tab and every agent status dot stay readable; count and status chips keep a border; a pressed toggle keeps its size; the other status dots (sidebar avatar, machine health, Hub server, live job) have their own shapes; a focused text field has an outline; and targets are 24 px at 125% and 150% scaling.
- **macOS:** Import Template and Import from Folder open the file chooser again.
- **Stylesheet fixes:** a cost-dashboard row colour the browser had silently dropped now applies (rows with no thinking level recorded are muted), and a broken welcome-page rule that never applied is removed.

### Fixes

- **Windows launch:** a team launch's failure dialog lists each member's error and a button to Settings.

### Internal

- A test parses every stylesheet; the Windows stress runners use Windows-safe paths and a watchdog; update-check tests use their own feed URLs; the wake test checks the launch command sent, not the screen; and 43 ptymanager tests run in parallel; the tmux lag test no longer starves its reading viewer.
- Windows branches added a note on agy under a folder with a space (8.3 names).

## v1.4.149 — Windows hardening round 2, a firewall warning, and an accessibility pass

### Features

- **Windows Firewall warning:** before Coral binds a network address on Windows, it reads the firewall rules (read-only, nothing is changed) and warns in the dashboard when an inbound rule allows Coral's programs on a Public network (or on no profile) and no block rule overrides it. The warning names the PowerShell command to narrow the rule. Private-only rules are not findings. It is checked again later while the server runs.
- **A warning for a tools folder others can change:** on a portable Windows install, Startup and Settings warn when the folder Coral's agent tools run from can be changed by any account other than SYSTEM, Administrators and TrustedInstaller.
- **Windows polish:** the Coral window has the Coral icon, the taskbar and Task Manager name the program by its version information ("Coral", "Coral server", "Coral tray", "Coral launcher"), and the installer has Coral's dialog art, links in Apps & features, and a product name that follows the build edition.

### Changes

- **The data folder must be Coral's own and private (Windows):** when Coral's data folder is readable by another account, Coral tightens it to your account and refuses to start if it cannot (`--allow-shared-data-dir` overrides it). It acts only on a folder that looks like Coral's: empty, with a database, or with Coral's own files. A folder that is not Coral's is refused and left untouched. The folder holding agents' session tokens (`session_tokens`) gets the same private access list.
- **Desktop app and tray hardening (Windows):** the app opens only http and https links, opens a file only inside the data folder and only a regular `.log` or `.txt` file, calls `rundll32` by its system path, and checks a per-launch token in Go on every native function the dashboard calls. DevTools are available only in a development build. The app no longer receives `CORAL_DEBUG` through its environment; `--debug` still works.
- **Auto-accept on Windows, a fourth round:** it waits on more commands it knows to be dangerous (a `cd` out of the folder, links, `wsl`, pipelines into a shell, archive extraction and clones written outside the folder, `bash` or `sh` by any path judged by its `-c` text). `git clone` with `-b`, `--depth` and the like no longer asks. As before, anything not recognised still passes, and the folder check reads the path text (junctions and mapped drives are not seen).
- **The input debug log on the PTY backend** now records the same entries the tmux backend does for the same input (the typed line, Enter and Ctrl-D as keys), and a person's text is still recorded by its length.
- **Agents' temporary launch files** are removed by their recorded paths when the agent stops, only inside a temp folder; the system temp folder itself is never read.
- **Accessibility:** "Who can read this" works by keyboard, the Messaging button fits at 200% text, Team settings has a Close button, close buttons are 44 px and folded sections work at 390 px, the Linked teams heading is a heading, and every dialog's Close and Escape go through one shared helper.
- **Settings switches are named and described properly:** every switch in Settings, Team settings, the health check, Coral Hub sign-in, the server form and the phone display menu is a labelled switch whose description is read with it by a screen reader; the server form's id, URL and API key fields are named by their label. The Settings wording says what each setting does before the explanation.
- **The README links to Coral Hub (coralhub.dev) at the top.**
- **Telemetry:** the tray follows a switch-off made while it runs, and its child's hook states the setting as it is now.

### Internal

- 57 routes tests that share nothing run in parallel; ptymanager and tmux tests leave nothing behind, so a second run passes; the browser suites wait for what they need instead of fixed sleeps; several loaded-machine flakes (a signed-in call, a toast, a badge, a team launch deadline) are fixed. Windows tests use private per-binary temp folders.
- Two gate fixes of our own: the tray telemetry test clears `CORAL_TELEMETRY_DISABLED` (CI sets it), and the startup-sequence golden follows the new order in which the dashboard registers the same listeners.
- New tests: hostile-command matrices for Windows auto-accept, real Claude, Codex and agy screens pinned against an independent terminal emulator, and a stress runner for input fidelity.

## v1.4.148 — Windows hardening, safer automated input, and a round of dashboard polish

### Features

- **`coral-board peek --lines N`:** shows the last N lines of an agent's terminal, rendered as the screen shows them.
- **Coral Hub can read one team's cross-team settings:** the owner's `GET /api/board/{project}/cross-team/settings` gives the guided setup what it needs. It is owner-only and changes nothing.

### Changes

- **Windows agents start from a filtered environment:** Coral's whole environment is no longer handed to an agent on Windows. CORAL_ names are removed from agents' environment on every OS, matched without regard to case, and an empty environment is refused.
- **Windows launch hardening:** PowerShell and the programs it starts are named by absolute path; DLLs load from System32 only in the desktop app; the launch script runs with Bypass for its own process only, so a Restricted machine policy no longer stops it and the agent's own shells are unchanged; native arguments are escaped for PowerShell 5.1 and prompt files are read as UTF-8; the current folder is not searched for programs.
- **Codex and agy on Windows:** Codex's hooks run, and its commands find the session token through a file when Codex hides the variable; agy gets hook commands cmd.exe can parse, and no hooks at all rather than a hook found by bare name. Codex's session token file stays in Coral's data folder (session_tokens) while the session runs; it is private to your user, and any of your user's processes can read it.
- **Windows stop:** a second Ctrl-C is sent, an idle shell is told to exit, and the whole process tree ends through a job object.
- **Auto-accept on Windows waits on commands it knows to be dangerous** (Remove-Item and its aliases, del, rd, move, copy, format, and others, in PowerShell and cmd). Anything not recognised still passes, and the folder check reads the path text: junctions and mapped drives are not seen. A rooted path (`\Windows`, a UNC path, `C:foo`, `~`, `%VAR%`) counts as outside the working folder.
- **Typed text from Coral is safer on both terminal backends:** text sent for an agent has no control characters or line breaks, and a text of nothing but control characters is refused rather than sent as a bare Enter. Multi-line text is one paste and CRLF is one line break. Enter is withheld when the rendered screen shows something other than the prompt. `/keys` sends key names as keys, follows the program's cursor-key mode, takes at most 64 entries, and answers `terminal_gone` for a gone session.
- **Workflow steps with other CLIs:** pi steps no longer get Claude's `--no-session-persistence`; a pi prompt never starts with `-` or `@`; the prompt of Codex and agy goes where it can only be the prompt, and agy steps run with `--disable-slash-commands`. Claude's launch prompt that starts with / gets a leading newline, so it is read as text, not run as a slash command.
- **The input debug log is private and short-lived:** secrets are redacted, what a person types is kept only as its length, the files are readable by you only, and the log deletes itself after 7 days unwritten. Text sent through an answer prompt or the plain /send and /keys routes is kept after secret redaction, not as its length.
- **Windows files and links:** `file:///C:/...` links open, network paths are refused by their text, a change in a subfolder clears its repository's file list, and the diff prune keeps objects that other diffs still link.
- **Dashboard polish:** terminal backend described in one line each, machine health in About, a Copy button for the Coral Hub start command (said once, with the order of the two commands), the chooser's Cancel, an Unsaved changes notice in Settings, the CLI paths folded, and Settings > Agents in groups with the units of the storage limits beside their fields; text on a phone is at least 13 px in the Servers view and the agent list.
- **Docs site:** every page has a title and a description, Open Graph and Twitter tags, and a robots.txt that names the sitemap.

### Fixes

- **A person's pasted text is logged by length** on both terminal backends (PTY and tmux), as a typed line is.
- **A cancelled key sequence stops at once** instead of waiting out its delay.
- **Terminal capture is the rendered screen with scrollback**, not the raw output ring.

### Internal

- Windows and Linux stress runners can choose the terminal backend and check input fidelity; test runners isolate USERPROFILE with HOME and agent tests leave nothing in the system temp folder.
- The browser gate balances its six parts from the last three green runs.
- Troubleshooting notes for Codex's Windows sandbox permissions.

## v1.4.147 — Team settings uses the room, and cross-team links arrive

### Features

- **"Who can read this" in the dashboard:** the artifact viewer and the details of an Artifacts row show, for the owner of this computer (and the computer's owner through Coral Hub), which owners hold the artifact, their teams, the teams linked through Coral Hub, and the plain note that this is a Coral control, not an operating-system boundary. Anyone else is refused and nothing is drawn. Every word from the server is shown as text.
- **Cross-team link table:** Coral receives Coral Hub's full snapshot of cross-team links over its own tunnel, ages it by this computer's clock and shows it to the owner. Nothing is admitted by it yet: it changes who can message or send files to whom in no way. This needs Coral Hub's matching update.

### Changes

- **Team settings uses the room and one scroll:** the side panel is `clamp(420px, 30vw, 640px)` wide until you drag it (a saved width is kept); on Workflow the team working mode comes first and the preset editor is behind an **Edit presets** disclosure; Prompt inspection is one read-only page with a select for the role, the effective texts shown and the defaults and Override folded, and Copy takes the texts of the role shown. On a phone the Save footer is one row ("Save preset instructions separately.") and the form no longer scrolls sideways.
- **Messaging and Network and Hub:** the Save hint no longer contradicts the Coral Hub line, each switch is described by what is said about it, and the Network and Hub switch rows are laid out like every other row.
- **Agent Team and Health check:** the team name is above the sections of the Agent Team dialog; the four template actions are one **More template options** disclosure; the Health check tab shows the check itself and keeps Reviews and the automatic rules in one closed disclosure; Customize tabs has its own icon; Team view cards no longer repeat their name; the template disclosure buttons meet 4.5:1 contrast on the light panel.
- **The Coral Hub sign-in and enroll pages:** buttons and inputs are 44 px high on every pointer.

### Internal

- One route table now says which routes are the owner's, read by the connector and by the server; there is no change in behaviour.
- A test refuses literal invisible characters in committed source (the existing ones are now written as escapes), and a census drives the real connector into the real router for every artifact and cross-team route.
- Tests that waited on the order of two background notices, on a freed limiter slot, and on the print-mode record no longer fail on a loaded machine.

## v1.4.146 — Artifacts have owners, and the owner of Coral Hub can switch cross-team messaging

### Features

- **Every artifact now has an owner.** An upload records the owner of the team the agent was launched onto (an image pasted in the dashboard belongs to that agent's team; a received file to the receiving team's owner); a record with no owner belongs to the owner of this computer. An agent can read an artifact only when it is owned by the owner of its team: anything else answers "not found", the same as an artifact that does not exist, so it does not even say that the artifact is there. The dashboard, the command line and an API key still read everything. **Two teams with the same owner can read each other's artifacts.**
- **Who can read an artifact:** a new owner-only route, `GET /api/artifacts/{id}/readers`, lists the owners that hold it and the teams whose agents can therefore read it (a dashboard view follows).
- **Coral Hub's owner can turn a team's cross-team messaging and file sharing on or off,** through four dedicated routes, behind a local lock: Settings > Network and Hub > "Let Coral Hub change cross-team settings" (on by default; off refuses Coral Hub in both directions). Team settings > Messaging says "Coral Hub can change these settings." while it can, and under each switch who changed it last ("Turned on by … via Coral Hub at …", or on this computer). This needs Coral Hub's matching update.
- **New Agent opens on the folder and Launch:** the first screen is the working directory, a "More options" button and Launch; the rest sits behind the button, every field still reachable. The working directory leaves the Workspace section, which is now called "Name and persona".

### Changes

- **One size rule for every dialog:** each dialog is as high as its content up to 90% of the window, `clamp(360px, 92vw, 960px)` wide, with one scroll area and its footer in view (Launch, Create Agent, Settings and Team settings no longer have a fixed height).
- **A launch problem shows as a line under its field, saying what to do next** ("Folder not found: /x. Pick one with Browse."), tied to the field for screen readers and cleared by the next edit; the toast stays.
- **Controls that did nothing or repeated are gone:** Claude in Bypass Permissions mode has no Allow row (and no Shell patterns field), the Allow and Deny chips are buttons with a pressed state, Settings > Security shows no Database encryption row in a build without encryption, there is no Remind agent button on a terminal, and the Agent Team dialog has no health check option (it is in Team settings > Health check).
- **`coral-board targets` lists a target by its address alone** when Coral Hub sends no reachable field, and says "(online)" or "(offline)" only when Coral Hub says so. The agent docs show the same.

### Internal

- The connector stamps the owner marker on the artifact routes for the owner's view through Coral Hub (the route list needs Coral Hub's matching update).
- A settings test waits for rows that only fill on a machine before hiding them, which ended an occasional failure.
- Test speed: the telemetry end-to-end test, the startup tests, the model-list fetch of the signed-in test CLI (12 s to 0.2 s) and the hanging-status tests of three packages no longer wait out long fixed limits.

## v1.4.145 — Artifacts through Coral Hub are for the owner

### Features

- **Coral now gives the owner, through Coral Hub, a list of every artifact on this computer** (file name, type, size, team, the agent or person that made it, and when) **and accepts the owner's delete**; Coral Hub's Artifacts tab uses them as its matching update arrives. A message that linked to a deleted artifact shows "Artifact unavailable". An artifact made before this release shows "unknown" for who made it.

### Changes

- **Cross-team messaging is set in one place: Team settings > Messaging.** The tab shows one status line and one button, the "Cross-team messaging" and "Share files with other teams" switches (moved from Workflow), and the teams on other Corals that are linked to this team. A team on another Coral can message yours only when your Coral Hub account has linked the two teams and cross-team messaging is on here; linking happens in Coral Hub. Through Coral Hub the switches stay read-only, with "This setting can only be changed on the computer itself". When a message from another team is refused, the reply names Team settings > Messaging.
- **Through Coral Hub, artifacts and pasted images are for the owner only**: the account this computer is linked to. People you have shared this Coral with in Coral Hub no longer see, list or comment on its artifacts or pasted images. On this computer the dashboard works as before.
- **This needs Coral Hub's matching update:** until it arrives, artifacts and image thumbnails do not open through Coral Hub (this computer's dashboard is unaffected).
- **Older link:** a computer linked to Coral Hub before Coral recorded its owner refuses artifacts and image thumbnails through Coral Hub until you link it again.
- **Pasting an image no longer shows a notice**, and the "Share pasted images with Coral Hub viewers" switch is gone, because the answer is now always the same: only the owner sees them. "Remove shared copy" on a thumbnail stays. The documentation now describes pasting in [Files and artifacts](https://subgentic.github.io/coral/files-and-artifacts/#images-you-paste-or-attach).
- **Agents can read Coral's own documentation:** `coral-agent docs` lists and prints the docs, every agent prompt now names it, and the Docs tab has a **Copy docs line for agents** button.

### Fixes

- **The Team settings tabs no longer move while they load.** The Messaging tab is drawn together with the others, so the tab strip is final the first time it shows and does not change when Health check arrives.

### Internal

- When an agent session has two active board subscriptions, Coral now always uses the same one (the newest, then the highest row), so a message is always addressed the same way.
- Every read of the latest release now checks that the reply is Coral's own release page, in the server's log line as well as in the update notice.
- A test seeded an agent under two names, so a notice could go to either one; it now uses one name, which ended a failure of about one run in sixty.
- The macOS update code, which is not switched on in any release yet, read only the first 64 KB of the system's process list, and a busy Mac's list is about 500 KB: it could not see the new Coral start and would have rolled back every update, and could have taken a running Coral for a stopped one. It now reads the whole answer and refuses one that was cut.

## v1.4.144 — Two release channels: Agentic and Approved

### Features

- **Choose your update channel in Settings > Updates:** **Approved (recommended)**, releases the team has approved, is the default for everyone; **Agentic (every release)** tells you about every release as soon as it is published, including ones not yet approved, which may change faster and be less tested. The channel decides what the update notice and the tray offer. Automatic install, when you turn it on, still uses Approved releases only. Through Coral Hub the channel is shown, not changed.
- **Waiting for approval:** on the Agentic channel, Settings > Updates lists the releases published since the newest approved one, each with its age and its release notes (shown as plain text), and a link to the repository's approval page. Only the repository's operator can approve; Coral never approves anything itself.
- **The update notice names the channel:** an Agentic release says "Agentic channel" and, until it is approved, "Not yet approved."

### Changes

- **Every release is published first as a pre-release** and becomes the latest release, with the stable download links, once it is approved. An installed Coral on an earlier version keeps following approved releases with no change.
- **The update check asks GitHub for Coral's repository by its numeric id** and accepts a release only if its page is Coral's own, so a repository that took over the old name could never be followed.
- **The tray asks the local Coral server for updates** instead of GitHub, so the tray, the dashboard and the server's log line share one check and one setting.

### Internal

- Each release records who pushed its tag and whether that exact commit passed the required checks, and stops before building anything if it did not. A weekly job re-checks the newest releases' files against their signed checksums.

## v1.4.143 — The Go module is now github.com/subgentic/coral

### Changes

- **The Go module path is now `github.com/subgentic/coral`** (it was `github.com/cdknorow/coral`), to match the repository's new home. This matters only if you build Coral from source with your own changes or import its `pkg/` packages in your own Go code: change the import paths from `github.com/cdknorow/coral/pkg/...` to `github.com/subgentic/coral/pkg/...`. Nothing changes for an installed Coral, and you do not need to reinstall or change any setting.

### Fixes

- **Windows builds made with `tools/build_windows.ps1` now report their version.** They reported none before: `coral --version` said "development build" and such a build never checked for updates.

### Internal

- **Every build now checks that its version and settings flags take effect**, so a build that quietly ignored them can no longer be published.
- The Python packages that build the documentation site, and the build tool for the CodeMirror editor bundle, were updated to clear security alerts. Nothing in a package or in the dashboard changes.

## v1.4.142 — Coral has a new home: github.com/subgentic/coral

### Changes

- **Coral's repository has moved to [github.com/subgentic/coral](https://github.com/subgentic/coral).** This is the first release built there. Links to the old address (`github.com/cdknorow/coral`) keep working: GitHub redirects the repository page, issues, releases and clones. Nothing changes for an installed Coral, and you do not need to reinstall.
- **The documentation moved to <https://subgentic.github.io/coral/>.** The old documentation address does **not** redirect, so update any bookmark or link you have to it.
- **Homebrew is unchanged for now:** `brew install --cask cdknorow/coral/coral` and `brew upgrade --cask coral` keep working, with no need to tap anything again.
- **The update notice looks at the new home:** the "a new version is available" notice in the dashboard, the tray menu and the server's startup log now reads releases from `subgentic/coral`. An installed Coral on an earlier version keeps finding this release through the old address's redirect.

### Fixes

- Built with Go 1.26.10, which fixes a certificate-verification issue in Go's crypto/x509 (GO-2026-6809).

### Internal

- Releases are now built from subgentic/coral and published only after every file is attached, and they cannot be changed afterwards; changes to main go through a pull request with a required check.

### Checking a release

Releases are signed by the repository's release workflow on GitHub, with no long-lived keys. Because the repository moved, **v1.4.142 and later are signed by `subgentic/coral`**, and **v1.4.141 and earlier were signed by `cdknorow/coral`**. Use the owner that matches the version you downloaded: a release's attestation stays with the account that built it, so for v1.4.141 and earlier `gh attestation verify --repo` finds nothing (it answers 404); the `--owner` form below finds them.

With the [GitHub CLI](https://cli.github.com/), for v1.4.142 or later (put the version you downloaded in `--source-ref`, for example `refs/tags/v1.4.142`: it ties the check to that release's tag):

```bash
gh attestation verify coral-linux-amd64.tar.gz --owner subgentic \
  --signer-workflow subgentic/coral/.github/workflows/release.yml \
  --source-ref refs/tags/v1.4.142
```

and for v1.4.141 or earlier (with the tag of the version you downloaded in place of `v1.4.141`):

```bash
gh attestation verify coral-linux-amd64.tar.gz --owner cdknorow \
  --signer-workflow cdknorow/coral/.github/workflows/release.yml \
  --source-ref refs/tags/v1.4.141
```

Offline, with [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) v2.4 or later, download `SHA256SUMS.txt` and `SHA256SUMS.txt.sigstore.json` from the same release. For v1.4.142 or later:

```bash
cosign verify-blob SHA256SUMS.txt --bundle SHA256SUMS.txt.sigstore.json --new-bundle-format \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/subgentic/coral/\.github/workflows/release\.yml@refs/tags/v'
sha256sum --check --ignore-missing SHA256SUMS.txt   # macOS: shasum -a 256 --check --ignore-missing
```

and for v1.4.141 or earlier, the same commands with `cdknorow/coral` in the identity:

```bash
cosign verify-blob SHA256SUMS.txt --bundle SHA256SUMS.txt.sigstore.json --new-bundle-format \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/cdknorow/coral/\.github/workflows/release\.yml@refs/tags/v'
sha256sum --check --ignore-missing SHA256SUMS.txt
```

The checksum command must print `<your file>: OK` for the file you downloaded; if it prints nothing, nothing was checked. If either check fails, the file is not an official release.

## v1.4.141 — A sender can no longer dodge the relayed-message limits by changing its names; Messaging settings show before the list is enforced

### Changes

- **Team settings > Messaging shows before the list is enforced:** a line says the list is not in force yet, and the owner can prepare and save it before turning the `cross_team_allowlist` switch on (the one-time prompt for a board that was open appears only once the list is enforced).

### Fixes

- **Relayed cross-team messages are limited per source Coral and target board, and per source Coral overall, whatever names the sender picks:** the limit used to be keyed on the sender's own board and subscriber strings, so varying them dodged it. Besides the existing per-sender limit (3 at once, then one every 6 seconds), a relayed sender is now limited to 30 messages at once and then one every 2 seconds per source Coral and target board, and to 120 at once and then one a second per source Coral overall. A slow sender is never limited, invented target names share the per-source budget, and another Coral has its own budget. The answer is the same 429 with `X-Coral-Limit: action`.
- **The Team settings tab strip no longer shrinks to 10 px under a tall panel**, which hid the tabs after it on a phone.

### Internal

- A browser suite of the allowlist editor with hostile data from the server (labels and ids with markup are text only, a suggestion without an id cannot be added, a bad date or count does not break the page, the list is full at 200), and a wait for the Messaging tab in the Team settings test that reads the tab order.

## v1.4.140 — Choose who may message a board, and see the files that arrive with a cross-team message

### Features

- **Who may message this board (off unless you turn it on):** with the `cross_team_allowlist` switch on, a board accepts cross-team messages only from the senders on its list, each named by its stable cross-team id (default deny). The list is edited by the Operator on this computer only, kept as one whole list in a single save (at most 200 entries, no duplicates, never the board's own id), limited to 30 edits an hour, and every change and every refusal is logged. A message from a sender that is not on the list is refused with `cross_team_refused` ("this board does not take messages from that sender", the same answer whatever the reason: not listed, no such board, off, deleted or nobody to deliver to). The switch is off by default, so nothing changes until you turn it on.
- **Team settings > Messaging:** the list as a screen (name, Coral and id, Remove; Add from this computer's boards or by id), the change and refusal log, and a one-time prompt for a board that was open before (the suggestions start unchecked and nothing is added until you check it). The tab appears only when the server enforces the list.
- **Files on the receiving board:** a cross-team message that arrived with files shows them under its text, as an image thumbnail or a file chip (name and size) that opens in the artifact viewer; a `coral://artifacts` link in an inbound message whose file did not arrive shows as muted text "(not on this machine)". The sender's own copy and ordinary posts are unchanged, and the "from <board>" badge text is darker in the light theme (it was 4.44:1).

### Internal

- Coral says in its hub handshake that it supports the list (`capabilities: ["cross_team_allowlist/1"]`); whether the list is in force is the separate setting above, default off. A hub reads the capability only from the destination's own handshake, a malformed or over-long list means unsupported, and an unknown capability is ignored.

## v1.4.139 — Linked files go with a cross-team message, and a received message lists the files that came with it

### Features

- **Files linked in a cross-team message are attached automatically:** when the Operator's cross-team files switch is on, the `coral://artifacts/...` and `/api/artifacts/...` links in the text of a cross-team send are attached with the message, within the same checks and limits as `--attach` (at most 4 files, and only the first 20 distinct linked files are looked at). `coral-board send` says how many were attached automatically because the text linked them, and still warns about any link it did not attach.
- **A received message lists its files:** a cross-team message that arrives with files carries `external.attachments` in its envelope, a list of `{id, name, media_type, size}` in the order the sender attached them (up to 16; left out when there are none), so the page that shows the message can offer them. The listed file name is cleaned.

### Internal

- A test matrix for auto-attaching linked artifacts on cross-team sends.

## v1.4.138 — A link to a file on another computer now says so, instead of failing without a reason

### Changes

- **A cross-team message that names a `coral://artifacts/...` link it did not attach now warns about it:** such a link is a file on this computer only, so the other Coral cannot open it. The message is still sent; the reply carries `warnings: ["unattached_artifact_links"]` and the links by name (at most 10), `coral-board send` prints "Warning: the message names N links that are not attached ... Send again with --attach coral://artifacts/<id> for each (at most 4 files)", and the `/api/artifacts/<id>` form of the same link is covered too.
- **`coral-agent artifact download` says why a link is missing:** an artifact that is not on this computer answers "this artifact is not on this computer. It may live on another Coral (a link from a message sent by another team or computer is only a name here): ask the sender to attach the file".

## v1.4.137 — Custom models in Settings, `coral://` links agents can always download, a tidier machine menu and board panel

### Features

- **Custom models:** Settings > Agents > Custom models lets you add, edit and remove model ids per provider (Claude, Codex, Antigravity and Pi; Pi takes no thinking levels) with the thinking levels each accepts, saved with Apply. They are merged into both model catalogs (the launch pickers and the model list), the pickers mark a custom model, and Codex and Antigravity now offer their thinking levels. A save that omits the models list can no longer wipe the set.

### Changes

- **Attached `coral://` links work from every agent sandbox:** the sent text carries the exact download command with the real link, and the agent prompt and docs say plainly what a `coral://` link is. `coral-agent artifact download` now works from Codex's default sandbox, which blocks connections to localhost: if Coral cannot be reached it reads the artifact from this user's own data folder, and the SHA-256 of what it reads must equal the link's digest (a server that answers, whatever the status, is never second-guessed).
- **Machine menu cleanup:** one launch row (Launch agent, Launch team, and Open in Servers as a text link; Open folder only on the hub's own machine), one Update control ("Update to <version>"), a two-column details grid, the version cut before `+` with the full build as the tooltip, and the header toggles instead of a Collapse button.
- **Team board panel:** Pause and Export are icon buttons with a name and a tooltip each; the pause icon, name and tooltip follow the state (Pause or Resume), Export shows pressed in select mode, and both are 44 px on a phone.

### Fixes

- **A workflow step's model is checked before it reaches the CLI:** a saved workflow could carry a model value shaped like a command-line option, which was passed to the agent's CLI unchecked; every place a model becomes a `--model` argument (launch, team launch and reset, restart, workflow steps) now accepts only a valid model id, and a step with anything else fails with "not a valid model id".

### Internal

- The safefs join census and the telemetry census classify the one new file join and the one new test file that mention the fetch command.

## v1.4.136 — Pasted images are sent to agents as links (never a path), a message you send is always yours, slash commands that ask a question, and a phone pinch-zoom fix

### Fixes

- **A pasted image is sent to the agent as its `coral://artifacts/...` link and one hint to fetch it (`coral-agent artifact download`), never as a path on this computer:** an agent's chat text no longer carries the uploads folder, and with it the OS account name. The chat still shows the thumbnails (older path-and-link turns too), the agent prompt and the docs say to fetch the link first, and every agent type's stand-in fetches and decodes it.
- **A message you send from the dashboard is shown as yours whatever its words:** text such as `[Coral ...]` or `Stop hook feedback:` typed by a person was being drawn as a Coral notice. The server now remembers what the Operator sent and marks those turns (`coral_notice: false`); the page trusts the mark and reads a turn's words only when no origin is recorded.
- **A pinch zoom no longer halves the page or the artifact viewer on a phone:** `--visual-viewport-height` is the visual viewport's height times its scale, so a zoom is not mistaken for a keyboard.

### Changes

- **A slash command sent from the chat behaves like one typed in the terminal:** its pending bubble clears once Claude ran it (the command shows as a small note), and the screen is read for the confirmation dialog it may open ("Switch model?", "Change effort level?"), which the chat then shows with Yes and No.

## v1.4.135 — Background jobs no longer start a signed-out Claude

### Fixes

- **A signed-out Claude is never started by a background job:** a workflow step, a goal, the knowledge indexer, the summarizer, a team import, the theme generator and the model list used to run `claude --print`, which on a Claude that was not signed in started its sign-in flow and held the login. They now ask one shared sign-in check first (twenty callers at once share a single status process, which is given no stdin, no terminal, the temp folder and no Coral session token, and is killed when the check gives up), and a signed-out Claude ends the job at once with "Claude is not signed in".
- **A sign-in check that does not answer is its own state:** after 6 seconds (then a 30-second back-off) the job is skipped with "Claude did not answer the sign-in check", never reported as "not signed in".

### Changes

- **Clear failed states for refused jobs:** a background run refused for want of a sign-in shows as failed in the run view, the run lists, the last-run line, the job's banner and its history, with "Claude is not signed in" and "Sign in with claude in a terminal, then run this again"; Antigravity (`agy`, `antigravity` and the old `gemini` type) reads "Antigravity is not signed in. Sign in with agy in a terminal", an agent type this build does not know reads "The agent's CLI is not signed in", and the theme generator's 409 says it too.
- **A calm status line while Claude is not signed in:** "Claude is not signed in: background jobs are waiting" (hover or tap to see how to sign in), looked at again each minute and gone once Claude is signed in; no alert and no toast. `GET /api/system/status` carries `claude_signed_in` and `claude_checked_at` once a check has been made.
- **`GET /api/artifacts/{id}/meta`** answers an artifact's id, size, media type and stored name without its bytes (404 exactly as the content read is). An artifact id is now exactly 64 lower-case hexadecimal digits: an upper-case spelling or any other form is refused with 400.

### Internal

- A stand-in `claude` CLI for tests (signed in, signed out, login in progress, hanging status) with a call log, which refuses to run where a real `claude` is reachable, and checks that every background caller starts nothing while Claude is not signed in.

## v1.4.134 — Replies to a task from Coral Hub, and a Download section at the top of every release's notes

### Features

- **Reply to a task (for Coral Hub):** `POST /api/board/{project}/tasks/{taskID}/reply` with `{"content": "..."}` stores a board message linked to that task, written as the signed-in hub user exactly as a normal board post is (the same size limit, cleaning, hub rules and write budget). The task's creator gets an `@` mention at the start of the reply when the name tags exactly that one member; if the creator has left, shares a name with another member, or is not a board member (a task made from the hub), the reply is stored with no mention and the answer says `creator_not_reachable`. The reply never changes the task. A task that is not on that board answers 404 before anything is written. Messages read back carry `task_id` when they are replies, and `GET /api/board/{project}/messages/all?task_id=N` lists the replies to one task, oldest first.

### Changes

- **Every release's notes start with a Download section:** direct links to that release's stable-name files (macOS, Windows installer, Windows portable, Linux x86-64 and ARM64) and the Homebrew line, because GitHub hides the disk image behind "Show more assets". The section is added when the release is finished and each link is checked first.

### Internal

- **A test-only build option for Coral Hub's relaunch tests (the `relaunchfixture` build tag):** it exists so the Coral Hub team can build a throwaway copy of Coral whose update-relaunch routes are switched on against a fixture updater, and test their half end to end. A build made with the tag listens on this computer only, refuses `--remote` and any non-loopback `--host`, connects to no hub unless the test names one, refuses the default data folder (`~/.coral`), and reports `+relaunchfixture` after its version. **No release contains it:** the release workflow does not use the tag, the relaunch routes still answer 404 in every build that ships, and a release's `coral --version` carries no suffix.
- **The Go tests run faster:** the heavy routes tests open a migrated copy of the board and sessions databases instead of parsing the schema again (two helpers, `NewStorePrepared` and `OpenPrepared`, for tests only), and the Kill tests use a folder of their own. Two guards fail if a file outside the test helpers imports them.
- The board database gains a nullable `task_id` column on messages and a partial index of the replies (it touches no existing row).
- Tests and tooling: a long-session soak check that runs only when asked for, faster browser checks, a check that refuses a commit that adds a built program or a file over 5 MB, and the real-Coral restart test waits for the run record the server writes just after it starts listening.

## v1.4.133 — Settings labels in sentence case, one spelling of "CLI paths", board reads that carry the board's identity, a stage lock that fails safe on Windows

### Changes

- **Settings labels are sentence case:** 17 labels were Title Case beside sentence-case neighbours; Customize Theme is no longer a second primary button, and a check fails on a new Title Case settings label.
- **"CLI paths" is spelled one way everywhere it is named:** the settings heading, the buttons in the team and install dialogs, and the messages Coral shows when a CLI is not found or its path cannot be run now read "Settings > CLI paths".
- **Board reads carry the board's identity:** the board's messages, all-boards messages and tasks replies set two response headers, `X-Coral-Board-Id` (the board's stable id, or the old name for a board that predates ids) and `X-Coral-Board-Created-At` (RFC 3339, UTC). Only a plain printable id becomes a header value. The reply bodies are unchanged.
- **Update screens** (still behind the update flag, which is off) have words for the two new cases: Coral could not be restarted automatically on this computer (no control in the top bar; Settings carries the sentence), and a roll-back after which Coral was stopped because the update could not restart it.
- **The docs name the stable download files** (`Coral.dmg`, `Coral-x64.msi`, `Coral-x64-portable.zip`, `coral-linux-amd64.tar.gz`, `coral-linux-arm64.tar.gz`, under `releases/latest/download/`) in the order of the README's download block; the versioned names are the same files.

### Fixes

- **A stage lock that cannot be read is never taken for stale, and a lock file is removed only while it still holds what this check wrote** (retried briefly on Windows), so two update checks can never hold the lock together and one check can never remove the lock another put at the path.

### Internal

- `coral-update status` shows version fields as versions or `unknown`, and who started an attempt as `local`, a hub pseudonym or `other`.
- Tests: the startup-order check no longer depends on how fast the processor is (it passed 20 of 20 at a 16× throttle, 0 of 6 before), and the linear-time guard's own test takes its trigger and floor as parameters so a quadratic stand-in is refused at any machine speed.

## v1.4.132 — Pasted JPEGs lose their embedded thumbnails, a terminal freeze fixed, smaller startup, keyboard and phone passes

### Fixes

- **Pasted JPEGs no longer keep an embedded thumbnail or free-text APP segments:** a JFIF or JFXX thumbnail (which can show the uncropped original) and the free text in APP0 and APP14 are now removed, along with the location and camera data that were already removed. Only the JFIF and Adobe headers stay. A JPEG pasted before this update is unchanged.
- **The terminal no longer freezes when you switch from chat to the terminal and back:** a fit loop could grow the terminal to over 100,000 rows and leave the page unresponsive for seconds. A fit now never makes the terminal taller or wider than its window, a row's height is the renderer's own measurement, and the first fit after a switch asks for the rows the terminal settles at.
- **The live feed no longer stacks sockets:** reconnecting closes and detaches the previous feed socket first, so a second call cannot open another or schedule a second reconnect.
- **Stopping Coral closes the dashboard and terminal sockets cleanly** (close code 1001, "Coral is stopping", up to about a second) instead of leaving them to drop with the process, and a workflow step that ignores SIGTERM is killed before Coral exits.

### Changes

- **Smaller startup:** the team view, team settings and the task detail dialog load the first time they are used (49 KB less in the startup bundle); when that first load is slow the clicked control shows it is working.
- **Keyboard and screen-reader pass:** Escape closes the preview's comments sheet and returns focus to its toggle, and the toggle's accessible name starts with its visible words.
- **Phone and theme pass:** a 44 px Comments toggle on a phone, docs-link contrast in the light theme, and checks at 320 and 390 px in both themes. Every bare colour and radius in the stylesheets is now a theme token.
- **The cost dashboard shows how many metrics were not stored** because the raw rows were at their 500,000 cap (`cap_dropped` in the call metrics summary).
- **Update screens** (still behind the update flag, which is off) say what the server sends: the words for each reason a relaunch is unavailable, Homebrew and unwritable installs, why nothing is ready yet, and how a roll-back is reported.
- **The server records how it was started** in the updates folder of its data folder (owner-only; a URL with a password is never recorded, and only an allowlisted part of the environment is kept), so an update can start it again. Nothing uses it until updates are turned on.

### Internal

- The automatic-update work continues without turning anything on: `coral-update status` (what the updater knows, read-only, no network), an apply that checks Coral can be started again before it begins, roll-back reporting, and `GET /api/system/update` carrying `checked_at`, `install_kind`, `upgrade_command`, `not_ready_reason`, `latest` and `last_rollback`. `coral-update` is in the source and in no package.
- An update rehearsal rig (a fake Sigstore, a fake GitHub, a tamper matrix and a script that replaces a real server by the next version) runs the real check and apply end to end; it is test code and no released program links it.
- CI: every checkout drops its token, a lint keeps actions pinned and workflow inputs out of scripts, a fuzz job (paste sanitiser, board key, comment anchor, `SHA256SUMS.txt`, `promote.json`) runs 2 minutes per target each night, and a fuzz finding is fixed (`SHA256SUMS.txt` parsing refuses a file name with a control character).
- Tests: a Windows release smoke (package list, version, MSI install and uninstall, a launched stand-in, a clean stop), a migration chain test from the 1.4.80 and 1.4.110 databases, startup-order and xterm-internals checks, and cleanup of unused code.

## v1.4.131 — A pasted image's path no longer shows your account name through Coral Hub, faster long artifact lists and replies

### Features

- **Servers tab: an "Updating" badge** for a server the hub is relaunching, read from the hub's typed status. It sits behind the update flag, which is off, so nothing changes yet.

### Changes

- **Long artifact lists draw faster:** the first 40 rows appear at once, the rest a few frames later, and the file-type icons are cloned instead of parsed per row.
- **Owner-only team settings refused through Coral Hub** (the health check, the team settings and the prompt inspection) now say so in plain words, and a plain refusal message from the server is shown only when it came from this computer.
- **Faster replies and searches:** compressed JSON replies reuse their gzip writer and encode buffer, and the board has a sender index and a per-board early exit for the all-boards search.

### Fixes

- **Through Coral Hub, a pasted image's path no longer shows this computer's account name in the chat (it reads `~/...`); since 1.4.129 it did.** This also covers Windows spellings of the path (backslashes, mixed separators, another letter case).
- **Claude project folder names match the Claude CLI's:** every non-alphanumeric character becomes a dash, and a name over 200 characters is cut and hashed, so sessions are found for folders with unusual names.
- **Call metrics no longer grow without limit:** past 500,000 raw rows, new rows are counted and dropped until the hourly roll-up makes room; existing rows are kept.

### Internal

- Dependencies: grpc 1.83.2 (GO-2026-6443, a server panic in grpc's xDS server routing; no shipped Coral program calls that path).
- The automatic-update work continues without turning anything on: `coral-update` gains hardening (the Sigstore root refresh, safer lock handling) and an apply step for macOS and Linux. It is in the source and in no package, and nothing calls it.
- Release checks: the stable-name copies of the packages are also a run artifact, so `verify-release.sh` checks them without downloading.
- Test and gate fixes: the teardown check, the live-feed reconnect test, the cancellation, gzip-budget and Windows updater tests, and the flake census.

## v1.4.130 — Artifact lists read like the agents tab, comments in the docked preview, and every release also publishes stable download names

### Features

- **Every release also publishes stable-name copies of its packages:** `Coral.dmg`, `coral-linux-amd64.tar.gz`, `coral-linux-arm64.tar.gz`, `Coral-x64.msi` and `Coral-x64-portable.zip` are byte-identical copies of the versioned files, listed in `SHA256SUMS.txt` under both names, so `/releases/latest/download/<name>` never goes stale. The Linux tarballs' Sigstore bundles are copied under the stable names too.
- **Artifact comments in the docked preview pane:** the preview beside the chat has a Comments button (or a panel beside the document) and the Comment button by a selection, as the full artifact viewer does.

### Changes

- **Artifact lists:** the header is one row (title, the search box, the filter icons, then Refresh and Copy at the right), and each name has the Browse tab's file-type icon, chosen by the name or, for a name with no extension, by its media type.
- **Through Coral Hub, the machine's owner can now see and save a team's health check settings and read the prompt inspection** (the owner's alone: the hub sends a marker the machine checks, the save is strict and size-capped, and an oversize body is refused with 413); the matching tunnel rows move from never to launch.

### Fixes

- **Preview no longer says "Coral was updated":** an artifact's bytes answer carries no build header, and a year-cached answer from an older build could trigger the banner; the banner now ignores a build header on an artifact's content path.
- **A name read from a file, a message or a request is one folder name before it is joined onto a folder:** artifact and cross-team ids are 64 hex digits (a pending record's id, a record's id, an attachment's id, the object and sidecar reads) and a team worktree's board name is one folder name. A census of every join of a variable name fails the build when a new one is not reviewed.
- **Git snapshots record an agent with no session id once per commit, not once per poll,** and a board settings update of several settings is one transaction. Existing duplicate rows are left in place.

### Internal

- Dependencies: the dependency scan found six advisories in x/net 0.57.0 and grpc 1.82.1 in the repository's tunnel library; no shipped Coral program contains that code, so none affected Coral 1.4.129. Both are updated in 1.4.130 anyway (x/net 0.60.0, grpc 1.83.1, x/crypto 0.57.0, x/text 0.42.0).
- The automatic-update work continues without turning anything on: the `coral-update` program (check, download and stage a verified release; no apply yet) is in the source and is not in any package; the update screens behind the off flag handle the hub's answers and use the final route names; a verified manifest's revocations are kept at once.
- Release checks: `verify-release.sh` verifies a release against the repository that built it (a table of homes by first tag, one today), a smoke test of the Linux tarball runs on each architecture's own runner in the release build (a smoke failure on a tag is fixed forward in the next version, never re-tagged), and `mock-agent` answers `--version` and `--help` so Coral's version probe no longer leaves a mock running.

### Tests

- A 50-round teardown check (nothing is left running after Settings, the artifact viewer, agent switching, list rebuilds, preview tabs and every dialog).

## v1.4.129 — Pasted images become artifacts you can see over Coral Hub, steadier banners, and one filter icon on the lists

### Features

- **An image you paste or attach in the dashboard is now an artifact:** the agent still reads the file by its path, and Coral also keeps a copy as an artifact on that agent, so the chat shows a thumbnail that previews locally and through Coral Hub (the image is served by the same artifact read the hub already uses; no new route). Send types the path and the `coral://artifacts/...` link of each image, the chat shows the file name with a "Remove shared copy" button, and a notice before the first paste after each Coral Hub connect says the image is visible there and that Coral cannot redact what is in an image, with "Don't share pasted images". JPEG, PNG and GIF are stripped of metadata (not re-encoded) in the agent's file and the artifact alike, and the JPEG stripper now keeps the EXIF Orientation tag so a portrait photo is not on its side. Pasted images follow the uploads retention setting.
- **A switch for it, in Settings > Network and Hub:** "Share pasted images" (on by default). Off, a request through the tunnel for an image that is only pasted gets a 404, and the hub's artifact lists and the image's comments hide it too. It is changed only on this computer (never through the tunnel).

### Changes

- **The "server unreachable" and "waiting for input" notices are small pills that no longer move the agent view:** they were blocks in the page flow, so each one pushed the view down when it appeared and up when it went (the unreachable one on every tunnel drop). They are now overlay pills under the terminal header, shown only after 1.5 seconds of being wanted and hidden at once, announced politely to screen readers, one at a time (the unreachable one wins).
- **Tasks and both artifact lists use the agents tab's filter pattern:** the search row has one icon that opens the Filters and Sort panel (a dot and the icon's name say when a filter is on, and the open state is kept per board or agent); the artifact lists have a second icon that opens or closes every row's details.

### Tests

- A property test that only the canonical JPEG Orientation segment survives the strip.

## v1.4.128 — Faster board search, and the first inert parts of automatic updates

### Changes

- **Board search is faster:** a search scanned every message of the board and lower-cased each whole message to compare it. An ASCII search term against ASCII text is now compared in place, with the same matches as before (a test pins that, including the Kelvin sign that folds to `k`).

### Internal

- **Automatic updates, the first parts, none of them on:** the web half (the "Relaunch to update" control, its confirmation, Settings > Updates and the failure notice) is behind a flag that is off, so nothing is shown, fetched or added to Settings; the update verifier (promote manifest, release checksums and their Sigstore bundle, the package; downgrade, replay and expiry rules) is a package that only the future `coral-update` program links, and a test holds that it is not in `coral`; and the machine half of the Coral Hub relaunch (`GET /api/system/update` at view tier and `POST /api/system/update/relaunch` at launch tier, owner only, behind an off switch) is in but has no updater to call. Nothing in this release downloads, stages or installs an update.
- Dependencies: adds github.com/sigstore/sigstore-go v1.3.0 and its modules, used only by the update verifier (not linked into coral).
- A temporary workflow (`sigstore-cert-probe`, started by hand) signs a dummy file and prints the certificate Sigstore issues to a manually started run; it will be removed in a later release.

### Tests

- The tests that change timings under the input gate's loop are race-safe, and the real v1.4.120 release bundle's fixtures are kept byte for byte on a Windows checkout.

## v1.4.127 — Shorter secrets after a key-named header are redacted, the light theme is readable at 320 px, and the Artifacts tabs have a cleaner layout

### Security

- **A value of 8 to 15 characters after a key-named header is now redacted on every path:** `X-Service-Key: <10 characters>` and `X-Custom-Auth-Token: <12 characters>` stayed readable because the header rule needed 16 or more. It now takes 8 or more; from 8 to 15 it applies only when the value is not a plain word, not a template and not one repeated character (`X-Api-Key: required` is a sentence, `X-Api-Key: Zq8Lm2Xv9` is a key). The header names are the listed ones plus any `X-` header whose last word is key, token or secret.

### Changes

- **The Artifacts tabs:** *Details* is an (i) icon in the Actions column (named, 44 px on touch, and the rows you opened stay open across a refresh), *Refresh* is a reload icon, and an agent on a team gets the same search, Filters and Sort as Team Artifacts, kept per agent.

### Fixes

- **The light theme at 320 px:** code in a chat bubble was 1.6 to 2.1:1 on its chip (the highlight theme was the dark one) and now uses a light palette; a file path in a chat bubble and a link in a board post were 4.0 and 4.4:1 and now use a darker link colour in the light theme; the Yes button of the confirm dialog was 4.2:1 (an unscoped rule reached every dialog); and the copy button on a code block is a 44 px target on a phone.

### Internal

- Dead code removed: four functions and one test-only wrapper that nothing calls, found by the `deadcode` tool on both tag sets.
- A script, `tests/release/smoke_macos.sh`, runs the released macOS app from its DMG in an isolated folder with a stand-in agent: the digest and signature, the loopback bind, analytics off honoured by the tray, a comment, the multi-select question and the settings read. It is run after the release verification.

### Tests

- The text200 suites and the board links check follow the light-theme work (the page scan moved to a shared helper, and the link colour is a token).

## v1.4.126 — Artifact comments no longer freeze the viewer on a large file, and the comments are reachable from the keyboard

### Fixes

- **Artifact comments no longer freeze the viewer on a megabyte:** with 500 comments on a 1 MB Markdown file the viewer froze for about 5.9 seconds when it opened, on every click on a comment card, every posted comment and every text selection (the phone made it worse). Opening takes about 0.2 seconds now, a click 0.03, a post 0.13 and a selection 0.14 (measured on a 1 MB file; a 5,000-line code file opens in 0.08 seconds).
- **Accessibility of the new comments, the question card and the full-screen preview:** a shut comments sheet on a phone was still in the Tab order and the screen reader's tree (it is now inert, as is the page behind an open sheet); a comment is reachable from the document and the document from its comment (a "Show in text" button, and `]` and `[` step through the comments); posted, saved, resolved, reopened and deleted comments are announced; and keyboard focus is kept after an action instead of dropping to the page, so Escape still closes the viewer.
- **A late event from an earlier tool no longer ends the wait for the one still pending:** a stale tool event could make a real question look closed and the answer be refused.

### Internal

- Routes write errors and read request bodies through one helper each (`writeError`, `decodeJSON`), with a check that fails on a new hand-written error literal or a raw JSON decoder; no response changes (the same bytes, status and content type).

## v1.4.125 — A numbered-menu draft is not answered as a prompt, and a team reset keeps its permission mode

### Fixes

- **A draft that looks like a numbered menu is no longer treated as a question:** text typed into an agent's input box that reads like `1. ... 2. ...` was taken for the agent's own menu, so an answer from the dashboard or an auto-accept could type a digit into your draft, and for Codex and arrow-menu agents press Enter, which sent the draft. A draft is not a menu, and a hook-reporting agent (Claude) is now answered only while its hooks say a request may be open. A census checks that every sender into a terminal (the input gate, a human, a screen check with a proof test, a launch, auto-accept and the primitives) declares its guard.
- **A team launched with a raw permission flag at team level lost it on a team reset:** `--full-auto` or `--permission-mode acceptEdits` (or plan) was not read back from a member's expanded flags, so the reset relaunched with the default mode. The mode the launch derived is now stored and the reset relaunches with it.
- **A Codex agent no longer ends at a bare shell when a sandbox or approval flag is spelled another way:** `-s X`, `--sandbox=X`, `-a X`, `--ask-for-approval X`, `--approval-mode X` and the `=` forms all collapse to one of each (the preset's when a capabilities preset applied, otherwise the last), since Codex refuses a second one.
- **With the keyboard up on a phone the comments sheet's Open, Resolved and All row stays above the composer:** at 320 px the filter row sat half under it. The sheet's header folds to one row while the keyboard is open.

### Tests

- The double-restart test fails in seconds instead of hanging to the package timeout when the session's turn is missing.

## v1.4.124 — A woken Codex agent no longer ends at a bare shell, and six more shapes of secret are redacted for Coral Hub

### Security

- **More ways a value after a name is redacted for Coral Hub:** an HTML-entity-encoded `=` or `:` (`password&#61;VALUE`, `&#x3d;`, `&equals;`, zero-padded or escaped again; `&#58;`, `&#x3a;`, `&colon;`), `npm config set //registry.npmjs.org/:_authToken VALUE` with a space and no equals (`_auth`, `_password`, yarn's `npmAuthToken` and pnpm too), an `.npmrc` line `_auth=BASE64` or `//host/:_password=BASE64` anywhere in the text, and a `pwd` name (`pwd=VALUE`, `DB_PWD=`; a value that is a path is left alone). Each had 0 of 2,000 samples survive in the hub's test harness.

### Fixes

- **A Codex agent woken after an earlier restart or resume ended at a bare shell:** in mode auto, dontAsk, acceptEdits or plan the wake typed `--sandbox workspace-write -a never` twice, and Codex refuses either flag twice. The launch command now keeps the last `--sandbox` and the last `-a`, so the mode's replaces a stored one. A capabilities preset's sandbox and approval flags also win over a stored flag.
- **Waking a Codex agent that never completed a turn starts a new one instead of resuming nothing:** Codex saves a session only after a turn, so `codex resume <id>` ended at "No saved session found" and a bare shell. A wake of such an agent now starts a new session (the permission mode and flags as before); an explicit Resume request is left as it is.
- **An agy agent whose quota is used up shows as blocked, with its reset time,** instead of idle, and agy's feedback survey ("How's the CLI experience so far?") is dismissed with `0` like Claude's, only when it is the last thing on screen and the input box is empty.

### Tests

- The two agy tests wait for the held-send loop to end before their cleanup restores the timings (a data race under `-race`).

## v1.4.123 — A multi-select question has a Submit button, and a secret in a key-named header is redacted whatever it looks like

### Security

- **A value after an `X-Api-Key`-style header was shown unredacted when it held a common word:** the redaction library's generic rule drops a value that holds a word such as `house`, `table`, `apple` or `false`, so `X-Api-Key: <32 characters with one of them in>` was shown whole (200 of 200 in the test), on every path. Coral now has its own rule for a value of 16 characters or more after a key-named HTTP header (`X-Api-Key`, `Api-Key`, `X-Auth-Token`, `X-Access-Token`, `X-Api-Token`, `X-Secret-Key`, `X-Auth-Key`, `Private-Token`; as a header line, a `curl -H` or a JSON header map), whatever it looks like. The library's suppression now only drops the placeholder itself.

### Fixes

- **A multi-select question in the "Claude is asking" card now has a Submit button:** the card showed the options as plain buttons with the marks inside their labels, had no Submit, and reported every toggle as "prompt still open". It now shows checkboxes read from the terminal (never flipped on the card alone) and a primary Submit (or Next when more questions follow); a toggle answers once the terminal shows the box flipped, Submit is ok only when the prompt goes, and a single-select card is unchanged.
- **Show branches and Show agent type work in team groups on the agent list:** the type line was left out of compact rows and a branch was not shown for every row that had one. The Display panel says how many agents have no branch or model on record.
- **A review task can no longer be reassigned, which left the new assignee refused "no open review":** the refusal names the way (`coral-board task review <id> --to ...`, which moves the reviewer and tells the old one they are off it). The availability report no longer counts a parked unassigned task as work to hand out.

### Changes

- **`GET /api/board/projects` returns each board's `board_id` and `created_at`,** so Coral Hub can tell a deleted and re-created board apart.

### Internal

- Every setting is declared once in a typed registry (type, default, limits, who writes it, when a change applies), and every reader goes through it; an undeclared key panics, and tests pin every opt-in switch to the word `true`. No setting changes meaning.

### Tests

- The header-key census allows the gate test's own example line.

## v1.4.122 — Double-clicking Restart starts one agent, and the hub's keyword rules cover ten more shapes at about 90 percent of the old speed

### Security

- **Ten more shapes of a value after a name are redacted for Coral Hub:** a URL-encoded JSON pair (`?cfg=%7B%22api_key%22%3A%22VALUE%22%7D`, once or twice encoded), an XML element (`<api_key>VALUE</api_key>`), an HTML form field, quote entities escaped again (`&amp;quot;`) or without the semicolon, `password%3D%20VALUE`, and `curl -b` and `--cookie` pairs, including coral-hub's four: `curl -bNAME=VALUE` and `-b"NAME=VALUE"` with no space, a URL-encoded key name (`api%5Fkey`), and `docker login -p VALUE`. Each had 0 of 2,000 samples survive in the hub's test harness.
- **The hub's keyword rules are faster:** they now read only each name and its value, not the kilobytes after them, so redaction on source-code-heavy terminal text is about 90 percent of the speed of the local rules (it was about 70 percent) and a dense 2 MiB reply no longer reaches the redaction time budget. A reply too large to check within the budget is still refused rather than shown.
- **A file or message refused before it is sent to another Coral through Coral Hub now says why:** the line (in the file, or in the re-encoded copy that found it) and the rule, never any part of the secret. A value that is a code reference (`config.PostHogKey`) is no longer taken for a secret, but a lowercase dotted passphrase after a password or secret name (`password = correct.horse.battery.staple`) is still refused.

### Fixes

- **Double-clicking Restart on 121 started two agents, and they piled up:** a restart or resume now runs one at a time per session (the second request gets 409), and Launch, Launch team, Wake and Wake all also run to their end when the request that asked for them is dropped, with a bound that grows with the number of sessions.
- **Held input is counted while it is being retried:** the sessions list showed no held input for about a second while the retry loop had a held send off the queue to try it again.
- **On a phone, touch targets are 44 px and overlays close with Back from the top:** the preview's tab close, the viewer's Close, the comments filter, the capped branch chip and the prompt inspection rows were smaller; the viewer's title is readable at 320 px, the comments sheet fits the keyboard, and Back closes the topmost overlay first.

### Tests

- A nightly `go-shuffle` job on main (`go test -shuffle` with the seed printed, `-count=3`, every package, both tag sets). The tmux test no longer ends with a timer goroutine still running under a shuffled run; the telemetry end-to-end test builds with the real module cache and retries only a refused module download; the saved-search browser check waits for its option.

## v1.4.121 — Comments on artifacts, and an agent that is restarted or woken is no longer left at a bare shell

### Features

- **Comments on a text artifact (phase 1):** the artifact viewer has a side panel (a bottom sheet on a phone) of its threads. Comment on a selected quote or on the whole document, reply one level, edit and delete your own, resolve and reopen, and the owner can lock comments per artifact or board. A comment belongs to that exact artifact and goes when the artifact is deleted or pruned. One permission policy decides every request; agents read the comments made on boards they are on; Coral Hub viewers can read comments but not write them yet (`artifact_comments_hub_write` is off). Limits: 8 KB body, 2 KB quote, 500 comments per artifact, 20 writes a minute per author. Comment bodies and quotes are redacted when they are saved (api_key=, password=, tokens and the Hub's keyword rules, with or without a hub).

### Security

- **More ways a value after a name leaked through the hub's keyword rules are closed:** a name followed by a URL-encoded colon and space (`password%3A%20VALUE`, once or twice encoded) and a name and value quoted with HTML entities (`&quot;api_key&quot;: &quot;VALUE&quot;`) were kept; both are now redacted. A chat page's trimmed tool fields and a saved snippet reached through the tunnel, what a hub viewer's team-artifact query matches on, and the scan of a file or message sent to another Coral through Coral Hub now all use the hub's rules too, governed by the one switch (`hub_redact_keywords` and `CORAL_REDACT_KEYWORDS`). A file or message sent to another Coral through Coral Hub is now refused when it holds a keyword and a value (about 1 percent more files than before, mostly test fixtures).

### Fixes

- **A restarted, resumed or woken agent is no longer left at a bare shell:** the dashboard drops a restart request (a reload, a tab change, a closed tab) after the old terminal is touched, and the rest of the restart then failed on that request's context: 22 of 30 restarts on the previous release left a bare shell, 7 of them with the launch command typed and unsubmitted, and the next board nudge was appended to it and run. Restart, resume and wake now run to their end whatever happens to the request (the old agent or the new one, never between). A launch line is also never left typed in a shell, and a restart's new shell is looked up again.
- **Agents keep their launch permission mode:** bypass permissions and presets survive restart, resume, wake, team launch and reset and model changes, and an agent comes back with the permission mode it was launched with instead of the global default (an invalid stored mode is dropped). An agent launched with `agy` was the case reported.
- **`coral-board post` and `send` refuse a literal `$(...)` or backtick command substitution that the shell did not expand,** instead of posting it as text.

### Tests

- Test-only: a soak test of 200 hub connect, stream and drop cycles; the heap_growth browser check starts its watchdog before Chrome connects; two package-level timings are atomic so a goroutine left by one test no longer races the next (the nightly `-race` failure); six tests no longer fail when `go test -count=N` runs them again in one process; the agy tests use a stub agent on PATH and one test no longer reads its two sessions' commands in random order.

## v1.4.120 — Secrets are redacted by keyword on the way to Coral Hub, a library filter bug that leaked secrets is fixed, and the tray respects Usage analytics

### Security

- **A secret containing the word "false" was shown unredacted, on every path:** the redaction library's global filter, `(?i)^true|false|null$`, was meant as "the secret is true, false or null" but read as "starts with true, contains false, or ends with null", so any finding that held `false` in any case (a random private key body does about 1 time in 3,600), began with `true` or ended with `null` was thrown away as a false positive, for every rule. The filter is now anchored when loaded, a library whose filter still has the unanchored form is refused, and a PEM private key block is also redacted by Coral's own rule from its header line to its end line, whatever its base64 holds.
- **Replies to Coral Hub redact the value after a recognised name, whatever it looks like:** `api_key`, `token`, `password`, a cookie, a `--password` flag and the like, in JSON inside a JSON string, URL-encoded, one-word names (`PGPASSWORD`, `MYSQL_PWD`), command-line arguments, `Cookie` and `Set-Cookie`, and full-width `=` and `:`. These rules apply to the cloud tunnel only (every local view is as it was), are on by default whenever connected, and can be turned off with the setting `hub_redact_keywords` or `CORAL_REDACT_KEYWORDS=off`. A cheap gate keeps their cost down on large replies. A reply too large to check within the redaction time budget is refused rather than shown, as before; keyword-dense text reaches that sooner.
- **The tray no longer sends usage events when Usage analytics are off:** the tray process started its own install and app-opened tracking and never read the saved setting (only `CORAL_TELEMETRY_DISABLED` stopped it). It now sends only after the server it supervises reports that Usage analytics are on. The telemetry and privacy docs name what each part sends.

### Fixes

- **A parked task is never offered or claimed until it is unparked:** `coral-board task claim` with no id handed an idle agent a parked task, and naming one worked too. All four reads now skip it and say it is parked.
- **A second tap closes the branch name's box on a phone:** the full-name box on an agent's branch chip is a hover effect only for a pointer that is not a finger, so it no longer sticks after a tap.

### Internal

- Three dashboard modules split out of team_dialogs.js and message_board.js (the AI team wizard, the team form's templates, board export), moves only.

### Tests

- A Windows checkout's CRLF no longer empties the docs table the entropy test reads; the dashboard censuses follow the moves.

## v1.4.119 — The artifact preview can take the whole screen on a phone

### Fixes

- **In the light theme, table row lines and hover highlights were invisible and error and warning text too pale:** they now use the theme's own colours (the six CSS variables nothing declared are declared, or replaced by the token that exists).

### Changes

- **On a phone the artifact preview can take the whole screen:** it sat in the bottom 280 px of the side panel with no way to enlarge it. The tab bar's 44 px toggle now shows on a phone and makes the preview a full-screen layer inside the safe-area insets, with a Back button, Escape, and the phone's back gesture to leave it, returning to the sheet with focus on the toggle. The desktop is unchanged.
- **Dead CSS is removed** (six unused custom properties, a repeated rule, an overridden display, an empty rule); computed styles do not change.
- **Settings dialog requests share one helper (`requestJson`) and the placeholder lines in the cost tables and message board one `emptyState` helper;** the same messages and markup as before.

### Tests

- Browser suites for the full-screen preview, the shared helpers, long scrolled row and team menus, and a dashboard probe for 10, 50 and 150 agents (opt-in); new census checks for undeclared CSS variables and for new `style.display` show/hide writes.
- The local-navigation suite no longer fails about 1 run in 10 (it read the panel's frame after the dashboard's guard had already removed it); a test and a line in the board agent docs say that a verdict or a plain assigned task needs no claim while the agent holds another task.

## v1.4.118 — A calmer dashboard: no expanding rings, and nothing animates with reduced motion

### Changes

- **No expanding ring around the status dots:** the working, waiting and stuck dots now only flash (by opacity), in the agent list, the status dots elsewhere and the team avatars; the row dot flashes by itself. Idle, sleeping and ready dots stay still.
- **With reduced motion set on the computer, nothing on the dashboard animates:** spinners are a static ring, pulsing dots rest, the shimmer stops, a toast appears where it stays, and smooth scrolls jump.
- **The agent header no longer repeats the model** (the model chip and the agent row's tooltip carry it). The git branch is cut short with an ellipsis when it is long (a worktree's branch can be 60 characters or more); hover it, move to it with Tab or tap it and the whole name opens in place.
- **`coral --help` and the docs say what `--host` and `--remote` do:** the default is this computer only; a network address (or 0.0.0.0) is used only when remote access is on (Settings > Privacy, or `--remote` for one run). The getting-started and remote-access pages name `--remote` and what remote access widens.

### Tests

- A census of every listener and its bind default (a new listener or a changed default fails a test), a browser check for reduced motion, and fixes to two dashboard suites (agent_state follows the dot rule, the phone Team settings suite no longer trips the sleep check).

## v1.4.117 — Team settings opens on the phone, and an optional agent type line in the agent list

### Fixes

- **Team settings opens from the team's menu on the phone:** the side panel opened over the team page but stayed shut, so the settings were drawn at no size behind the chat. They now open on screen.

### Changes

- **A "Show agent type" option in the agent list's Display panel (sidebar and phone), off by default.** The second line under an agent's name ("Claude · Opus 5.5") is hidden as the dashboard starts; tick Show agent type next to Show branches to bring it back. The choice is kept per browser, shown in the Display summary and count as "Agent type shown" when on, and cleared by Clear.

## v1.4.116 — The team health switch moves to Settings, and "off" means off for telemetry and the health monitor

### Security

- **The health monitor and usage events now honour every word for off:** two settings that are on by default were read by comparing with the one word `false`. The team health monitor ignored the Settings dialog's own off (stored as `False`), and `telemetry_enabled` ignored `off`, `0` and `no` sent through the settings API (the dialog itself was fine for telemetry): Settings > Privacy off must be off. Both are now read as off for `false`, `0`, `no` and `off` in any case, with surrounding spaces ignored (at startup, in the privacy view and when it is saved). Other settings were checked: the rest are written by their own routes or validators, which store only `true` or `false`.

### Changes

- **The switch for team health checks (all teams) moves from Team settings to Settings > Agents,** and the Global settings tab goes. The setting itself (`board_health_monitor`, on by default) and its restart-applied behaviour are unchanged, and a team's Health check tab now says it needs the switch, with a link to Settings, when it is off.

## v1.4.115 — The local Inbox has tabs, and the row menu button is quieter

### Changes

- **The local Inbox has "Needs you" and "Saved for later" tabs, like Coral Hub's:** one panel at a time, and the tab you chose is kept across reloads.
- **The row menu button has no hover fill:** only its dots change colour, on hover and on keyboard focus (it no longer draws a box over the row).

## v1.4.114 — Coral listens on this computer only unless remote access is turned on

### Security

- **Remote access is off by default everywhere:** Coral listens on this computer only (`127.0.0.1`) unless remote access is turned on (Settings > Privacy, or `--remote`). The server's default host (`CORAL_HOST`) and the tray's `--host` are both `127.0.0.1`, so the default no longer relies on the loopback restriction alone. Turning remote access on widens that default to every interface; a host that was asked for (`--host`, `CORAL_HOST`) is kept as given, the tray passes `--host` only when it was given one, and settings that cannot be read never widen the bind. Anyone who already had remote access on keeps it, and Coral Hub is unaffected. The first-launch question defaults to no, no launcher, installer, service or workflow passes `--remote`, and the Mobile QR dialog with access off now names Coral Hub as the other way in.

### Other changes

- **Docs:** the README's remote-access example gains the `--remote` it needs.

## v1.4.113 — Separate model and thinking chips, a quieter team health icon and attention rows, and Cost by provider, model and level

### Changes

- **The chat bar has two chips, one for the model and one for the thinking level:** the single chip ("Claude · Opus 5.5 · High") opened one long menu with every model above the levels. The model chip ("Claude · Opus 5.5") opens a menu of models, the thinking chip ("High") a menu of the levels the model takes (none for a CLI that reports no level). Each keeps what the chip did before: a live switch for Claude, "Apply with restart" for other CLIs, the pending or failed state on the chip that changed, one shared note line, Home, End, Escape and Tab with focus back on its own chip, and the names "Model: ..." and "Thinking level: ...". One menu is open at a time.
- **The Cost view has By Provider, By Model and By Thinking Level tables:** By Provider is the totals per agent type, By Model is today's table, and By Thinking Level is by provider, model and level (with the thinking tokens apart from every total); a model that was not recorded is "Not reported" and sorts last. The "Not reported" remainder is now per provider and model. `GET /api/token-usage/summary`'s `by_thinking` rows gain `model`; a database from an earlier release is rebuilt once in one transaction when it is opened, every row kept under the empty model.
- **The team health icon is quiet:** on is the accent-coloured pulse icon alone (no fill, ring or border, 44 px on a phone), and off has no icon at all (turn the check on from the team's info button, Team settings, Health check, or the Agent Team dialog, as before).
- **An agent row's attention states are quiet:** there is no amber or red wash across a Needs input or Stuck row, no expanding ring around its pill, and no amber border or glow on the phone's card; the state pill has the neutral pill's surface, weight and hairline with only its text warm, and the words still say which state it is.
- **The row menu button blends with the row it sits on:** its hover fill is a faint tint of its own text colour, so the selected row's blue tile shows through, instead of an opaque grey box.

### Fixes

- **The team health icon no longer blinks in and out:** a live-list tick whose database read failed reported every team's health check as off (an empty map reads as "the default, off"), and the next tick as on again. The list now keeps the last state it read.

### Other changes

- **Docs:** the "By Thinking Level" screenshot in the guides shows provider, model and level.

## v1.4.112 — An agent stuck behind Claude's session survey is freed, the model chip sits with Send, and a "Show branches" option

### Fixes

- **Claude's session survey no longer holds every message back:** Claude Code sometimes draws a session survey above its input box ("How is Claude doing this session? (optional)", choices "1: Bad 2: Fine 3: Good 0: Dismiss"). Coral's gate held every send behind it as a dialog, so board messages and nudges waited for a person who never answered and the agent sat stuck. When a held send is behind a dialog and the survey is the only thing open (not a permission prompt, not the bug-report box, not quoted in the scrollback), Coral now sends one `0` (Dismiss; never `1`, `2` or `3`, Coral rates nothing for you), waits up to 4 s for the survey to be gone, and only then delivers the held send, with the gate's usual checks still run first; a survey that does not go is tried once per 30 s per session and the send stays held. Coral never sends the `0` while the input box holds text that may be yours (a `0` typed into a draft would join it); the held send then waits until you send or clear it. This is complete on the tmux backend; on the PTY backend (the default on Windows) a box showing Claude's faint suggestion counts as a draft, so the survey is dismissed there only from an empty box.
- **The model chip no longer covers the chat input:** it moves into the composer's buttons row, level with Stop and Send, with its note as a small line under that row (on a phone it stays the row under the input).

### Changes

- **A "Show branches" option in the agent list's Display panel, off by default:** the worktree line (icon, branch and git state) under every agent on its own worktree is hidden unless you tick it (it is kept in this browser, like Group by server). The branch stays in the row's tooltip and the agent's header either way; with it on, the summary line and the Display button's count say "Branches shown", and Clear turns it off.

### Other changes

- **Tests:** the `heap_growth` browser suite names the step it hangs in, says what the page is doing, and re-runs once in a fresh process on a first stall, failing by name on a second (it timed out about one run in four on GitHub's runners, with the cause not proven).

## v1.4.111 — Release checks need a pinned cosign, and a phone Settings switch lines up

### Fixes

- **On a phone a Settings switch's toggle is level with the row's (i) button,** not about 17 px above it: the toggle's cell had a label's generic bottom margin inside the box that gets centred. One-line rows are 6 px shorter than before; two-line rows keep their height.

### Other changes

- **Release checks need a pinned cosign:** `verify-release.sh` used to skip its Sigstore bundle rows when cosign was not installed, and every verification of v1.4.105 to v1.4.110 ran on a host without it (the bundles were checked by hand and pass). Now `after-release.sh` and `finish-release.sh` require the pinned cosign v2.4.1 (its sha256 is pinned in the repo for darwin and linux, amd64 and arm64, and a distro build is refused), `tools/devops/install-cosign.sh` installs it only when its checksum equals the pin, and a missing cosign or no bundle to check is a failed row, not a skip.
- **Internals:** `pkg/redact/redact.go` and `routes/identity_observer.go` are split by concern, moves only.
- **Tests:** `settings_apply_oracle` records the terminal's assignments just before `applySettings`, so a layout sync that lands while the dialog opens can no longer add a stray `fontSize` (it failed about 2 runs in 4).

## v1.4.110 — Agent panels and app links can no longer navigate away or run files, a bounded hub, and the tail-poll regression fixed

### Security

- **An agent's panel can no longer navigate itself and send what you type out in an address:** on a local Coral an Agent UI panel was an iframe loaded by URL. That isolates it from the dashboard, but a panel could still navigate itself (`location.href`, a meta refresh or a link) and CSP has no directive for that, so something the Operator typed into a form the panel drew could leave in an address. A panel is now read as text and handed to the sandboxed host frame (as the hub already did): the child frame has no navigation and no requests, and the card says the content is the agent's.
- **The app window opens only plain web links, and judges a link by its parsed address:** the app window passed any link straight to the system's opener, so a file path or `file:` URL an agent wrote in a chat message (`/tmp/x.command`) ran when the Operator clicked it, a custom scheme started its handler, and a leading `-` was an option. The opener now takes only `http` and `https`. The link handler also judged a link by a string prefix, which `http://localhost:8420.evil.example/` and `http://localhost:8420@evil.example/` passed, so the window itself navigated to such a page, which then had the native bindings; it now uses the parsed address.
- **The tray's "Shutdown, Kill Agents and Stop Server" reaches every agent:** a folder name with `?`, `#`, `/`, a control byte or a bad escape changed the kill request's route or made it unbuildable (a nil-pointer panic before the server was stopped, so the menu item silently did nothing). The name is now escaped and an unbuildable request skips only that agent.
- **The hub keeps a bounded amount of each remote machine:** at most 5000 sessions, a row over 256 KiB as the machine sent it is not kept, and the rows kept never pass 16 MiB in all per machine (a row stays raw until measured, so an oversize one is never decoded into the hub's memory); a feed message no longer costs a re-encoding of everything held. An agent's token sees no remote machine.

### Fixes

- **A v1.4.108 change made each live-list poll of an active agent about 35 us slower** (58 us to 93 us in a benchmark of a session with thousands of calls); it is back to baseline: the pending tool calls are read when a checkpoint is taken instead of on every poll, and a read no longer asks the file for its size twice.
- **The Restart form says why it shows Default** when the agent last ran at a level other than the stored one ("The agent last ran at Max (changed inside the CLI)").

### Changes

- **The token poller is lighter:** it records one poll's Claude and Pi calls in a transaction instead of one transaction per call, so a first start's usage scan of a large transcript store takes about 0.4 s of CPU instead of 8.6 s.

### Other changes

- **Public docs:** a "Using the dashboard" page and what an agent can and cannot read, with five screenshots, in the site's guides.
- **Internals:** the History page's number and filter group leave `app.js`, and `ptymanager/automated_input.go` and `tunnel/local.go` are split by concern, moves only.
- **CI:** when the browser gate finds a flaky suite, it now runs that suite 10 times on the candidate and on `main` and writes both rates in the run's summary (a human still decides).

## v1.4.109 — A hub redirect closed, workflow output capped, and the docs say what an agent may read

### Security

- **A connector's redirect can no longer send a hub user to another site:** the hub proxy's response filter let through a `Location` of slash, backslash and a host (`/\evil.example/x`), which Go reads as a path with no host and a browser reads as `//evil.example/x`, so a compromised connector could redirect a hub user's browser away. A `Location` now survives only when every value is a path on the hub's own origin (one leading `/`, not followed by `/` or a backslash, no backslash, control character or space, no scheme, host or user); a second `Location` header is checked too. The hub proxy also gets fuzz targets for this filter, for `Content-Disposition` file names and for the `api_key` strip, and one whose oracle is a browser's reading of a redirect.
- **A workflow step's output is capped:** a shell or agent `--print` step wrote its output straight to `stdout.txt` and `stderr.txt` with no bound, so a runaway command (`yes`) filled the disk until its timeout (300 s by default, up to a day). Each stream now keeps its first 64 MiB, the file ends with a line saying where it was cut, and the step's result carries a note; the step passes or fails as before, and a process the step leaves running keeps writing (capped, never killed) while the step ends within about 3 s of its own process when something it started still holds its output.

### Other changes

- **The docs say which reads an agent's token is refused:** where each route is described (workflows, scheduled jobs, session history, sessions, token usage, board, teams and auth), the docs now say that an agent's token gets 403 `forbidden_for_agent` on the Operator's reads and `forbidden_other_session` on another board's session, and what it may read instead (its own board's tasks, `coral-agent history`, its own session and its board mates); `coral-board`'s usage marks `workflow list`, `status` and `runs` Operator only.
- **Internals:** `message_board.js`, `live_chat.js`, `utils.js`, `app.js` and `tasks.js` are split further by concern, moves only.
- **Tests:** the Chats page's URL test checks that a button remembers its page and that a new search starts again at page 1.

## v1.4.108 — Agent tokens see other boards as a roster only, a faster server, and the Cost tables on a phone

### Security

- **An agent's token sees other boards' sessions as a roster, not what they work on:** `GET /api/sessions/live` and the `/ws/coral` feed carried every session's prompt, first prompt, summary, waiting text, branch, working directory, log path and git state to any agent token. An agent's own row and its board mates' rows are unchanged; any other board's row (and a session on no board) is reduced to an allow-list of roster fields (name, type, session id, display name, board and role, status and state flags, model, thinking level and the like), so a free-text field added later stays private until it is listed. `GET /api/sessions/{id}/resolve` does the same for a session outside the agent's board. This was found by QA's release check of v1.4.105.
- **More reads across every board are the Operator's:** the per-session token usage list and the usage by branch, the goal generator's metrics, and the eleven workflow, scheduled-job and run `GET` routes (they held shell commands and prompts that often hold a credential; an agent's token now gets 403 `forbidden_for_agent`, so agents can no longer list workflows, runs or jobs through `coral-board`, and get a clear refusal; the Operator's command line and the dashboard are unchanged). For an agent's token the usage summary leaves out its per-session `by_agent` rows and keeps the totals. Editing a scheduled job is refused what creating one refuses.
- **Hooks and agent tools bound what they read:** a hook's stdin is capped at 32 MiB (a longer payload is read and discarded, never held, so the CLI still writing it gets no `EPIPE`), a server reply at 8 MiB, a debug-log line at 2 KiB; no credential is printed in the server URL of an error or debug line; and every program names a session's board state file the same safe way.
- **Sign-in:** the password throttle counts an IPv6 network once (as the failed-key limit already did); a sign-in link or the key form ends the session it replaces; a password sign-in that began before a password change cannot create a session after it; and a request authenticated with `?api_key` makes a browser session only for a browser navigation that holds none (a script polling with the key in its URL used to grow the session list without bound).

### Fixes

- **The Restart form shows the level and model the agent really runs** (the bug dates from the live switch in v1.4.104): it showed Default for the thinking level and, after a live switch from the chip, the model the agent ran when it was chosen; a restart confirmed as it stood would have started on that old model.
- **The built-in Claude model list names the current models** (Opus 5.5, Sonnet 5.5, Haiku 5.5 and Fable 5.1) when the CLI gives no list, and a running model the list lacks shows as itself instead of under "Other...". The two `[STARTUP]` lines lost their diagnostic marker.

### Changes

- **The server does less work:** a transcript chunk is read into one buffer (the biggest allocation site of a server whose chats page back through their transcripts), the token poller decodes only the Claude lines that can hold usage, and an image's place in a transcript is remembered so a chat of many images no longer rescans the transcript for each one. Older chat pages are faster: a read checkpoints every 1 MiB inside a chunk, so paging back through a long chat reads far less (p95 163 ms to 90 ms in the scale test). Steady-state server CPU is down 32% and allocations 51% in the scale test.
- **The Cost tables on a phone:** the sort buttons and filter icons are 44 px on a coarse pointer, Cost and Share come right after the columns that name the row, the keyboard stays on a heading after a sort, and the By Thinking Level filter buttons filter; on a phone each Settings (i) button sits beside its label.

### Other changes

- **Internals:** `tmux/client.go`, `background/workflow_runner.go` and `startup.go` are split by concern, moves only.
- **Tests and CI:** the heap guard also covers terminals, resizing, the board and History, and failed polls give up their response bodies; `TestProcUsageAgreesWithGopsutil` uses a relative tolerance; a data race in a ptymanager test is fixed; several browser suites (`terminal_switch`, `chat_jump_latest`, `list_bars_themes`, `saved_search`, `saved_for_later`) clean up after an early failure.

## v1.4.107 — Pasted Claude and Codex images show as thumbnails, a "Using the Dashboard" help page, and an accessibility pass

### Fixes

- **A busy machine behind Coral Hub is "busy", not "unreachable":** a terminal or dashboard upgrade that Coral refused with 429 (the socket caps) arrived at the hub as an upstream error, so the machine was marked unreachable and the browser got 502 "remote unreachable". It is now a 503 "remote busy" with the cap's code (the dashboard already backs off on it); the machine stays reachable and the `Retry-After` it gave is passed on (whole seconds, 1 to 60, 5 when none). `GET /api/agents/models?type=...` now goes through the tunnel at Launch tier, with `type` as its only query parameter.
- **Deleting a board with agents running is refused:** the agents used to stay live in a sidebar group named with the raw board id (and Cost by Team listed that id). The route now answers 409 `board_has_running_agents`, naming how many and which, and 503 `running_agents_unknown` (deleting nothing) when the running terminals cannot be listed. The dashboard's Delete names the running agents instead of asking, shows the server's message when it refuses anyway, and no longer treats a refused delete as done.

### Changes

- **Pasted Claude and Codex images show as thumbnails in Chat:** the chip shows a thumbnail taken from the transcript's own image data, through `GET /api/sessions/live/{name}/attachment/{ref}`, where the reference is a hash of the image (it holds no path and no offset). The route finds the image in the session's own transcript, decodes it under a size cap, serves only PNG, JPEG, GIF and WebP (never SVG, HTML or text), and applies the same hardening as the upload route (`nosniff`, inline, a sandboxing CSP, private no-cache). It is local only (not served through Coral Hub), and an agent token of another board gets 403, so a hash cannot probe another session's transcript.
- **A "Using the Dashboard" help page:** a task-oriented page for the controls added since v1.4.102 (model and thinking level and the chat-bar chip, the Cost view's By Thinking Level table, the team health monitor, the Display panel, the sectioned dialogs and the image thumbnails), with five screenshots; help pages can now show screenshots from the dashboard's own `static/docs-img` folder (lower-case name, `.png`, no query).
- **An accessibility pass on the recent surfaces:** the model chip's name holds the model and level and its menu takes Home, End and Tab, with focus returning to the chip and a visible focus ring; the "switches after this reply" note is announced; pending and failed chip text reaches 4.5:1 contrast in the light theme; the Cost tables' sort headers are buttons with `aria-sort` and the filter icon is a named button.

### Other changes

- **Tests and CI:** a browser guard that the dashboard's memory (heap, DOM nodes, listeners, documents) stays bounded over hundreds of agent switches and feed bursts; the git fixtures of a board test no longer flake (a background-maintenance race with the temp-dir cleanup); `TestGetUploadDispositionNameIsPlain` runs on Windows with a name Windows can hold instead of skipping; the hub cleans a hand-written `Retry-After` in a refusal.

## v1.4.106 — Chat shows thumbnails of images, a full Content-Security-Policy on the dashboard, and an update check that stays on one host

### Security

- **The dashboard page has a full Content-Security-Policy:** `default-src 'self'`, with `connect-src` limited to the page's own origin (and its own `ws://` and `wss://` origin), `font-src 'self' data:`, `worker-src 'self'`, `frame-src 'self' http: https:` (the Linked preview's frame is the one cross-origin load the dashboard makes on purpose) and `style-src 'self' 'unsafe-inline'` (about 330 style attributes, and the HTML previews' frames inherit the policy). It was found by running the whole browser suite once with the policy report-only and once enforced.
- **The update check follows redirects only on the same host and never from https to http:** it used a client that followed up to 10 redirects anywhere; it now allows the same host and scheme and at most 5 hops (GitHub's answer for a renamed repository stays on `api.github.com`). Coral still never downloads or runs an update.
- **The tray runs only the `coral-app` installed beside it:** it used to fall back to a `PATH` search for a program to run; if the app is not next to the tray it now says to reinstall.
- **A long quoted credential with escapes is dropped to its closing quote:** an escaped quote inside an over-long quoted value no longer ends the drop (and a long backslash run is redacted in linear time).
- **The session scope judges a shared folder name by its exact session:** an agent's own display name or title no longer opens another board's folder name, and a tmux-style name's session id is parsed out of it.

### Changes

- **Chat image chips show a thumbnail for images uploaded through Coral** (pasted Claude and Codex images get thumbnails in the next release): a fixed 48 px preview, loaded lazily as the chips come near the screen and hidden on any failure. It comes from `GET /api/upload/{name}`, a local-only Operator route (an agent's token is refused; not served through Coral Hub; the type is what the bytes are: PNG, JPEG, GIF or WebP, never SVG; `nosniff`, `private, no-cache`, and a name over 255 bytes is a 400).
- **Light-theme dialog fields and the team card look right:** the launch and agent-config fields were filled with black at 18% and the team card with black at 8%, both written for the dark panels, which read as a grey wash on light; two tokens (`--field-fill`, `--inset-fill`) keep the dark values and give light the page's own field colour and a faint inset. On a phone the model chip has 8 px below it instead of sitting on the screen's bottom edge.

### Other changes

- **The agent list does less work per message:** a feed message writes only what changed (a write of the same value is still a mutation), and `escapeHtml` skips strings with nothing to escape (DOM mutation records outside the chat in a feed burst at 4x CPU went from 2913 to 464).
- **Internals:** the test-only window functions and exports moved to `test_hooks.js` behind `window.__coralTest`; the hooks are installed only in a dev build started with `CORAL_TEST_HOOKS=1`, and a release build never has them.
- **Tests and CI:** tests for 20 security decisions no test covered (25 mutants killed): foreign-origin password and Hub-link writes, over-long passwords, the upload and saved-message caps and others; `tools/verify-release.sh` checks that the tap's cask carries the DMG's sha256 from `SHA256SUMS.txt`; the devops README makes the GitHub `go-tests` the Linux go test proof (about 6 minutes, in agreement with the epoch host twice) and the epoch run the fallback.

## v1.4.105 — An agent's token sees only its own session and board, a redactor leak closed, and WebSocket limits

### Security

- **An agent's token reads its own session and board, not every session:** every route about one session (chat, terminal capture, files, file content, diffs, events, history, subagents, resolve-path and usage turns: 33 routes, plus deleting a session's events) used to answer an agent's token for any session, by name or id, on any board. The target must now be the agent itself, a session of its resume lineage, or a session on the same board (refused with 403 `forbidden_other_session`; a lookup that fails is a 503, never let through). The Operator, the dashboard and the command line are unchanged, and none of the agent tools needed anything that is now refused.
- **Six reads across every session, board and team are the Operator's:** `GET /api/sessions/history`, `GET /api/sessions/history/search`, `GET /api/board/tasks`, the two team knowledge reads and `GET /api/system/identity-report`. No agent tool calls them.
- **An agent's WebSocket gets the feed but no notifications:** `/ws/coral` gave every connection the Operator's UI notifications and marked them delivered once written, so an agent that connected while no dashboard was open read the pending alerts (an agent waiting for approval, say) and kept the next dashboard from showing them. There are also per-process caps on open sockets (512 dashboard, 256 terminal; an upgrade over the cap is a 429 with the action-limit mark) and the terminal socket's client-frame limit is set explicitly to 32 KiB (a bigger frame closes it with 1009).
- **A long quoted credential no longer shows its tail:** a quoted value longer than the redactor's reach (about 500 bytes) was redacted to there, and what followed a space or a control character after it showed. The whole value is now dropped up to its closing quote, as the held-across-writes path already did.

### Fixes

- **The launch dialog stays dimmed while the Servers list refreshes:** the cached list was emptied, so the dialog un-dimmed for a moment and a click then reached a hub that refused it; the last answer now stands until the new one arrives, and the controls scan no longer clears the dimming of the dialog's own buttons.
- **A model's display name is bounded:** a model with no display name took its value (up to 128 characters) as its name past the 80-character cap, and a name cut at the cap could end in a space.

### Other changes

- **Internals:** `routes/system.go`, `sessions_launch.go`, `sessions_files.go`, `jsonl/reader.go` and `store/sessions.go` are split by concern, moves only.
- **Tests and CI:** fuzz targets for the parsers of untrusted input (query parsers, the board route, the redactor, the model lists, the transcript cursor), run for 10 minutes each with no crashers after the two fixes above; the race detector runs in CI (the five high-risk packages per release batch, every package nightly); the relay oracle waits for the hub's hook instead of 20 ms; `saved_sidebar_close` no longer has a focus move left to take its field's focus; the browser gate splits `terminal_switch` into three suites so the shards run it side by side, and its part durations are refreshed from the last gate; the epoch gate script gives `go test` a 45 minute timeout.

## v1.4.104 — Choose a thinking level, switch model and level from the chat bar, and a quicker dashboard

### Changes

- **Per-agent thinking level (reasoning effort):** the Launch, Restart and team-agent forms have a Thinking select beside the Model (Default first, narrowed to the levels the chosen model takes), and Settings has a default for it. Claude takes low, medium, high, xhigh and max (passed to the CLI as `--effort`); other agent types are added as each is checked. A restart keeps the agent's level unless you change it, and a resume and a team reset start agents with the level they had.
- **A chip by the composer says which model and thinking level the agent runs, and switches them:** on Claude, choosing another model or level types the CLI's own `/model` or `/effort` once the agent is between turns (mid-turn it says "Switches after this reply"), and the chip changes only when the transcript confirms it; if the CLI refuses, the chip shows the CLI's own message. For a CLI with no live switch it says "Apply with restart" and opens the Restart dialog with the choice filled in. The switch is Operator-only (`POST /api/sessions/live/{name}/model`; an agent token gets 403).
- **Rows and the info panel show the level the agent really ran at,** as its own transcript records it (Claude's and Codex's), next to the level it was started with.
- **Cost by thinking level:** the Cost view has a By Thinking Level table, with a "Not reported" bucket for calls whose level was not recorded (CLIs that write none, and everything before this release).
- **The dashboard is quicker:** switching between agents redraws the two rows that change instead of the whole list (the switch median went from 38 to 24 ms at a 4x slowdown), and first load no longer has a startup task over 50 ms (the longest was 55 to 69 ms at 4x).

### Fixes

- **Selecting the agent that is already selected keeps its highlight** (it used to remove it).

### Other changes

- **Internals:** `internal/board/store.go`, `routes/board.go`, `render.js` and `tasks.js` are split into cohesive files, moves only; the dashboard's Display menu, summary line and group toggle are found inside the list's headers instead of by page-wide selectors; the block-code marker comes from `crypto.getRandomValues`.
- **Tests and CI:** the Linux go test of both tag sets now also runs on GitHub (`go-tests.yml`, about 6 minutes, with the routes and tunnel packages in shards of their own) and the epoch gate script ships `.github`, no longer starts the gate under `nohup` and runs `go test` with the telemetry opt-out unset; the slowest background and routes tests run their seeds in parallel; eight browser suites wait for their layout to settle instead of a guessed time; four known timing flakes (inputlog, the tunnel throughput floor, hub_launch_access, team_health_settings) and the SIGHUP kill tests are made deterministic; `tools/test-macos-app.sh` runs in a throwaway data folder and HOME and stops only what it started; `tools/devops/after-release.sh` chains the release wait, the verification and the cask.

## v1.4.103 — Dialogs rebuilt in the Settings layout, a compact agent-list header, and a team health monitor that starts off

### Security

- **Route gates see the route Coral will run:** a name with an encoded slash (`a%2Fb`) reached its handler decoded, while the checks before routing (the operator-only guard, the body limit, the identity actor's route, the agent write budget and the board-name and open-path reads) looked at the decoded path and found no route. They now use the path the router uses. No session, team or board name has a slash today, so nothing was reachable; this closes the class.
- **One client cannot fill the Hub sign-in's open flows:** the sign-in held five open flows and refused a new one while all five were under a minute old, so five starts a minute from one address kept everyone else out. A client (an IPv4 address or an IPv6 /64) now holds at most two; its next start replaces its own oldest. Failed API keys are counted per IPv6 /64, and an IPv4-mapped address counts as its IPv4 address.
- **A Hub session's sensitive read is named from the routed path** in the Operator-actions log, so a name with an encoded slash is recorded.
- **Two caps:** a history search stops at 5000 matches and says `truncated`; Antigravity prompt watchers stop at 256 running at once.

### Fixes

- **A killed agent leaves the list at once:** Kill (an agent's menu, a folder's Kill All, a team's Kill All) used to leave a crossed-out "Ended" row to dismiss. The row is gone, with its Dismiss controls; the agent's chat stays in History. An agent that ends by itself still shows as asleep ("Terminal gone") with its Restart chip.
- **Opening an agent from a notification shows the Agents tab first:** a needs-input toast's link or a desktop notification's click selected the agent but left the Inbox (or Chats, Servers, Workflows, Jobs) highlighted and the agent list hidden.
- **Deleting a board deletes all of it:** its tasks and everything on them, reminders, waits, settings and health records are removed with its row, in one transaction (the Hub's operator-action record and Coral's health-action record are kept). A task on another board that was blocked only by one of these tasks is no longer blocked. The confirmation now says "Delete board "X" with all its messages and tasks? This cannot be undone."
- **Ending a terminal agent is quick:** it sends SIGHUP, then SIGTERM, so an interactive shell no longer holds a kill for the full 5 s grace.
- **The Tasks list keeps its Cost column** in a dock from 560 px of list width.

### Changes

- **The team health monitor starts off for new teams,** because it uses tokens and a user may not know what it does. Teams that were running it keep it (the first start stores "on" for every existing board); a team that turned it off keeps that. An icon beside each team's info button shows and switches it (a clear accent pill when on, a plain outline when off). Handing reviews to the default reviewers is now its own setting, on by default and independent of the monitor, so it works with the monitor off. The health check also reminds the orchestrator that `coral-board peek "<agent>"` shows a stuck agent's terminal.
- **Restart, New Agent and Agent Team (and the team's agent dialog) are sectioned dialogs on the Settings layout:** a section list with icons, the open section's heading and description, and a fixed footer (a selector on a narrow dialog). New Agent starts with Workspace; Agent Team opens on Members, then Board & workspace, then Defaults. A problem found on Launch or Save opens its section and marks it. The requests they send are unchanged. On a phone the team member cards' buttons are 44 px.
- **The agent list's header has one Collapse/Expand toggle, and Filters and Sort sit inside the Display expander;** shut, a summary line names the filters and the sort that are on, with Clear. Same on a phone.
- **Long chats are quicker:** code blocks render about 60% faster and typing does less work per key.
- **Controls have visible edges:** inputs, selects, buttons, search boxes, chips and the Hub's fields use one `--control-border` token that is at least 3:1 against its panel in both themes; dividers and cards keep their quiet lines.

### Other changes

- **Dead code is gone, with guards:** 58 window functions (57 hooks the guard found plus `hideTeamDetails`, which went with its popover) and 13 other functions (about 260 lines) of dashboard JavaScript that nothing called, and 33 CSS rules for classes nothing draws; `test:cloud` now fails on new dead JavaScript or CSS. Nothing a person sees changes.
- **The dashboard is faster to type and scroll in a long chat:** no page-wide lookup per keystroke or scroll, code blocks are coloured after the sanitizer, and the Display menu looks only inside its headers.
- **Tests and CI:** the Windows tests run in five shards (the longest job 283 s, was 464 s, and the whole run 470 to 600 s); the browser gate's six parts are balanced from fresh durations (the slowest 335 s, was 369 s); CDP suites remove their own message boards and the gate fails a board left behind; the slowest board and server tests, and three timing tests, no longer wait on sleeps (the board package 472 s to about 200 s on the test host); a flaky Chat comparison test is fixed.

## v1.4.102 — Agents keep their place in the list across restarts, Codex pasted images read as chips, and a weekly active-installs count

### Fixes

- **A restarted or resumed agent keeps its place in the agent list,** and so does its team: the list used to put the replacement last. The list is now served in a stable order (an agent's place is carried over from the session it replaces, and a team takes the place of the earliest of its agents), and the dashboard puts a new row where the server says. A new agent or team still comes last; a team with no saved order shows in creation order.
- **A pasted image in a Codex chat is a chip, not raw text:** Codex records the image as an `<image ...>` wrapper around a base64 data URL, and Chat showed all of it (and search indexed it). The wrapper and the data URL are now left out of the message and the search index, and each image shows as an attachment chip named like `[Image #1]`, with the file's base name only; typed text is kept.

### Changes

- **A weekly active-installs count:** `installation_active_week` is sent once per install per ISO week, with the standard properties only and no new identifying data, alongside the existing usage events. `PRIVACY_CONTROLS.md` lists it. Like every usage event, it is not sent when usage events are turned off in Settings or `CORAL_TELEMETRY_DISABLED` is set.

### Other changes

- **Our own checks no longer download a release's assets:** the release workflow keeps the checksums and signature bundles as a run artifact, and the tap update, the runtime check, `verify-release.sh`, `finish-release.sh` and the cask script read the run's artifacts and compare them with GitHub's digest of each published asset. Our own CI and test runs send no usage events.
- **Tests and CI:** a census that fails when a runner or workflow that starts Coral lacks the usage-events opt-out; an end-to-end test of a keyed build against a local capture endpoint (what is sent, and that nothing identifying is); and a check that every event the dashboard sends leaves the page as a POST to the server's tracking endpoint with only reviewed property names.

## v1.4.101 — The Preview pane resizes again, can be maximised, and the servers pill is gone

### Fixes

- **The Preview pane can always be resized:** its drag limit subtracted the side panel's width twice, so on an ordinary window the pane was stuck at its narrowest; dragging now goes as far as the room allows, a saved width is held to the room when the window shrinks, a corrupted saved width no longer opens the pane over Chat, and a width saved on a bigger display no longer squeezes Chat out on a smaller one; the handle can also be resized with the keyboard. Every way of opening the pane (a markdown or image artifact, a code or image file from the Files tab) keeps Chat visible.

### Changes

- **Maximise the Preview pane:** a toggle in the pane's header makes it cover Chat and fill the centre; the same button or Escape restores it.
- **The servers pill beside the agent list's search is removed,** with its dropdown. The Display menu stays, and the agent list's Filters still filter by machine. A server that needs an update is shown in its group header's details and in the Servers tab.

### Other changes

- **Tests and CI:** the render-time test's bound is loosened to clear Windows' slower newline handling while still failing an unbounded render.

## v1.4.100 — The Inbox speaks up, a clearer message for a damaged board, and steadier tests

### Changes

- **The Inbox tells screen readers who has just started waiting:** each agent that newly needs you is announced once ("<agent> needs you: <why>"), only while the Inbox is on screen; redraws and refreshes of the same list stay quiet.
- **A damaged message-board database gets an actionable message:** Coral still refuses to start rather than create a fresh board over the damaged one and lose its history, but the error now names the file and says it can be moved aside or restored from a backup.

### Other changes

- **Internals:** a server built without a board answers 503 "board unavailable" on the board's routes instead of crashing, and its background restores skip the board.
- **Tests and CI:** the browser gate also fails a suite that leaves rows in a shared table or on a message board that was already there, and removes the boards a suite made; five timing tests and the tunnel throughput tests are steadier under load (the URL-finder test counts work instead of timing it; the rest re-measure or compare ratios), without being weakened; the route tests are faster again.

## v1.4.99 — The bookmark always opens everything's Saved, cross-team names cleaned, and old tag colours repaired

### Security

- **Names and labels on team artifacts are plain text:** the fields an agent, the Operator or a Coral Hub user sets on a task's artifacts (name, media type, revision, digest, kind, runner, branch, remote, times and the other evidence fields), an upload's name and media type, and the origin board of a relayed cross-team message must be valid UTF-8 within a size limit, with no control characters, line separators or format characters such as direction overrides. Before, an upload's name could carry a terminal escape into the cross-team message that is typed into the receiving team's Orchestrator terminal.
- **Names stored before this release are cleaned too:** when a cross-team message lists its attachments, each stored name is cleaned in that line by the same cleaner the hub copy already used (the stored name itself is unchanged, and the panels escape it), so an upload made before these checks cannot reach another team's terminal either.
- **Old tag colours are repaired:** on start, any stored tag colour that is not `#rgb`, `#rrggbb` or `#rrggbbaa` is reset to the default blue, and the number changed is logged. Only the colour value changes; no tag is deleted, renamed or moved. From the first start of this release, `GET /api/tags` returns only hex colours.

### Fixes

- **The top bar's bookmark always opens the Inbox at Saved for later, for everything,** on every page, with everything's counts on its badge; a second press or Escape goes back. The Saved tab in an agent's or a team's dock still shows only what was saved there.
- **Typing isn't lost when a list refreshes:** the Cloud servers enrollment-code box no longer empties while you type in it during the 5-second refresh; a box keeps your text and caret across a background redraw.
- **Antigravity permission prompts show as waiting sooner:** Coral looks for the prompt every 500 ms for 6 seconds instead of at 1.5, 3 and 5 seconds, falling back to the old pace when many tools are running at once.

### Other changes

- **Simpler code, behaviour unchanged:** the dashboard's startup is a list of named steps, the Team artifacts panel is built from small block builders, and the Settings dialog's Save is split into reading the form, saving and applying, each compared with the old code.
- **Internals:** Coral's pty backend ends its sessions together at shutdown instead of one by one, and a nil board store no longer panics in the server constructor.
- **Tests and CI:** the route tests are about a third faster and the terminal tests wait for what they check instead of a fixed time; the URL-credential finder's speed is tested by counting its work rather than by a clock; the browser suites that save messages remove them again.
- **Docs:** the bookmark always opens the Inbox's Saved for later.

## v1.4.98 — Tag colours checked on both sides, colours that meet AA, and focus that survives a redraw

### Security

- **A tag's colour is only ever a hex colour:** the server refuses a tag colour that is not `#rgb`, `#rrggbb` or `#rrggbbaa` (400 "color must be #rgb, #rrggbb or #rrggbbaa"), from the API, and in the store the scheduler uses, and the dashboard draws only such a colour, falling back to the default blue, so a colour like `red;position:fixed;inset:0` can no longer restyle the page, including from a tag stored before this release. An empty colour still means the default.

### Changes

- **Colours that meet AA in both themes:** placeholders use the muted text colour instead of the browser's own grey; the warning amber, a chip's ×, the Critical priority and red on its own tint are darker or lighter where they missed 4.5:1; blue text on a light tint (the History tab strip's open tab, a phone's Working chip) uses the deeper accent; a phone's side panel shows its open tab's icon; and the browser's own controls follow the light or dark theme.
- **Keyboard focus stays put through a background refresh:** the sidebar's team button, the lists that refresh in place and the Servers lists keep focus on the control you were on.
- **The Tasks table no longer cuts off Cost and Created** in a narrow dock: it wraps below 790 px instead of scrolling sideways.
- **Docs:** the Inbox tab, Saved for later following the page you are on, and the search bar every list shares.

### Other changes

- **Simpler code, behaviour unchanged:** the Codex agent's launch command is built in separate steps, compared with the old code by a test.
- **Tests and CI:** the browser gate fails a suite that leaves the server's settings changed, and the 16 suites that did now put them back; a census checks that every server test closes its board store; a test checks that a cursor's summary shares no memory with its message; several flaky browser and terminal tests wait for what they check instead of for a fixed time.

## v1.4.97 — An Inbox on every Coral, Saved for later by agent or team, and a label-escaping sweep

### Changes

- **Every Coral has an Inbox tab:** Needs you (the agents waiting for input, each opening that agent) above Saved for later, with the waiting count as its badge. Through Coral Hub it is still the hub's own Inbox; everywhere else the dashboard builds it. At about 768 px wide with the Servers tab shown, the extra tab moves Board under More.
- **Saved for later follows where you are:** on an agent's or a team's page it shows only what was saved there, with a heading that says which, and its counts and badge are of that scope; on the home page it shows everything. A board message's author is the board's own word, not the request's.
- **The top bar's bookmark opens the Inbox at Saved for later** where the scope is everything; Escape or a second press goes back.

### Other changes

- **Simpler code, behaviour unchanged:** the cross-team receive path, workflow runs and the artifact relay are split into named steps, each compared with the old code.
- **Tests and CI:** a browser test plants markup in 19 fields and in file and folder names, opens the pages that show them and fails if any of it is drawn as markup; a census lists every value put into HTML without escaping. It found no injection, and the one unescaped value it found, an unknown agent type in the CLI-not-found dialog, is escaped. The browser suites put back the settings they change and stop the agents and teams they start; `tools/check-patch.sh` gives each Go package 30 minutes; several flaky tests now wait for what they check.

## v1.4.96 — History events search, a Servers search, agy waiting states, and faster boards with many agents

### Changes

- **A past session's Activity in History can be searched,** with the same event search as a live agent's: text, type, day range and order, paged.
- **The Servers list has a search box from six machines up:** name, id or address, with the count.
- **The Activity and History events panels have one Filters button:** the tool groups are checkboxes in its menu, two to a line. The old per-group counts and Toggle All are gone; Clear all resets the groups.
- **Faster board status and board posts with many agents:** the first read of a Claude transcript starts at its end instead of decoding the whole file, and reading messages no longer waits behind a mention index being rebuilt.

### Fixes

- **An Antigravity (agy) agent waiting on a permission prompt or a question shows as waiting for input** in the live list, so the sidebar badge, the health check and the Inbox count it.
- **Resume no longer types into a terminal it couldn't prepare:** a live tmux pane whose respawn or rename fails is refused instead of carried on. Resume also sets the tmux terminal's title, and a launch failure now reads "launch command failed: ...".
- **Every search field trims what you type before it is sent,** so a pasted tab at either end no longer fails the search.

### Security and privacy

- **Only you can change an agent's display name, icon or name colour:** those routes refuse an agent's token, which could have set them on any session.
- **The task, message, event, Saved and Team Artifacts searches refuse control characters** (C0, DEL and C1) in their text parameters, checked before trimming.

### Other changes

- **Searches of an agent's or a session's events through Coral Hub can no longer take a computer's CPU for seconds:** they count in the Hub's search pool; the plain Activity list is not counted.
- **Simpler code, behaviour unchanged:** paged session lists, cross-team relays, Resume and agent history search are split into named steps, each compared with the old code; about 50 unused imports, unused functions, 40 needlessly exported names, dead CSS and the top bar's file search (hidden since April; the Files panel keeps its search) are removed from the dashboard.
- **Tests and tools:** `tools/check-patch.sh` runs every check a patch needs before it is posted (formatting, vet, the whole test suite on both tag sets, the frontend checks, the route-query drift and a scan of the commits, including their author identity); several timing-sensitive tests now wait for what they check instead of a fixed time.

## v1.4.95 — Search everywhere, a faster Cost view, and pty restarts that keep the agent

### Changes

- **Search everywhere, on one shared bar:** Tasks (text, status, priority, agent, updated since, order), the message board (text, From, Mentions me, dates, matches marked and stepped through), the agent list (State, Board and Machine filters and a sort, plus Collapse all / Expand all for teams), Activity, Saved for later and Team Artifacts. A search the server cuts short says N+, and the count is announced to screen readers once it has settled.
- **New server searches:** `q` on the task list and on Saved, a board message search (`GET /api/board/{project}/messages/search`), and event searches for live agents and past sessions (type, text, time range, order, paged). The message search takes a slot in Coral Hub's search pool.
- **The Cost view is much faster with a long history:** it reads an hourly aggregate of token usage that is kept exact as new usage arrives.
- **The search indexer does less work:** a cursor for every live agent (bounded by memory, not a count of 64), one scan per pass, and batched writes.
- **Customize tabs lists only the tabs the row shows,** each with its icon.

### Fixes

- **A Chat message with an attached image is confirmed** when the agent takes it late or queues it behind its turn, instead of showing "the agent didn't confirm". A message to an agent in the middle of its turn counts as sent only when its input box is empty.
- **Restarting or resuming a live agent on the pty backend works again:** it answered an error. The new terminal now starts before the old one ends, so a failed start leaves the agent running.
- **History's date_to filter includes the whole day,** fractions of a second included, for boards and agent sessions.

### Security and privacy

- **Only you can send a UI request to an agent:** `POST /api/agent/ui-request` refuses an agent's token, which could have typed text into another agent as if you had asked.
- **Coral Hub users with View access no longer see passwords in URLs:** a URL's password, and a user name that is itself a token, are masked in what the connector sends them.
- **Searches through Coral Hub match only what the viewer can see:** they run on the redacted text, and a search for a token or key is refused.
- **An agent's session name is checked for control characters,** like the other identity fields, on board and agent requests.

### Other changes

- **Simpler code, behaviour unchanged:** the terminal and live-feed WebSockets, the history list, Chat, the agent state derivation, the hub identity guard and task updates are split into named steps, each compared with the old code on random inputs.
- **Docs:** `coral --version` is documented, the troubleshooting curl sends the API key, the guides no longer claim to describe v1.3.23, callouts render, and the privacy page links to the managed Coral Hub setup.
- **Tests and CI:** terminal captures keep their exact bytes on Windows checkouts, slow browser tests wait for the layout instead of fixed sleeps (phone layout about 4 times faster), the route-query generator follows helper calls, and the gate's suite durations are refreshed.

## v1.4.94 — A Coral Hub Inbox tab, open work first in the task list, and a search index you can repair

### Changes

- **Through Coral Hub, the dashboard has an Inbox tab:** it shows the hub's Inbox page, with the number of agents waiting for input as its badge (on the phone too, in the view list).
- **The task list shows open and active work first by default,** finished tasks after; a third click on a sorted column gives the default order back.
- **coral db rebuild-search** rebuilds the session search index after a backup.
- **Team Artifacts, Servers and the task list read better with many items,** and the narrow dock's Priority and Agent headers no longer touch.

### Fixes

- **Saved for later opened from the home view can be closed:** the bookmark toggles it, Escape closes it, and its panel has a close button.
- **The startup database check no longer reports damage that isn't there;** it now confirms a problem before warning, and clears the notice when a recheck passes.

### Security and privacy

- **The Inbox tab's frame loads only the hub's own /inbox page,** set once by the dashboard, with no referrer, no extra permissions and no messages between the frame and the dashboard; the dashboard's own security policy is unchanged.
- **The database backups that coral db compact and rebuild-search write are now readable only by you** (they were created with the default file mode, which let other users of the computer read them wherever the data folder allowed it).

### Other changes

- **Simpler code, behaviour unchanged:** the task detail, the board's task list, the live feed handler, the chat's history refresh and terminal creation in the dashboard, and the terminal's paste check, its state decision and the live session rows on the server are split into named steps, each behind a test that pins what it does.
- **Docs:** the install guides list the Windows downloads, the Linux arm64 tarball and the Homebrew tap; the hub and desktop alert pages say what Coral shows.
- **Tests:** every CDP suite that intercepts requests guards against the browser closing mid-request, which made hub_account_menu flaky.

## v1.4.93 — Open terminal works from a waiting agent, slash commands read as sent, and simpler code

### Fixes

- **Open terminal on an agent that needs input shows the terminal again;** it stayed blank on Connecting.
- **A slash command sent from Chat (such as /compact) no longer shows 'the agent didn't confirm'.**

### Security and privacy

- **A slash command counts as sent only once it has left the input box:** an event from the agent (a /clear's session start), a menu it opens, or, after the confirm wait, an agent that has started and shows nothing against it, confirms it then (a CLI's own command sends no hook). A command still in the box after its Enters is reported not submitted whatever the agent does, and the next message is refused until the box is empty, so two texts are never typed together.

### Other changes

- **Simpler code, behaviour unchanged:** the Launch dialog, launch steps, personas, board launches, team wizard and team form are moved out of modals.js into their own modules, each behind a test that pins what it does; `coral-board`'s main.go is split by command; parseAgyEntry reads each kind of entry in its own helper; an unused terminal method and an unreachable settings refusal are removed.
- **Tests and CI:** the old-vs-new oracles run several times faster with no rule left unexercised; a table test checks that every refusal added in v1.4.91 and v1.4.92 reaches a Coral Hub caller intact; two manual CDP gate runs on one branch no longer cancel each other; the gate's suite durations are refreshed.

## v1.4.92 — A ceiling on agents starting agents, a default artifact store limit, and faster CI

### Changes

- **Screen readers hear what a chat send is doing:** one persistent live region announces it, and the message bubbles' labels no longer carry a role of their own.

### Fixes

- **Restarting an agent on the PTY backend works again;** it answered an error.
- **After a send is confirmed, the Working row goes back below the Sent bubble** instead of staying above it.
- **The Launch dialog's agent form is readable in the light theme** (4.5:1 contrast).

### Security and privacy

- **An agent may not start live agents past 64:** launching, or launch-team, with an agent's token is refused with 429 agent_limit once Coral already has 64 live agents running (sleeping ones are not counted); the Operator and the dashboard are never refused. Raise it with the agent_launch_limit setting (0 for none).
- **The artifact store has a 20 GiB limit when none is set:** a new upload past it is refused (artifact_quota) and nothing is ever deleted; set `artifact_store_max_bytes` (0 for no limit) or raise it in Settings > Storage. A Coral that already holds 16 GiB or more when it upgrades has its limit raised once to twice its size, logged and said in Settings > Storage, so an upgrade never stops uploads on its own; a limit you set yourself is never changed.

### Other changes

- **Simpler code, behaviour unchanged:** `coral-board` working-mode and read, a review verdict, a job run and a git poll, the six background services' ticker loops, the board's task query, task dependencies and unread counts, the team settings, agent selection and Settings dialogs are split into named steps, and the Settings, Info, flag shortcut and CLI dialogs are their own modules (modals.js from 3,118 to 1,880 lines); the review verdict is compared with the old code, and each dialog's behaviour is pinned step by step before its split.
- **Faster CI:** windows-tests runs in three shards (about 11 minutes down to 7), the CDP gate's six parts have about equal recorded time (about 6 minutes down to 5), and the release's notices step prints its time per phase.
- **Tests:** two flaky tests fixed (the chat's jump-to-latest release and a received-artifact cleanup).

## v1.4.91 — Limits on everything a caller can grow, a lighter chat transcript, and keyboard-friendly menus

### Changes

- **Long chats use far less memory:** Coral keeps a window of each conversation's newest messages and reads older pages from a checkpoint near them, in chunks of at most 8 MiB, instead of holding the whole transcript.
- **Action menus work with the keyboard and screen readers:** an agent row's menu, and the team, workflow and folder group menus, are proper menu buttons; Tab out of a menu closes it.
- **A long agent name keeps its distinguishing end,** and the phone's Agents page has a search box.
- **"Load earlier" on the board draws only the page it adds.**
- **`coral-board export` reads every message of a board,** a page at a time, and says when it cannot.
- **The live session feed (/ws/coral) sends the same rows as the session list,** so it now includes the prompt, model, model source and cache token counts; nothing was removed or renamed.

### Fixes

- **In a PTY terminal, Enter is now sent as the key a keyboard sends (a carriage return) for every agent,** so a raw-mode CLI submits instead of getting a new line.
- **Antigravity (agy):** a permission prompt that wraps across lines is read correctly, and auto-accept reads the PTY screen.
- **An unsubmitted chat message is remembered by its mark, not its text.**

### Security and privacy

- **Every size a caller can grow now has a limit:** list pages are capped at 500, request headers at 64 KiB, a terminal at 1000 × 300, a launch-team at 32 agents with at most 2 team generations at once, a board post at 256 KiB, task titles, bodies and wait reasons, open tasks per agent, active waits, notifications, UI panels and their events, the hook task cache, the file list cache, and the failed-key limiter. A refusal carries a message that `coral-board` shows (413 too large, 507 disk nearly full, 409 a limit reached, 429 too fast).
- **Writes that could fill the disk are refused with 507 when it is nearly full.**
- **Each agent session has a write budget** on the routes an agent can loop on (a burst of 60, then 240 a minute); its 429 is marked as a limit on that sender, so the dashboard keeps polling.
- **Each write of a reply on the local listener has a 60-second deadline** (rolling, per 64 KiB written), so a client that stops reading frees the handler; a slow client that keeps reading is not cut. Event streams and WebSockets have none.
- **A cut task list keeps every open task,** then the newest done ones, so the coordination report still sees the board's whole open state (it logs when it received a cut list).

### Other changes

- **Simpler code, behaviour unchanged:** board status, Restart, launches, settings, the auto-accept shell check, cross-team sends, the notification pass, startup, the health rules, the orchestrator's check, `coral-board wait` and the agent list's rendering are split into named steps; several of them are compared with the old code on random inputs. One duration and one cost formatter replace ten copies.
- **Tests and CI:** gofmt runs on every gate branch, the gate summary shows each flaky or failed suite's first failing line, and the landing and release scripts are in the repository; the agent list's whole markup is pinned by a golden test; launch tests no longer depend on a real agent CLI.

## v1.4.90 — Chat that knows when a message was really sent, agy auto-accept, and a Coral Hub first run

### Changes

- **Chat says Sent only when the agent actually submitted the message,** and shows the conversation's own state while an agent is still starting, instead of an unopened terminal connection's. Messages to one agent go one at a time, and a message is never typed after one that wasn't submitted.
- **Auto-accept now works for Antigravity (agy) agents when you turn it on:** it answers permission prompts with the one-time option only ("Yes, run command", "Yes, allow once"), never an "always allow" one, and only when a tool call is actually pending; it doesn't act on questions, plans, sign-in or trust menus. Usage and rate limits are detected for agy too.
- **Coral Hub first run:** with no machine connected, the Agents page shows "Connect your first machine" with a button to enroll one; all-offline machines and machines with no agents get their own lines. Add a machine leads with installing Coral, and a machine at View says it can't launch as soon as the dialog opens (one message, nothing sent).
- **The Coral Hub account menu has Account and Saved for later,** and a link can open one saved message directly.
- **Through Coral Hub, the New Agent dialog's CLI check reaches the machine,** and a refusal says it needs the Launch tier.
- **The agent list is lighter:** each row's action menu is drawn when you open it (with 100 agents the list drops from about 7,500 to 2,400 elements).
- **The Coral Hub's wording calls a connected computer a "machine"** throughout sign-in and enrollment.

### Fixes

- **On a phone, a long agent name that wraps no longer has its state chip drawn over the second line.**
- **A terminal that follows a pane taller than its window keeps its prompt line in view.**

### Security and privacy

- **One message at a time to each agent, bounded:** two sends close together can no longer type both texts into an agent's input box (a second message waits for the first, with a limit), and nothing is typed after a message that wasn't submitted.
- **The Coral Hub's CLI check passes only the exact allowlisted query** (an agent type, optionally the recheck flag, never a path); every other system route stays refused through the hub.

### Other changes

- **Faster board reads and tmux commands with many agents:** a board's numbered agent names are cached, mention terms are memoized, and tmux commands build their environment in one pass.
- **Tests and CI:** the CDP gate can repeat chosen suites many times on a manual run, for verifying a flake fix; a scalability harness for many mock agents; unused and duplicate CSS removed; new tests for all of the above.

## v1.4.89 — A Codex transcript privacy fix, a health check that says who waits on whom, and agy prompts answered from Chat

### Security and privacy

- **A Codex session's chat shows only its own transcript.** A Codex started from another Codex session's shell inherited that session's thread id from the environment, so its chat showed the parent's conversation; and the token poller could pick another session's rollout in a shared folder by its creation time. A session's transcript is now found only by what names it (Codex's own payload, the recorded link), never by folder, time or the environment; transcript lookups accept only a well-formed session id, and a bound session keeps its link unless the new rollout is marked as its own.
- **agy's exit codes are read only from that step's own files,** through the safe file layer, under the conversation folder the payload names.

### Changes

- **The Orchestrator's health check says what each agent is doing and who waits on whom:** working, waiting (and on whom, for how long), at a prompt, idle or asleep, with a wait the awaited agent has already answered marked stale; a wait longer than 10 minutes is escalated once to the agent it waits on. An @mention that names nobody still posts, with a warning and the closest name.
- **Antigravity (agy) prompts are answered from Chat:** its menus with the arrow keys and Enter, its trust menu too; a launch's start shows as a start, its tool calls read as commands, reads, writes, edits, searches and fetches, and a command that exited non-zero shows as a failed tool with its exit code. Auto-accept for agy comes in a later release.
- **The agent hover shows the model, and Analytics by model has separate Provider and Model columns, with real model names;** a session that switched models is split across them.
- **The gear menu ends with the running Coral version.**
- **The sign-in page, the home-screen icons and the docs site use the current Coral mark** instead of the old multicolour logo.

### Other changes

- **Tests and CI:** new tests for all of the above; the sign-in page suites no longer flake (the dashboard's service worker hid their navigations); six terminal-capture fixtures restored byte for byte.

## v1.4.88 — A navigable Settings, Team Artifacts search, and working install links

### Changes

- **Settings is navigable:** a wide dialog with its eight groups down the left (General, Agents, Terminal, Notifications, Files and Git, Storage and cleanup, Network and Hub, Security and privacy), one pane at a time, each label and control on one row, on/off settings drawn as switches, and every explanation behind an (i) button. On a phone, in a small window or with large text, the groups become a selector at the top.
- **Team Artifacts has a search box, filters, sort and Load more,** all answered by the server, so a search covers every artifact, not just the loaded page, with a count such as "12 of 340". The API (`GET /api/board/{project}/artifacts`) gains the same search, filters, sort, cursor paging and totals.
- **The Coral Hub icon in the top bar opens the hub when connected,** and the Connect to Coral Hub screen when not (it no longer opens Settings).
- **Sleep All and Wake All are gone from the top-bar gear menu.**
- **An unverified task shows nothing,** in the list or the detail; only a task with evidence attached keeps its small icon.

### Fixes

- **The Antigravity install command points at Google's live installer,** and a missing CLI links its vendor's official install page, in the theme's link colour.
- **Coral also finds agy in ~/.local/bin, where the installer puts it, even when that folder isn't on PATH;** the install note says how to add it to your own PATH.
- **Through Coral Hub, the New Agent dialog can check whether a CLI is installed** (it showed "Could not check claude CLI (HTTP 404)"); the check runs the CLI's --version on your computer, so it needs the Launch tier, and its reply carries no path.

### Security and privacy

- **Through Coral Hub, the Team Artifacts search matches what the viewer sees, after redaction,** so it can't be used to guess a redacted secret a character at a time; and the task filters (assigned_to, created_by, board) now refuse a value containing a secret.

### Other changes

- **Tests and CI:** new browser and Go tests for all of the above.

## v1.4.87 — Prompts answered from Chat that really close, honest permission presets per CLI, and task verification

### Changes

- **Answering an agent's prompt from Chat now checks that the prompt closed:** Codex's menus get the Enter they need (and never a second one), Claude Code's unnumbered menus are answered, prompts are read on the PTY backend too, and an answer counts only once the prompt is gone (otherwise 409 `prompt_still_open`).
- **The agent form says what each CLI enforces for the permissions you choose:** Claude's Allow and Deny, Codex's sandbox and approval mode, Antigravity's mode. Coral adds no permission layer of its own; the line names what the chosen CLI keeps, and no more. Codex can't enforce shell patterns per launch, so its presets set only the sandbox and approval mode, and the form says so.
- **While a startup prompt (folder trust, hook review, a CLI update) holds an agent, it is marked as waiting for your input,** so Coral Hub's Needs you page and notifications see it.
- **A finished board task carries a verification state** (for Coral Hub and agents: a field and a filter on the task API): verified when a reviewer other than the author approved it or a server-run check passed, evidence attached, or unverified. It is a label, never a blocker. In the dashboard it is quiet: a small muted icon after the title for a task that nobody independently confirmed (evidence attached or unverified), and one plain line in the task detail.

### Security and privacy

- **Prompts on the PTY backend are read with a small screen renderer of Coral's own:** it acts only on text, cursor moves and erases, with bounded input and work, and it is fuzz-tested.
- **Antigravity's capabilities win over the dashboard's permission flag,** so a read-only preset no longer launches with `--dangerously-skip-permissions`.
- **Of the completion gates, only a server-run check makes a task verified;** gates decided from what the worker uploads (report, test evidence, landed revision) give evidence, not verified. A reviewer other than the author still verifies.
- **The Codex hook reads its rollout file through the safe file layer** (no symlink as its name), and only for shell commands.

### Fixes

- **A failed Codex shell command shows as a failed tool in the activity feed,** with its exit code.
- **Codex's SessionStart is labelled by its source** (started, resumed, compacted, forked or /clear) instead of always "/clear".
- **The sidebar's count badges reach 4.5:1 contrast in both themes** (they were 3.7 to 4.4:1).

### Other changes

- **Tests and CI:** new and steadier tests for all of the above, including a Windows line-ending fix in a docs table test.

## v1.4.86 — The right sidebar on every page, Save for later across servers, and no PATH CLI in place of a configured one

### Changes

- **The right sidebar is on every page,** with tabs for the kind of page: an agent keeps its tabs; a board has Subscribers, Team artifacts, Tasks and Saved; Chats and the other pages have Saved. Open or closed, its width and the last tab are remembered per kind of page. A board opens its sidebar only in a wide window (about 1300 px and up); on a phone the top-bar button opens the side panel on every page. Left, Right, Home and End move along the tab strip.
- **A board's toolbar fits on one row:** on a phone, and on a desktop with the sidebar open, Select, Export, Pause and Delete move into a More menu (Delete last and red; a paused board's Resume in the warning colour).
- **Save for later across servers in a local hub:** messages of a registered server's agents and boards can be saved; each bookmark is kept on that server, the Saved tab lists every online server's items with the server named, and Open, Done and Remove go to the right server. A server that does not answer is one line in the list. An agent that has ended on a registered server can't be opened from the hub yet; its item says so and can be marked done or removed.
- **GET /api/board/tasks takes filters, sort, cursor paging and a choice of fields** (for Coral Hub). With no parameters, or only `limit`, the reply is unchanged; an unknown parameter is now a 400.

### Security and privacy

- **Coral never runs the CLI on PATH in place of the one set in Settings > CLI Paths:** the prerequisite check the dashboard makes on every page load ran the CLI on PATH when the settings could not be read or the configured path could not be resolved at that moment; it now answers that it cannot use the configured CLI (or 503 when settings can't be read), never PATH. The theme generator now uses the configured CLI too. A test fails if any code looks up or runs an agent CLI by its bare name.
- **Browse and file reads refuse many more credential locations under the home folder,** and Coral's own data folder: cloud and deployment CLIs' folders, password managers and mail, macOS Containers, Preferences and Accounts, and on Windows the app-data folders that hold credentials and browser profiles (never all of `AppData\Local`, so the temp folder and ordinary projects stay reachable).
- **The saved-message routes also accept an authenticated API key as the Operator** (what a local hub uses to reach a registered server); an agent's token is still refused.
- **A local hub's requests to a registered server can't leave that server's proxy path,** whatever ids or names the server sends (dot segments, escapes and control characters are refused), and ids that go into request paths must be plain numbers.

### Other changes

- **Tests and CI:** new browser and Go tests for all of the above.

## v1.4.85 — Save for later, a faster board, and agents kept to their own history

### Changes

- **Save for later:** bookmark any chat or board message, find it again in the new Saved tab of the right sidebar, and click it to jump back. Mark items Done or remove them. On a phone with large text the Saved entry moves into the settings menu, with its count. Agents and boards on other servers in hub mode can't be saved yet, and Coral Hub's own dashboard has no Save button yet; the roll-up across servers on Coral Hub is being agreed with coral-hub.
- **A jump-to-latest arrow** appears above the chat input when you scroll up in an agent's Chat view, with a dot when new messages arrive.
- **The message board stays fast on long boards:** each message is cleaned and rendered once, a poll appends only what is new, and the view keeps a window of about 300 messages ("Load earlier" brings back older ones). While you read higher up, an "N new messages" pill takes you down. Long sender names wrap on a phone, and an open board reflows at 200% text.
- **Mentions are found through an index,** so reading a busy board (and the unread counts) stays quick however far back an agent's cursor is. The first start after upgrading indexes the existing board messages once.
- **Dialogs are built when you first open them** (Settings, Launch, Job, Create task, Info, Default prompts, Custom view and the top-bar menu), so the dashboard starts lighter. On a phone the agent list is built once, tooltips load on first hover, and there is no welcome screen.

### Security and privacy

- **An agent's token can only search its own history:** another session's history answers 403, and history search with the API key or from a plain terminal answers 401.
- **An agent's token is refused** the terminal stream (`/ws/terminal`), the auto-accept "disable all" switch, uploads and upload deletes, and the open-in-editor action. The signed `coral auto-accept off --all` command still works.
- **In a release build, strict local auth can be turned off only with `--strict-local-auth=false`;** the environment variable is ignored. The docs now say so, and several other security claims in the docs were corrected (Security's audit): the team config's field is `capabilities` (`permissions` and `permissions_preset` were never read, so they restricted nothing), and pending received files can be removed by Prune and Automatic cleanup after the chosen age.
- **Save for later is the Operator's own:** an agent's token is refused on every saved-message route (403), the snippet kept with an item is redacted when it is saved, and through Coral Hub the list is View and changes are Interact tier.
- **Saved items find their message by a key made with a secret unique to this install** (`.message_key_secret` in the data folder), so the key can't be used to guess redacted text.
- **Coral Hub's access modes are described as the route table enforces them,** in the dashboard and in the docs, and a test pins the two together.
- **A message deleted on the server stays on your screen while you read higher up on the board** (or are selecting messages, or waiting on a search jump) until you scroll back to the bottom or reload. Only your own screen; the server no longer has it.

### Fixes

- **Switching from Chat to Terminal no longer shows a blank terminal:** it no longer waits on the terminal library, it repaints the screen, closes a stuck connection, and opens none while you are in Chat view. Retry after a failed load and the popout window attach again.
- **The auto-accept "turned off" notice shows once per pause,** also across reloads.
- **Browse through Coral Hub works past the home folder again** (folders show as `~` and `~/folder`, so your home path and account name stay off the Hub); it failed on the first folder click since v1.4.73.
- **A confirmation gives focus back to the button that asked,** also when you cancel it or press Escape inside a dialog that stays open; a dialog opened from the top-bar menu returns focus to the menu button.
- **On a phone, message buttons (Copy, Save) no longer overlap the next message.**
- **"Load earlier" on the board keeps keyboard focus** across a poll, and tabbing onto it loads nothing.

### Other changes

- **The server's startup log names the dashboard build and the agent docs it serves** (a count and a hash), warns when no agent docs are embedded, and a test catches a stale copy of the agent docs.
- **Tests and CI:** new and steadier browser and Go tests for all of the above (including a 250 ms Windows bound for one tunnel timing test and failure reports on two flaky suites).

## v1.4.84 — Kill keeps terminal logs, a limit on received files, and a stricter net for cross-Coral sends

### Changes

- **Behaviour change: Kill now keeps the agent's terminal log on tmux too.** Until now, killing an agent removed its terminal log when Coral used tmux (the pty backend always kept it), so if you relied on Kill to clear a log, it no longer does. The log stays in the `logs` folder of Coral's data folder, and every Kill confirmation (Kill Session, Kill All Agents, Kill Team, Kill Run when an agent step is running, Reset Team) now says so. Coral's search reads the CLIs' own transcripts, not these logs.
- **Settings > Storage > Terminal logs lists your terminal logs and can delete the ones that belong to ended agents.** It shows how many logs there are, how big they are and how many are an ended agent's; the button ("Delete 12 terminal logs of ended agents (45 MiB)") asks first, says what those agents' terminals showed can't be recovered, and keeps the logs of live, sleeping and archived agents. It is never automatic, Operator only (the status is Operator-only too), and not available through Coral Hub. If anything about the set changed since it was shown, nothing is deleted. A crashed agent that was never marked stopped keeps its log, and logs with no matching session stay until you delete them by hand.
- **Files received from another Coral are limited to 2 GiB in total by default.** Past the limit, new received files are refused (`store_full`); nothing already received is deleted, and your agents' own uploads are never counted. The limit, what is received so far and a warning from 80% (and at the limit) are in Settings > Storage > "Received files limit (GiB)"; 0 means no limit, and an empty field is left unchanged rather than saved as 0. The first time received files reach 80% you get one notice (a toast, or the status line if Settings is open).
- **Received files that no message ever listed now count toward that limit, and Settings > Storage shows them with a "Delete N unlisted files" button** (Operator only, asks first, removes exactly the files it names).
- **Team Artifacts > Details lists "Also received from"** when the same file arrives again from another Coral or board: one line per later sender (the first sender stays), each marked as the sender's own claim for the board name.

### Security and privacy

- **Sending a file or a message to another Coral now looks for secrets in more places.** The scan covers the file as written and re-encoded forms of it (percent-encoding, HTML entities, split fragments, reversed text, and base64 or hex that decodes to text, two layers deep), and the text of the message as well (refused as `message_secret`). A scan that can't finish in its time or size allowance refuses the send (`attach_unchecked` or `message_unchecked`) instead of letting it through. **This is a net, not a boundary:** rot13, base32, compressed data, three layers of encoding, other split characters and binary files are not caught, and the docs list them.
- **Only one file is scanned at a time on a Coral:** a send that has to wait more than 30 seconds for another file's scan is refused as `attach_busy` (try again in a minute); `coral-board send --attach` waits long enough for that.
- **A file from another Coral is listed only when its message arrives.** The sender pushes the files first; one whose message doesn't come within 30 minutes is never listed (it still counts toward the limit above, and Settings > Storage lists it so you can delete it), and a retry of the same message and file is not charged to the quota again (up to five free retries a day). The sending side also re-reads the switch before each file it pushes. The receiving side reads the board's cross-team switches again just before storing, so a switch turned off after a send began is respected.
- **An agent can no longer read the ids of received files nobody has listed.**
- **A download whose stored size disagrees with the file is refused** (`artifact_corrupt`, no bytes) instead of sending a wrong Content-Length.

### Fixes

- **Files over 4 MiB sent from another Coral were refused (413) since v1.4.79; they are accepted again,** up to the 8 MiB limit.
- **A confirmation asked from the Settings dialog was drawn underneath it,** so you saw a dimmed page and no question. This hit Prune, Start automatic cleanup and the new delete buttons; confirmations now appear above Settings.
- **The secret scan's time allowance now grows with the size of the file** (3 s plus 4 s per MiB, 100 s at most per file), so a large text file is no longer refused as unchecked on a busy machine.
- **After restarting an agent in Chat view (the default, and the usual view on phones), the 'Agent is restarting' overlay now clears** once the agent is back, and no longer stays up when you switch to another agent. The bar above the chat says Restarting meanwhile, and switching agents during a restart no longer reconnects the terminal to the wrong agent. The Session ended overlay had the same gap.
- **Settings > Storage no longer saves an old limit after its sizes fail to load**: the fields stay empty and off until the store answers.
- **Turning auto-accept on just as Disable All runs, or just as you press Stop on that agent, can no longer leave it on afterwards** (the enable answers 409 `stopped_meanwhile`).
- **`coral-board send` prints the refusal code** (for example `attach_secret: ...`) when a send is refused, so a script can tell refusals apart.

### Other changes

- **Less memory churn and a cheaper machine-health view:** compressed API replies reuse their compressors, and on Linux the machine-health process scan reads each process once (same numbers), and board lookups on every agent request use prepared statements.
- **Team Artifacts and Settings > Storage:** keyboard focus returns to the right control after Delete, Retry, Prune and Shrink; 44 px touch targets; the Prune confirmation names saved diffs and unlinked artifacts in plain words; the artifact store limit shows when it is reached.
- **Tests and CI:** new browser and Go tests for all of the above.

## v1.4.83 — Background service restart fix

### Fixes

- **A background service's restart loop reads its retry delays once, when it starts,** so changing them later (as the tests do) can no longer race with a loop that is still winding down. The delays and behaviour are unchanged.

### Other changes

- **Tests and CI:** the sign-in page test checks the address a redirect reaches instead of which server answered it, and the browser test gate reports only the newest attempt of a re-run part.

## v1.4.82 — Stricter settings, and paced file pushes over Coral Hub

### Security and privacy

- **Settings are checked against a list of known keys.** A save with an unknown settings key is now refused (400) as a whole; it used to be stored silently. `terminal_replay_bytes` is clamped to 4 KiB-16 MiB, and values over 256 KiB (2 MiB for the saved lists) are refused.
- **Agents can no longer read `/api/settings`;** only the Operator can.

### Coral Hub

- **The Coral Hub tunnel can pace a file pushed to a machine, so keystrokes stay responsive on slow links; Coral Hub opts in per push.** Below about a 1.3 Mbit downlink, a pushed file can still delay keystrokes, since the pace can't go lower and still finish within the 120 s limit.

## v1.4.81 — CLI paths that can't run are reported, stricter setting values, and one "Nothing to save" message

### Fixes

- **A CLI path set in Settings > CLI Paths that can't be run now makes background jobs (model list, summaries, knowledge, goals, workflow agent steps) report it, instead of falling back to the claude on PATH.**
- **Every settings form says the same thing when Save finds nothing to save: "Nothing to save: no setting changed."** The Settings dialog, the Default prompts dialog and the scheduler's job editor now say it, like Team settings, instead of "Settings saved", "Default prompts saved" or closing silently. A Save that changed something still says it saved.

### Security and privacy

- **Settings: a value must be text, true or false, or a whole number (lists and objects are refused); large numbers are stored in full; if the artifact storage cap can't be read, Coral stops taking new files instead of allowing unlimited storage.**

## v1.4.80 — No more stale tabs overwriting settings, redacted agent knowledge, and safer workflow saves

### Fixes

- **A second or stale dashboard tab no longer overwrites settings it didn't change.** Settings, Team settings (including the Health check tab) and the scheduler's job editor now save only the fields you changed (and refresh from the server when they open), so a CLI path, a cross-team switch or a job edited elsewhere is kept. The default prompts dialog and working-mode presets do the same; saved personas, team templates and macros keep items added elsewhere, and deleting a macro removes the one you clicked.
- **Saving a workflow in the dashboard no longer erases step settings it doesn't show.**
- **Saving the Settings dialog before it finished loading could clear your CLI paths; it no longer can.**
- **Resuming a plain terminal pane is refused** ("Resume not supported for terminal") instead of typing a Claude resume line into the shell.

### Security and privacy

- **Distilled agent knowledge is now redacted with Coral's secret rules:** before a transcript is sent for distilling, before anything is written (distill, Save, team import), and whenever it is shown or put in an agent's prompt. Knowledge files written by older versions are redacted when read but may still hold secrets on disk; distill the team again, or open and Save each agent's knowledge, to rewrite them.
- **Coral removes per-session state files and Pi session folders of sessions that ended over 30 days ago** (exact names, your own files only; nothing on Windows).

### Other changes

- **An agent that exits on its own can show in the session list for up to 5 s,** since the list now reuses one tmux pane listing for a few seconds.

## v1.4.79 — Send files with cross-team messages, a tidier temp folder, and clearer first steps

### Cross-team files

- **Teams can send files with a cross-team message:** `coral-board send --attach coral://artifacts/<id>` (up to 4 files, 8 MiB each) attaches files an agent of the sending team uploaded while on that board. A received file appears in the receiving board's Team Artifacts, labelled with where it came from; a file from another computer is checked by its own bytes (never its name or declared type) before it's kept. Off by default: turn it on per board with **Share files with other teams** in Team settings (you only, not through Coral Hub). Files are not scanned for malware and their contents are not redacted (text files sent to another Coral will be checked for secrets before they leave).
- **Sending to a team on another Coral** goes through Coral Hub and stays unavailable until your Hub supports it; the Coral Hub tunnel can carry a streamed file once a hub enables it (off unless the hub opts in; no behaviour change today).

### Security and privacy

- **Launch files (an agent's prompt, settings and instructions) are now written to `<data dir>/tmp`,** readable by you only, instead of the shared system temp folder; ones a crash left behind are removed at startup once they're over a day old and no running agent uses them (in the old system temp folder, only files of sessions this Coral saw end). The first start of this version clears old leftovers, which can take a few seconds in the background.
- **A long message Coral pastes into an agent's terminal no longer stays in tmux's paste buffer** afterwards.
- **A resumed session's copied files and `coral-board export --output` are readable by you only.**
- **The retired proxy's CA private key and certificate are deleted from the data folder.** If you ever added that proxy's CA to a browser or keychain by hand, remove it.

### Dashboard

- **A new agent's empty chat says what to do next** (type below, wake it, or that it ended without messages).
- **Every Kill confirmation says that, on the tmux backend, Kill also deletes the agent's terminal log (Sleep and Archive keep it),** and a new docs page, *What Coral stores*, lists what Coral keeps on this computer and when it's deleted.
- **On a phone, the agent header keeps the branch name when there's room** and stays on one row at every phone width.

### Fixes

- **Restarting a plain terminal pane no longer starts a Claude agent in it.** It typed `claude --session-id ...` into the shell, using whatever `claude` is on PATH rather than the one set in Settings > CLI Paths; it now restarts as a shell in the same folder.

## v1.4.78 — Launches always use your CLI path, an idle dashboard stays small, and a faster first load

### Fixes

- **A launch no longer falls back to the CLI on PATH when Coral's settings can't be read** (it fails with a message), and a team launch whose client hangs up uses the CLI set in Settings > CLI Paths for every agent. Restart, resume and wake, scheduled jobs and workflow steps do the same.
- **When Coral's settings can't be read, the agent model list says so** instead of reporting that the CLI isn't installed.
- **An agent is no longer told "You have N unread messages" when it already read them during its turn** (a notice held until the turn ended is re-checked just before it's typed).

### Dashboard

- **An idle dashboard no longer grows its memory:** in a test with 20 agents and a busy board (about 80 messages a minute), the page's heap grew from 5.7 MB to 46.6 MB in 6 minutes (109 MB in 10); it now stays at about 6 MB. The board panel keeps the newest 300 messages while you're at the bottom, and Load earlier brings back the rest.
- **Faster first load:** measured on a test setup with 20 agents and a 650-turn chat, the first message shows in 2.33 s instead of 3.10 s on a slow (4G) connection with a cold cache, and in 0.29 s instead of 0.90 s when cached. The startup modules are in the bundle, the bundle is smaller, the agent list loads alongside the hub check, and a linked agent's chat is fetched while the list loads.
- **Text sizes follow your browser's text size across the dashboard** (they were fixed pixels almost everywhere), header icons scale, and at 200 percent text on a phone the Files, Analytics, Workflows and dialog controls no longer run off the screen. Icons grow with the text too, and buttons on tablets and touch laptops are 44 px.
- **Screen readers no longer read icon names aloud as words** (decorative icons are hidden from them, and icon-only buttons have labels).
- **The create-password and sign-in page** follows your text size, its controls are 44 px on touch screens, the first field has focus when it opens, and a refused field is focused and says why.
- **Started without a terminal, Coral names the exact command to run to accept the terms**, on Windows in both PowerShell and Command Prompt forms.
- **The desktop app's Terms of Service dialog defaults to Decline,** so pressing Return never accepts; it's readable by keyboard and screen reader and uses the system font.
- **The dashboard's cached files are refreshed once with this update;** the build id now also changes when any minifier option or the bundle's format changes.

## v1.4.77 — Long messages to agents arrive as text again (security fix)

### Security

- With the tmux backend, on v1.3.10 to v1.4.76, a dashboard message longer than 900 characters did not arrive as text (the agent got a line naming a script file); resend it after updating, or use the PTY backend meanwhile; a leftover coral-command-*.sh in the temp folder can be deleted (Coral now removes ones older than a day at startup). Only someone who could already type into that agent's terminal (you, a Coral Hub user with Interact access, or an API-key holder) could send such a message; agents could not.

## v1.4.76 — Nudges wait for an agent's turn, Coral's notices drawn as notices, and safer remote servers

### For teams

- **Automated board nudges and messages now wait for a Claude agent's turn to end** (@-mentions still go at once; a prompt or menu on screen still holds everything). Turn it off with the setting `gate_signals_claude` set to false, 0, no or off, or with `CORAL_GATE_MODE=screen` in the server's environment.
- **Coral's own notices (task updates, board notices) no longer show as your chat messages;** they're drawn as Coral notices.

### Security

- **A remote server reached directly no longer receives the browser's X-API-Key, X-Coral-* or Sec-Fetch-* headers,** and its replies are served sandboxed like a connector's (HTML under /api/remote is a download; Coral's own sandboxed previews still show).
- **A remote server not marked private must use https;** plain-http servers show a warning, and servers already registered over http keep working.
- **Listing remote servers and remote boards is for the Operator, not agents.**
- **The remote-server address check also refuses NAT64 and 6to4 addresses** that embed a private address.
- **Dashboard files are now served with nosniff, and /static files with Cross-Origin-Resource-Policy: same-origin.**

### Dashboard

- **The Servers tab shows the plain-http warning** in a server's row and its edit form ("Warning:" with an icon, not only a colour), and a refused http address is explained next to the URL field. The Add/Edit server form now matches the other dialogs and fits a phone at 200 percent text.
- **The Terminal view ends in a slim bar** with the connection state, the columns and rows and the typing hint, which no longer floats over the last line.
- **The Model field of the launch and restart forms is a picker:** Default, the listed models by name, or Other… for free text.
- **The Mobile connect dialog says "Sign in to see the API key (run: coral open)"** when you're not signed in, instead of a generic error.
- **Screen readers:** a rail tile's name now includes its state and unread count ("qa, Working, 9 unread"), read once.
- **Agent header:** the status dot is named for screen readers (and labelled on touch screens); on a phone at 200 percent text the agent's name stays visible; step bars and Load older are 44 px on touch screens.
- **The dashboard's cached files are refreshed once with this update** (a new build id); from now on a change of bundler, minifier or stylesheet order does the same.

### Performance

- **Agent requests are cheaper:** a token's owner is cached for up to 10 s, and a revoked token is refused on its next request. Measured in a synthetic in-process test with 8 agents: requests waiting for the database's single write connection fell from about 2,000 to 6-8 per run, p99 from 1.9-3.1 ms to under 0.9 ms, and throughput rose from 10-14k to 16-18k requests a second.
- **The live agent list reuses the encoded form of rows that didn't change:** in a synthetic test with 20 agents, 55 percent fewer allocations per update (2,896 to 1,314) and about 16 percent less time.
- **The board notifier looks up agents' terminals less often:** tmux list-panes calls fell from 48 to 26 a minute in total (the rest come from other parts of Coral), and the notifier's CPU by 55 percent.
- **Shutdown no longer waits up to 30 s** for the call-metrics flush when the database is busy; it gives up after 5 s and logs what it dropped.

### Fixes

- **Startup no longer waits for the local sign-in check before loading the agent list.**

## v1.4.75 — Chat roll-up, a warning when local sign-in is off, and tighter Coral Hub access

### Security

- **A red banner says when local sign-in is off:** "Local sign-in is OFF: any program on this computer can use Coral." It shows on the dashboard, including the agent pop-out, can't be dismissed, is re-checked every minute, and a failed check leaves it as it was. It isn't shown in Coral Hub's view.
- **With local sign-in off, Coral refuses to start on the default `~/.coral` folder** (however it is spelled, including a symlink or a different letter case on macOS and Windows); use a folder of its own.
- **`CORAL_STRICT_LOCAL_AUTH=0` now works only in dev builds;** use `--strict-local-auth=false`, which works in every build. A release build ignores the variable, stays strict and logs a warning naming the flag. If you set CORAL_STRICT_LOCAL_AUTH=0, Coral is strict again after this update.
- **Reading or regenerating the API key needs a verified Operator credential** (the API key, the dashboard sign-in or the signed command line), even with local sign-in off; an agent's token or a request with no credential is refused.
- **Coral's secret masking also catches** a bearer token or Basic value in an Authorization header, JSON web tokens, Hugging Face and Google OAuth tokens, an AWS secret key written with spaces around the "=", and a key with an invisible character inside it.
- **Identity, step 2 (still log only):** the Operator now sets the identity mode with `PUT /api/system/identity-actor` (refused through Coral Hub; the default is log and enforcement stays off). Planning commands now accept you, the Operator, in any spelling of the name, so the signed command line's lower-case "operator" is no longer refused.

### Coral Hub

- **Sleeping or killing one agent through Coral Hub now needs Launch access,** as sleeping all agents already did.
- **Nine routes are refused through Coral Hub:** reassigning or snoozing tasks, saving or resetting a working-mode preset, session task reorder, task runs and a task's changes diff. Adding a new preset still works; for an existing one, the Hub view hides Save and Reset built-in and says why.

### Dashboard

- **Chat roll-up:** consecutive collapsed groups with nothing visible between them become one bar showing the number of updates and steps and the newest group's label. Opening it shows the groups as before. A reply or your message ends the run, new groups fold into a closed bar without the page jumping, and chat find opens the bar in front of a match.
- **Keyboard and screen-reader fixes:** the tab chooser announces moves, hides and resets and closes when you Tab past either end; the display menu closes on Escape; a server's menu takes the arrow keys, Home and End; the Health check tab's controls are named by their titles; Sign out other browsers keeps focus in place.

## v1.4.74 — The orchestrator's health check, a faster first load, and steadier agent restarts

### Security

- **Coral's secret masking now also catches Anthropic and OpenAI keys and AWS access key IDs,** in Coral Hub views, logs and agent events.
- **Coral now records who each board and agent request really comes from** (an agent's own token, you, or a Coral Hub user), in the log only. Nothing is refused on that basis yet; this is the first step toward refusing a request made under someone else's name.
- **An agent can no longer change a board's settings,** even with a valid token; only you can, on the computer that runs Coral.

### For teams

- **The health check is now the orchestrator's action list,** typed to it rather than posted on the board: what Coral escalated, an idle agent with no work, a reviewer whose queue grows while another is idle, queue problems, and what moved. When nothing needs it, it's one line.
- **Team settings has a Health check tab:** whether the check runs, how often, who gets it, and each automatic rule on or off with its numbers. Only the Operator can change them, on the computer that runs Coral, not through Coral Hub.
- **Park a task** with `coral-board task park <id> --reason "..." [--until DATE]` (and `unpark`): a parked task gets no reminders or nudges and stays off the check. The orchestrator acknowledges a check item with `coral-board health ack <id>`, and it stays off the check until its state changes. Only the team's orchestrator or you (the Operator) can park, unpark or acknowledge, and not through Coral Hub.
- **Reminders:** `task add --remind-at TIME` has Coral remind the assignee once at that time; `coral-board wait --operator "question"` records a question for you, listed in the check with its age until you answer on the board; Coral reminds the orchestrator, once, of an @-mention it hasn't acted on for 10 minutes; and it tells you on the board when an agent's messages have waited at a dialog for 10 minutes (or a little later; once per hold).
- **Agent text Coral shows in these lines is cleaned first:** redacted, cut to one line of 120 characters, with control, invisible and direction-changing characters removed.

### Dashboard

- **Faster first load:** the dashboard starts from one minified script and one stylesheet instead of more than 130 modules and 24 stylesheets. On a slow connection the agent list appears in about half the time (6.2 s to 3.2 s on simulated 4G).
- **At 200 percent text** the display menu and a server's menu stay inside the screen, a server header keeps its name beside the Update marker, and the Settings window no longer scrolls sideways on a phone.
- **Settings checkboxes and the Health check tab have phone-sized tap areas,** and the tab fits at 200 percent text.
- **Contrast:** text that fell under 4.5:1 when hovered, focused or selected is fixed.

### Fixes

- **A restarted or resumed agent keeps its board membership:** its subscription moves to the new session before the agent starts, and moves back if the launch fails.
- **Creating the dashboard password** could fail when two creations raced; each now uses its own temporary file.

## v1.4.73 — Your own side-panel tabs, a tray that restarts Coral, and a sturdier server

v1.4.72 was tagged but never released (its release check found that the Operator's own command line was refused when joining or posting to a board); this release carries all of it, with that fixed.

### Security

- **Agent and subscriber names that only look like a reserved name (a Cyrillic or Greek letter, or C0ral) now count as that name,** and agents can no longer post as Operator.
- **The update check never sends a credential:** a GITHUB_TOKEN in the environment is no longer attached to Coral's request to GitHub, and the answer is cached for an hour (Check for Updates asks GitHub again at most once a minute), so the dashboard can't make Coral call GitHub over and over.
- **Values that end up in the page are checked first:** a team colour must be a #rrggbb colour, and a Coral Hub update notice must look like a version, or neither is shown.
- **A request's identity fields could slip past the board's checks** when spelled in another case (SUBSCRIBER_ID) or followed by more JSON; every check now reads exactly what Coral acts on. A body declared over 4 MiB is refused on the agent routes too.
- **Two parser fixes found by fuzzing:** strict JSON replies refuse a key that differs only in case, and an origin whose host is only dots is refused.

### Dashboard

- **Choose which tabs show in an agent's or a team's side panel and put them in any order:** the tune button at the end of the tab row, or right-click the row. Chat always stays shown. Reset to default puts them back.
- **The sidebar header is one tidy row:** the collapse button is last on the right; the search box, server button and icons share one height; the server button always shows its dot, count and Update marker; and a click outside closes the server list and the display menu.
- **A server's menu** sits in the same column as the team, folder and agent menus, and starts with Launch team, Open folder and Launch agent.
- **The collapsed sidebar looks like the expanded one:** plain text in the same colours, with the selected agent on the same highlight.
- **Coral's own notices (unread messages, health checks, stop-hook feedback) fold into the steps they prompted,** and a short reply to them folds too, with its first sentence as the group's title. Expanding a group shows them as before.
- **Chat:** your messages use up to 85 percent of the reading width, and pasted text shows without Claude Code's pasted_content wrapper.
- **"No goal yet" is readable on every row in the light theme** (at least 4.9:1), and an agent's goal is asked for again only when your requests changed, at most every 30 minutes (Settings can turn it off).
- **Faster first load:** the dashboard tells the browser which of its scripts to fetch first.
- **A Sign out button in Settings > Browser sign-in ends this browser's session and clears its saved drafts;** your display preferences stay.
- **Blocked browser storage can no longer stop the dashboard,** and signing out of Coral Hub also clears the agent UI's saved drafts.

### Stability

- **The tray now runs Coral's server as a child it watches:** after a crash it restarts it (1, 5, then 30 seconds), and after 3 crashes in 5 minutes it stops and shows why, with Open Log and Retry. A wrong database password is never reused.
- **When the server can't start,** the tray menu says why instead of offering only Quit, and the "Server Disconnected" window names the right log.
- **A viewer that stops reading is cut off after a write has waited 15 seconds** (a terminal, the live feed, the Coral Hub bridges and event streams seen through the Hub), freeing its connection and stream slot. A terminal viewer that falls a whole buffer behind now redraws from a fresh replay instead of showing garbled output, and is closed if that keeps happening.
- **Coral checks its databases a minute after start** and tells you on the dashboard if one is damaged, with what to do; a database that takes more than 5 seconds to open is logged while Coral waits.
- **Turning the terminal input debug log off** now stops its writer and closes its file.

### Performance

- **Raw token usage is kept for 30 days, then rolled into hourly totals;** every total, count and latest value stays the same, and a re-read old turn is never counted twice.
- **Both databases trim their write-ahead log** after a checkpoint and are optimized after start and every 6 hours.
- **A quieter idle server:** the thinking tracker no longer opens every transcript every 2 seconds, board status reads every listed agent's tasks in one query, and the notifier reads every agent's subscription and unread count in one pass.
- **Startup answers sooner:** the pass over existing tmux panes runs beside startup, and a one-time event repair no longer runs on every start.

### For teams

- **Coral sends a task's review to the reviewers it names** (task add --reviewers, or a "Reviewers:" line), nudges an idle agent to claim work assigned to it, and asks a quiet agent for a one-line status, reporting it on the board if it stays quiet. These are on by default, with fixed texts, at most 3 actions per agent and 20 per board an hour.

## v1.4.72 — Your own side-panel tabs, a tray that restarts Coral, and a sturdier server

### Security

- **Agent and subscriber names that only look like a reserved name (a Cyrillic or Greek letter, or C0ral) now count as that name,** and agents can no longer post as Operator.
- **The update check never sends a credential:** a GITHUB_TOKEN in the environment is no longer attached to Coral's request to GitHub, and the answer is cached for an hour (Check for Updates asks GitHub again at most once a minute), so the dashboard can't make Coral call GitHub over and over.
- **Values that end up in the page are checked first:** a team colour must be a #rrggbb colour, and a Coral Hub update notice must look like a version, or neither is shown.
- **Two parser fixes found by fuzzing:** strict JSON replies refuse a key that differs only in case, and an origin whose host is only dots is refused.

### Dashboard

- **Choose which tabs show in an agent's or a team's side panel and put them in any order:** the tune button at the end of the tab row, or right-click the row. Chat always stays shown. Reset to default puts them back.
- **The sidebar header is one tidy row:** the collapse button is last on the right; the search box, server button and icons share one height; the server button always shows its dot, count and Update marker; and a click outside closes the server list and the display menu.
- **A server's menu** sits in the same column as the team, folder and agent menus, and starts with Launch team, Open folder and Launch agent.
- **The collapsed sidebar looks like the expanded one:** plain text in the same colours, with the selected agent on the same highlight.
- **Coral's own notices (unread messages, health checks, stop-hook feedback) fold into the steps they prompted,** and a short reply to them folds too, with its first sentence as the group's title. Expanding a group shows them as before.
- **Chat:** your messages use up to 85 percent of the reading width, and pasted text shows without Claude Code's pasted_content wrapper.
- **"No goal yet" is readable on every row in the light theme** (at least 4.9:1), and an agent's goal is asked for again only when your requests changed, at most every 30 minutes (Settings can turn it off).
- **Faster first load:** the dashboard tells the browser which of its scripts to fetch first.
- **A Sign out button in Settings > Browser sign-in ends this browser's session and clears its saved drafts;** your display preferences stay.
- **Blocked browser storage can no longer stop the dashboard,** and signing out of Coral Hub also clears the agent UI's saved drafts.

### Stability

- **The tray now runs Coral's server as a child it watches:** after a crash it restarts it (1, 5, then 30 seconds), and after 3 crashes in 5 minutes it stops and shows why, with Open Log and Retry. A wrong database password is never reused.
- **When the server can't start,** the tray menu says why instead of offering only Quit, and the "Server Disconnected" window names the right log.
- **A viewer that stops reading is cut off after a write has waited 15 seconds** (a terminal, the live feed, the Coral Hub bridges and event streams seen through the Hub), freeing its connection and stream slot. A terminal viewer that falls a whole buffer behind now redraws from a fresh replay instead of showing garbled output, and is closed if that keeps happening.
- **Coral checks its databases a minute after start** and tells you on the dashboard if one is damaged, with what to do; a database that takes more than 5 seconds to open is logged while Coral waits.
- **Turning the terminal input debug log off** now stops its writer and closes its file.

### Performance

- **Raw token usage is kept for 30 days, then rolled into hourly totals;** every total, count and latest value stays the same, and a re-read old turn is never counted twice.
- **Both databases trim their write-ahead log** after a checkpoint and are optimized after start and every 6 hours.
- **A quieter idle server:** the thinking tracker no longer opens every transcript every 2 seconds, board status reads every listed agent's tasks in one query, and the notifier reads every agent's subscription and unread count in one pass.
- **Startup answers sooner:** the pass over existing tmux panes runs beside startup, and a one-time event repair no longer runs on every start.

### For teams

- **Coral sends a task's review to the reviewers it names** (task add --reviewers, or a "Reviewers:" line), nudges an idle agent to claim work assigned to it, and asks a quiet agent for a one-line status, reporting it on the board if it stays quiet. These are on by default, with fixed texts, at most 3 actions per agent and 20 per board an hour.

## v1.4.71 — Phones and larger text, the dashboard by keyboard, and leaner background work

### Security

- **A failed terminal command no longer writes what was typed into coral.log, and repeated failures are logged once with a count.** tray.log and app.log now rotate like coral.log.

### Fixes

- **Coral no longer stalls an agent's messages behind Claude Code's bug-report or session-survey prompt:** it waits for the prompt to close, never types into it, and shows that delivery is held.
- **Chat no longer hides an agent's reply inside a collapsed steps group when more work follows it in the same turn** (for example after a board notification). Short notes written alongside a tool call still fold into the group.
- **Pressing Enter in a team's preset name box saves the preset only.** Before, it also saved the whole team settings form (working mode, cross-team and health settings).
- **On a Mac with exactly 1 or 16 open network connections, Coral's check of who owns a local connection misread the system's socket list and refused the request;** it now reads the list correctly.
- **A large redacted reply through Coral Hub could occasionally be refused as over its size budget while another was finishing;** the budget is now released before the reply is sent.
- **Phones:** the selected agent's header no longer pushes its last buttons off the screen, and small controls across the dashboard (the Chat/Terminal toggle, Find, Copy, dialog close buttons, a password field's Show and more) are 44 px touch targets. Landscape and 200 percent text work too: at 200 percent zoom the page no longer scrolls sideways.
- **Text follows a larger text size:** the dashboard's type scale, the composer, the home screen and the headings are in rem, the same size as before at the default setting. The composer grows with the text, and an empty phone composer is tall enough for its placeholder.
- **More of the dashboard works by keyboard:** the Artifacts panel and the Team view keep the focus through a refresh, and the Team settings tabs and the Team Artifacts tab work by keyboard.

### Performance

- **The agent screen readers share one capture:** the poll, the login-screen check and the team status's activity line run one capture-pane between them while the screen is unchanged, and a new keystroke still shows on the next read.
- **Coral's own call statistics keep per-call detail for 24 hours, then hourly totals for the rest of the 14 days;** after the upgrade the older per-call rows are summarized over the first few runs.
- **Board posts no longer make Coral re-check every agent on every board.**
- **Quieter logs:** a task completed outside an agent session no longer logs a warning, and a working folder that isn't a git repository is warned about once instead of every 2 minutes.

## v1.4.70 — Command-line signing hardened, a quieter first launch, and terminal fit fixes

### Security

- **A signed command-line request can no longer have its query changed:** the signature covers the raw query string. The command line's handshake (its hello) is limited per caller, so a flood can't push out another command line's handshake. A coral or coral-board command from an earlier version is refused by this server (and the reverse): use the coral commands that come with this Coral. Agents are not affected.
- **coral reset-password also replaces the command-line token,** so a program that read the old one can no longer act as the Operator. A command-line tool that started before the reset reads the new token once by itself. If a signature doesn't verify, the error says a coral command from another version may be the cause.
- **The docs say why Coral sends no HSTS** (put it on a TLS reverse proxy instead) and that renewing a TLS certificate needs a restart.

### Fixes

- **Knowledge distillation includes every current team member:** agents added later, sleeping agents and restarted agents; members stopped on their own are left out.
- **Agents show the model they actually use, and Coral can list each CLI's models.**
- **The first-launch "Enable remote access?" question is asked only in a terminal.** When Coral is started by the tray, a service, a script or CI, it no longer waits for an answer: remote access stays off and the log says how to turn it on.
- **The "Set a password" prompt closes once a password is set elsewhere,** for example in a browser while the Coral app's window stays open.
- **The terminal fits its window exactly:** the last row no longer ends in the bottom padding at some window heights.
- **install-cli.sh now links coral-hook-session-start too,** the one shipped program it missed, so a CLI-only setup gets every hook on its PATH.

### Performance

- **17% fewer tmux commands with many agents.**
- **Compressed replies reuse their compressors:** a 16 KB reply now allocates 45 KB instead of 852 KB and takes 14 µs instead of 56 µs.
- **A Claude agent's login screen is checked again only when its terminal writes something, or after a minute,** not every 3 seconds.
- **Session log memos and the CLI version cache no longer grow** with every session or CLI version the server has seen.

## v1.4.69 — Stability, agent launch limits, and the dashboard by keyboard

### Security

- **An agent can no longer start another agent with fewer restrictions: an agent's launch keeps only --model, and a saved team template keeps its choices only when launched as saved. Pages on another port of this computer can no longer read Coral's API.**
- **Every request body is capped** (8 MiB by default; uploads, agent artifacts and the editor's file save keep their larger limits), so one large request can't take the server's memory: it gets 413.
- **The opt-in profiler (pprof) says plainly that it has no credential,** in the log and in the docs.

### Stability

- **A panic in a background task no longer stops Coral:** every goroutine the server starts recovers it and logs it once with its stack; the server and its agents keep running, and services restart with backoff.
- **When Coral stops, workflow runs in progress end, with every process their shell steps started** (on Windows, a step's children end with it).
- **A stuck ps, tmux or osascript can no longer block Coral for good:** each runs with a deadline.
- **Long-running caches stay bounded,** and a scheduled run's first prompt ends with the run.

### Fixes

- **After a restart, Coral wakes the agents that were running once,** instead of repeating the wake and its "agents were running when Coral stopped" notice every few seconds (a bug since v1.4.49).
- **Through Coral Hub, a short busy moment no longer ends your sign-in:** a busy answer to a sign-in check is retried for up to 10 minutes, and sign-in has its own capacity.
- **Artifact uploads over the hourly cap now answer 429 Too Many Requests with Retry-After** (was 413); 413 stays for a file that's too large.
- **Windows: a burst of requests through Coral Hub no longer fails some of them,** and the process-tree check reads one snapshot.
- **A pty agent command with quotes or a backslash runs as written** (for example sh -c 'sleep 60').
- **With TLS on Coral's own port, an https dashboard on this computer gets the Sign in with Coral Hub button.**
- **The agent count shows on folder, team and server group headers in the sidebar too,** collapsed or not.

### Accessibility

- **The dashboard works by keyboard in more places:** every dialog moves focus in and back; the Docs list, the board's @mention list, the Tasks list and Notes, the Activity filter and the file tree work by keyboard; controls that appear on hover also appear on keyboard focus; and in the Chat composer Tab reaches the text box first.
- **19 dialogs and the Settings and prompt boxes have names for screen readers,** and seven contrast and focus fixes, including the sign-in page's links and Show button in the light theme.

## v1.4.68 — Every request needs a credential, a password for the browser, and Sign in with Coral Hub

v1.4.67 was tagged but never released (its release checks failed on the test scripts, not the product); this release carries all of it.

### Security

- **Coral now requires a credential for every request, also from this computer.** This update turns it on for existing installs too: an open tab or a bookmark lands on the sign-in page once; the Mac app, the tray and coral open sign you in as before, and Coral Hub keeps working. Open it from the app or tray or with coral open; scripts send the API key in a header (coral api-key prints it; the ?api_key= parameter is deprecated). The key keeps out other accounts and sandboxed agents, not programs you run yourself. CORAL_STRICT_LOCAL_AUTH=0 turns this off (weaker).
- **A password for the browser.** Any password you choose is accepted. On this computer, the sign-in page offers to create one. From another computer you need the one-time setup code: coral setup-code prints it on this computer (so does the console, if you started Coral from a terminal); it is never written to a log. "Keep me signed in" lasts 30 days. Wrong passwords slow down but never lock you out, and coral open and the API key keep working. Change it or sign other browsers out in Settings > Browser sign-in. coral set-password sets it from the command line, and coral reset-password replaces it, even with Coral stopped, and signs every browser out.
- **Sign in with Coral Hub (off by default).** It lets your Coral Hub account sign in to this computer's dashboard. It gives that account full control of this computer, so turn it on only for an account you trust: in Settings and in Coral Hub. If this computer can't reach Coral Hub, a Hub sign-in can last up to 10 more minutes. To turn it off, switch off Sign in with Coral Hub in Settings > Browser sign-in, which signs out every Hub sign-in at once. Hub sign-in is off on every computer until the owner turns it on in Settings and in Coral Hub, so an update changes nothing for existing links. Ending a browser session (signing out, a password change or reset, a new API key) also closes its open terminals and live views.
- **Browser sign-ins survive a restart** (they are kept in Coral's database, hashed), **and every sign-in cookie is now SameSite=Strict,** so a link into the dashboard from another site's page arrives without the cookie.
- **A sign-in link opened by another user of this computer is refused,** and the link stays good for its owner. coral open warns when it prints a link in an agent's terminal or in tmux.
- **Shell quoting:** a data folder with a space, an apostrophe or a shell character could break or alter a workflow's shell step, the terminal log path or a tools path; now they are quoted. A key name that starts with "-" is always sent as a key, never read as a tmux option.

### Fixes

- **Saving a file in the dashboard's editor works again (broken since v1.4.60).**
- **The API-key sign-in form works from another computer again:** it was redirected to the sign-in page instead of signing in.
- **A dashboard tab that is no longer signed in says so** and waits, instead of showing broken views.
- **coral-agent's artifact upload fails loudly when Coral refuses it** (including a 429 with its Retry-After), and never prints a dead coral:// link.
- **Settings and Servers:** checkbox rows have their box on the left, the Database encryption menu fits its text, and a server's error says what to do once, with the raw error under Details.

## v1.4.67 — Every request needs a credential, a password for the browser, and Sign in with Coral Hub

### Security

- **Coral now requires a credential for every request, also from this computer.** This update turns it on for existing installs too: an open tab or a bookmark lands on the sign-in page once; the Mac app, the tray and coral open sign you in as before, and Coral Hub keeps working. Open it from the app or tray or with coral open; scripts send the API key in a header (coral api-key prints it; the ?api_key= parameter is deprecated). The key keeps out other accounts and sandboxed agents, not programs you run yourself. CORAL_STRICT_LOCAL_AUTH=0 turns this off (weaker).
- **A password for the browser.** Any password you choose is accepted. On this computer, the sign-in page offers to create one. From another computer you need the one-time setup code: coral setup-code prints it on this computer (so does the console, if you started Coral from a terminal); it is never written to a log. "Keep me signed in" lasts 30 days. Wrong passwords slow down but never lock you out, and coral open and the API key keep working. Change it or sign other browsers out in Settings > Browser sign-in. coral set-password sets it from the command line, and coral reset-password replaces it, even with Coral stopped, and signs every browser out.
- **Sign in with Coral Hub (off by default).** It lets your Coral Hub account sign in to this computer's dashboard. It gives that account full control of this computer, so turn it on only for an account you trust: in Settings and in Coral Hub. If this computer can't reach Coral Hub, a Hub sign-in can last up to 10 more minutes. To turn it off, switch off Sign in with Coral Hub in Settings > Browser sign-in, which signs out every Hub sign-in at once. Hub sign-in is off on every computer until the owner turns it on in Settings and in Coral Hub, so an update changes nothing for existing links. Ending a browser session (signing out, a password change or reset, a new API key) also closes its open terminals and live views.
- **Browser sign-ins survive a restart** (they are kept in Coral's database, hashed), **and every sign-in cookie is now SameSite=Strict,** so a link into the dashboard from another site's page arrives without the cookie.
- **A sign-in link opened by another user of this computer is refused,** and the link stays good for its owner. coral open warns when it prints a link in an agent's terminal or in tmux.
- **Shell quoting:** a data folder with a space, an apostrophe or a shell character could break or alter a workflow's shell step, the terminal log path or a tools path; now they are quoted. A key name that starts with "-" is always sent as a key, never read as a tmux option.

### Fixes

- **The API-key sign-in form works from another computer again:** it was redirected to the sign-in page instead of signing in.
- **A dashboard tab that is no longer signed in says so** and waits, instead of showing broken views.
- **coral-agent's artifact upload fails loudly when Coral refuses it** (including a 429 with its Retry-After), and never prints a dead coral:// link.
- **Settings and Servers:** checkbox rows have their box on the left, the Database encryption menu fits its text, and a server's error says what to do once, with the raw error under Details.

## v1.4.66 — Agents can no longer use the Operator's controls, and the phone list can group by server

### Security

- **An agent's token is refused on the Operator's routes in every mode, and on commands that act on other agents (send, keys, kill, restart, sleep, wake, auto-accept) and on working-mode, groups and team knowledge writes.** On a hub, an agent's token is also refused on the hub proxy, so an agent can't act on another server. Run these yourself with the API key or from the dashboard; the agent docs say which calls are the Operator's.
- **Logged request URLs hide login_token and any value whose name ends in _token, _key or _secret,** so a sign-in link's token can't end up in coral.log.
- **Unix: a special file planted at a log path can't hang Coral.** Coral's log and cache writes open without blocking and refuse anything but a regular file.
- **CORAL_DBCRYPT_TEST_EXIT_PHASE, a test switch that stops the database encryption migration, is now read only by test builds,** so a shipped Coral can't be made to exit mid-migration by an environment variable.

### Fixes

- **Turning off auto goals or turning on auto summarize through the settings API now takes effect.**
- **A Coral running as a hub: the 'Coral was updated, reload to use the new version' banner goes away after a reload.** A connected server's build was added to the hub's own, so the dashboard never matched its build and the banner came back on every load.
- **Phones: Group by server, Show server and Show summaries** are in the agent list's ≡ display menu. Below 768 px they used to be only in the sidebar, which phones don't show.

### Performance

- **The icon font is 119 KB smaller, and startup makes fewer requests.**

### Other changes

- **Code cleanup:** the server's route setup and the sessions handlers are split into smaller files, with no change in behaviour.

## v1.4.65 — Coral Hub: faster terminal, tier fixes agreed with Coral Hub, and Windows link hardening

### Security

- **Coral Hub's terminal no longer waits to show output, and viewers who can type (Interact or Launch) now see everything as it is.** Viewers who can only look still get tokens hidden, but a secret typed key by key, or split across two pieces of terminal output, can be visible to them in part (or, when typed, in full); a secret that arrives in one piece is still hidden. This changes only what is hidden in replies and the terminal: files named like secrets (.env) and routes a tier may not use stay refused at every tier, and logs and stored data are unchanged. Affected: v1.4.61 to v1.4.63 delayed the terminal echo; the split-secret exposure for View viewers applies from this release.
- **Coral Hub tiers, agreed with Coral Hub.** Through Coral Hub, a team's sleep, Sleep all, a board's pause and deleting a board message now need Launch, like their undo (wake, wake all, resume); one agent's sleep stays Interact. The agents' own board and task calls (mark-read, messages/check, peek, tasks/claim, tasks/current, submit-review, release-review, waits and waits/poll, groups and members, unsubscribe, and GET /api/sessions/resolve) can no longer be reached through Coral Hub at any tier. The query parameters target_session_id, include_ancestors, max_excerpt_chars, team_id and events_limit are no longer accepted through Coral Hub. A board subscribe through Coral Hub now subscribes the Coral Hub user, never the board's Operator. The dashboard dims Sleep all, a team's sleep, a board's pause and Delete message below Launch.
- **Windows: Coral's logs and caches are no longer written through a link or junction planted at their path** (inputlog's terminal debug log, logrotate, the hook cache, the pipe log, usage and stall files). Affected: Windows builds before v1.4.65, only where an agent can write the folder.

### Fixes

- **Coral Hub shows your home folder as ~ however it is typed.** On Windows and macOS the home folder is matched without regard to case, and on Windows with either slash.
- **Same-instant timestamps (common on Windows) no longer drop a usage report or bring back an agent stopped on its own when its team stops.**

### Performance

- **Terminals reconnect with backoff.** After a server restart, open terminals retry after 1 to 2 s, then longer (up to 15 s, with jitter), instead of all at once every 3 s.
- **Pulsing status dots cost no main-thread paints** on an idle dashboard.

## v1.4.64 — Windows: a crash no longer blocks the next start, and the server's security checks now run on Windows

### Security

- **coral-tray --stop never stops another program.** After a crash, a left-over tray.pid could name a process id the system had since reused, and `--stop` would end whatever had that number (on every OS). A pid now counts only while it is the same Coral tray executable.
- **The Windows launch hand-off script is private to you.** A long launch command goes to PowerShell through a script in the temp folder. That script now gets an owner-only access list before the command is written, so it stays private even if TEMP points at a shared folder.
- **The server's security tests now run on Windows.** Coral's sign-in, auto-accept tier, forged-stamp and cloud same-user checks are now tested on Windows in CI, as on Linux and macOS (until now that package was only compiled there).

### Fixes

- **Typed output shows on the next poll again.** In v1.4.63, a dashboard tab could show the screen from before your last keystroke for up to 2 seconds (the shared terminal capture didn't notice new output); a capture is now shared only while the output is unchanged.
- **Windows: Coral starts again after a crash or power cut.** The single-instance lock is now one the system releases when Coral stops, instead of a file that stayed behind and made Coral refuse to start ("another Coral server is already running") until it was deleted by hand.

## v1.4.63 — Windows hardening, fewer background processes, and smoother review and board flows

### Security

- **The same-user check now covers macOS and Windows.** Coral's cloud routes refuse a request from another local account on macOS and Windows, as they already did on Linux (403 `other_user`). Both checks fail closed when Coral can't attribute a connection, except that on a macOS version whose connection table Coral doesn't recognise the check is skipped (as before this release) and the reason is logged once, so an OS update can't lock you out.
- **Secret files on Windows are private to you.** The API key, the sign-in token, the command-line token and each session token are written with an owner-only access list (you and SYSTEM). A file found readable by others is refused; for the API key, Coral treats the key as exposed and makes a new one, so clients holding the old key need the new one.
- **Windows: a long launch command no longer leaves its script behind, and non-ASCII text in it is read as written.** Coral hands a long launch command to PowerShell through a temporary script. On Windows with a temporary folder whose path has a non-ASCII character (an accented or curly-apostrophe user name), the script was read in the ANSI code page by Windows PowerShell 5.1, so it could not delete itself and any non-ASCII text in the command (a prompt, a name, a path) was misread. The script now starts with a byte-order mark, and the paths Coral gives PowerShell are literal, so folders with [ ] in their names work too. Also: PowerShell quoting now covers the curly quotes ‘ ’ ‚ ‛ (a launch flag or path with one could end its quoting early). Affected: Windows builds from the v1.3.31 line through v1.4.62, for users with a non-ASCII or [ ] character in their temp folder path; the curly-quote quoting applies to every Windows launch whose flags, imported team values or paths contained one.

### Fixes

- **A rejected task can be resubmitted without claiming it again,** so another task in progress doesn't block the fix.
- **An unsent board message survives an agent switch** within the same board.
- **The Servers list says when a machine needs an update.** Through Coral Hub, a machine below the Hub's minimum version shows "Update Coral to <version>" in the Servers list and its details.

### Performance

- **Fewer git and tmux processes** (571 to 334 in 80 seconds with 40 agents). The git call that names a repository's filters runs only before commands that can run a filter, and one tmux call answers every agent's pane size and process instead of one call per agent.
- **Dashboard tabs watching the same agent share its terminal capture** for 2 seconds instead of each reading it.
- **A transcript's first read is bounded** (8 MiB), so a large transcript is no longer decoded whole at once.

### Other changes

- **Code cleanup:** unused code removed across the server.

## v1.4.62 — Agents stay on their own boards, a sign-in card, and a lighter server and dashboard

### Security

- **An agent's board requests work only on boards its session is subscribed to.** Before, an agent with a session token could read another team's board by name (for example `GET /api/board/<other>/messages/all`) unless strict local auth was on. Now any request on a board the session isn't subscribed to gets 403 `identity_mismatch` in every mode, including `coral-board status`, `working-mode` and `wait --board`. Joining a board and cross-team sends still work. Affected: v1.0.0 through v1.4.61, servers with more than one board.
- **Notices and identity are tied to the right session.** coral-board's process-tree identity (used when an agent's environment is stripped) is accepted only for a process the session's terminal backend reports as its own now, nearest ancestor first, so a reused process number can no longer hand an agent another session's board and role; and a board notice (mention, task assignment, wait, nudge) is typed only into a live agent session subscribed on that board, by its exact type and id.
- **coral-board outside an agent session.** The Operator's plain-terminal `coral-board` keeps its board in one fixed state file, and a process that carries an agent's markers but no session gets no board. There is no longer a state file per host name. Without a state file, `join` says it isn't in an agent session and exits 1.
- **Corrections to v1.4.61's notes**, also made in its release body: the betterleaks rules do not redact a secret-named query parameter followed by another parameter (`?api_key=...&limit=5`; the last parameter is usually caught), a URL password whose host is example.com or example.org (the rules treat it as a placeholder), OpenAI keys (sk-, sk-proj-, sk-svcacct-) written on their own, or project keys of the current 150+ character form even after a name (`OPENAI_API_KEY=...`).

### Fixes

- **A Claude agent waiting for a sign-in says so.** Its chat shows a sign-in card that opens the terminal, the agent list labels it "Sign in", and the usage-limit notice comes from the CLI's own error instead of any "usage limit" text on the screen.

### Performance

- **Server.** Idle CPU about 78% lower with 40 agents (machine health 0.85 s -> 0.06 s a minute); a board poll with nothing new 60 ms -> 0.4 ms on a 300,000-message board; a server on a large board serves in 1.9 s instead of minutes, and the unread notifier is ready in about 3 s on a large board; an unread mention more than 2,000 board messages old no longer nudges or flags the agent (coral-board read still shows it). Machine health samples only while a dashboard or Coral Hub feed shows it, and reads only the agents' own process trees; each session's count of edited files is kept as events arrive; and every dashboard tab and Coral Hub feed shares one build of the session list per interval.
- **Dashboard.** An agent switch settles in about 160 ms instead of about 340 ms, and idle layout work is down about 77% (one seeded setup: 40 agents, two tabs). Time formats, rendered markdown and the composer's size are reused instead of rebuilt; a long chat page is drawn in slices so it never blocks the page; the board panel keeps its messages across agent switches; and the pollers no longer rewrite an unchanged task list or activity timeline.

### Other changes

- **API.** `/api/sessions/resolve` returns `session_name` as `<type>-<id>` (it was the bare id). `GET /api/sessions/live/{name}/subagents` and `/subagents/{id}` answer 404, not an empty list, for an unknown session and under a name that isn't that session's.

## v1.4.61 — Secret redaction from a maintained rule set

### Security

- **A message is read by id only through its own board.** coral-board read --id (and messages/all?id=) answered with a message from any board on the server, so an agent that walked message ids under its own board could read other boards' messages; another board's id now answers like a missing one. It needed an agent or user who could reach the same Coral server's board API; boards on different servers were never reachable. Through Coral Hub it was reachable at the View tier (the same GET route), so a Hub member with View access to a computer could read other boards' messages on that computer by id; fixed by the same change. Affected: v1.0.0 through v1.4.60, servers with more than one board.
- **Secret redaction now uses betterleaks' rules (v1.9.0, about 460 rules) and only them.** This covers what Coral Hub viewers see, agent events stored on this computer, and coral.log. Coral's own detection rules are no longer used.
  - Newly redacted: Stripe and npm tokens, plus the rest of betterleaks' provider rules. Fewer false markers appear in ordinary code.
  - No longer redacted:
    - Authorization header values (`Authorization: Basic ...`, `Bearer ...`, `Digest ...`, and the JSON form); only a `curl -H "Authorization: ..."` line is caught.
    - Low-entropy values after a secret name (`token=abc123`, `secret: "changeme"`, `api_key=12345678`, `--password=hunter2`, a passphrase with spaces).
    - Any value named `passphrase`.
    - A URL password containing a raw `/`.
    - A secret directly after another keyword (`token: Authorization: Basic ...`).
    - Everything nested under a secret-named JSON key (`{"secrets":{...}}`), and a number under one (`{"password": 12345678}`).
    - AWS access key IDs that don't decode as real ones, the AWS documentation example key, and shorter or looser token shapes than the providers issue (a 30-character `github_pat_`, a 40-character `sk-ant-` key, a loose `xoxb-`).
    - OpenAI keys (sk-, sk-proj-, sk-svcacct-) written on their own, and project keys of the current 150+ character form even after a name (OPENAI_API_KEY=...).
    - A secret-named query parameter followed by another parameter (?api_key=...&limit=5; the last parameter is caught), and a URL password whose host is example.com or example.org (the rules treat it as a placeholder).
  - Text the rules can't check in time (3 s for a Coral Hub reply or stream unit, 0.25 s for a coral.log line; input dense with keywords) is not passed on: Coral Hub shows "could not be checked", and coral.log has a note in place of the line.
  - A secret in terminal output is checked line by line; a line longer than 8 KiB is checked in overlapping pieces. An unfinished line is held until it ends, or for up to a second when the output goes quiet; then it is shown, except a token-like tail.

### Fixes

- **The History tab shows auto-accept events like the live view.** It uses the live view's icons, titles and filters (an Auto-accept filter included), and its filter list shows each group's count as soon as it opens.
- **Dialogs no longer stack a second Escape listener** when opened twice without closing (the team wizard and create-task).

### Other changes

- **Smaller downloads, slightly larger programs.** An unused 6.9 MB image is no longer built into the Coral programs, and coral, coral-tray and launch-coral now include the betterleaks rules (about 8.5 MB), so each of those is about 1.6 MB larger. The packages are smaller than v1.4.60's, because the rules compress well and the image didn't: the macOS DMG is 128 MB (was 139), the Linux tarballs 44 and 40 MB (were 49 and 46), and the Windows installer 94 MB (was 96).
- **License notices in every package.** Each package now has a `licenses` folder listing the third-party components Coral includes, with their licenses and where their source is: the Linux tarballs, the Windows ZIP and installer (`Coral\licenses`), and the macOS app (`Coral.app/Contents/Resources/licenses`).
- **A tidier dashboard.** Large dashboard files are split into smaller modules, and the dialogs' parts load on first use. Code nothing used is removed from the server.

## v1.4.60 — Navigation that fits every screen, safer review verdicts, and a lighter dashboard

### Fixes

- **The top navigation fits at every width.** The tabs keep today's look with their spacing fixed: at tablet widths they no longer run together, and the row never wraps. When the tabs don't fit (large text, zoom, more tabs), the last ones move under a More menu, never the active one. On phones, one dropdown in the top bar names the current view and lists the others.
- **The phone's Send button is dimmed again for a View-only agent** (a regression in v1.4.59). The server refused the send either way.
- **A review verdict goes to the revision the reviewer read.** `coral-board task approve` and `reject` without `--round` send the round the reviewer last saw, so a verdict typed after the author resubmits is refused instead of applying to a revision they haven't read.

### Other changes

- **A smaller dashboard.** Code left from the old inline handlers is removed, along with exports nothing else uses and about 900 lines of CSS that styled nothing. A test now fails on a stylesheet rule that nothing builds. The @file and /command lists share one implementation.

## v1.4.59 — A redaction fix for v1.4.58, secrets redacted at rest, and controls that say what they do

### Security

- **A long value no longer lets the next secret through.** In v1.4.58, a quoted or secret-named value of 508 to 511 bytes followed directly by another credential (another secret-named value such as DB_PASSWORD, or an Authorization header) could let that credential through Coral Hub unredacted. Only v1.4.58 was affected (v1.4.57 is not).
- **The redactor is fuzz-tested.** Go fuzz targets now feed the Hub redactor random input, split across writes, raw and inside JSON. Each target checks that a planted secret never shows, that the JSON stays valid, and that a 64 KB input is redacted within a second.
- **Secrets are redacted where Coral stores them.** Agent events and every line of coral.log are redacted before they are written; a panic's value is logged through the same redaction.
- **The board's Export dialog warns that the export may hold secrets and is not encrypted.**

### Fixes

- **Controls the Coral Hub doesn't allow are dimmed.** Through Coral Hub, a control that the machine's access level (View or Interact) doesn't allow is dimmed, and its tooltip gives the reason. Before, it looked available and only answered "Blocked" when clicked.
- **A sleeping agent's Terminal view keeps its Wake bar,** and View-only access through the Hub really dims Send.
- **No more silent controls.** Send with an empty box flashes the box, and the team launcher's Move up and Move down buttons are disabled at the ends of the list.
- **Task reviews survive a rejection.** When one reviewer rejects a task, the other reviewers keep their review tasks. A resubmission opens the next round for the same reviewers. A verdict is refused while the task is back with its author, and a verdict on an earlier round is refused (`coral-board task approve/reject --round N`).

### Other changes

- **API: the compatibility paths are gone.** The board routes take `subscriber_id` only. A Coral Hub chat post must name its Hub user. `PUT /api/system/hub-operator` takes only `{"disabled"}`, and its tunnel GET answers `{"disabled"}` only. `POST /auth` is gone; use `POST /auth/key`.

## v1.4.58 — Redaction by structure and by name, smaller replies, and tabs that notice updates

### Security

- **Redaction by structure.** Through Coral Hub, a JSON reply that Coral holds whole is now redacted by its structure: each key and value is redacted as the text it stands for, at any depth, and the reply is written back valid. A value is redacted together with its key, so `{"Authorization": "Basic ..."}` is caught, and every string under a member whose key contains "authorization" is treated as an Authorization value however it's nested.
- **Values named as secrets are redacted.** A value whose name marks it as a secret (password, passwd, passphrase, secret, secret_key, access_key, token, auth, api_key, private_key, credential and the like, so access_token, client_secret and AWS_SECRET_ACCESS_KEY too) is redacted, by its key in a JSON reply (and everything under such a key, whatever it holds) and as `name=value` or `name: value` in text. Limits: an unquoted value of letters only after a colon (`password: correcthorse`) is kept on purpose, and an unquoted value after a space-separated flag (`--api-key VALUE`) is not covered.
- **Passwords in URLs are redacted.** The password in a URL (scheme://user:password@host, in postgres, mysql, redis, mongodb, amqp, https and the like) is redacted through Coral Hub, raw and in JSON, whatever characters it holds (a raw / or ? or # too); a port (https://host:8443/path) and a plain user@host stay as they are. A password over about 1 KB, or with non-ASCII characters, is not covered.

### Fixes

- **Smaller replies.** JSON replies of 1 KB or more from /api/ are gzipped for a browser that accepts it, so the board's message page and other mid-size replies no longer go out uncompressed.
- **A tab left open across an update says so.** A dashboard tab that loaded an older build shows that the server was updated and reloads itself when idle, and the offline page cached by the service worker stays current.
- **Fewer terminal reads for chat.** One read of a session's prompt options answers its chat polls for 2 seconds, so an agent without prompt hooks costs one screen read per 2 seconds instead of one per poll.
- **The diff view has no dead buttons.** The file preview's read-only Diff mode no longer shows Accept and Reject buttons that did nothing.

### Other changes

- **No code for older servers.** The dashboard and the server ship together, so the dashboard's fallbacks for older servers and reply shapes are removed.
- **Dependencies.** chi 5.5.0, go-chi/cors 1.2.2, golang.org/x/sys 0.49.0, x/term 0.47.0, gopsutil 4.26.9 and fsnotify 1.10.1. The vendored CodeMirror bundle is rebuilt from a recorded, pinned input and checked byte for byte.

## v1.4.57 — Redaction fixes, lighter chat pages, and PowerShell for Windows agents

### Fixes

- **Chat search hits in older history are highlighted again.** A search hit in an older part of a long history opened its page but wasn't highlighted or scrolled to (a regression in v1.4.56).
- **Lighter chat pages.** A chat opens with its latest 30 messages instead of 100, and long tool fields (a tool's output, a written file, an edit) are cut to 2 KB on the page; opening a card shows the full text. Through Coral Hub this needs a Coral Hub built with the v1.4.57 library; the dashboard a Hub serves always matches its library. A switch back to an agent you saw in the last 5 minutes shows its kept page at once and fetches only what came after.
- **The board chat panel asks only for new messages;** deleted messages disappear within a poll.
- **The pending tool call and prompt options now come with the chat reply;** GET /pending-tool and GET /prompt-options no longer exist, so a Coral Hub or dashboard older than this build will not find them.
- **Lower CPU use.** The session indexer reads a growing transcript from where it stopped, and the git poller runs one `git status` per repository per pass instead of two.
- **The older-hooks banner no longer covers the home page.**
- **Windows agents start through PowerShell.** An agent's terminal never starts cmd.exe: Windows starts pwsh.exe, then Windows PowerShell, and with neither the launch is refused with "install PowerShell to launch agents". A `CORAL_SHELL` naming cmd is ignored.

### Security

- **Authorization headers are redacted however they're quoted.** A quoted Authorization header with a Basic credential in agent output, and a Digest Authorization header, are now redacted through Coral Hub (they could pass in earlier releases, including v1.4.54 and v1.4.55).
- **Cut chat fields never show part of a secret.** A tool output cut short in a chat page never ends in the start of a secret.
- **Kept chat pages are forgotten on sign-out** and when the Coral Hub account changes.
- **Terminal sessions are found by session only.** With the pty terminal backend, Coral finds an agent's session by its session id or exact name, never by its folder, so teammates working in one folder can't reach each other's terminal.

### Other changes

- **Pinned test dependencies and wider vulnerability scans.** CI installs the frontend test dependencies exactly from a tracked lockfile without running install scripts, scans the whole module for reachable vulnerabilities in every build tier, and checks that `go.mod` is tidy.
- **API: the chat route takes a trim parameter** (the dashboard sends trim=2048); a Coral Hub in front of this build must allow it.

## v1.4.56 — Faster chat history, sends that no longer stall, and replacing a refused key

### New

- **Replace a refused secret key.** When Coral refuses its secret key file (other accounts could read it), Settings > Coral Hub offers Replace the secret key. After a confirmation that lists what it discards (the Coral Hub connection and the remote servers' API keys), it makes a new key; the steps to recover by hand are also in the message. It's offered only when Coral refuses the key, never when the file just can't be read.
- **Set Coral Hub's access at launch.** `coral --cloud-max-mode view|interact|launch`, or `CORAL_CLOUD_MAX_MODE`, sets the most Coral Hub may do on this computer at every start; an invalid value stops Coral with exit code 2. While the flag or variable is set, a cap changed in the dashboard is replaced at the next start.
- **Task reviews on the board.** submit-review hands a task to the team's orchestrator, who completes it or delegates the review with coral-board task review; each reviewer runs task approve or task reject --note. The last approval completes the task, a rejection returns it to its author with the note, and a team with no orchestrator completes on submit. Review commands are not available through Coral Hub.
- **"Run agents at lower priority".** The agent priority setting is now a checkbox, on by default: agents and everything they start run below Coral itself, so the dashboard, Coral Hub and your own apps stay quick. It changes scheduling only and applies to agents started afterwards.

### Fixes

- **Sends no longer stall behind a long paste.** A long automated paste that reached the agent's input box could be read as one that didn't, and every later board message and nudge then waited behind it. Coral now reads the whole pane for the input box, and a held paste of its own that it can't confirm gives up after a minute (10 seconds with an @-mention behind it), with a notice, instead of blocking every later send.
- **Chat history loads faster.** A resumed session's earlier transcript stays cached instead of being re-read every minute, a cold read decodes in parallel, and the goals tracker reads a transcript from its end, so a long Claude session's chat is no longer re-parsed whole every minute.
- **Lower CPU use.** Agent log status is read incrementally, with a one-pass cleaner for terminal escape codes.
- **Quicker agent switches.** The dashboard no longer loads the file search list on every agent switch; a board's status is computed once for requests that arrive together. The selected agent's polls and terminal stop when you go back to the list, open another view or hide the tab.

### Security

- **Browser storage per Coral Hub account.** Drafts, pending messages, recent folders and starred files are kept per Coral Hub account, removed on sign-out, and old drafts and star lists age out (3 and 30 days).
- **One session cookie per Coral.** Over plain HTTP on this computer (localhost), browsers send Coral's session cookie to every other web server on the same host name, whatever its port; Coral now names the cookie per server so two Corals don't sign each other out, and the session lasts 24 hours. Existing sign-ins keep working until they expire. Use HTTPS (the cookie is then a __Host- cookie) or the strict local auth setting where other people run servers on the same computer. Signing out ends this server's session under every cookie name. API replies are marked no-store by default (artifacts and ETag-validated replies keep their own caching), and the QR code's reply, whose address can carry a sign-in token, is no longer cached for an hour.
- **Large replies through Coral Hub.** Through Coral Hub, a reply over 2 MiB is refused instead of streamed (the dashboard pages history and the board export), and the computer checks at most 32 MiB of replies at once, answering busy beyond that. The dashboard says when a reply was too large instead of showing an empty view.
- **Review verdicts and strict local auth.** Review verdicts are as strong as strict local auth: with it off, a local caller without a token can name any subscriber on board routes.

## v1.4.55 — Chat history through Coral Hub, Windows key files, and no inline scripts

### New

- **Resume agents on the current hooks.** The dashboard lists the Claude agents still running an older hook set, by team, and resumes one on the current set while keeping its conversation, settings, worktree and board. An agent in the middle of a turn or waiting on a prompt is skipped unless you confirm.
- **coral-board post - reads the message from input.** A lone `-` as the message of `coral-board post` or `send`, or as a task's `--message` or `--body`, reads the text from standard input, as written: `coral-board post - <<'EOF'` posts multi-line text with backticks and `$( )` untouched. Empty input is refused and nothing is sent.

### Fixes

- **Chat history through Coral Hub loads again.** A long Claude conversation that quoted a private key inside code failed to load through Coral Hub; the reply was cut in the middle of the JSON. It loads again.
- **Windows: Coral Hub enrollment and remote API keys work.** On Windows, Coral refused every existing key file, because it checked a Unix file mode Windows never reports. Since v1.3.42 that broke storing the Coral Hub token and remote servers' API keys (an enrollment could be left half done), and since v1.3.14 the same check applied to the database key. Key files are now checked by their Windows access list: readable only by you, SYSTEM and Administrators. A key file other accounts could read is refused, with the steps to recover by hand. Enrollment now checks the token can be stored before enrolling.
- **Confirmations work after a board Export.** The board Export dialog no longer reuses the shared confirmation dialog, so later confirmations work. Deleting a board message and killing a workflow run now ask first, and the last browser-native confirm prompts use Coral's own dialog.
- **SQLite 3.53.4.** The database engine moves from SQLite 3.50.4 to 3.53.4, which carries SQLite's fix for a WAL-reset corruption bug.
- **Messages that start with "-" reach tmux agents.** A message or nudge starting with `-` (a bullet) was read by tmux as an option and dropped, and Enter was still sent. It's now typed as text, and no Enter follows a failed send.

### Security

- **No inline scripts in the dashboard.** The dashboard no longer runs inline scripts or handlers; Coral Hub's copy already had the same policy.
- **The token redactor no longer leaks an escaped key.** The Coral Hub View-reply redactor could leave part of a private key in a reply when the key was preceded by a backslash. It now ends a key only at an unescaped quote, and a JSON reply (up to 16 MiB) that redaction would leave invalid is never sent.
- **Operator-only routes are enforced everywhere.** Five artifact routes (delete an artifact since v1.4.35; retention acknowledgement, prune, and keep/unkeep since v1.4.39) were missing from the agent guard, though their own handlers refused agents anyway. Every Operator-only route is now enforced from one table, and the server won't start if the table names a route it doesn't serve. The hub's remote-server registry and the board's remote subscriptions are now the Operator's too. These routes are refused to agents when strict local auth is on, and recorded in the identity report otherwise.
- **Board actions need the right person.** Deleting a board message works only through its own board. Deleting messages and pausing or resuming a board are the Operator's (refused to agents when strict local auth is on, and recorded in the identity report otherwise); a pause survives a restart. Cancelling a task, editing its title or priority, nudges, reminders and snoozes need a planner or the assignee, and publishing needs a planner. Every board request body is capped at 4 MiB.
- **Safer file paths.** A network (UNC) path in a request is refused before Coral touches it. File previews read through a held-open folder: a FIFO or socket is refused for everyone, and a hard link to a file elsewhere is refused for Coral Hub and remote requests; the agent CLIs' credential files are denied by name. Themes, workflows and knowledge folders can't be named like a Windows device (`CON`, `NUL`, `COM1` and the rest); a theme or workflow already named that way becomes unusable and needs a new name.
- **Terminal pastes.** Pastes are cleaned of control characters; a multi-line paste asks first when an app has turned bracketed paste off; and the terminal no longer types its own query replies or focus reports into the agent (an app that asked for focus reports won't get them).
- **tmux panes are found by session only.** Coral finds an agent's pane by its session id or exact session name, never by a bare name or a pane title an agent can set.
- **Safer commands on Windows.** The Windows handoff script quotes an apostrophe in its path, and opening a browser never goes through `cmd.exe`.
- **A flag-like prompt stays a prompt.** A prompt that is only a flag (for example --dangerously-skip-permissions) is passed to Claude and Codex after --, so it is the prompt and not an option.
- **DOMPurify 3.4.17.** The HTML sanitizer moves from 3.3.3 to 3.4.17, and every vendored library is listed in a manifest a check re-derives.

### Windows

- **A colour icon everywhere.** The tray, the coral, launch-coral, coral-tray and coral-app programs, the Start Menu shortcut and the Apps & features entry show Coral's colour icon in every size.

### Other changes

- **Coral Hub routing.** Coral Hub can give this computer a route key, which Coral sends back only on its next connections to that hub.

## v1.4.54 — More terminals through Coral Hub, a rotating log, and safer hooks and updates

**After upgrading, relaunch or resume your Claude agents.** Coral's hooks now name their tools by absolute path, and Claude reads its hooks only when an agent starts, so agents started before this release keep the older hooks until they're relaunched or resumed.

### Fixes

- **More terminals through Coral Hub.** When Coral Hub names the viewer, one user can now keep up to 32 long-lived connections (terminals and live views) open on a computer instead of 4, and Coral Hub's own feed of the computer keeps a small reserve no user can take. The default pool sizes are now 64, 64 and 4 (general, long-lived and search). A terminal refused for too many open streams says which limit it hit and waits 15 seconds before retrying instead of 3; the dashboard shows that reason once Coral Hub runs this release's hub proxy.
- **coral.log rotates.** The server's log now rotates at 20 MB and keeps 5 copies, so it uses about 120 MB at most. An older, oversized log keeps only its last 5 MB when it first rotates. `coral-shadow-report` reads the rotated copies too, oldest first.
- **Update offers are checked.** The server and the tray share one update check: it offers a release only when it's strictly newer than this build (a dev or beta build is no longer told to move to an older release), and it ignores error replies, oversized replies and tags that don't look like a version.

### Security

- **Hooks are run by absolute path.** Claude, Codex and agy run Coral's hooks by their full path in Coral's tools folder, so a program with the same name in an agent's folder or earlier on `PATH` doesn't run in its place.
- **No exception entitlements on macOS.** Every signed binary in Coral.app now runs under the hardened runtime with no entitlements at all (no JIT, no unsigned executable memory, no exemption from library validation), and the release build smoke-tests the signed app before publishing it.
- **Private files in the data folder.** Coral's other logs, the instance lock, board state, the generated team, and the knowledge, workflow, pi session and theme folders are now created and kept readable by your user only (files 0600, folders 0700). coral.log and its copies are 0600 and, on macOS and Linux, never written through a symlink.
- **The sign-in token file doesn't linger.** The startup sign-in token's file is removed once its token is used or expires, a leftover is removed at startup, and the file is removed at a clean shutdown.
- **A retired table is wiped.** The unused `connected_apps` table, which could still hold sign-in tokens from a retired feature, is securely deleted and dropped at startup.
- **Fewer inline scripts in the dashboard.** The live agent view, its command pane, the sidebar and its menus, the history list, the board chat panel and the dialogs (the scheduled job dialog's flag shortcuts included) no longer use inline event handlers. Restarting, killing, renaming and resetting agents, groups and teams are on the shared list of destructive actions a popout guards.

### Other changes

- **The Windows downloads in the README.** The README lists the Windows MSI and portable ZIP, says they aren't code-signed yet (SmartScreen warns), and gives the `gh attestation verify` command for them.
- **Pinned build actions.** Every GitHub Actions step in Coral's workflows is pinned by commit, and Dependabot proposes updates.
- **Steadier Windows tests.** Two tests that failed intermittently on Windows are fixed; the remote feed's keepalive no longer counts a ping cut short by its own shutdown as a miss.

## v1.4.53 — Coral reads Claude's question and plan menus correctly

**After upgrading, relaunch or resume your Claude agents.** Claude reads Coral's hooks only when an agent starts, so agents started before this release keep the older behaviour until they're relaunched or resumed.

### Fixes

- **Claude's question and plan menus are read correctly by the signal-based gate.** The signal-based rule (gate phase 2, still off by default) now treats Claude's question and plan menus as waiting for you from the moment they open, and Coral's own "message held" notice no longer replaces the agent's open request. The current screen rule, which decides today, was already holding in these cases.
- **Agents started by an older Coral stay on the safe rule.** Every Claude hook now carries its hook set's version; an agent running an older or unknown set stays on the screen rule under the signal-based gate (phase 2, still off) until it's relaunched.
- **Quieter logs.** A disagreement between the agent's signals and its screen is logged once per occurrence instead of on every re-check.

### Security

- **Fewer inline scripts in the dashboard.** The task list, the task dialog and the dependency picker no longer use inline event handlers.
- **Look-alike names.** Agent names in Coral's notices also drop variation selectors, Hangul fillers and the braille blank, which draw nothing and could make two names look alike or a name look empty.

## v1.4.52 — The agent's team in its header, and fewer inline scripts

### New

- **The agent header names its team.** Under the agent's name, on a phone and on the desktop, the header shows the agent's team, and on a hub with remote servers its server ("Local" for this computer, as in the sidebar).

### Security

- **Fewer inline scripts in the dashboard.** The Cost, Workflows, scheduled jobs, History and message board views no longer use inline event handlers; their buttons and fields are wired up by Coral's own scripts instead. Content an agent writes can't name one of those actions. This is a step toward a dashboard policy that allows no inline scripts at all.
- **Invisible characters are removed from board posts.** Zero-width spaces, invisible operators and tag characters, which can hide or fake what you read, are dropped from board posts, task titles and agent screens in the command line, and the agent names in Coral's notices lose every Unicode format character too. Joiners that emoji and some scripts need are kept in posts.
- **The command line escapes board names.** Every `coral-board` command now escapes the board name in its requests, so a board name with `?` or `#` in it can't send a command to the wrong place.

## v1.4.51 — Fairer hub limits, and tidier tmux

### Security

- **Agent CLIs no longer inherit Coral's tmux socket.** New Claude, Codex and similar agents start without `TMUX` and `TMUX_PANE`, so a plain tmux command run by an agent no longer lands on Coral's tmux server, and the hooks identify an agent by its own session name first. For real isolation, choose the pty terminal backend.
- **Fairer Coral Hub limits.** When Coral Hub names the viewer, one user can hold only a share of each of this computer's connection pools, so one busy viewer can't use them all. A reply that stops moving for 60 seconds frees its slot (event streams and long polls are never cut), and the pool sizes are now settings.

### Behaviour changes

- **Coral's own tmux server no longer loads `~/.tmux.conf`.** It starts with an empty config (a server already running keeps its settings until it restarts). Set `CORAL_TMUX_CONF` to a config file to keep settings like mouse mode.

## v1.4.50 — Fewer rate-limit cascades, and steadier history

### New

- **A team's agents no longer hit the API all at once.** Coral spaces its own automated messages to the agents of one team 2–3 seconds apart, and launching, resetting, resurrecting or waking a team starts its agents 2–3 seconds apart too. Requests still answer at once, and your own typing, keys and kills are never delayed. Coral never throttles or retries the agent CLIs' own API calls.
- **A notice when agents stop on their own.** If agents end by themselves (rate limited, overloaded, an API error or an exit), Coral sends one notice per team: a dashboard notification and a board post from Coral Health Monitor.
- **Optional: resume agents stopped by an API error.** Settings › "Resume agents stopped by an API error" (off by default) wakes such an agent again after about 2, 5 and 15 minutes, then stops. Your own Wake resets the count.

### Fixes

- **History pages load reliably under load.** Several requests for the same session's history at once could make one of them answer "not found"; each request now reads the whole transcript itself.

## v1.4.49 — Agents wake after a restart, and an accurate "Key rejected"

### New

- **Agents wake again after Coral restarts (pty).** With the pty terminal backend, Coral puts agents to sleep when it stops cleanly (or marks them asleep at the next start after a crash) and now wakes them again when it starts, one at a time, up to the number set in Settings (8 by default; more after you confirm). They resume their conversation with their stored settings and permission mode; a tool that was running is not continued. You can turn this off, and Coral skips it if it has restarted several times in a row. Each woken agent on a board gets a short message from Coral, and you get one notice per start.

### Fixes

- **"Key rejected" means the key was rejected.** A self-hosted hub showed "Key rejected" for a remote whose key was fine, for example one reached through an SSH port forward or one answering a call that needed an agent's own token. It now shows "Key rejected" only when the remote refused the key, says so when the remote refused the host name instead, and clears the status as soon as you edit or test the key.
- **A remote that drops briefly no longer flashes offline.** Coral Hub keeps showing a remote server as online through a short drop and reconnect, and the live feed sends a keepalive every 30 seconds.

### Security

- **More Coral Hub limits (audit H1, H2, H4).** A board subscription's webhook URL can't be set through Coral Hub. Template names are checked against the repository's own listing before anything is fetched, with one fetch per address at a time and a bounded cache. Opening a file in your desktop editor is no longer reachable through the hub at all.

## v1.4.48 — Pinned folders, a terminal backend setting, and more hardening

### New

- **Pinned folders.** Pin a folder to keep it in the Agents list even when no agent is running there; an empty pinned folder stays until you unpin it. Its menu launches an agent, a custom agent or a terminal into it. **Open folder** on the home page and in the **+New** chooser pins a folder you pick by path or with Browse.
- **Terminal backend setting.** Settings › Terminal backend explains the choice between **tmux** (agents keep running through restarts, upgrades and crashes, but any program running as you can reach their terminals through the socket) and **pty** (better isolation; agents sleep when Coral stops and wake resumes them). A change applies when Coral restarts, and switching from tmux to pty first puts your agents to sleep. Under pty, a banner offers to end agents left in Coral's old tmux server. `--backend` still wins over the setting.

### Security

- **Tighter dashboard policy.** The dashboard now loads script files, audio and video only from Coral itself, and its forms can post only to Coral. Agent content can no longer contain video, audio, picture, svg or math elements, so it can't make your browser fetch remote media.
- **A notification's link shows where it goes.** A web link in a notification shows its host next to the label (a look-alike name shows as `xn--…`), and marks plain `http` links "not encrypted".
- **Every list is bounded.** A list request through Coral Hub or the dashboard (history, runs, board messages, events, deliveries) can't ask for an unbounded number of rows; a negative or oversized limit falls back to the route's default or maximum.
- **Coral Hub can't hold this computer's connections open without limit.** Requests from the hub share a bounded pool and time out once fully sent, and a busy hub gets a clear 429.
- **Secrets at rest.** Regenerating the API key fails if the key can't be saved (the key file is written atomically and checked on load). A webhook's delivery errors never store its URL, and webhook lists show URLs masked with a fingerprint. A live session's saved state names its MCP servers and nothing more.
- **Windows: junctions can't lead out of a session's folder.** Every path check resolves Windows junctions, so the file viewer, diffs and file content can't read outside the session through one, and a path that fails to resolve (for a reason other than not existing yet) is never treated as inside.
- **Windows: the data folder is private.** A new Coral data folder on Windows is readable only by you and SYSTEM. If an existing folder can be read by Everyone, Authenticated Users or Users, Coral warns at startup and on the dashboard; it doesn't change the folder itself.
- **The command-line token** is refused if its file has more than one name (a hard link).

### Fixes

- **Workflows on Windows:** path templates in shell steps now work (they expand with forward slashes, which Git's shell reads correctly).
- **Pending command-line sign-ins are dropped oldest-first on every OS** (Windows' coarse clock could drop a newer one).

## v1.4.47 — Security fix: agent content can never answer a prompt

**Security fix — please upgrade.** A button inside an agent's message could answer a pending permission prompt when the operator clicked it; agent content can no longer contain controls, and only Coral's own prompt card answers a prompt.

### Security

- **Agent content is never live UI.** Everything an agent writes (messages, notes, history, summaries, team knowledge) is rendered without buttons, form controls, dialogs or other interactive elements, and without ids, roles or data attributes that could pass for Coral's own. A task-list checkbox is still shown, as a disabled one.
- **Only Coral's own prompt card answers a prompt.** Clicking an answer or submitting a text answer works only inside a prompt card Coral created, never inside an agent-written plan or a card-shaped message.
- **Notification links go only where they should.** A notification's link may be a Coral doc, a page in the dashboard, or an `http(s)` address (opened in a new tab). Anything else is shown as plain text, and the server refuses it when the notification is posted.
- **Docs links stay inside Coral.** A link from one doc to another opens it in the Docs tab, and the docs view follows the same link rules as the message board.

### Windows

- **Safe file access works on Windows.** Coral now refuses a hard-linked file and follows junctions when checking that a path stays inside its folder. Drive-relative and drive-rooted paths (`C:x.png`, `\x.png`) are refused in auto-attach. Coral's own logs and workflow notes are never written through a hard link.
- **The signed command line can read its token on Windows again.** It had been refused since Oct 14, so commands that sign as you didn't work there. Repository paths from git are also reported in Windows form, so they compare correctly with Coral's own.

## v1.4.46 — Phone navigation

### Dashboard

- **The current tab stays in view on a phone.** At phone width the navigation strip scrolls so the tab you're on is always visible: when you open a page such as Docs or Analytics directly, switch tabs, or move through the tabs with the keyboard. In a narrow desktop window, the mouse wheel scrolls the strip sideways.

## v1.4.45 — Message board fixes, and subscribers on a phone

### Message board

- **The board view has a page margin.** The "Message Board" heading no longer touches the sidebar, and Delete, the Subscribers column and attachments no longer run past the window's right edge. Long subscriber names wrap instead of being cut off, and a wide image in a post no longer makes the board scroll sideways.
- **See a board's subscribers on a phone.** At phone width, where the Subscribers column is hidden, the board header has a Subscribers button that opens the list as a sheet. From it you can see who is on the board and open a subscriber's chat history.

## v1.4.44 — Private agent logs, safe base branches, and the last file-access fixes

**Please upgrade.** This release adds the last file-access fixes from this round of Security's audit. One cleanup step is recommended on shared Linux machines (below); nothing else needs any action.

**On a shared Linux machine, delete old `/tmp/*_coral_*.log` files.** Agents' terminal logs now live in your Coral data folder, readable only by you, and a running agent's log moves there when Coral reattaches to it. Logs left by agents that ended before the upgrade stay in `/tmp`, where other users could read them.

### Security

- **Agents' terminal logs are private.** Every install keeps agents' logs in `<data dir>/logs` (the folder readable only by you, each log readable only by you), never in the shared temp folder, and never writes a log through a link. Coral moves an older agent's log only when you own it.
- **A base branch can't reach git as an option.** A base branch starting with `-`, or containing whitespace, a control character or a backslash, is refused (400 naming `base_branch`) when you launch a team with a worktree, create or edit a scheduled job or a workflow, or run a task, including a Launch-tier request through Coral Hub. It's checked again before git runs, and a job whose stored branch is refused fails with a clear message.
- **PROTOCOL.md comes only from Coral itself.** A release build takes the PROTOCOL.md next to the Coral executable, never one in the folder the server was started from or in your home folder (unless you set `CORAL_ROOT`), so a repository or an agent can't plant every later agent's system prompt.
- **Upload listings stay on the board.** A board's artifact list shows a session's uploads only when that session is on the board, and an agent's token lists only its own uploads or its board's.
- **No guessable temp files for agent launches.** If Coral can't create an agent's prompt, settings or instructions file safely, the launch fails with a clear error instead of falling back to a name another user could guess. Restart and resume build the new launch before touching the old terminal, so a failure leaves the agent as it was.
- **More files are opened safely:** agents' history indexes (Claude, Codex, pi, agy, Gemini), auto-attached files and the command-line token are opened as regular files only, never through a symlink, and the token's checks apply to the file actually read.
- **Agent types and board names are checked where they enter,** so a crafted agent type, session id or board name can't make Coral write a log, a state file or an upload folder outside its own folders. The uploads folder is readable only by you.

### Behaviour changes

- **A board whose id contains `:` or `\` gets a fresh `b-` id** the next time Coral starts; its name is kept as the label.
- **An agent launch now fails with an error** when its temporary files can't be written, instead of starting without them.

## v1.4.43 — Safer local access behind a reverse proxy, several listen addresses, and file-access hardening

**Please upgrade.** This release closes more of Security's audit findings on file access and tightens how Coral trusts same-machine requests. Read the two behaviour changes below if you put Coral behind a reverse proxy or use strict local auth.

### Security

- **A reverse proxy on the same machine can't lend its clients local trust.** Tailscale Serve, Caddy or nginx reach Coral from 127.0.0.1, so a proxy that rewrote Host to localhost used to give every client it proxied, remote ones included, the no-credentials local pass. A request that carries a proxy header (X-Forwarded-For, Forwarded, Via, X-Real-IP, Tailscale headers and similar) is now treated as remote: it needs your key or a dashboard login.
- **New setting: `require_auth_on_loopback`** (off by default, Operator-only, never through Coral Hub). For setups where the proxy strips its headers, turn it on and every direct local request must carry a credential: an agent's session token, a dashboard login, the signed command line, an API key or a Coral Hub stamp. `CORAL_REQUIRE_AUTH_ON_LOOPBACK=off` turns it off without the dashboard.
- **Files reached through paths someone else chose are opened safely.** Saving a file from the dashboard, diffs, the changed-files list, Codex transcripts and the git poller now open files without following symlinks, refuse hard-linked files, FIFOs and devices, and stay inside the session's own folders. For a strict caller, nothing git runs or obeys (`.git`, `.husky`, the repository's hooks folder) can be written.
- **Diffs are bounded for every caller but this computer's dashboard,** not only through Coral Hub: a committed `.env` at the diff base is no longer returned, a home folder or the filesystem root is refused as a working tree, and a repository's `core.worktree` can't point Coral at an unrelated folder.
- **A session's teardown removes only its own temporary files,** and auto-attach refuses network (UNC) and device paths on Windows.
- **Codex's link file can only name its own transcript,** inside Codex's sessions folder, and every transcript read stays in that folder.

### Behaviour changes

- **A dashboard behind a TLS reverse proxy now logs in** instead of riding the local pass.
- **With `require_auth_on_loopback` on, a terminal or `curl` without a token no longer reaches Coral** from the same machine. Agents, `coral open` and `coral cloud connect` keep working, because they carry their own credentials.

### Listening on several addresses

- **`--host` takes a comma list,** for example `--host 127.0.0.1,100.101.102.103`, with one listener per address on the same port. A list of only network addresses also listens on 127.0.0.1 so this computer's agents keep a loopback address (logged at startup); `--no-auto-loopback` or `CORAL_NO_AUTO_LOOPBACK=1` turns that off. Agents always use the loopback listener.
- **Every-interface together with another address is refused** before anything binds, with a clear message.
- **A local request's Host must name this server** (localhost, a loopback address or an address Coral listens on), which closes DNS rebinding through a listened network address.
- The quickstart explains these, including the advice for computers other people log in to: keep the loopback listener and turn on strict local auth.

## v1.4.42 — Security hardening, a safer team cleanup, and an easier-to-read board

**Please upgrade.** This release closes a set of findings from Security's audit of the agent-facing CLIs, hooks and routes. None needs any action beyond upgrading.

### Security

- **Git ignores a repository's own config commands.** Coral runs git in agents' folders with the repository's command-running settings turned off: the fsmonitor, hooks, external diff and text-conversion programs, the pager and ssh command, and filter drivers. A repository can't make Coral's git run a program outside the agent CLI's permission prompts.
- **Board text and CLI output can't control your terminal.** `coral-board` and `coral-agent` strip terminal control sequences and text-direction characters from what the server sends, so a post can't rewrite, hide or fake output, set the title or touch the clipboard. Use `--raw` to see the exact bytes.
- **An agent's session token goes only to this Coral.** The hooks and `coral-board` send it only to loopback or the configured Coral, refuse a redirect to another host, and ignore a saved board state that points elsewhere. Board state files are now readable only by you.
- **Team import and the CLI check are yours, and bounded.** Importing a team and checking a CLI by path are Operator-only. A team import reads only regular files inside the folder (no symlinks, no hidden or sensitive paths, with size caps), and the CLI check runs only a known CLI or the path you configured.
- **Resume ids are validated.** A resume session id from a request must be a real session id for that agent type (Codex keeps its own ids), so it can't reach a path outside the agent's sessions. An id that isn't valid for the agent type now gets a 400 (most people will never see it).
- **Workflow run folders are private.** Each run's folder is created fresh and readable only by you, and a step can't make Coral write or read a file outside it, through a symlink or a hard link.
- **Completion checks run only inside the repository.** A registered `go_test` check accepts only `./`-relative package patterns, never a go flag or an outside path.
- **`launch-coral` is safe with any folder name.** A subfolder's name can't run commands through the terminal window it opens, on macOS or Windows.
- **Claude settings from a repository stay where Claude puts them.** Coral no longer raises a repository's Claude settings (permissions, env, statusLine, apiKeyHelper) to command-line precedence, and a repository's permissions can no longer replace your global deny rules; a repo's own hooks were already held behind Claude's folder-trust prompt on 2.1.296 (older CLIs not tested).

### Behaviour changes

- **Coral's own git no longer runs a repository's hooks or filter drivers, and no longer looks into submodules,** so the changed-file and diff views don't show submodule changes. Git LFS still works, git-crypt files stay encrypted in those views, and agents' own git is unchanged.
- **A registered `go_test` check's package must be `./`-relative,** for example `./internal/board`, not `internal/board`.

### Fixes

- **Removing a team never deletes uncommitted work.** When Coral cleans up an orphaned team's worktree, it now leaves the folder in place if git can't find its repository, and git removes it only when it's clean. Previously uncommitted and untracked changes could be lost. troubleshooting.md explains how to remove a kept worktree by hand.
- **`coral-board` reports failures.** A refused request (for example a 401 from a key-protected Coral at a non-local address) now exits non-zero with the status, instead of printing success while the message was lost. The error never shows the token.
- **HTML artifact previews keep their inline styles.** Inline styles and embedded fonts now apply in the dashboard's preview; scripts, external URLs, `@import` and images from the network stay blocked. (On Coral Hub the hub page's own policy still blocks an HTML artifact's style blocks in the preview; style attributes work, and downloading shows the page fully. Nothing about what the hub serves changed.)

### Message board and chat

- **An easier-to-read board.** In the board view and the team chat, each post shows the sender's initials in their colour at the left, the name and time on one line, and the body beneath, with no boxes or bars. Posts in a row from one sender share the avatar. A single line break in a post now shows as a line break, and sender names meet WCAG AA contrast in both themes.
- **Chat notices line up.** Coral's notices in the chat view (board and task nudges, health checks, "Interrupted") start at the left edge of the message column.

### Coming next

- **Gate phase 2 for Claude (off by default).** Built in, but not turned on: with the setting `gate_signals_claude` on, routine board nudges to a Claude agent wait until its turn ends, while a message that @-mentions it still goes at once. It stays off until a week of shadow results is clean. When it's turned on, a script's unsigned send to an agent in the middle of a turn waits instead of typing at once. `CORAL_GATE_MODE=screen` is the kill switch.

## v1.4.41 — Coral Hub moved to app.coralhub.dev

**Coral Hub moved to app.coralhub.dev; run `coral cloud connect --force` to reconnect.** The old address, app.coralai.ai, no longer serves Coral Hub, so a Coral connected there is disconnected until you connect it again.

### Coral Hub

- **The new address.** Release builds now connect to https://app.coralhub.dev by default. Coral's messages, the dashboard and the docs name the new host. A Coral that was never connected just shows Connect, which now goes to app.coralhub.dev.
- **Reconnecting.** A Coral enrolled with the old host doesn't connect after the upgrade. Settings › Coral Hub and the hub indicator in the header (amber) say "Coral Hub moved to app.coralhub.dev: run coral cloud connect --force". To reconnect:
  1. On the computer running Coral, run `coral cloud connect --force` and approve the code in your browser.
  2. In Coral Hub, revoke the server's old entry.
- **Your token stays safe.** Coral never sends its token to a host it wasn't enrolled with and never follows a redirect to a hub. Until you connect again, it contacts neither hub. The "moved" notice stays until reconnecting succeeds, and a failed attempt shows its own error next to it.

## v1.4.40 — Security hotfix: the task hook's cache stays in its own folder

**Security advisory — please upgrade.** Delete the old `/tmp/coral_task_cache` (or `$TMPDIR/coral_task_cache`) folder: older versions stored task subjects and pieces of hook payloads (prompts, commands) there, readable by other users on a shared machine.

### Security

- **An agent's task id can no longer reach files outside the hook cache.** The task hook (`coral-hook-task-sync`) built its cache file's path from the task id the agent sent, so a crafted id could write to or read files elsewhere, outside the agent CLI's permission prompts. This is fixed, and nothing is needed beyond upgrading. The cache file is now named by a hash of the id, and the cache lives in your Coral data folder (readable only by you), not in the shared `/tmp`. Its debug log records event names and ids only. The hooks also escape the agent's folder name in the addresses they call, so an unusual folder name can't redirect a request.

### Coral Hub

- **An oversize panel gets a clean error.** When a redacted reply's full length is known, Coral tells the hub that length, so the hub can refuse an artifact or agent UI panel that's too large with its clear "panel too large" message instead of a cut-off page. Content-Length itself is still removed, and a streamed reply of unknown length sends no length.

### Install

- **Homebrew:** `brew install --cask cdknorow/coral/coral` installs Coral again. If you installed Coral with `brew install cdknorow/coral/coral` before (the old Python formula), run `brew uninstall coral`, then `brew install --cask cdknorow/coral/coral`.

## v1.4.39 — Free space from stored diffs and artifacts

Nothing is removed unless you ask: a dry run always comes first, and automatic cleanup is off by default.

### Changes

- **Settings › Storage** shows the artifact store by age and by board, and lets you free space:
  - **Shrink** rewrites closed tasks' and stopped sessions' stored diffs through v1.4.35's filter, leaving out binaries, dot-files and oversized sections. **Recommended first for a large store**: it keeps every diff, just smaller.
  - **Prune** removes closed tasks' and stopped sessions' diffs, and artifacts nothing links to, older than the age you choose.
  - **Keep** marks an artifact so it's never pruned. An explicit Delete clears the mark.
  - **Automatic cleanup** (off by default) prunes on a schedule you set (`artifact_retention_days`), at most 2 GiB a run. It starts only after you review a dry run ("Review and start") or 24 hours after you turn it on, and logs each removal.

  Shrink and Prune show a dry run first, then ask you to confirm.
- **`coral artifacts prune --older-than 14d`** does the same from the command line (`--kind diffs|objects|diff-objects|all`, `--shrink`). It shows a dry run first and asks you to confirm; `--yes` confirms without asking (needed when there is no terminal). The shortest age is 1 day. Only you can run it: the dashboard or the signed command line, never an agent's token, a script, an API key or Coral Hub.
- **What is never removed:**
  - an open task's diff, or a running or sleeping session's;
  - an artifact that anything still links to: board posts, tasks, notes, chat transcripts, launch prompts or team knowledge. Coral checks every column of both databases and the knowledge files, and if that check fails, nothing is removed;
  - a kept artifact.
- **Auto-attach is now off by default.** Turn it on in Settings; an existing saved setting is kept. While it's off, a link to a local file in an agent's board post stays as written, and agents share files with `coral-agent artifact upload`. If you ever pressed Save in Settings before this release, check Settings › Board › Attach files agents link: older versions stored it as on.
- **Auto-attach's limit** is now 2 GiB in any rolling 7 days, instead of 2 GiB ever.

## v1.4.38 — Tokens are hidden in the live terminal and live updates too

### Security

- **The live streams are redacted.** What the live terminal (`/ws/terminal`) and the live session updates (`/ws/coral`) send to Coral Hub now goes through the same token scanner as every other View reply (v1.4.37). The terminal's output is scanned as one stream, so a token split across two pieces of output is still caught. A partly typed token that goes quiet is redacted after a second rather than shown. The same "Hide tokens from Coral Hub viewers" switch controls it.
- **Coloured tokens are caught.** A token right after a terminal colour code (for example a highlighted line in a shell or a log) is now found, in the live terminal and in captured output alike. This also fixes a gap in v1.4.37's terminal capture and polling.
- **Still not caught:** a token broken up inside the terminal's output (an escape code or cursor movement in the middle of it, a program's own line wrap, a partial redraw) passes as it is. View access still shows a whole session; redaction is a safety net, not a guarantee.
- **Home folder paths through Coral Hub, follow-ups:**
  - a path inside a command (a workflow step like `cd ~/proj && make test`, its arguments, a script) is shown from `~`;
  - so is any other field whose value is a path under your home folder, and a `file:///…` link in an error message;
  - killing an agent no longer returns the local file path of its stored diff;
  - a missing task's or board's cost answers 404 without internal error text.

### Fixes

- A store test that could run for minutes under `-race` is now bounded (the connection pool itself was correct).

## v1.4.37 — Tokens are hidden from Coral Hub viewers

### Security

- **Coral Hub viewers no longer see well-known token formats.** Before a reply leaves this computer for Coral Hub, Coral replaces these with `[redacted token]`:
  - GitHub tokens (`ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_`);
  - `sk-` keys;
  - AWS access key ids (`AKIA`, `ASIA`);
  - Slack `xox*-` tokens;
  - Bearer and Authorization values;
  - PEM and PGP private-key blocks.

  This covers every View reply: the terminal capture, chat, transcripts, events, the board, knowledge and workflow output. It holds however long the token is and wherever a reply is split. The dashboard on this computer is never changed. View access still shows a whole session; redaction is a safety net for well-known formats, not a guarantee (other secrets, and tokens split by terminal wrapping, can show).
- **The switch "Hide tokens from Coral Hub viewers" is on by default.** You can turn it off only on this computer (Settings › Coral Hub), never through the hub, and a change applies within a second.
- **Not yet covered:** the live terminal and the live updates stream. Redaction for those comes in v1.4.38.
- **Redact before you upload.** An artifact (an attachment) is shown whole through Coral Hub, byte for byte, so take tokens out of a file before uploading it.
- **Known false match:** an `sk-` package name of 20 or more characters is shown as `[redacted token]` on the hub.
- **Old git remote records with `user:pass@host:path`** are cleaned once at startup too, completing v1.4.36's cleanup. If such a remote ever held a token, rotate it.

## v1.4.36 — Security hotfix: file diffs, git remote URLs and home paths through Coral Hub

**Security advisory — please upgrade.** If a git remote URL ever held a token (`https://user:TOKEN@host/...`), rotate that token: older copies may remain in Coral's database files until they are overwritten.

### Security

- **A file's diff has the same bounds as the file.** Through Coral Hub (and for API keys, curl and other callers that aren't this computer's dashboard), `GET /api/sessions/live/{name}/diff?filepath=` now refuses, as if the file were missing, a session root of `/` or your home folder, sensitive paths, dot-files and dot-folders (`.env`, keys, `.pem`, `.git/config`) and symlinks that lead out of the session. An untracked binary file or one over 5 MiB is shown as a note instead of its content, the diff is filtered and capped like the other diffs, and the working directory is not sent. The dashboard on this computer is unchanged.
- **Credentials are removed from git remote URLs.** A remote like `https://user:TOKEN@host/repo.git` is recorded and shown as `https://host/repo.git` (its `?query` and `#fragment` dropped too), and `user:pass@host:path` becomes `host:path`; existing records of https/ssh-style URLs are cleaned once at startup (older `user:pass@host:path` records are cleaned in a later release). A plain `git@host:org/repo` is unchanged.
- **A diff is of one file.** Through Coral Hub the file path is taken literally, so a pattern like `*` or a folder name never widens the diff.
- **No home paths or hidden file names through Coral Hub.** In answers to Coral Hub, a path under your home folder is shown as `~/…` (the working directory, repository and similar path fields, and error messages that quote a path), and log paths and tmux commands are left out. A `~/…` path the hub sends back is understood, so resuming or launching in a folder still works. The changed-files lists leave out hidden files (dot-files, dot-folders and sensitive paths) through the hub. The dashboard on this computer is unchanged.

## v1.4.35 — Task diffs stop filling the disk; the artifact store gets caps, a Delete button and private files

**Upgrade note:** at startup Coral makes its data folder private to your user: folders become 0700, and the databases, `coral.log` and artifact files 0600. Coral's hooks and CLIs run as the same user and keep working.

### Changes

- **Task and session diffs stay small.** A stored diff (a task's or session's `changes.diff`) leaves out sensitive paths and dot-files (also when renamed or copied), binary files and any file section over 256 KiB, each replaced by a one-line note; a hidden file's note doesn't name it. It is cut at 8 MiB, and identical diffs are stored once. Before this, every task done in a shared checkout stored the whole working tree's untracked files again, about 4.5 GiB a day on a busy machine. Existing diffs aren't removed. The live diff on this computer is unchanged.
- **Delete an artifact.** The dashboard's artifact lists have a Delete button for you (not in Coral Hub or the pop-out). It asks first, and other tabs drop the deleted row. Links to a deleted artifact show it as unavailable; a copy a browser already cached may remain. Settings shows the artifact store's size, with task and session diffs on their own lines.
- **Upload caps.** Each agent session can upload 100 artifacts and 512 MiB an hour (413 `artifact_quota`); 64 MiB stays the limit for one file. An optional `artifact_store_max_bytes` caps the store's total (off by default). Uploads are streamed, never read whole into memory.
- **Image metadata.** Uploaded JPEG, PNG and GIF images have their metadata removed, as auto-attached ones already did. WebP, HEIC/AVIF and TIFF files that carry metadata are refused; **a TIFF saved by macOS (for example from Preview) usually won't upload; export it as PNG**.
- **The first upload names an artifact.** Uploading the same file again doesn't change the name or type others see, and an object is never visible half-written.
- **Numbered agents, follow-ups.** Launching an explicit numbered name that's taken picks the next free number ("Backend Dev 3", not "Backend Dev 2 2"). Resetting or resurrecting a team reports each agent's real name. A nudge for "@Backend Dev 2" no longer reaches "Backend Dev", and "@Backend Developers" doesn't either.
- Saving several settings at once is one change, so a later save can't interleave with it.

### Security

- **Diffs through Coral Hub are filtered.** Through Coral Hub, old stored diffs are filtered when served (sensitive paths, dot-files and binaries left out), and the working-directory header is dropped. Untracked files in a checkout can hold personal data, such as screenshots.
- **Deleting an artifact is the Operator's.** `DELETE /api/artifacts/{id}` needs a person (the dashboard or the signed CLI) and never works through Coral Hub; an upload and a delete of one file can't race.

## v1.4.34 — Two agents in one role get their own names; a Coral Hub status light in the header

### Changes

- **Two agents in the same role get their own names.** An agent that joins a board where a running agent already holds its role is named "Backend Dev 2" (then 3, …), so the two never share a subscription, mentions or read state. `@Backend Dev` reaches only "Backend Dev"; use `@all` to reach everyone. "@Backend Dev 2" skips "Backend Dev" only on a board that has a "Backend Dev 2", so "@Orchestrator 3 tasks are ready" still reaches the Orchestrator elsewhere. Concurrent launches of one role never pick the same name. The "Join it / Create another" choice now shows how many of a board's agents are running next to its members.
- **Mentions and unread counts are exact:** '@Dev' no longer counts a message for '@Developer' or an email like user@dev.com, and the unread badge matches what reading the board returns.
- **Coral Hub status in the header.** When this Coral is enrolled with Coral Hub, an icon next to Home, the panel toggle and Settings shows it: green when connected, muted with a slow pulse while connecting (the pulse stops if you prefer reduced motion), amber when enrolled but disconnected. Its tooltip gives the state, the hub's host and when it connected; a click opens Settings at Coral Hub. It's hidden when this Coral isn't enrolled.
- **Readable links on the board.** Links in board posts and the names in Subscribers use the theme's link colour instead of the browser's dark blue, which was unreadable on the dark theme; they underline on hover and show the focus ring.
- **Very old boards named `.` or `..`** (or with `/`, control or bidi characters in the name) get a fresh `b-` id at startup, with their name kept as the label, so the dashboard can open them again. A backup of the board database is taken first (same file permissions as the database), and the log names each table it updated.
- The site links (Coral Hub's "request access" page and the specs) point to coralhub.dev.

### Docs

- The README's signature check now works with cosign 2.x (`--new-bundle-format`), and says the checksum step must print `<file>: OK`.

## v1.4.33 — Boards have ids; every dashboard view has its own URL; Markdown files preview in the dashboard

**For scripts that launch agents or teams by board name — please check.** Boards now have ids. A launch with `board_name` starts a **new** board; if a board with that name already exists, the launch answers **409 `board_name_exists`** with the candidates (`[{board_id, board_label, created_at, members}]`). Send `board_id` to join one of them, or `new_board: true` to start another with the same name. Existing boards keep their names as their ids, so links, scripts, `CORAL_BOARD` and agents keep working. `coral-agent launch --board <id>` joins an existing board. The dashboard asks "Join it" or "Create another" for you. Starting another board with a name that's taken (`new_board: true`) is Operator-only: dashboard or signed CLI. An agent's token gets 403 `forbidden_for_agent` in strict mode, and the call is recorded in default mode.

### Changes

- **Board ids and labels.** New boards get a `b-` id and a label you can rename in Team settings (the label only; the id never changes). Lists show the label, with a short id when two boards share one. A board route accepts an id, or a name that matches exactly one board; an exact id always wins. A new board may share a legacy board's name: the name still means the legacy board, and if the legacy board is deleted, the name resolves to the remaining board with that label. A name shared by several boards answers 409 `ambiguous_board`, so scripts should use ids. A name can't look like a board id (`b-` followed by 10 letters or digits); such names are refused with 400 `invalid_board_name`.
- **Every view has a path.** The dashboard's views are real URLs: `/agents`, `/agents/<id>[/<tab>]`, `/chats/<id>`, `/chats?q=…`, `/team/<board>[/settings]`, `/board/<board>`, `/workflows`, `/jobs`, `/analytics`, `/docs/<name>` and `/servers`, with an `@server` marker for a remote server. Reload, Back and Forward keep the view. Old `#…` links still work: they are read once, at the first load, and replaced with their path. A page whose object doesn't exist says "not found" and shows its list.
- **`coral open --path /chats`** (or any dashboard page) signs you in on that page, and the sign-in redirect keeps the page's path.
- **Markdown artifacts preview in the dashboard.** A Coral artifact linked in a board post opens in an in-dashboard viewer; a Markdown file is rendered (sanitized, with raw HTML shown as text) instead of downloaded. Links inside it can't move the dashboard: web, mailto and artifact links stay links, a `#fragment` scrolls inside the file, and Back, Forward or Escape closes the viewer.
- **A board or team named `.` or `..`** (possible only for very old boards; new names like these are refused) can no longer be opened from the dashboard: rename it with v1.4.32 or earlier before upgrading if you want to keep opening it (a later release gives such boards a new id, #3541).
- Concurrent launches of one new board name start one board.

### Security

- **Links in agent-written text can't move the dashboard.** In board posts, chat, tasks, notes and knowledge, web links open in a new tab, mailto and Coral artifact links stay links, a link to a dashboard page opens through the dashboard, and anything else (relative paths, `#…`, other schemes) shows as text.
- **A `#fragment` link in a board post or chat can no longer drive the dashboard.** Only the path and query are read on Back and Forward; an old hash link is honoured only at the first load.
- In strict mode an agent's board access is checked against the board the URL resolves to, compared exactly, so a label or a case variant can't pass for another board.
- The dashboard's pages send `Referrer-Policy: same-origin`, so session, team and board names in the new paths don't leave in a `Referer` header. They do appear in this machine's access logs and, through Coral Hub, in the hub's.

## v1.4.32 — Files linked in an agent's board post are attached; board and chat never load remote images

**Behaviour change — auto-attach, on by default.** When an agent links a fresh local image or Markdown file in its own board post (`![shot](out/shot.png)` or `[notes](notes.md)`), Coral uploads it as an artifact and points the link at `coral://artifacts/<id>`, so the team and Coral Hub viewers can open it. Only files modified in the last 24 hours, only from the agent's working folder (images also from its git root), and only for posts made with the agent's own session token; the Operator's posts are untouched. Turn it off in Settings (`board_auto_attach`).

### Changes

- **Auto-attach limits.** PNG, JPEG, GIF and WebP up to 10 MB (SVG never), and Markdown up to 1 MB. At most 8 files and 25 MB a post, 50 attempts and 100 MB an hour per agent, and 2 GiB per Coral. Hidden files, dot folders, and names that look like secrets or credentials are never attached, and nothing is attached from `/`, a temp folder, or your home folder itself. Location and camera data are removed from images (JPEG EXIF, GPS and XMP; PNG text chunks; GIF comments). **Animated GIFs attached this way play once.** A file's name appears in the artifact and in the post, so keep private names out of linked file names.
- **Board and chat never load remote images.** An image in board, chat or Markdown text that points at another site, or at a relative path, now shows as a link (or plain text) and is never fetched, so a post can't track who reads it. Coral artifact images and inline `data:` images still render. The dashboard sends a Content-Security-Policy that allows only Coral's own images, links open with `rel="noopener noreferrer"`, and Coral's pages can no longer be embedded in another site's frame.
- **New board and team names** use only letters, digits, `-` and `_` (starting with a letter or digit, at most 64 characters; `projects`, `tasks` and `remotes` are reserved). Existing boards keep their names.
- **Cleaner dashboard URLs.** The address bar no longer carries `?page=1&page_size=50` on every page; only non-default history-search settings appear, and old URLs still work.

### Security

- **A request that repeats `Origin` or `Sec-Fetch-Site` is refused** (403 `foreign_origin`, any method, WebSocket upgrades too).
- **Behind a trusted proxy, `X-Forwarded-Host` is read from its last value**, the one your proxy added, so an appending proxy can't be fooled by a client's own header. It is still accepted only from loopback or `CORAL_TRUSTED_PROXIES`.

## v1.4.31 — Workflow steps get their tools; the sidebar groups by server; prompts stay out of the process list

**Behaviour changes — please check your workflows.**
- Workflow steps that declare tools, capabilities, MCP servers or a permission mode now apply them (they were silently ignored before), so re-check your workflow definitions.
- Workflow steps run on Claude only for now: a non-interactive (print) step on Codex, Gemini, Antigravity or Pi is refused with a clear error, when it is saved and when it runs, until each CLI is verified. Interactive steps on those CLIs still work.

### Changes

- **Sidebar, grouped by server.** "Group by server" puts each server at the top level with a computer icon (an offline server is dimmed, with its state in the tooltip); "Show server" shows teams as "server-team". Teams share one neutral color unless you picked one, the palette varies only in brightness, folder groups get a folder icon, and a header row is itself the expand button (Enter or Space works). The top tabs read Agents, Servers, ..., Docs. A server that hasn't been checked yet is no longer shown as offline.
- **Distilling a team's knowledge works through Coral Hub** (start it from Launch, see its status in View). One run per team at a time and at most one a minute; through Coral Hub an error reads "distillation failed; see the dashboard on this machine", and the status never carries file paths or email addresses.
- **The dashboard no longer signs you out over an unrelated refusal.** Only a 401 that means "signed out" sends a tab to the sign-in page.
- **Smaller fixes:** a message released from a hold says what ended the hold; `coral-board targets` reports failures the way `send` does; "awaiting review" shows only for a task under review; killing an agent outside a git checkout no longer reports a changes-artifact error.
- **Docs:** files shown to you are `coral://` artifact links rather than local paths, and a `coral://` link only opens on the Coral that stored it.

### Security

- **Prompts stay out of the process list.** The prompts Coral passes to `claude --print` (knowledge distilling, session summaries, the goal check, and Claude workflow steps, which can carry earlier steps' output or board content) now go on stdin instead of the command line, where any local user could read them. A workflow step's settings file is removed after the step runs.
- **An agent's token can't read the API key or delete a board.** Both are the Operator's (403 `forbidden_for_agent` with strict local auth, recorded in the identity report otherwise). Leaving a board is still allowed.
- **Reverse proxies.** Behind a proxy that rewrites `Host`, the dashboard's own changes pass v1.4.30's cross-site check again: `X-Forwarded-Host` counts as Coral's own host when it comes from loopback or from an address in `CORAL_TRUSTED_PROXIES`. The proxy must set `X-Forwarded-Host` itself (e.g. `proxy_set_header X-Forwarded-Host $host`), not pass a client's value through, or set `proxy_set_header Host`.
- A board peek from another site's page no longer counts as activity on a task, and DNS rebinding to `127.0.0.1` is pinned as refused by a test.

## v1.4.30 — Security hotfix: other web pages can no longer change your Coral

**v1.4.30 replaces v1.4.29**, which was never fully published (its macOS build couldn't run); the code is the same.

**Security advisory — please upgrade now.** Fixed: another web page could change Coral's settings (including the Claude CLI path) or post as the Operator through your browser; Coral now accepts changes only from its own page. This affected every v1.4.x release, in the default mode and with strict local auth.

- Open Coral with one host name (`localhost` or `127.0.0.1`) and use it consistently: a change made from a page opened under the other name is refused.
- Installs behind a proxy that rewrites the `Host` header: check that changes from the dashboard still work; a fix for that setup follows.

### Security

- **Changes only from Coral's own page.** A request that changes something (POST, PUT, PATCH or DELETE) and carries a browser's origin must come from Coral's own page (the same scheme, host and port), in every mode, before any other check; anything else gets 403 `foreign_origin`. This includes pages on another `localhost` port. Tools that send no browser origin (the command line, the agents' tools, coral-tray) and Coral Hub's connector work as before. Coral's CORS and its terminal WebSockets accept only Coral's own origin as well.
- **An agent's task and note list is its own.** Creating, editing, reordering or deleting a live session's tasks and notes now acts only on the caller's own list: an agent works on its own (by its session token), the Operator (the dashboard, the verified CLI, Coral Hub) on any agent's, and anyone else without a credential gets 401 `session_token_required` (a terminal session still manages its own list in default mode). Before, one agent could change another agent's tasks and notes, and a task's notify could type into that agent's terminal.
- **An agent that hasn't joined a board can't act as the Operator.** It can join under a new name, but never post or change board settings as the Operator, or act as a name another session holds. A board request's `session_name` must be the agent's own session, and a board database error never lets a request skip these checks.

### Fixes

- **A board message posted while a Claude agent is mid-turn is delivered once.** With the default screen rule, the turn-end hand-off no longer repeats a nudge that was already typed during the turn.
- **`coral --version`** prints `coral <version>` and exits before anything a server does (also `-v` and `version`, and after other flags). `--help` and a bad flag no longer log a server startup line, and a stray argument after the flags is refused with the usage.
- **`coral-board send --address`** on a Coral Hub that doesn't relay messages (`relay_unsupported`) no longer suggests retrying.
- Work started by a relayed cross-team message (webhooks, message waits) finishes before Coral closes its board store.

## v1.4.29 — Security hotfix: other web pages can no longer change your Coral

**Security advisory — please upgrade now.** Fixed: another web page could change Coral's settings (including the Claude CLI path) or post as the Operator through your browser; Coral now accepts changes only from its own page. This affected every v1.4.x release, in the default mode and with strict local auth.

- Open Coral with one host name (`localhost` or `127.0.0.1`) and use it consistently: a change made from a page opened under the other name is refused.
- Installs behind a proxy that rewrites the `Host` header: check that changes from the dashboard still work; a fix for that setup follows.

### Security

- **Changes only from Coral's own page.** A request that changes something (POST, PUT, PATCH or DELETE) and carries a browser's origin must come from Coral's own page (the same scheme, host and port), in every mode, before any other check; anything else gets 403 `foreign_origin`. This includes pages on another `localhost` port. Tools that send no browser origin (the command line, the agents' tools, coral-tray) and Coral Hub's connector work as before. Coral's CORS and its terminal WebSockets accept only Coral's own origin as well.
- **An agent's task and note list is its own.** Creating, editing, reordering or deleting a live session's tasks and notes now acts only on the caller's own list: an agent works on its own (by its session token), the Operator (the dashboard, the verified CLI, Coral Hub) on any agent's, and anyone else without a credential gets 401 `session_token_required` (a terminal session still manages its own list in default mode). Before, one agent could change another agent's tasks and notes, and a task's notify could type into that agent's terminal.
- **An agent that hasn't joined a board can't act as the Operator.** It can join under a new name, but never post or change board settings as the Operator, or act as a name another session holds. A board request's `session_name` must be the agent's own session, and a board database error never lets a request skip these checks.

### Fixes

- **A board message posted while a Claude agent is mid-turn is delivered once.** With the default screen rule, the turn-end hand-off no longer repeats a nudge that was already typed during the turn.
- **`coral --version`** prints `coral <version>` and exits before anything a server does (also `-v` and `version`, and after other flags). `--help` and a bad flag no longer log a server startup line, and a stray argument after the flags is refused with the usage.
- **`coral-board send --address`** on a Coral Hub that doesn't relay messages (`relay_unsupported`) no longer suggests retrying.
- Work started by a relayed cross-team message (webhooks, message waits) finishes before Coral closes its board store.

## v1.4.28 — Message your other Corals; an agent's token decides who it is

**Upgrade note:** cross-Coral sending needs the matching Coral Hub release (v1.4.28 is the oldest Coral it relays for). An agent's tools now must present its own session token on the routes only an agent calls; an agent still running from **before v1.4.19** loses `coral-agent task`, `artifact upload` and `ui` until you restart it.

### Changes

- **Message a team on your other Coral.** An Orchestrator can send a cross-team message to a board on another Coral of the same Coral Hub account, relayed by Coral Hub: `coral-board send --address <server>/<team> "..."` (with `--reply-to` to answer one). `coral-board targets` lists the teams your account owner linked this board to. Sending is off unless the board opts in, the Operator sends only as a person (the dashboard or the signed coral CLI, not an anonymous script), and the receiving board must still list your server in its `cross_team_servers` setting. Messages carry a hop count (at most 3) and a message id, so a retried send is delivered once.
- **Coral Hub tells a board when a link changes.** When the account owner links or unlinks a team, both boards get a short notice from **Coral Hub** ("You can now message …", "… can no longer message you"). It is Coral's own fixed wording, never text from the hub, and repeats are dropped.
- **coral-shadow-report** adds an agreement-by-state table (agent type, signal state, open request kind).

### Security

- **A presented session token decides the identity, in every mode.** A board request that claims another subscriber or the Operator, or a `session_id` naming another session on the routes an agent's tools call (personal tasks, artifacts, agent UI panels, clearing events, token-usage and model reports), is refused with 403 `identity_mismatch`.
- **Agent-only routes need the agent's token** in every mode (401 `session_token_required`): `coral-agent task` (except finishing a task), `artifact upload`, writing a UI panel, and the hooks' token-usage and model reports. Terminal sessions (plain shells, no token) keep these for themselves in default mode; strict mode refuses them.
- **Relay streams are bounded.** The tunnel's relay request has a fixed shape and size, a 5-second read deadline and a per-tunnel in-flight cap; Coral Hub re-checks the account, link and target mode on every message.
- The auth doc now states what default mode still allows (a local process acting as a terminal session, or as the Operator with a browser's same-origin headers) and that strict mode closes both.

### For integrators

- Tunnel protocol version 2: `Welcome.Capabilities` (the hub advertises `relay`); connectors open `relay` streams to `/relay/v1/message` and `/relay/v1/targets`. Version 1 peers still connect.
- New routes: `POST /api/board/{project}/relay-messages`, `GET /api/board/{project}/relay-targets`, and `POST /api/board/{project}/hub-notice` (Coral Hub only, via `X-Coral-Hub-Notice`).
- A sent message's mirror on your board carries `relay_message_id`; a failed send prints its client message id so a retry de-duplicates.

## v1.4.27 — Needs input when an agent asks, and only then; agents' status events are secured

**Upgrade note:** agents' status events (from their hooks) are now accepted only from the agent itself. An agent that is still running from **before v1.4.19** stops updating its status in the dashboard until you restart it.

### Changes

- **Needs input as soon as an agent asks.** When Claude asks for a tool permission, asks you a question or wants a plan approved, the agent shows **Needs input** at once, and the dashboard says which: **Permission**, **Question**, **Plan** or **Input**. The flag clears when the request is answered, denied or interrupted.
- **Trust screens count as a question.** Claude's and Antigravity's "trust this folder" screens now raise Needs input.
- **A held message reports what's holding it.** If a question opens while Coral's message is waiting (for example behind a draft), the agent raises Needs input after two minutes instead of staying calm.
- **Board messages at the end of a turn (Claude team agents).** When a Claude agent on a team finishes a turn with unread board messages, it is told to read them before it stops, instead of getting a typed nudge afterwards, and a held nudge is dropped once the agent has read the board. This is on by default for Claude team agents and off for sessions a person drives; set `turn_end_handoff` to `off` to turn it off.
- **Antigravity status from its own hooks.** Antigravity agents report their status through per-session hooks, and Coral writes nothing to your repository or `~/.gemini` for them.
- **Browse on a remote server.** In Coral Hub, **Launch Agent › Browse** on a connected server now starts at that machine's home folder (it used to fail with "not found").
- **Messages from your other Corals (receiving side).** A board can accept cross-team messages from an Orchestrator on another Coral of the same Coral Hub account, relayed by Coral Hub. It is off unless the Operator lists that server in the board's `cross_team_servers` setting (and cross-team messaging is on). Sending them needs a Coral Hub update that isn't out yet.
- **Fix:** a workflow run you kill is no longer recorded as failed.

### Security

- **Agent status events are bound to the agent.** `POST /api/sessions/live/{name}/events` and the pending-tool endpoint accept an agent's events only with its own session token (401 without one, 403 `identity_mismatch` for another session), only known event types, and a capped size. Auto-accept acts only on a verified request.
- **Hub-relayed messages are trusted only from Coral Hub.** The source server is named by a header that Coral accepts only on a request its own Coral Hub connector carried; anything else gets 403.

### For integrators

- `PUT /api/board/{project}/settings` takes `cross_team_servers` (Operator only; Coral Hub server ids, up to 20; `[]` clears).
- A relayed cross-team message's `external` envelope carries the sending Coral's `server_id`, plus `relay_message_id` and `hops`.
- `coral-board read --id N --json` prints the message as JSON (one object per line), including its `subscriber_id`.
- A dev or test server never uses the installed app's hooks; `CORAL_HOOK_DIR` overrides, and the startup log names the hook source.

## v1.4.26 — Antigravity drafts are protected; "Waiting on" instead of "Ready for input"

**Please upgrade if you use Antigravity (agy) agents.** In v1.4.25 and earlier, a message from Coral could be added to text you were typing in an Antigravity agent's input box and submitted with it. Coral now holds its message until you send or clear your draft, as it already does for Claude, Codex and Gemini.

### Fixes

- **Your draft is never added to, for any agent:** Coral reads Antigravity's input box, and for any agent whose input line already holds someone else's text, it holds its own message instead of pasting it. A new Antigravity agent's first prompt also waits for its input box.
- **Antigravity no longer shows "Needs input" while it is working:** its busy line ("Working...", "Generating...", "Running command...") is not taken for a question. Coral's message is still held until the agent is ready.
- **Board mentions reach agents promptly:** a new mention that arrived just after an agent read its previous message could wait up to 15 minutes for a nudge; it is now nudged at once.
- **Agent priority:** Settings only accepts **Low** or **Normal**; any other value is refused (HTTP 400) and nothing in that save is changed.

### Changes

- **"Waiting on" in the dashboard:** an agent that is waiting on the board (`coral-board wait`) now shows a calm "Waiting on <agent>", "Waiting on task #N" or "Waiting on commit <ref>" instead of "Ready for input", with no attention badge. Needs input, Check terminal, Stuck, Sleeping and Working still take precedence. With agents of the same name on several boards, each shows its own team's state.

### For integrators

- `PUT /api/settings` answers **400** `agent_priority must be "low" or "normal"` for any other `agent_priority` value, and saves nothing from that request.

## v1.4.25 — Hotfix: board messages and nudges reach Claude agents again

**Please upgrade from v1.4.24.** In v1.4.24, once a Claude agent finished a turn, Coral could stop delivering board messages, nudges and other automated messages to it: Claude shows a faint suggestion for your next prompt in its empty input box, and Coral took it for text you had typed, so it held every message for that agent indefinitely. Coral now recognises the faint suggestion as an empty box, and delivers the held messages. A real draft in the input box (anything you typed, including a draft followed by Claude's faint suggestion, or a suggestion you accepted with Tab) still holds Coral's message until you send or clear it.

### Fixes

- **Messages to idle Claude agents are delivered again** (the faint prompt suggestion counts as an empty input box).
- **A message held by a draft is reported properly:** it keeps its original hold time, so after two minutes the dashboard says it is waiting on the input box, held nudges expire after an hour as intended, and the log records each hold once instead of every 2 seconds.
- **No stale messages after a restart:** a message that was being delivered while an agent was killed or restarted is dropped, instead of reaching the new agent.
- **"Needs input" means a real prompt:** an agent is marked as needing your input only when it waits on a selection menu or a question, not when Coral's own message is waiting behind a draft in the input box, and the flag clears once Coral delivers or drops the held message (before, it could stay on for Antigravity and Codex agents).
- **Terminal pane size after a reconnect:** after a dropped connection, a renamed agent or a Restart, the dashboard reconnects to the agent's own terminal, so its pane keeps the right size. Restart from the dashboard reattaches to the restarted agent.

### For integrators

- The automated-input log line for a held paste now names the reason (a draft in the input box, no input box on screen, or a paste that didn't reach the box) and is written once per reason.

## v1.4.24 — Faster on busy machines; Run as a hub from Settings; automated messages never mix with your typing

**A busy machine stays responsive.** Agents now run at a lower OS priority than Coral, the database reads behind the dashboard no longer queue behind writes, and Coral Hub's small replies overtake large downloads on the tunnel. Coral's own messages to an agent never mix with what you are typing in its input box.

### Changes

- **Agents run at a lower priority than Coral** (nice +10 on macOS and Linux, below normal on Windows), so the dashboard and Coral Hub stay quick while agents use every CPU. On a test machine with 16 busy agents on 2 cores, the agent list's p95 went from 57–80 ms to 18–19 ms. Choose it in **Settings › Agent priority** (**Low**, the default, or **Normal**), or with `--agent-priority` / `CORAL_AGENT_PRIORITY`. A change applies to agents started afterwards. At startup Coral also lowers agents an older Coral started in its own tmux server; it never touches your own tmux sessions.
- **Faster reads:** sessions.db and board.db have separate read connections (the encrypted build too), so chat, the agent list, polls and unread counts no longer wait behind a write. The agent list's reads also run in parallel.
- **Coral's messages never mix with your typing.** Board messages, nudges and other automated text go into an agent's input box as one paste, and Coral checks it arrived before pressing Enter. If you have a half-written message (or your own paste) in the box (for Claude, Codex and Gemini agents, whose input box Coral can read), Coral holds its message until you have sent or cleared yours, and it only ever submits its own text.
- **Settings › Run as a hub** turns a running Coral into a hub server (the same as `--hub`), for people who start Coral from the macOS or Windows app. It's saved and applies on the next start, and the app can restart Coral for you. Only the dashboard on this computer can change it, and with `--strict-local-auth` an agent can't.
- **Coral Hub over a slow link:** small replies (the terminal, the agent list) are no longer stuck behind large downloads on the tunnel; on a download-heavy link, chat history's p99 went from 626 ms to 319 ms.
- **Phone:** a **Fit to width** toggle shrinks the terminal's font to show the whole pane, instead of scrolling sideways.
- **Fix:** the terminal no longer jumps to the top after you switch agents or hide and show it.
- **Fix (tray app):** the tray app's files and its cleanup of a leftover Coral process are limited to its own data folder, so a second Coral never touches the first; on Windows, quitting and restarting the app now actually stop the old process.

### For integrators (Coral Hub, scripts)

- `GET /api/system/hub-mode` returns `{saved, running, forced, restart_required, can_restart}`; `PUT` with `{"enabled": bool}` saves it; `POST /api/system/restart` answers **202**, or **409** `restart_unavailable` when Coral can't restart itself (started from a command line). All three answer only the dashboard on this computer (**403** `local_dashboard_required` otherwise; **403** `forbidden_for_agent` for an agent under `--strict-local-auth`).
- Debug phases (`CORAL_DEBUG_PHASES`): the agent list's `db_identity`, `db_enrichment` and `db_goals_board` phases are now one `db_reads` phase.
- `CORAL_PPROF_ADDR` now also serves block and mutex profiles.
- Field debugging for the tunnel: `CORAL_TUNNEL_SCHEDULER=off` and `CORAL_TUNNEL_LOWAT=off` turn off the new send scheduler and socket setting.

## v1.4.23 — Fix: scheduled jobs, task runs and workflow steps type Claude's first prompt right away again

**Please upgrade from v1.4.22.** In v1.4.22, Claude agents started by a scheduled job, a task run or a workflow step waited two minutes before Coral typed their first prompt: Coral didn't recognise Claude's input box, so it held the prompt until its two-minute fallback. Coral now recognises the input box, and the first prompt is typed as soon as Claude is ready. Agents launched from the dashboard or as a team were unaffected (their first prompt is passed at launch). Prompts are still held while an agent shows a permission or selection prompt, as in v1.4.22.

### Changes

- **Coral Hub stays out of the way on a fresh install.** A new install no longer shows the **Connect to Coral Hub** screen, a terminal prompt or an enrollment code on its own. Connect from the home screen, **Settings › Coral Hub** or `coral cloud connect`. Starting Coral with an explicit `--cloud-url` (or `CORAL_CLOUD_URL`) still offers it at launch; `--cloud-connect` / `CORAL_CLOUD_CONNECT` choose `prompt`, `auto` or `off`, and an unrecognised `CORAL_CLOUD_CONNECT` value logs a warning and means `off`. A server that is already connected reconnects as before.
- **Machine health:** a health card in Settings and a status dot for this computer; the machine view in Coral Hub shows each machine's status, CPU, memory, load, disk and agents.
- **Faster dashboard:** chat no longer waits on the database for each refresh (agent details are cached in memory), and terminal, live-list and board-task polls answer 304 when nothing changed.
- **Encrypted build:** databases now use the settings Coral intended (WAL, a 30 s busy timeout, and foreign keys on sessions.db). Any existing orphan rows are reported once in the log at startup, by table count only, and never block startup.
- **Linux arm64:** the tray app builds for linux/arm64.

### For integrators (Coral Hub, scripts)

- `GET /api/cloud/status` reports `offer_at_launch`.
- Terminal, live-list and board-task polls support `ETag` / `If-None-Match` (and `X-Coral-Validator`) and answer **304** when nothing changed.
- The Coral Hub feed carries a `machine_health` event (CPU, memory, load, disk, agents), field-diffed, every 15 seconds.

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

# Offline, with cosign v2.4 or later (cosign 2.x needs --new-bundle-format)
cosign verify-blob SHA256SUMS.txt --bundle SHA256SUMS.txt.sigstore.json --new-bundle-format \
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
