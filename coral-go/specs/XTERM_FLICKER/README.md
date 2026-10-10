**Status: Superseded** — this spec has been replaced by TERMINAL_UNIFIED_STREAM (merged to main).

# xterm Terminal Flicker Fix

## Problem

The xterm terminal in Coral's web UI flickers when displaying tmux-backed sessions. Users see the screen briefly flash blank between content updates, and the text appears to jump between the top and bottom of the terminal.

## Current Architecture

### Server-side (Go: `websocket.go` wsTerminalPolling)
1. Watches tmux pipe-pane log file for mtime changes (10ms poll loop)
2. On change: runs `tmux capture-pane -t <target> -p -e -S-200` to grab 200 lines of scrollback + visible content
3. Compares to previous capture — only sends if content or cursor position changed
4. Sends `{ type: "terminal_update", content: "<raw ANSI>", cursor_x, cursor_y, alt_screen }` via WebSocket

### Client-side (JS: `xterm_renderer.js`)
1. Receives `terminal_update` message
2. Converts `\n` to `\r\n` for xterm.js
3. Writes: `\x1b[2J\x1b[3J\x1b[H` + content + cursor_position
   - `\x1b[2J` = clear visible screen
   - `\x1b[3J` = clear scrollback buffer
   - `\x1b[H` = cursor home
4. Calls `terminal.scrollToBottom()` if no cursor position

## Root Cause

The `\x1b[2J` (clear screen) causes xterm.js to blank the visible area for one render frame before the new content is drawn. Even though clear + write happen in a single `terminal.write()` call, xterm.js processes escape sequences incrementally and the clear visually precedes the content paint.

The `\x1b[3J` (clear scrollback) causes a scroll position jump — xterm.js reflows the buffer which can trigger a visible scroll to the top, followed by `scrollToBottom()` jumping back down.

Combined, these produce a flicker where the screen alternates between blank/top-of-buffer and the actual content at the bottom.

## Constraints

1. **Scrollback must be cleared** — The captured content already includes 200 lines of tmux scrollback. Without clearing, the xterm buffer grows by 200 lines per update, causing severe slowdown.

2. **Scrollback should be accessible** — Users want to scroll up to see previous output. The alternate screen buffer (`\x1b[?1049h`) eliminates flicker but has zero scrollback.

3. **Input must stay responsive** — The poll loop runs every 10ms. Any approach that blocks or causes heavy DOM reflow on each cycle will make typing laggy.

4. **Content is a full snapshot** — Unlike PTY streaming where only new bytes are sent, poll mode sends the entire visible + scrollback content each time. This is inherent to the tmux capture-pane approach.

## Approaches Tried

### 1. Alternate Screen Buffer (`\x1b[?1049h`)
- **Result**: No flicker, but zero scrollback — users can't scroll up at all.
- **Why**: Alt screen is designed for full-screen apps (vim, less). It's a separate buffer with no history.

### 2. Cursor Home + Overwrite + Erase (`\x1b[H` + content + `\x1b[J`)
- **Result**: No flicker on visible content, but scrollback grows unbounded → slowness.
- **Why**: Without clearing scrollback, each update adds 200 lines to the buffer.

### 3. Write then Clear Scrollback (write callback + `terminal.clear()`)
- **Result**: Broke rendering — clear happened at wrong time, content disappeared.
- **Why**: `terminal.clear()` in a write callback is not safe; xterm.js state may not be settled.

### 4. `terminal.clear()` Before Write
- **Result**: Same flicker — `clear()` blanks above the viewport but triggers a relayout that's visible.

## Proposed Solutions (To Investigate)

### A. Double-Buffered Canvas Approach
Use two xterm.js instances overlaid. Write to the hidden one, then swap visibility via CSS (opacity/z-index). The swap is a single CSS property change — atomic and flicker-free.
- **Pro**: True zero-flicker, preserves scrollback on active terminal
- **Con**: Double memory/CPU for two terminal instances

### B. requestAnimationFrame Batching
Buffer the clear + write into a single rAF callback. The browser won't repaint until the callback completes, so the clear and write happen in one paint frame.
```js
requestAnimationFrame(() => {
    terminal.write('\x1b[2J\x1b[3J\x1b[H' + converted + cursorSeq);
});
```
- **Pro**: Simple, single terminal instance
- **Con**: May not help if xterm.js internally batches differently

### C. Diff-Based Updates
Instead of sending full snapshots, compute a diff between the previous and current capture on the server side. Only send changed lines as ANSI cursor-position + content sequences.
- **Pro**: Minimal writes, no clear needed, preserves scrollback naturally
- **Con**: Complex server-side diff logic, ANSI sequences in captured content make diffing harder

### D. Server-Side Scrollback Management
Have the server track how much scrollback the client has seen. Send only new lines (appended at the bottom). Periodically send a "trim" signal to drop old scrollback.
- **Pro**: Matches how real terminal streaming works
- **Con**: Significant protocol change, complex state management

### E. Hybrid: Alt Screen + Overlay Scrollback
Use alternate screen for live updates (no flicker), but maintain a separate scrollback buffer in JS. When user scrolls up, show the JS buffer in an overlay. When they scroll back down, return to the live alt screen.
- **Pro**: Best of both worlds
- **Con**: Complex UI implementation, two rendering modes

### F. CSS `content-visibility: auto` or `will-change: transform`
Hint to the browser that the terminal canvas should be composited on its own layer, reducing repaint cost.
- **Pro**: Simplest change
- **Con**: May not affect xterm.js canvas rendering at all

## Recommendation

Start with **B** (rAF batching) as it's the simplest. If that doesn't help, move to **C** (diff-based updates) which solves the root cause — sending only what changed instead of the full screen.

## Cursor Positioning

### Problem — Dual Cursors
tmux `capture-pane -e` renders reverse-video (SGR 7) at the cursor position in
the captured content. xterm.js also renders its own native blinking cursor when
positioned via ANSI cursorSeq (`\x1b[row;colH`). When both are visible, two
cursors appear.

Additionally, `cursor_x`/`cursor_y` from tmux `display-message` are relative to
the VISIBLE pane, but the captured content includes up to 200 lines of
scrollback. cursorSeq positions at `(cursor_y+1)` which is near the top of the
full content, not at the actual cursor location within the visible area.

### Solution — Mode-Based Cursor Toggle
The server sends `alt_screen` (from tmux `alternate_on`) in the
`terminal_update` WebSocket message.

**Normal shell mode (`alt_screen=false`):**
- Hide xterm native cursor (`\x1b[?25l`)
- Don't send cursorSeq
- tmux reverse-video in content is the sole cursor (static, always correct
  position)

**Alt screen mode (`alt_screen=true`, vim/nano/TUI):**
- Show xterm native cursor (`\x1b[?25h`)
- Use cursorSeq — safe because alt screen capture has no scrollback, so
  `cursor_y` maps directly
- Blinking cursor at correct position

### Row Sync Fix
tmux `resize-window` now receives both `-x` (columns) AND `-y` (rows) to keep
tmux and xterm.js dimensions in sync. Previously only columns were sent, causing
`cursor_y` mismatches when the xterm container height didn't match tmux's
default row count.

### Layout Height Fix (Native App)
`native.css`: `.native-app .layout { height: calc(100vh - 37px) }` — the native
app top-bar is shorter than the browser version (37px vs 49px), which caused
fitAddon to calculate wrong row count.

### Known Issue — Multi-Client Cursor Mismatch

When multiple clients (e.g. browser + native app) are connected to the same
tmux session with different window sizes, the cursor position can be wrong for
one client. tmux has one pane size — whichever client resizes last wins. The
other client's xterm viewport won't match tmux's actual dimensions.

**TODO:** Investigate whether we are computing cursor position from the actual
tmux pane size (queried via `display-message`) or from the local xterm window
size. If xterm.js fitAddon calculates rows based on its own container, but tmux
has been resized by another client, there will be a mismatch. We should either:
1. Send the actual tmux pane dimensions (`pane_height`, `pane_width`) in the
   `terminal_update` message so the frontend can detect mismatches
2. Or lock the terminal display to the tmux pane size rather than fitting to the
   container

## Files Involved

- `coral-go/internal/server/routes/websocket.go` — `wsTerminalPolling()`, `doCapture()`, `alt_screen` flag
- `coral-go/pkg/frontend/static/xterm_renderer.js` — `onmessage` handler for `terminal_update`, cursor toggle
- `coral-go/internal/tmux/client.go` — `CapturePaneRawTarget()`, `ResizePaneTarget()` (now includes rows)
- `coral-go/pkg/frontend/static/css/native.css` — layout height fix for native app
- `coral-go/internal/ptymanager/session_terminal.go` — `ResizeTarget` interface (rows param)
- `coral-go/internal/ptymanager/session_terminal_tmux.go` — tmux resize with rows
- `coral-go/internal/ptymanager/session_terminal_pty.go` — PTY resize with rows
