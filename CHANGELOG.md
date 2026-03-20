# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## 4.0.2 — 2026-03-20

### Fixed
- **Tray app fails gracefully** — Exits with clear error when rumps or tmux is not installed instead of silently continuing
- **README cleanup** — Removed homebrew, DMG, and tray install sections until fixed

## 4.0.1 — 2026-03-20

### Changed
- **Rename Live Sessions to Workspace** — Sidebar section renamed for clarity
- **Lowercase folder names** — Session group headers no longer uppercase
- **Smaller branch text** — Branch names reduced to 8px for compact sidebar
- **Command pane default height** — Reduced from 400px to 300px

## 4.0.0 — 2026-03-20

### Added
- **Editable default prompts** — Customize orchestrator and worker board instructions in Settings panel with Reset to Default support
- **Configurable receive modes** — Board subscribers can set notification mode: `mentions` (default), `all`, `none`, or group-based filtering via new `board_groups` table
- **+Team send button** — Appends role-aware team collaboration reminder (orchestrator vs worker) when sending messages; configurable in Settings
- **Configurable data directory** — `--data-dir` CLI flag and `CORAL_DATA_DIR` env var to customize where Coral stores databases, uploads, themes, and state files
- **Reorderable session groups** — Move Up/Move Down in sidebar kebab menu to control folder group order; persisted in settings
- **Unified Chat History** — Message boards merged into Chat History as "Group Chats" alongside "Agent Chats" with type filtering (All/Agent/Group) and full-text search across board messages
- **Agent emoji icons** — Set emoji icons per agent via kebab menu emoji picker (Slack-style grid with search); icons display in sidebar, board messages, and subscriber list
- **`coral-agent-icon` CLI** — Agents can set their own emoji icon (`coral-agent-icon set 🦊` / `coral-agent-icon clear`)
- **Icon macro button** — Default macro prompts agents to pick an emoji for themselves
- **Scrollbar toggle** — Show/hide scrollbars globally via Settings
- **Cross-links in Chat History** — Board subscribers link to agent's Chat History; agent history links back to Group Chat
- **Orchestrator crown** — Orchestrator sorted to top of team with gold crown icon for visual distinction
- **Sleep polling detection** — Agents in sleep loops (e.g. `sleep 120 && coral-board read`) show as idle instead of working

### Fixed
- **Safe `{board_name}` substitution** — Uses `.replace()` instead of `.format()` to avoid crashes with literal curly braces in custom prompts
- **Tooltip overflow** — +Team tooltip uses `position:fixed` to escape `overflow:hidden` containers
- **Board card header size** — Reduced from 14px to 11px for compact sidebar
- **Python 3.8 compatibility** — License field in pyproject.toml uses table form for older setuptools

## 3.2.1 — 2026-03-19

### Fixed
- **Team sidebar grouping** — Agents now group correctly from the first WebSocket poll by falling back to the live_sessions DB when the board subscription hasn't completed yet
- **README stale references** — Removed Base Mode/Bash Mode, updated action table, fixed tmux attach examples

### Changed
- **Documentation refresh** — All 22 screenshots replaced with current Coral UI, 5 new screenshots added, orphaned doc_features files removed, all pages verified by QA
- **Session ended overlay** — Clean overlay with Restart button and spinner when session ends, replacing text-only message
- **Built-in Demo Team template** — One-click 3-agent team (Orchestrator, Lead Dev, QA) in team template selector

## 3.2.0 — 2026-03-19

### Added
- **System prompt injection** — Behavior prompt, board instructions, and agent role injected via systemPrompt settings file instead of fragile tmux send; agents have persistent context from startup
- **Prompt delivery verification** — Retries up to 3 times with pane capture verification to ensure the initial prompt reaches the agent
- **Side panel toggle button** — Replaced confusing drag handle with a clear toggle icon in the action bar; panel fully hidden when closed, open by default
- **Welcome screen tip** — Quick-start hint: "Launch an Agent Team to see multi-agent collaboration in action"
- **Goal update toasts** — Brief toast notification when agents update their PULSE:SUMMARY, making real-time activity visible
- **Built-in Demo Team template** — Pre-loaded team template for quick demo launches
- **Session ended overlay** — Clean "Session ended" message with Restart button when agent finishes, replacing infinite reconnect loop
- **Default --dangerously-skip-permissions for teams** — Agent teams default to auto-accept mode for autonomous collaboration
- **SVG vector logo** — Clean scalable logo for the dashboard

### Fixed
- **Agents joining wrong board** — Write board state file before agent launch to prevent race condition with coral-board CLI
- **Team agents split across groups** — Fixed sidebar grouping when launching agent teams
- **Disconnected banner loop** — Eliminated persistent "Disconnected — reconnecting" banner when sessions end; server sends terminal_closed message
- **Pause badge confusion** — Shows "scrolled up" vs "text selected" as appropriate
- **Done badge removed** — Green dot is sufficient; removed redundant "DONE" badge from sidebar
- **xterm resize on panel toggle** — Terminal reflows correctly when opening/closing side panel
- **Sidebar rendering** — Fixed stray backtick from escapeAttr migration
- **Mobile scrollbar** — Reduced from 8px to 3px on mobile viewports

## 3.1.4 — 2026-03-19

### Fixed
- **Sidebar rendering broken** — Fixed stray backtick that broke sidebar session list rendering
- **Mobile scrollbar** — Reduced scrollbar width from 8px to 3px on mobile viewports
- **Notarization log fetching** — Fixed notarization log retrieval in build workflow

## 3.1.3 — 2026-03-19

### Added
- **Agent launch modal improvements** — Preset role selector, Add Agent to Board from sidebar kebab menu, saved personas and team templates with JSON export/import
- **Share/Save Agent Team** — Export running teams as JSON or save as reusable templates from sidebar

### Fixed
- **Board names with spaces** — URL-encode board names in CLI API calls and harden JS escaping in onclick handlers to support spaces and special characters

## 3.1.2 — 2026-03-18

### Fixed
- **Agent Team launch failure** — Fixed crash when launching agent teams caused by unsupported parameters being passed to the session launcher

## 3.1.1 — 2026-03-18

### Added
- **System tray support** — `pip install agent-coral[tray]` installs the `coral-tray` macOS menu bar app with dashboard launch, update checker, and agent shutdown

### Fixed
- **Tray shutdown error** — Fixed `RuntimeError: Event loop is closed` on quit by joining the server thread before exit

## 3.1.0 — 2026-03-18

### Added
- **Mobile-responsive layout** — Full mobile support with bottom tab navigation, pull-to-refresh, swipe navigation, tablet sidebar overlay with hamburger toggle, and compact mobile views for message board, history, and scheduler
- **Saved agent personas** — Save custom agent configurations (name, prompt, flags) for reuse across all launch modals
- **Team templates** — Save and load full agent team configurations; import/export as shareable JSON files with versioned format
- **Agent preset selector** — Preset role buttons in the single-agent launch modal for quick agent setup
- **Add Agent to Board** — Add new agents to existing teams directly from the sidebar group kebab menu
- **Save/Share Agent Team** — Save running teams as reusable templates or export as JSON from the sidebar
- **Message board pagination** — Load Earlier button for browsing board history
- **Folder tags** — Sidebar session groups with folder-based tagging
- **macOS code signing workflow** — GitHub Actions workflow for signed and notarized DMG builds (requires Apple Developer certificate)

### Changed
- **Batch polling** — Single endpoint for capture, tasks, and events; Page Visibility API pauses polling when tab is hidden
- **Dashboard identity** — Message board identity renamed from "Developer (Dashboard)" to "Operator"
- **Loading optimizations** — Faster initial page load and reduced API calls

### Fixed
- **PULSE:SUMMARY not displaying** — Fixed regex failing on lines with embedded OSC escape sequences; simplified log parsing to tail-based approach
- **Protocol instruction echoes** — Filter out template text (e.g. `<your current goal>`) from PULSE event parsing
- **Mobile agent list** — Fixed agent selection, settings tab, history view, and hamburger menu on mobile
- **Mobile board input** — Fixed input hidden by nav bar and goal text wrapping
- **Mobile header layout** — Back button, goal, and menu all on single line
- **Message board rendering** — Wrapped `marked.parse()` in try/catch, increased message limit to 500

## 3.0.0 — 2026-03-17

### Added
- **Hooks via `--settings` temp file** — No longer modifies user's `settings.local.json`; reads and deep-merges the full settings hierarchy (global → project → local), appends Coral hooks, writes to `/tmp/coral_settings_<id>.json`
- **System prompt in settings file** — PROTOCOL.md and per-agent persona consolidated into `systemPrompt` field in the temp settings file, removing `--append-system-prompt` flag
- **Sidebar kebab menu** — Per-agent dropdown with Attach, Restart, Rename, Session Info, and Kill Session actions
- **Collapsible agent groups** — Click group headers to collapse/expand, persisted in localStorage
- **Group-level Kill All** — Kill all agents in a group from the group header kebab menu
- **Agent group cards** — Agents on the same message board are grouped into bordered cards with auto-generated accent color and board link icon
- **Terminal session icon** — Terminal sessions show a `>_` icon instead of generic "Agent" label
- **Drag-to-reorder agents** in sidebar with persistent ordering
- **Message board operator controls** — Pause agent reads and delete messages from the board UI
- **Terminal input polish** — Disconnect indicator, input queue, and WebSocket resize support

### Fixed
- Diff view line numbers overlapping code text (diff2html position fix)
- Sidebar hover jitter eliminated (opacity transitions instead of display toggle)
- Tooltip no longer covers sidebar kebab menu (z-index fix)
- SSRF protection hardened per security review
- Critical and high severity security audit findings addressed

### Removed
- `install_hooks()` function that wrote hooks into user's `settings.local.json`
- Dead `install_hooks()` call sites in web_server.py, session_manager.py, and base.py
- Stale coral-hook/corral-hook entries cleaned from all settings files

## 2.5.0 — 2026-03-16

### Added
- **Batch board unread counts** — New `get_all_unread_counts()` method replaces N+1 per-agent `check_unread()` queries with a single DB pass
- **Log status mtime cache** — `get_log_status()` skips re-parsing when log file hasn't changed, eliminating up to 4MB of file I/O per agent per poll cycle
- **WebSocket diff updates** — Dashboard sends only changed/removed sessions instead of the full array (~80% payload reduction with 10 agents)
- **DB indexes** — Added indexes on `agent_events(session_id, event_type)` and `git_snapshots(session_id, recorded_at DESC)` for faster queries
- **Performance test suite** — 23 new tests covering critical performance paths (git store, log snapshots, board queries, pulse detector, idle detector)
- **Test DB isolation** — `conftest.py` with per-test temp database directories to prevent SQLite lock contention across concurrent test runs
- **Toolbar WebSocket input** — `sendRawKeys()` now sends through WebSocket instead of POST requests, with POST fallback

### Changed
- **CoralStore simplified** — Replaced 80+ manual delegation methods with `__getattr__` dynamic dispatch (414→81 lines)
- **Shared helpers extracted** — `_build_session_list()`, `_resolve_workdir()`, `get_diff_base()` deduplicated across endpoints (~130 lines removed)
- **Migration blocks consolidated** — 12 separate `ALTER TABLE` try/except blocks replaced with data-driven loop
- **Idle detector optimized** — Uses `Path.stat()` instead of reading 200 log lines per agent every 60s

### Fixed
- **Message board cursor bug** — Read cursor no longer advances past unseen messages from other agents
- **"Unknown" author names** — Board subscriber records now use tmux session names matching message post IDs
- **Consolidated CoralStore instances** — `restart_session()` uses single store instead of creating 3 separate ones

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
