# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## 2.4.1 — 2026-03-16

### Added
- **Redesigned agent team selection flow** — Default team now includes 3 agents (Lead Developer, QA Engineer, Orchestrator) with compact card UI, edit/collapse toggle, and preset picker with 8 predefined roles
- **Terminal scrollback setting** — Configurable scrollback buffer (1k–100k lines) in user settings, applies to existing terminals on save
- **Remote Server field** in the new agent launch modal for cross-server board support

### Fixed
- **xterm.js scrollbar now clickable** — Use CSS `clip-path` on `.xterm-screen` to expose the native scrollbar without breaking terminal rendering
- **Wider xterm scrollbar** — Increased from 6px to 12px with hover highlight for easier interaction
- **Session resume** now restores prompt and board subscription correctly

## 2.4.0 — 2026-03-15

### Added
- **Cross-server message board** — Agents on different Coral instances can join each other's boards with local polling for tmux notifications (NAT/firewall-friendly)
- **Remote board proxy endpoints** — Local Coral forwards board API calls to remote servers so the dashboard can display remote board data
- **Remote board CLI registration** — `coral-board --server <url> join` auto-registers with local Coral for background notification polling
- **Coral-managed URL routing** — Board server URL written to agent state file on launch, agents use plain `coral-board` commands without `--server` flag
- **`origin_server` subscriber field** — Remote subscribers tagged on the board, local notifier skips them (remote poller handles notifications instead)
- **Tray app `--home` flag** — Override the working directory for the tray app

### Fixed
- **message_check.py hook** — Use `server_url` from board state file instead of hardcoded localhost, fixing unread notifications for remote boards

## 2.3.1 — 2026-03-15

### Fixed
- Unified icon set across all surfaces — monochrome coral silhouette used for system tray, favicon, top bar, welcome screen, and diff viewer

## 2.3.0 — 2026-03-15

### Added
- Monochrome template icon for macOS menu bar — renders white on dark, black on light
- Update notifications in tray app — checks PyPI on launch, shows macOS notification
- "Check for Updates" menu item for on-demand version checks
- Homebrew Cask (`Casks/coral.rb`) for `brew install --cask coral` with DMG download

## 2.2.1 — 2026-03-15

### Added
- Detect missing tmux on startup with macOS notification and "Install tmux..." menu item
- Auto-open dashboard in browser when the tray app launches
- Homebrew formula (`Formula/coral.rb`) with tmux as a dependency

### Fixed
- Agent discovery in macOS .app bundle — use `tempfile.gettempdir()` for correct TMPDIR
- Add `/opt/homebrew/bin`, `/usr/local/bin`, `/opt/local/bin` (MacPorts) to PATH in .app bundle
- Clarify tray menu labels: "Shutdown — Kill Agents & Stop Server", "Quit — Exit Coral"

## 2.2.0 — 2026-03-15

### Added
- **Inter-agent message board** — Agents communicate via shared project boards with per-subscriber read cursors, @mentions (`@notify-all`, `@<session_id>`, `@<job_title>`), and auto-pruning
- **Agent Teams** — Launch multiple agents on a shared board with per-agent roles and behavior prompts from the `+New` modal
- **coral-board CLI** — Terminal interface for agents to join boards, post messages, read updates, and list subscribers
- **Message board background notifier** — Nudges idle agents with unread messages every 30 seconds via tmux
- **Board hover cards** — Live session tooltips show board subscription and unread message count
- **macOS system tray app** (`coral-tray`) — Runs Coral as a menu bar icon with Open Dashboard, Shutdown, and Quit actions. Launches as a background process so the terminal is freed immediately
- **macOS .app installer** — py2app build pipeline produces `Coral.app` and `Coral.dmg` for drag-to-Applications install
- **GitHub Actions macOS build** — Automatically builds and attaches `Coral.dmg` to GitHub Releases
- **Session prompt persistence** — Prompt and board_name stored in live_sessions DB; restored on session restart

### Fixed
- Terminal scroll-pause no longer breaks when scrolling down — only resumes when user reaches the bottom
- Message board refresh no longer jumps to bottom when user is scrolled up reading older messages
- Duplicate board_store instantiation in launch_session()
- Raw SQL replaced with proper store method in session info endpoint

## 2.1.1 — 2026-03-15

### Added
- Update notification toast on dashboard load when a new PyPI version is available
- `/api/system/update-check` endpoint for version checking
- "Check for Updates" toggle in Settings modal
- `/release` skill and automated PyPI publish via GitHub Actions
- `CHANGELOG.md` with Keep a Changelog format

### Fixed
- Browse button in terminal launch modal

## 2.1.0 — 2026-03-10

### Added
- Two-step launch modal with Agent vs Terminal chooser
- Bidirectional terminal input via xterm.js and WebSocket

### Fixed
- Toolbar tooltip clipping and multi-line paste handling
- Browse button in terminal launch modal

## 0.9.0

### Features

- **Redesigned session header** — Inline goal display, branch info, and editable goal directly in the session header.
- **Resizable right sidebar** — Split into two independently resizable blocks for better layout control.
- **Welcome screen improvements** — Added quick-action buttons for creating jobs, webhooks, and accessing documentation.
- **Top bar links** — Added documentation and GitHub links to the top navigation bar.
- **Scroll-pause for terminal** — Terminal output pauses auto-scroll when the user scrolls up, with a resume button.
- **Changed files panel with diff viewer** — View file diffs for live agent sessions directly in the dashboard.
- **Image drag-and-drop and clipboard paste** — Support for sending images to agents via drag-and-drop or paste.
- **Instant tooltips** — Toolbar buttons now show tooltips without delay.
- **xterm.js terminal renderer** — Full terminal emulation with WebSocket streaming, now the default for Claude agents.
- **One-shot task runs API** — Jobs sidebar for managing scheduled and on-demand task runs.
- **Fit-pane-width setting** — Auto-resize tmux panes to match browser width.
- **Done vs Needs Input states** — Dashboard now differentiates between agent states waiting for input vs completed.

### Bug Fixes

- Fixed sidebar widths and removed activity chart border.
- Reset activity data on session restart and poll events in xterm mode.
- Fixed arrow key forwarding from xterm terminal to tmux sessions.
- Eliminated xterm flickering and fixed tmux auto-resize in xterm mode.
- Fixed session cycling when multiple agents share the same directory.
- Classified "waiting for your input" notifications correctly as done state.
- Paused xterm terminal updates while user has text selected.
- Prevented job sessions from resuming on restart and fixed auto-accept.

### Documentation

- Added MkDocs Material documentation site at https://cdknorow.github.io/coral/.
- Feature documentation for all major Coral features with screenshots.
- API reference pages for Jobs, Webhooks, and more.

## 0.6.2

### Bug Fixes

- **Fix session cycling when multiple agents share the same directory** — Terminal WebSocket connections no longer cycle between sessions that share a working directory (e.g. two agents both in `worktree_2`). Added a generation counter to suppress stale `onclose` reconnect handlers, and a guard to skip reconnecting if already connected to the same session.
- **Fix WebSocket coral handler overwriting session_id on name match** — When a session restarts and multiple sessions share the same directory name, the dashboard no longer accidentally switches to the wrong session via the name-based fallback.
- **Fix git snapshots collision for same-directory sessions** — Changed the `git_snapshots` UNIQUE constraint from `(agent_name, commit_hash)` to `(session_id, commit_hash)` so each session tracks its own git history. Includes an automatic DB migration for existing databases.
- **Fix git poller skipping second session in shared directory** — The git poller now stores snapshots for all sessions in a directory, not just the first one discovered.
- **Fix git state lookups to use session_id** — Live session API and WebSocket now look up git state by `session_id` first, falling back to `agent_name` for backwards compatibility.
- **Fix frontend task/note creation missing session_id** — Tasks and notes created from the dashboard now include `session_id` in the request body, ensuring data is properly scoped per session.
