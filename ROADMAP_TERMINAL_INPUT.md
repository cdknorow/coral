# Roadmap: Direct Terminal Input via xterm.js

This document tracks the plan to overhaul terminal input handling so that all keyboard input is captured by xterm.js and sent directly to tmux, providing a native terminal typing experience in the web dashboard.

---

## Problem

Today the terminal has `disableStdin: true` and only intercepts 5 keys (Escape, arrows, Enter). All other text input goes through a separate `#command-input` textarea, which sends the full command string via a POST endpoint. This creates a split experience — you can't type naturally into the terminal, use tab completion, send Ctrl+C interactively, or interact with CLI prompts.

There are three separate input paths:
1. Command textarea → POST `/api/sessions/live/{name}/send`
2. xterm custom key handler → POST `/api/sessions/live/{name}/keys` (5 keys only)
3. Global document keyboard handler → same POST endpoint (duplicate of #2)

---

## Goal

Replace all three input paths with a single flow:

```
xterm.js onData → WebSocket → tmux send-keys
```

This matches the standard web terminal pattern (ttyd, Wetty, GoTTY) and gives users a native terminal feel with full keyboard support.

---

## Implementation Plan

### Phase 1: Bidirectional WebSocket

**Files:** `src/coral/api/live_sessions.py`, `src/coral/tools/tmux_manager.py`

- [ ] **Upgrade the terminal WebSocket to bidirectional** — Currently `/ws/terminal/{name}` is read-only (server polls tmux pane content every 500ms and pushes to client). Add support for client → server messages carrying raw input data.
- [ ] **Define input message format** — Client sends `{ "type": "terminal_input", "data": "<raw bytes>" }` over the existing WebSocket.
- [ ] **Backend input handler** — On receiving `terminal_input`, call `tmux send-keys -l` with the literal data. For control characters (already encoded by xterm.js as escape sequences), pass them directly.
- [ ] **Keep POST endpoints working** — Don't remove `/send` and `/keys` yet; they're used by macros and other UI features. Deprecate for direct typing only.

### Phase 2: Enable xterm.js stdin

**Files:** `src/coral/static/xterm_renderer.js`, `src/coral/static/controls.js`

- [ ] **Set `disableStdin: false`** — Enable the xterm.js input handling (line 96 in `xterm_renderer.js`).
- [ ] **Attach `terminal.onData(data => ...)` handler** — This fires for every keystroke with properly encoded terminal data (UTF-8 text, control sequences, etc.). Send the data over the WebSocket.
- [ ] **Remove `_xtermKeyMap` and `attachCustomKeyEventHandler`** — The custom 5-key interception (lines 141-169) is no longer needed; `onData` handles everything.
- [ ] **Remove global document keyboard handler** — The duplicate handler in `app.js` (lines 412-434) is no longer needed.
- [ ] **Update `sendRawKeys()` in controls.js** — Refactor to send over WebSocket instead of POST when a WebSocket connection is active.

### Phase 3: Focus Management

**Files:** `src/coral/static/app.js`, `src/coral/static/xterm_renderer.js`

- [ ] **Terminal focus state** — Track whether the terminal is focused. When focused, all keystrokes go to tmux. When not focused (user clicked a modal, task input, search, etc.), keys go to the UI element.
- [ ] **Click-to-focus** — Clicking the terminal area focuses it for input. Clicking outside unfocuses.
- [ ] **Visual focus indicator** — Show a subtle border or glow when the terminal is capturing input, so the user knows where keystrokes are going.
- [ ] **Escape hatch** — Define a key combo (e.g., Ctrl+Shift+Escape) that always unfocuses the terminal and returns focus to the UI, in case the user gets "trapped."

### Phase 4: Command Textarea Integration

**Files:** `src/coral/static/controls.js`, `src/coral/templates/includes/views/live_session.html`

- [ ] **Decide on textarea role** — The command textarea is still useful for composing multi-line commands, pasting large blocks, and macros. Keep it but make it a secondary input method.
- [ ] **Textarea sends via WebSocket** — When submitting from the textarea, send the text over the WebSocket (same path as direct typing) instead of the POST endpoint.
- [ ] **Auto-focus terminal after send** — After submitting a command from the textarea, return focus to the terminal so the user can interact with the output immediately.

### Phase 5: Cleanup & Polish

- [ ] **Remove deprecated POST endpoint usage for typing** — Once WebSocket input is stable, remove the direct-typing code paths through POST.
- [ ] **Reduce tmux poll interval** — With bidirectional WebSocket in place, consider reducing the 500ms poll to 200-300ms for snappier output display, or explore `tmux wait-for` / `tmux control mode` for push-based updates.
- [ ] **Handle reconnection** — If the WebSocket drops, queue input and reconnect automatically. Show a "disconnected" indicator.
- [ ] **Paste support** — Verify that pasting (Ctrl+V / Cmd+V) works correctly through `onData`. xterm.js handles bracket paste mode automatically.
- [ ] **Terminal resize** — Ensure `terminal.onResize` still sends dimensions to the backend (existing resize endpoint).

---

## Technical Notes

### Key xterm.js APIs

| API | Purpose |
|-----|---------|
| `terminal.onData(cb)` | Fires with encoded input string for every keystroke |
| `terminal.onBinary(cb)` | Fires for binary data (mouse events in some modes) |
| `terminal.onResize(cb)` | Fires when terminal dimensions change |
| `disableStdin: false` | Enables the built-in input handling |

### Key files

| Component | File |
|-----------|------|
| xterm creation & keyboard | `src/coral/static/xterm_renderer.js` |
| Global keyboard shortcuts | `src/coral/static/app.js` |
| Command/key sending | `src/coral/static/controls.js` |
| WebSocket terminal endpoint | `src/coral/api/live_sessions.py` |
| tmux key/command dispatch | `src/coral/tools/tmux_manager.py` |
| Terminal HTML container | `src/coral/templates/includes/views/live_session.html` |

### tmux input methods

| Method | Use case |
|--------|----------|
| `tmux send-keys -l "text"` | Literal text (typed characters) |
| `tmux send-keys C-c` | Control characters |
| `tmux send-keys -H XX` | Hex byte values (for raw escape sequences) |

### Compatibility considerations

- **Browser clipboard access** — Paste requires `navigator.clipboard` permission or falls back to Ctrl+Shift+V.
- **Ctrl key conflicts** — Some Ctrl combos (Ctrl+W, Ctrl+T, Ctrl+N) are intercepted by the browser. These cannot be captured. Document this limitation.
- **Mobile/touch** — `onData` doesn't fire from virtual keyboards well. This is a known xterm.js limitation; not a priority.
