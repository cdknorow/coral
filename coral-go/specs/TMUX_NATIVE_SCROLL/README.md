# Tmux Native Scroll

## Goal

Replace xterm.js scrollback management with tmux's native copy-mode scroll.
Instead of capturing 200 lines of scrollback per update (which causes flicker,
dual cursors, and buffer growth), capture only the visible pane and let tmux
manage scroll history natively.

## Branch

`feature/tmux-native-scroll`

## Architecture

### Normal mode (no scroll)
- Server captures visible pane only (no `-S-200`)
- xterm.js has minimal scrollback (`scrollback: 1`)
- Terminal updates render directly

### Scroll mode
1. User scrolls up in browser → frontend sends `scroll_up` WebSocket message
2. Backend enters tmux copy-mode, scrolls up, captures visible content
3. Server sends `terminal_update` with `in_scroll_mode: true`
4. Frontend renders scroll content, shows "Updates paused — scrolled up" badge
5. Live updates buffered while scrolled up
6. User clicks resume or scrolls to bottom → `scroll_to_bottom` message
7. Backend exits copy-mode, resumes live updates

## What's Implemented

### Backend (websocket.go, tmux/client.go)
- `scroll_up`, `scroll_down`, `scroll_to_bottom` WebSocket message handlers
- `EnterCopyMode`, `ExitCopyMode`, `ScrollUp`, `ScrollDown`, `IsInCopyMode` tmux client methods
- `in_scroll_mode` flag in `terminal_update` messages
- Copy-mode cleanup on WebSocket disconnect
- Mouse mode toggle (disable on scroll entry, re-enable on exit)
- `CaptureRawOutput` always visible-only (removed `-S-200`)

### Frontend (xterm_renderer.js)
- `scrollback: 1` (not 0 — xterm 5.5.0 converts wheel to arrow keys with scrollback:0)
- `customWheelEventHandler` set as property after Terminal construction (not constructor option — xterm 5.5.0 uses setter, ignores constructor)
- Direct `addEventListener('wheel')` on `.xterm-helper-textarea`, `.xterm-viewport`, `.xterm-screen`, container as fallback
- All listeners use `{ capture: true, passive: false }` with `preventDefault` + `stopPropagation`
- macOS natural scrolling handled (positive deltaY = scroll up through history)
- Alt screen gating (no scroll during vim/nano)
- Scroll state reset on WebSocket reconnect
- `resumeScroll()` sends `scroll_to_bottom` and flushes buffered content

### Interface (session_terminal.go)
- `ScrollUp`, `ScrollDown`, `ExitScrollMode`, `IsInScrollMode` added to SessionTerminal interface
- PTY backend stubs (no-op)

## Known Issues (Unsolved)

### 1. Scroll events not reaching tmux
**Status:** Frontend captures wheel events and sends WebSocket messages, but
the terminal content does not change (tmux copy-mode scroll not visually
reflected).

**Symptoms:**
- `[SCROLL] sending scroll_up, rows: 3` appears in browser console
- "Updates paused — scrolled up" badge appears
- Terminal content does not change (no scrollback visible)

**Possible causes:**
- Backend may not be processing `scroll_up` messages correctly on the WebSocket
  polling path (the message types are handled in the input reader goroutine —
  verify they reach the tmux client)
- tmux copy-mode may not be entering correctly (verify with
  `tmux display-message -t <target> -p '#{pane_in_mode}'`)
- The polling capture loop may be overwriting scroll content with live content
  before the frontend can render it (race between scroll capture and poll capture)
- `in_scroll_mode` flag may not be set correctly in the response, causing the
  frontend to treat scroll content as normal content

### 2. xterm 5.5.0 scrollback:0 behavior
With `scrollback: 0`, xterm converts wheel events into arrow key escape
sequences (`ESC[A`/`ESC[B`) sent directly to the terminal application via
`triggerDataEvent`. This bypasses all wheel event handlers including
`customWheelEventHandler`. The workaround is `scrollback: 1`.

### 3. customWheelEventHandler is a setter, not constructor option
In xterm 5.5.0, `customWheelEventHandler` must be set as a property on the
terminal instance AFTER construction (`terminal.customWheelEventHandler = fn`).
Passing it in the Terminal constructor options is silently ignored.

### 4. macOS natural scrolling
macOS natural scrolling inverts deltaY — positive deltaY means the user is
physically scrolling up (pulling fingers down). The handler must treat
`deltaY > 0` as "scroll up through history".

### 5. Auto-resume race condition
When `_userScrolledUp` is true, incoming `terminal_update` messages with
`in_scroll_mode: false` were resetting the scroll state, cancelling the user's
scroll before the backend could process it. Fixed by buffering updates while
scrolled up and not auto-resuming based on server state.

## Next Steps

1. **Debug backend scroll processing** — Add logging to websocket.go scroll
   handlers to confirm `scroll_up` messages are received and tmux commands
   are executed. Check tmux pane state after scroll commands.

2. **Verify tmux copy-mode** — Manually test:
   ```bash
   tmux -S ~/.coral/tmux.sock send-keys -t <session> C-b [    # enter copy-mode
   tmux -S ~/.coral/tmux.sock send-keys -t <session> -X scroll-up
   tmux -S ~/.coral/tmux.sock capture-pane -t <session> -p     # should show scrolled content
   ```

3. **Check polling loop interaction** — The terminal polling loop runs every
   100ms. If it captures the pane between scroll commands, it may overwrite
   scroll content. Consider pausing the poll loop while in scroll mode.

4. **Consider alternative approach** — Instead of tmux copy-mode, capture
   scrollback on-demand: when user scrolls up, run
   `capture-pane -t <target> -p -e -S-<offset>` with increasing offset.
   This avoids copy-mode entirely and gives direct control over what's shown.

## Files Changed

- `internal/server/routes/websocket.go` — scroll message handlers, in_scroll_mode flag
- `pkg/frontend/static/xterm_renderer.js` — wheel handlers, scroll state, buffering
- `internal/tmux/client.go` — copy-mode methods
- `internal/ptymanager/session_terminal.go` — scroll interface methods
- `internal/ptymanager/session_terminal_tmux.go` — tmux scroll implementation
- `internal/ptymanager/session_terminal_pty.go` — PTY stubs
- `pkg/frontend/static/css/components.css` — pause banner styles
- `pkg/frontend/static/message_board.js` — board pause indicator
- `pkg/frontend/templates/includes/views/message_board.html` — pause banner HTML
