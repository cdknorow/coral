# Unified Terminal Streaming Spec

**Status:** Shipped (merged to main 2026-04-23 — commit 9526729, merge 19361b1)
**Owner:** —
**Depends on:** none
**Supersedes:** `TMUX_POLLING_SPEC`, partially `XTERM_FLICKER`

## Summary

Replace Coral's dual terminal-delivery architecture (PTY streaming + tmux capture-pane polling) with a single raw-byte streaming protocol. Tmux is retained as a process-persistence and attach layer, but its output is streamed via `pipe-pane` instead of polled via `capture-pane`. Both backends emit the same wire format — raw ANSI bytes — and the client writes them to xterm.js without interpretation.

This eliminates the flicker, cursor-positioning hacks, and dual-code-path bug surface documented in `XTERM_FLICKER`, preserves session survival across `coral-go` restart, and keeps `tmux attach` available for operators.

## Background

### Current architecture

Coral's web terminal uses two incompatible delivery modes, selected at WebSocket-connect time based on whether the backend's `Subscribe()` returns a non-nil channel (`websocket.go:484-501`):

**PTY streaming (`wsTerminalStreaming`):**
- Server subscribes to a PTY pub/sub channel, forwards every chunk as `{type: "terminal_stream", data: <raw bytes>}`.
- Initial snapshot from a 256 KiB ring buffer, replayed on connect.
- Client appends via `terminal.write(data)` — xterm handles cursor, alt-screen, scrollback natively.
- Sessions do not survive `coral-go` restart.

**Tmux polling (`wsTerminalPolling`):**
- Server polls `tmux capture-pane -e -S-200` on fsnotify/mtime changes (throttled to 15 ms).
- Sends `{type: "terminal_update", content, cursor_x, cursor_y, alt_screen}` — a full-screen snapshot plus cursor metadata.
- Client clears screen + scrollback (`\x1b[2J\x1b[3J\x1b[H`) and rewrites on every update.
- Sessions survive `coral-go` restart (tmux daemon outlives the parent).

### Problems

The dual-protocol design is the root cause of most current terminal bugs:

1. **Flicker is unfixable within polling** (`XTERM_FLICKER/README.md:59-96`). Six approaches tried, all rejected — the `\x1b[2J\x1b[3J` clear-on-every-update is structural.
2. **Cursor positioning is four stacked workarounds** (`XTERM_FLICKER/README.md:100-154`): dual-cursor toggle, alt-screen flag, scrollback offset math, multi-client mismatch noted as unresolved.
3. **Every client-side bug has two code paths.** The scrollback-stacking bug fixed 2026-04-21 (`websocket.go:518-527`) only affected the streaming path because polling already clears on every update — that asymmetry will bitrot.
4. **Client can't tell which mode it's in.** The mode is decided server-side based on an interface quirk — `TmuxBackend.Subscribe()` returns `(nil, nil)` to force fallback (`tmux_backend.go:168-173`). The client has to handle both message shapes simultaneously.
5. **Polling spawns subprocess load** (`TMUX_POLLING_SPEC/README.md:29-33`): N agents × M tabs × ~10 captures/sec. Prior optimization work mitigated but did not eliminate this.

## Prior Art: coder/coder

coder solved the same problem in `agent/reconnectingpty/`. Their architecture is the reference for this proposal.

**Single interface, single wire format** (`reconnectingpty.go`): `ReconnectingPTY{Attach, Wait, Close}`. Clients send JSON in, receive raw bytes out. No dual message types; no cursor metadata; no alt-screen flag. The byte stream IS the authoritative terminal state.

**Two backends behind the interface:**

- **`buffered.go`** — in-process fallback. 64 KiB `circbuf` stores scrollback; `doAttach()` replays the full buffer to the new conn before live output starts (lines 195-209). Multiple clients tracked in a map; live output fan-out multiplexes to all (lines 76-100). Sessions do NOT survive agent restart.
- **`screen.go`** — GNU screen backend, preferred when available. Each session is a `screen -S <id>` daemon. Attach runs `screen -x -RR -q`, which reattaches if present or creates if absent (lines 213-217). Scrollback handled natively by screen. Sessions survive coder-agent restart because the screen daemon outlives the parent.

**Key properties of coder's design:**

- The wire protocol is the same for both backends: raw bytes.
- Backend selection is an implementation detail of the server; the client does not know or care.
- Screen is a persistence layer, not a render source — coder never polls `screen hardcopy`. They let screen own the session, and stream its output directly.
- Attach-time daemon spawn (not creation-time) avoids the hardcoded 24×80 initial-size problem (`screen.go:59-63`).
- Mutex around attach guards against concurrent `screen -S <id>` spawns creating duplicate daemons (`screen.go:36-42`).
- Resize is last-writer-wins across multiple clients — accepted and documented.
- Inactivity timeout closes idle PTYs.

## Proposed Architecture

### Wire protocol

One message type per direction.

**Server → client:**
```json
{"type": "stream", "data": "<raw PTY bytes, base64 or utf-8 string>"}
```
Sent both as the initial replay and as live output. Client writes directly to xterm via `terminal.write(data)`. No clearing, no cursor metadata, no alt-screen flag.

Other server-originated messages remain: `terminal_closed`, `mode` (optional, advisory), `resize_ack` (optional).

**Client → server:** unchanged.
```json
{"type": "terminal_input", "data": "<keystrokes>"}
{"type": "terminal_resize", "cols": N, "rows": M}
```

### Backend interface

Replace the current asymmetric `TerminalBackend` with a unified interface that both backends implement identically:

```go
type TerminalBackend interface {
    Spawn(name string, cmd []string, opts SpawnOptions) error
    Attach(name string, subscriberID string) (<-chan []byte, error)  // never nil for a live session
    Replay(name string) ([]byte, error)                              // recent history for initial seed
    Unsubscribe(name, subscriberID string)
    SendInput(name string, data []byte) error
    Resize(name string, cols, rows uint16) error
    Close(name string) error
    ListSessions() []SessionInfo
}
```

`Subscribe`/`CaptureContent` merge into `Attach` + `Replay`. `Attach` is authoritative — it never returns nil. If a backend cannot stream, it is not a valid backend.

### Tmux backend — stream via pipe-pane, not capture-pane

This is the load-bearing change. Coral already uses `pipe-pane` to log agent output to disk (one of the reasons the tmux polling path watches mtime in the first place). That pipe is a raw byte stream of PTY output — exactly what the unified protocol requires.

**New `TmuxBackend.Attach` flow:**

1. Ensure `pipe-pane -o 'cat >> <logPath>'` is active for the session.
2. Open the log file for reading, seek to end (or to a backfill offset — see "Replay" below).
3. Tail the file with fsnotify for `Write` events; on each event, read new bytes and emit on the subscriber channel.
4. Fan out to multiple subscribers from a single tail goroutine (one tail per session, N subscribers).

**New `TmuxBackend.Replay` flow:** read the last M bytes of the pipe-pane log file (M tunable, default ~256 KiB to match PTY ring buffer). This replaces `capture-pane -S-200` entirely.

**No more polling.** `wsTerminalPolling`, the 15 ms throttle, `DisplayMessage`, `CaptureRawOutput`, the cursor-metadata query — all deleted.

### Client simplification

`xterm_renderer.js` drops ~150 lines:

- `onmessage` collapses to one branch: `terminal.write(data.data)`.
- Delete `_pendingContent`, `_flushPending`, `_xtermSelecting` buffering logic.
- Delete wheel-scroll pause logic (xterm's native scrollback handles this).
- Delete dual-cursor `\x1b[?25l/h` toggle.
- Delete `alt_screen` handling — the ANSI stream carries `\x1b[?1049h/l` natively.

Selection-pause badge remains if we want it, but it can be removed too — xterm's own selection is non-destructive.

### Session persistence

Inherited from tmux, unchanged from today. The tmux daemon owns session processes and outlives `coral-go`. On restart, `startup.go:reconcileOrphanedSessions` continues to discover living sessions via `tmux list-panes`. PTY backend still loses sessions on restart; this is accepted (matches coder's `buffered` backend).

### Scrollback

- **Live:** xterm.js internal scrollback, capped by `terminal_scrollback` setting (default 20000 lines) — `xterm_renderer.js:174`.
- **On reconnect:** server replays the last M bytes from the log file (tmux) or ring buffer (PTY). Replay is prefixed with `\x1b[2J\x1b[3J\x1b[H` so reconnects don't stack duplicates (already done for PTY, websocket.go:516-529; extend to tmux).

### Cursor and alt-screen

Neither is sent as metadata. The raw byte stream contains the shell's own ANSI cursor-position sequences and alt-screen enter/leave codes. xterm.js interprets them correctly. This deletes all of `XTERM_FLICKER`'s "Cursor Positioning" section.

### Multi-client behavior

Matches coder. All attached clients receive the same byte stream. Input from any client is serialized to the PTY. Resize is last-writer-wins. Two browsers on the same session see identical output; typing from either goes to the same shell.

## Migration Phases

### Phase 1 — Tmux pipe-pane stream prototype (2-3 days)

**Goal:** Tmux backend can serve via the same streaming protocol as PTY, behind a feature flag.

- Add `TmuxBackend.Attach` that tails the pipe-pane log via fsnotify and emits raw bytes.
- Add `TmuxBackend.Replay` that reads last N bytes of the log.
- Behind a flag (`--terminal-stream-mode=unified` or similar), wire `wsTerminalStreaming` to work with both backends.
- Keep `wsTerminalPolling` in place as the default — this phase ships dormant code only.

**Exit criteria:** With the flag on, tmux-backed sessions render via `terminal_stream` messages, no `capture-pane` calls happen, one browser and one `tmux attach` both see live output.

### Phase 2 — Flip the default, keep fallback (1 week observation)

- Default to unified streaming mode. Polling path remains in code as fallback for bugs.
- Monitor: latency, missed output, reconnect correctness, multi-client behavior, memory (one tail goroutine per session + ring of bytes per subscriber).

### Phase 3 — Client cleanup (1-2 days)

- Delete `terminal_update` handling, `_pendingContent`, cursor hacks, wheel-scroll pause, alt-screen flag from `xterm_renderer.js`.
- Server stops sending `terminal_update` permanently.

### Phase 4 — Server cleanup (1-2 days)

- Delete `wsTerminalPolling` and everything it calls: `CaptureRawOutput`, `DisplayMessage`-for-cursor, fsnotify+throttle scaffolding specific to polling, the 15 ms throttle constant.
- Simplify the `TerminalBackend` interface per the proposed shape.
- `selectBackend` no longer has to reason about streaming vs polling capability.

### Phase 5 — Retire specs

- `TMUX_POLLING_SPEC` → superseded, mark shipped/retired.
- `XTERM_FLICKER` → flicker section obsolete; keep cursor-positioning section only as historical context.

Phases 3-5 are "when it feels solid" — no forced timeline.

## Tradeoffs

### What we gain

- **One protocol, one client code path, one bug surface.**
- **No flicker** — no clear-and-redraw in the browser.
- **Accurate cursor in all modes** — ANSI stream is authoritative.
- **Lower server load** — no capture-pane subprocess storm (`TMUX_POLLING_SPEC` numbers obsolete).
- **Session survival preserved** via tmux daemon.
- **`tmux attach` keeps working** — tmux still owns the session.
- **Multi-client improves** — coder-style fan-out gives every client the same byte stream without per-client tmux subprocess cost.

### What we give up

- **No cursor query.** Today polling mode can recover cursor position from `display-message` even if the stream has ANSI drift. With unified streaming, we trust the shell's own ANSI output. This is how every other terminal-in-browser (coder, ttyd, Wetty) works.
- **Replay accuracy depends on log-file correctness.** If pipe-pane fails to write, reconnect replay is incomplete. Mitigation: health check pipe-pane status at attach time; fall back to `capture-pane` only as emergency seed.
- **New operational surface: log file growth.** Pipe-pane logs need rotation/truncation. Today we already have this; it just becomes more load-bearing.

### What we don't solve

- **PTY backend session loss on restart.** Same as today. If the user wants PTY sessions to survive restart, that's a separate project (detached supervisor process, out of scope).
- **Operator `screen`/`tmux attach` cursor mismatch with multi-client resize.** Last-writer-wins is the industry-standard compromise; documenting, not fixing.

## Files Involved

**Delete:**
- `internal/server/routes/websocket.go:wsTerminalPolling` and its helpers (~300 lines)
- `pkg/frontend/static/xterm_renderer.js`: `terminal_update` branch, `_pendingContent`, wheel-scroll pause, dual-cursor toggle (~150 lines)
- `specs/TMUX_POLLING_SPEC/` — superseded
- `specs/XTERM_FLICKER/` cursor-positioning section — obsolete

**Modify:**
- `internal/ptymanager/backend.go` — unify the `TerminalBackend` interface
- `internal/ptymanager/tmux_backend.go` — replace `CaptureContent` + nil-`Subscribe` with real `Attach`/`Replay` via pipe-pane tail
- `internal/ptymanager/manager.go` — align `PTYBackend.Attach` signature with new interface (mostly renames)
- `internal/server/routes/websocket.go:wsTerminalStreaming` — becomes the only handler; prefix `Replay` output with `\x1b[2J\x1b[3J\x1b[H` for tmux backend too
- `pkg/frontend/static/xterm_renderer.js` — collapse handlers
- `pkg/frontend/static/agent_docs/websockets.md` — document single `stream` message

**Add:**
- Pipe-pane log tail goroutine per tmux session (one producer, N subscribers fan-out)
- Feature flag for Phase 1/2 rollout

## Open Questions

1. **Log file location for pipe-pane streaming.** Today the log path is per-agent and may be rotated. Do we need a second dedicated pipe-pane pipe, or is the existing agent log reusable as the stream source? (Needs confirmation by reading `session.go:57-61` and `tmux_backend.go:56`.)
2. **Binary safety of the log file.** If agents print high bytes or partial UTF-8 sequences, we must forward them verbatim. JSON-encoded strings handle this if we either base64 or UTF-8-tolerant-encode. Verify coder's JSON message format for reference.
3. **Replay size tuning.** 256 KiB matches the PTY ring buffer but may under-serve long Claude Code sessions with dense TUI output. Default and setting TBD.
4. **Do we keep PTY backend at all?** With tmux streaming cleanly, PTY backend's only remaining niche is Windows (where tmux is unavailable). Confirm this matches today's Windows default (`main.go:58-62`).
