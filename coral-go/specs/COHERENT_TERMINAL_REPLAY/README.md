# Coherent Terminal Replay

**Status:** Shipped locally on `main` (2026-09-15)
**Initial implementation:** `2f72bd1` (merged by `aac963d`)
**Supersedes:** The raw tmux replay portion of `TERMINAL_UNIFIED_STREAM`

## Summary

When a browser opens or reconnects to a tmux-backed terminal, Coral seeds
xterm from tmux's interpreted pane state rather than replaying an arbitrary
suffix of the raw `pipe-pane` log. The browser includes its current terminal
dimensions in the WebSocket URL, and the server resizes tmux before capturing
the initial snapshot.

Live output remains a raw binary stream. This change affects only the initial
replay seed.

## Problem

The `pipe-pane` log is a stream of terminal operations, not a serialized final
screen. Reading its last 256 KiB can begin in the middle of a cursor movement,
line replacement, color sequence, or application redraw. Replaying that suffix
into a fresh xterm can expose overwritten text, duplicate portions of an agent
response, or begin from terminal state that no longer applies.

Historical output could also retain an old narrow layout because the server
captured or replayed content before learning the browser's current dimensions.

The first coherent-snapshot implementation exposed a second issue: tmux
`capture-pane` separates grid rows with LF (`\n`), while xterm interprets LF as
moving down without returning to column zero. Sending the capture unchanged
caused a diagonal or staircase layout. Captured rows must be sent as CRLF
(`\r\n`).

## Implementation

### Browser dimensions at connection time

`internal/server/frontend/static/xterm_renderer.js` adds xterm's current
`cols` and `rows` to the terminal WebSocket query string. The existing
`terminal_resize` message is retained for compatibility and later resizes.

### Resize before snapshot

`internal/server/routes/websocket.go` validates the query dimensions and
resizes the backend before attaching and requesting replay. Clients that omit
the parameters retain the previous behavior.

### Coherent tmux snapshot

`internal/ptymanager/tmux_backend.go` now implements tmux replay with
`capture-pane -p -e` against the resolved tmux target. The requested capture
depth is derived from `terminal_replay_bytes`, using 80 columns as a
conservative byte-to-line conversion and a minimum of 200 lines.

PTY-backed sessions continue using their raw in-memory replay buffer because
they do not have a tmux grid to capture.

### Newline normalization

Before the captured snapshot is returned, lone LF separators are converted to
CRLF. Existing CRLF sequences are preserved. This ensures every captured row
begins at column zero when xterm parses the replay.

### Soft-wrap preservation and single initial resize

The tmux capture uses `capture-pane -J` to join physical grid rows that tmux
marks as parts of one soft-wrapped logical line. Hard line boundaries remain
LF-separated and are subsequently normalized to CRLF. This prevents replay
from permanently preserving a historical narrow pane width.

The browser supplies initial dimensions only through the WebSocket query. It
does not immediately send a second identical `terminal_resize` from `onopen`.
Subsequent xterm resize events still use the normal resize message. Avoiding
the redundant initial resize prevents a TUI from redrawing just after its
snapshot was captured and placing a duplicate copy into the live stream.

## Files Changed

- `internal/ptymanager/tmux_backend.go`
- `internal/ptymanager/tmux_backend_test.go`
- `internal/server/routes/websocket.go`
- `internal/server/routes/websocket_terminal_test.go`
- `internal/server/frontend/static/xterm_renderer.js`
- `specs/COHERENT_TERMINAL_REPLAY/README.md`

## Verification

Automated coverage verifies:

- tmux replay contains recent output;
- overwritten raw-stream text is absent from the captured snapshot;
- captured lines use CRLF rather than bare LF;
- tmux soft-wrapped rows are rejoined into logical lines;
- initial WebSocket dimensions reach tmux before replay;
- replay frames remain binary and live streaming continues afterward.

Manual verification:

1. Produce several screens of long output in an agent.
2. Make the Coral window narrow, then widen it.
3. Switch to another agent and back.
4. Refresh the page and reopen the agent.
5. Confirm there are no duplicate response fragments or staircase indentation.
6. Confirm new terminal output continues to stream normally.

## Tradeoffs

- This improves tmux-backed sessions only. Native PTY sessions still replay a
  raw byte buffer.
- `capture-pane` returns tmux's stored grid and scrollback, so behavior is
  bounded by tmux's history limit.
- Output produced between snapshot capture and live-tail delivery can still be
  a narrow race boundary. The tail is attached before replay to avoid losing
  output; in a highly active pane, a small amount of duplicate output remains
  preferable to missing output entirely.
- A genuine layout change immediately after connection can still cause the
  running TUI to redraw. Coral only suppresses the redundant same-size resize;
  it does not suppress real resize events.
- The byte-oriented `terminal_replay_bytes` setting is approximate for tmux
  snapshots because capture depth is expressed in lines.

## Rollback

The coherent replay was merged with merge commit `aac963d`. To remove the
entire feature after reverting any later follow-up fixes, use:

```bash
git revert -m 1 aac963d
```

If only the CRLF normalization causes trouble, revert the follow-up commit
whose subject is `fix tmux snapshot newline rendering`; this restores coherent
capture while returning captured output unchanged:

```bash
git log --oneline --grep='fix tmux snapshot newline rendering'
git revert <commit-from-the-command-above>
```

After either rollback, rerun the focused terminal tests and manually verify a
reconnect before publishing a build.
