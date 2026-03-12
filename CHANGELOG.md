# Changelog

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

- Added MkDocs Material documentation site at https://cdknorow.github.io/corral/.
- Feature documentation for all major Corral features with screenshots.
- API reference pages for Jobs, Webhooks, and more.

## 0.6.2

### Bug Fixes

- **Fix session cycling when multiple agents share the same directory** — Terminal WebSocket connections no longer cycle between sessions that share a working directory (e.g. two agents both in `worktree_2`). Added a generation counter to suppress stale `onclose` reconnect handlers, and a guard to skip reconnecting if already connected to the same session.
- **Fix WebSocket corral handler overwriting session_id on name match** — When a session restarts and multiple sessions share the same directory name, the dashboard no longer accidentally switches to the wrong session via the name-based fallback.
- **Fix git snapshots collision for same-directory sessions** — Changed the `git_snapshots` UNIQUE constraint from `(agent_name, commit_hash)` to `(session_id, commit_hash)` so each session tracks its own git history. Includes an automatic DB migration for existing databases.
- **Fix git poller skipping second session in shared directory** — The git poller now stores snapshots for all sessions in a directory, not just the first one discovered.
- **Fix git state lookups to use session_id** — Live session API and WebSocket now look up git state by `session_id` first, falling back to `agent_name` for backwards compatibility.
- **Fix frontend task/note creation missing session_id** — Tasks and notes created from the dashboard now include `session_id` in the request body, ensuring data is properly scoped per session.
