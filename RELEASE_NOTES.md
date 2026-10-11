# Release Notes

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
