# Release Notes

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
