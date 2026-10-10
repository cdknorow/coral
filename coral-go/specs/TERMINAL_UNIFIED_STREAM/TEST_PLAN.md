# Unified Terminal Streaming — Test Plan

**Scope:** verification of the migration described in `README.md` — single raw-byte streaming protocol for both PTY and tmux backends, replacement of `wsTerminalPolling` by a unified `wsTerminalStreaming`, and collapse of the dual client handler in `xterm_renderer.js`.

**Owner:** QA Engineer
**Executed in:** Phase D (after Phases B/C land)
**Prior phases:** Phase A research spike may refine specific cases; this plan is the starting contract.

## Preconditions (global)

All tests run on branch `terminal-unified-stream` after Phases B and C have landed.

- `coral-go` built with `make dev` (skips EULA + license).
- tmux ≥ 3.2 available on `$PATH` (macOS/Linux paths).
- Test server command: `./coral --host 0.0.0.0 --port 8450`.
- A test workspace under `~/.coral/workspaces/qa-term/` is clean at the start of each case.
- Clear any orphaned tmux sessions before a run: `tmux kill-server` (ok to run as a test hook; no production state).
- Browser: Chromium-based (Chrome or Edge) and Firefox, for cross-engine verification on selected cases.
- Agent-docs reminder: run `make build` before manual tests so the frontend is up-to-date (`CLAUDE.md` — agent_docs sync).

### How to read each case

Each case specifies:
- **Type:** `unit` (Go `go test ./...`) / `integration` (Go test against a live backend) / `manual-browser` (run server, drive xterm).
- **Preconditions:** what must be true before the steps.
- **Steps:** ordered, reproducible.
- **Pass:** objective, observable pass criteria.
- **Fail:** observable failure signals — if any appear, file a bug and block.

Unless otherwise stated, a failed step invalidates the case.

---

## 1. Single-Client Happy Path — Tmux Backend

### 1.1 Plain shell output renders, no flicker (manual-browser)
**Preconditions:** tmux backend active (non-Windows). New tmux session created via Coral UI. Browser console open to watch for warnings.

**Steps:**
1. Create a new agent; open its terminal tab in one browser.
2. At the shell prompt, run: `for i in $(seq 1 200); do echo "line $i"; done`.
3. Observe rendering during and after the burst.
4. Scroll up with the mouse wheel / trackpad.

**Pass:**
- No visible blanking between lines — no flash-of-empty frame.
- All 200 lines present in scrollback after the burst completes.
- Scrolling up reveals earlier lines without page-jump or re-flow to the top.
- No `terminal_update` messages observed in browser DevTools → Network → WS frames. Only `terminal_stream` (or the renamed `stream` per spec §Wire protocol) messages.
- No `\x1b[2J\x1b[3J` sequences written mid-stream (only permitted in the initial replay seed).

**Fail:** visible flicker; missing lines; scrollback cleared; `terminal_update` messages observed; renderer `_pendingContent` code path still active (see xterm_renderer.js:55-147).

### 1.2 Alt-screen app — vim (manual-browser)
**Preconditions:** as 1.1. `vim` installed.

**Steps:**
1. Run `vim /tmp/qatest.txt`.
2. Enter insert mode; type a paragraph of mixed ASCII + unicode: `héllo — wörld ✅ 日本語`.
3. Move the cursor with arrow keys; observe cursor position after each move.
4. Save and quit (`:wq`).
5. Re-run `vim /tmp/qatest.txt` and confirm file contents match what was typed.

**Pass:**
- xterm native cursor tracks the shell's reported position (no "two cursors", no reverse-video+native duplicate — the dual-cursor toggle documented in `XTERM_FLICKER/README.md:100-154` is gone).
- Alt-screen enter produces no scrollback pollution: on `:q`, browser scrollback is exactly what it was before `vim` launched (the shell's own `\x1b[?1049l` restores).
- All unicode characters display correctly; cursor column matches grapheme position.

**Fail:** two cursors; cursor off by N columns; unicode mojibake; scrollback polluted with vim TUI lines.

### 1.3 Alt-screen app — less (manual-browser)
**Preconditions:** as 1.1.

**Steps:**
1. `less /var/log/system.log` (macOS) or any long text file.
2. Page down (`Space`), up (`b`), search (`/error`).
3. Quit (`q`).

**Pass:** paging feels smooth; no flash-of-empty between pages; on quit, pre-less scrollback is intact.
**Fail:** blank frames during paging; prompt lost after quit.

### 1.4 Alt-screen app — htop (manual-browser)
**Preconditions:** as 1.1. `htop` installed.

**Steps:**
1. Run `htop`.
2. Let it refresh for at least 10 seconds.
3. Use arrow keys to navigate the process list.
4. Quit (`q`).

**Pass:** refresh rate smooth; column widths stable; no tearing; on quit, scrollback unchanged.
**Fail:** visible redraw glitch each tick; column widths jumping; tearing.

---

## 2. Multi-Client Fan-Out

### 2.1 Two browsers, identical byte stream (manual-browser)
**Preconditions:** tmux backend active. Two browser tabs (same or different browsers) attached to the same agent terminal.

**Steps:**
1. Open agent terminal in Browser A.
2. Open same agent terminal in Browser B.
3. Type `echo hello-from-B` in Browser B.
4. Observe Browser A.
5. Type `echo hello-from-A` in Browser A.
6. Observe Browser B.

**Pass:**
- Each echo appears in both browsers within ~100 ms.
- Final scrollback in both browsers is byte-identical (compare via copy-paste into `diff`).
- No duplicated or missing lines in either browser.

**Fail:** lines appear in only one browser; out-of-order rendering; divergent final state.

### 2.2 Fan-out unit coverage (unit — Go)
**Preconditions:** new test in `internal/ptymanager/tmux_backend_test.go` (to be added by Lead Dev; QA verifies existence and correctness).

**Expected coverage:**
- `TmuxBackend.Attach(name, subA)` and `TmuxBackend.Attach(name, subB)` return two distinct non-nil channels.
- Writing a fixed byte pattern to the underlying pipe-pane log produces the same bytes on both channels.
- `Unsubscribe(subA)` stops delivery to `subA` but not `subB`.
- No goroutine leak after both subscribers unsubscribe (verify via `goleak` or runtime.NumGoroutine delta).

**Pass:** `go test ./internal/ptymanager/... -run TestTmuxBackendFanOut -race` passes.
**Fail:** flakiness under `-race`; either channel missing bytes; goroutine leak.

### 2.3 Input serialization (manual-browser)
**Preconditions:** 2.1 setup.

**Steps:**
1. Simultaneously type long strings in Browser A and Browser B (e.g., both press-and-hold `j`).
2. Observe what reaches the shell (end-of-line prompt echoes, or run `cat` and watch stdin).

**Pass:** all keystrokes from both clients arrive at the shell; no crash; order within a single client is preserved (interleaving across clients is acceptable — tmux serializes input per-pane).
**Fail:** lost keystrokes; shell crashes; pane becomes unresponsive.

---

## 3. Reconnect Replay

### 3.1 Close and reopen single browser (manual-browser)
**Preconditions:** tmux backend active. One browser attached to an agent that has produced ~500 lines of scrollback.

**Steps:**
1. Produce output in the terminal (`seq 1 500`).
2. Note the last 10 lines visible.
3. Close the browser tab.
4. Wait 5 seconds.
5. Reopen the agent terminal in a new tab.
6. Observe initial render.
7. Scroll up.

**Pass:**
- Initial render shows the same tail — last 10 lines match what was noted in step 2.
- Browser shows at most one copy of each line (no duplication from stacking replay seeds — the scrollback-stacking bug fixed 2026-04-21 must not regress).
- No flash-of-empty: initial content appears in one paint frame, not blank→content.
- Scrolling up shows the earlier lines up to the server's replay window (~256 KiB by default, per spec §Scrollback).

**Fail:** duplicate lines; blank frame on reconnect; missing tail; empty scrollback on second open.

### 3.2 Reconnect while agent is actively writing (manual-browser)
**Preconditions:** agent running `while true; do date; sleep 0.1; done` in the terminal.

**Steps:**
1. Close the browser tab while output is streaming.
2. Reopen the terminal within 2 seconds.

**Pass:** replay seed ends cleanly, then live stream resumes at the current agent output with no gap longer than ~1 second and no duplicated lines at the seam.
**Fail:** gap of many seconds; duplicate lines at the seam; garbled ANSI at the boundary.

### 3.3 Replay window size (integration)
**Preconditions:** new test writes >300 KiB to a tmux-backed session's log, then calls `TmuxBackend.Replay(name)`.

**Pass:** returned slice has length ≤ 256 KiB (or the configured `terminal_replay_bytes` setting) and contains the *tail* of the log, not the head.
**Fail:** replay returns the head of the file; replay returns the entire file unbounded.

---

## 4. Resize

### 4.1 Single-client resize stability (manual-browser)
**Preconditions:** terminal open, running `bash` at a normal prompt.

**Steps:**
1. Resize the browser window horizontally to narrow (~40 cols), then wide (~200 cols), then back.
2. Observe prompt and any visible output.
3. Run `tput cols; tput lines` after each resize.

**Pass:** `tput cols`/`tput lines` reflect the final window size; prompt redraws correctly; no stray characters left from the previous width.
**Fail:** tput values lag behind by one resize; corrupted prompt; terminal becomes unresponsive.

### 4.2 Multi-client last-writer-wins (manual-browser)
**Preconditions:** two browsers on the same session; different window sizes.

**Steps:**
1. Resize Browser A to 80×24.
2. Resize Browser B to 120×40.
3. Run `tput cols; tput lines` — note the reported size (should match whichever client resized last, i.e. B).
4. Trigger a resize in A again (resize window slightly).
5. Run `tput cols; tput lines` again — should now match A.

**Pass:** tmux pane size matches whichever client most recently sent a `terminal_resize` message. The other client's xterm viewport may show visual mismatch — that is accepted per spec §Tradeoffs.
**Fail:** pane size diverges from any client's last-sent value; resize message lost; shell crashes.

### 4.3 Resize during TUI (manual-browser)
**Preconditions:** `htop` running (from case 1.4).

**Steps:**
1. While `htop` is running, resize the browser window.
2. Observe `htop` redraw.

**Pass:** `htop` redraws to new dimensions within one refresh tick; no corruption of background shell on exit.
**Fail:** columns misaligned after resize; shell prompt garbled after `htop` exits.

---

## 5. Binary Safety & Wire Format

**Wire format (Phase A decision, pending final operator approval):** stream data is sent as **binary WebSocket frames** (`websocket.MessageBinary` server-side; `event.data instanceof ArrayBuffer` client-side, written via `terminal.write(new Uint8Array(event.data))`). Control messages (`terminal_closed`, `resize_ack`, `mode`) remain **JSON text frames**. This eliminates the `string(data)` UTF-8 sanitization path in today's code that silently replaces invalid bytes with U+FFFD.

### 5.1 High-byte echo (integration)
**Preconditions:** Go test spawns a session and injects bytes via `SendInput` / reads via `Attach`.

**Test payload:** all 256 byte values (`0x00..0xff`) written in batches, plus a known ANSI escape `\x1b[31mRED\x1b[0m`.

**Pass:**
- Every byte written to the session input reaches the subscriber channel (echo via `cat` or a local loopback pipe).
- ANSI-escape sequences arrive byte-identical — no re-encoding, no UTF-8 sanitization damage (bytes must NOT be coerced through `string(data)` on the Go side before hitting the wire).
- No panic on `0x00` or on partial UTF-8 sequences.

**Fail:** any byte dropped; bytes substituted (e.g. `\xff` → `\xef\xbf\xbd` replacement char); sequence split across messages corrupts a multi-byte codepoint.

### 5.2 Partial UTF-8 across chunk boundary (integration)
**Preconditions:** as 5.1.

**Steps:** write the 4-byte emoji `"✅"` (`\xe2\x9c\x85`) split across two separate `SendInput` calls (2 bytes + 1 byte), then continue immediately with another 3-byte emoji.

**Pass:** subscriber receives all 6 bytes in order; when concatenated, they decode to two valid emoji.
**Fail:** bytes lost at the split point; invalid replacement characters introduced.

### 5.3 ANSI-heavy output — Claude Code (manual-browser)
**Preconditions:** Claude Code or Codex agent launched in the session.

**Steps:**
1. Send a long prompt that triggers streaming token output with spinners.
2. Observe rendering for 60 seconds.
3. Copy a region of the spinner-heavy output and paste into a diff tool against the agent's own log file (`~/.coral/logs/...`).

**Pass:** spinner renders without tearing; no stuck "half-drawn" spinner frames. Copy-paste content round-trips byte-for-byte (ignoring terminal newline conversions).
**Fail:** spinner stuck; visible tearing; copy-paste differs materially from log.

### 5.4 ANSI-heavy output — btop (manual-browser)
**Preconditions:** `btop` installed.

**Steps:**
1. Run `btop`.
2. Let it render for 30 seconds.
3. Resize the browser during render.
4. Quit (`q`).

**Pass:** gauges and braille characters render correctly; colors stable; resize redraws cleanly; on quit, scrollback unchanged.
**Fail:** wrong Unicode codepoints rendered; colors stuck; tearing.

### 5.5 Stream data arrives as binary WebSocket frames (manual-browser)
**Preconditions:** Chrome/Firefox DevTools → Network → WS frames open on a live terminal session.

**Steps:**
1. Trigger any terminal output (`echo hi`, `seq 1 20`, `htop`).
2. Inspect each inbound WS frame in DevTools.
3. In the DevTools console: `ws = /* get the terminal socket */; ws.binaryType` — verify it reads `"arraybuffer"`.
4. In the renderer source, confirm the `onmessage` handler routes `event.data instanceof ArrayBuffer` → `terminal.write(new Uint8Array(event.data))` and only runs `JSON.parse` for string frames.

**Pass:**
- Inbound frames carrying terminal bytes show as **Binary** (not **Text**) in the DevTools WS view.
- `binaryType` is `"arraybuffer"`.
- Renderer has a binary fast-path; no `JSON.parse` of payload bytes.
- Content-wise: full ANSI byte stream reaches xterm unchanged (spot-check by running `printf '\xff\xfe\xfd'` and confirming those three bytes arrive — xterm may render them as replacement glyphs, but the wire bytes must be preserved).

**Fail:** stream data arrives as text frame; binary-frame branch missing from renderer; `binaryType` default (`"blob"`) still set; visible mojibake from silent re-encoding.

### 5.6 Control messages remain JSON text frames (manual-browser + integration)
**Preconditions:** as 5.5.

**Steps (manual-browser):**
1. Trigger a resize by dragging the browser window.
2. In DevTools → Network → WS, look for `resize_ack` (if implemented) — must be a **Text** frame with valid JSON.
3. Kill the session server-side (e.g. `tmux kill-session -t <name>`).
4. Observe the `terminal_closed` frame — must be **Text** / JSON.
5. If the backend emits an advisory `{"type":"mode", ...}` (per spec §Wire protocol), confirm it too is a Text frame.

**Steps (integration):**
- Add `tmux_backend_test.go` coverage that asserts the server writes `MessageBinary` for stream payloads and `MessageText` (via `wsjson.Write`) for all control messages.

**Pass:**
- `terminal_closed`, `resize_ack`, `mode` all arrive as text frames with parseable JSON.
- Client handles mixed frame types without error (binary fast-path for bytes, JSON parse for text).
- No "unexpected end of JSON input" errors in the browser console.

**Fail:** control message sent as binary; client tries to `JSON.parse` an ArrayBuffer; any silent handler swallowing errors.

---

## 6. Windows PTY Backend

### 6.1 PTYBackend basic streaming (manual-browser, Windows)
**Preconditions:** Windows host, Coral built and running with PTY backend active (tmux unavailable). Browser on the same machine or LAN.

**Steps:**
1. Launch an agent; open its terminal.
2. Run `dir`, `echo hello`, and a multi-line batch (e.g. `for %i in (1 2 3 4 5) do @echo line %i`).
3. Resize the browser window.
4. Close and reopen the browser tab.

**Pass:**
- All output renders via `terminal_stream` (or renamed `stream`) messages — same handler as tmux path.
- Resize updates cols/rows in the PTY (verify via an app that prints its dimensions).
- Reconnect replays the PTY ring buffer tail (~256 KiB); no duplication.
- No regression vs. today's PTY streaming behavior: input responsive, no crash.

**Fail:** any message type other than `terminal_stream`/`stream` on the wire; resize broken; reconnect shows empty terminal; PTY session lost mid-test.

### 6.2 PTYBackend session does NOT survive restart (manual-browser, Windows)
**Preconditions:** as 6.1.

**Steps:**
1. Confirm session running.
2. Kill coral-go (CTRL-C on the server process or `taskkill /IM coral.exe`).
3. Restart coral-go.
4. Re-open browser.

**Pass:** session is *absent* from the agent list — this is the accepted tradeoff per spec §Session persistence. Session reappears as "stopped"; user can restart it manually.
**Fail:** server crashes on restart; stale phantom session that accepts input but never produces output.

### 6.3 PTYBackend unit tests still pass (unit)
**Steps:** `cd coral-go && go test ./internal/ptymanager/... -race` on a Windows runner if available, else cross-compile + integration on macOS/Linux for the portable parts.
**Pass:** all pre-existing tests green; any new unified-interface tests also green.
**Fail:** any test red.

---

## 7. Tmux Attach Operator Path

### 7.1 Operator tmux attach in parallel with browser (manual-browser + terminal)
**Preconditions:** tmux backend active; one browser attached; operator shell on the host.

**Steps:**
1. From the operator's shell: `tmux list-sessions` — note the Coral-managed session name.
2. `tmux attach -t <session>`.
3. Type `echo hello-from-tmux-attach` at the shell in the tmux pane.
4. Observe the browser.
5. In the browser, type `echo hello-from-browser`.
6. Observe the operator's tmux attach pane.

**Pass:** both clients see both echoes; pipe-pane log is not disrupted; detaching from tmux (`C-b d`) does not close the browser connection.
**Fail:** operator attach kicks out the browser (or vice versa); pipe-pane stops writing the log (would break streaming).

### 7.2 Pipe-pane remains active during tmux attach (integration)
**Preconditions:** a unit/integration test that verifies `pipe-pane` status before and after a `tmux attach` operation.

**Pass:** `display-message -p '#{pipe_in}'` or equivalent shows the pipe is still active after attach/detach.
**Fail:** pipe-pane flag flips off after a tmux attach.

---

## 8. Session Survival Across coral-go Restart

### 8.1 Restart, reconnect, session alive (manual-browser)
**Preconditions:** tmux backend; one agent running a long command (`sleep 600 && echo done`).

**Steps:**
1. Confirm the sleep is running (via `ps` or by observing no prompt).
2. Kill coral-go (CTRL-C).
3. Restart: `./coral --host 0.0.0.0 --port 8450`.
4. Open the agent terminal in the browser.

**Pass:**
- Browser attaches; scrollback replay seed shows the pre-restart output.
- When the `sleep` completes, `done` appears live — the shell's state was preserved by the tmux daemon.
- `reconcileOrphanedSessions` (mentioned in spec §Session persistence) discovered the session; agent list shows it as running.

**Fail:** session missing from list; attaching produces "pane not found"; `done` never appears.

### 8.2 Restart during heavy output (manual-browser)
**Preconditions:** agent running `yes | head -c 500000` (stream 500 KB fast).

**Steps:**
1. Start the output; while it's streaming, kill coral-go.
2. Restart immediately.
3. Reopen browser.

**Pass:** no data loss in the pipe-pane log (verify by `wc -c <logfile>` — size should be ≥ the expected 500 KB minus a small boundary window). Browser resumes live streaming once output continues.
**Fail:** log truncated; restart crashes; `pipe-pane` duplicated (double-logging) on restart.

---

## 9. XTERM_FLICKER Regression List

The flicker spec (`specs/XTERM_FLICKER/README.md`) enumerates symptoms and mitigations. Each symptom below MUST be absent under unified streaming.

### 9.1 No `\x1b[2J\x1b[3J` mid-stream (manual-browser)
**Steps:** In Chrome DevTools → Network → WS frames, filter frames from the server. Run `seq 1 100` in the terminal.
**Pass:** only the *initial* replay frame on (re)connect contains `\x1b[2J\x1b[3J`. No subsequent frame contains those bytes.
**Fail:** any non-initial frame contains `\x1b[2J\x1b[3J`.

### 9.2 No alt-screen-only fallback (manual-browser)
**Steps:** In DevTools console, search xterm_renderer.js for the alt-screen cursor toggle code (`\x1b[?25l/h` branching on `data.alt_screen`).
**Pass:** that branch is deleted. The renderer has a single branch: `terminal.write(data.data)`.
**Fail:** the branch still exists.

### 9.3 No scrollback growth from cursor-home overwrite (manual-browser)
**Steps:** run `for i in $(seq 1 10000); do echo $i; done`. Leave the tab open 5 minutes while idle.
**Pass:** browser memory usage (DevTools → Performance Monitor) remains flat after the burst completes. Xterm buffer is capped at the `terminal_scrollback` setting (default 20000 lines).
**Fail:** memory grows unbounded; browser tab goes unresponsive.

### 9.4 No broken render from mis-timed `terminal.clear()` (manual-browser)
**Steps:** run a mix of fast-changing output and user input; confirm content never disappears.
**Pass:** no disappearing content; no need for a write-callback + clear() dance (that code is gone).
**Fail:** content flashes and disappears; `_pendingContent` symbol still referenced in the JS.

### 9.5 No flicker from `terminal.clear()` pre-write (manual-browser)
**Steps:** covered by 1.1 + visual inspection.
**Pass:** as 1.1.
**Fail:** as 1.1.

### 9.6 Dual-cursor artifact absent (manual-browser)
**Steps:** at a `bash` prompt, type, then pause without pressing Enter. Look at the cursor.
**Pass:** exactly one cursor visible — the xterm native cursor. No static reverse-video cell indicating a "tmux cursor".
**Fail:** two cursor artifacts; cursor and reverse-video cell both visible.

### 9.7 Multi-client cursor mismatch documented, not silently broken (manual-browser)
**Preconditions:** two browsers on the same session, different sizes.
**Steps:** observe cursor column in the shell prompt on both.
**Pass:** per spec §Multi-client behavior, last-writer-wins resize is expected. Both cursors may not visually match, but neither browser is broken — both accept input, both render output. No "cursor off by N columns from where typing actually occurs" on the most-recent-resizer.
**Fail:** typing in the most-recent-resizer shows characters in a different position from the cursor.

---

## 10. Code-Surface Cleanup Verification

This section verifies Phase 3/4 deletions actually landed (per spec §Files Involved).

### 10.1 Server-side deletions (unit — go vet / build)
**Check:** `grep -rn "wsTerminalPolling\|doCapture\|CaptureRawOutput\|_pendingContent\|terminal_update" coral-go/` returns:
- Zero hits in `internal/server/routes/websocket.go`.
- Zero hits in `pkg/frontend/static/xterm_renderer.js`.
- `specs/TMUX_POLLING_SPEC/` and superseded `specs/XTERM_FLICKER/` sections marked retired (spec §Phase 5).

**Pass:** as above.
**Fail:** any dead reference to the deleted symbols.

### 10.2 Interface unified (unit)
**Check:** `TerminalBackend` in `internal/ptymanager/backend.go` has only one attach path. Both `PTYBackend` and `TmuxBackend` implement it. `Subscribe` returning `(nil, nil)` to force fallback (tmux_backend.go:168-173) is gone.

**Pass:** `go vet ./...` clean; `go build ./...` clean; interface matches spec §Backend interface shape.
**Fail:** dual shapes; `nil` channel fallback still possible.

### 10.3 Full test suite (unit)
**Command:** `cd coral-go && go test ./... -race -count=1`.
**Pass:** all tests green. No `-race` warnings.
**Fail:** any failure or race.

---

## 11. Out-of-Scope (Documented, Not Tested)

Per spec §Tradeoffs:
- PTY backend session loss on `coral-go` restart — expected, covered advisorily in 6.2 only.
- Multi-client resize visual mismatch on non-most-recent resizer — documented in 4.2 and 9.7; accepted.

---

## Summary Test-Type Counts

| Type | Count |
| --- | --- |
| Unit (Go `go test`) | 6 (2.2, 3.3, 5.1, 5.2, 6.3, 10.1–10.3) |
| Integration (Go against live backend) | 4 (3.3, 5.1, 5.2, 5.6, 7.2) |
| Manual browser | ~24 across §1–§9 (adds §5.5 binary-frame, §5.6 control-frame) |

Execution order: run unit + integration first (fast-fail gate), then manual browser walks in the order §1 → §9. §10 is a post-merge audit. §11 is reference only.

## Open Questions for Phase A Spike — Resolved

Answered by Lead Dev spike (memo: board msg #3073):

- **Q1 (log file location / reuse):** existing pipe-pane log is the stream source — no second pipe. `TmuxBackend.Spawn` already writes `naming.LogFile(logDir, agentType, sessionID)` via `pipe-pane -o 'cat >> <path>'` (tmux_backend.go:56). Case 5.3 no longer needs to distinguish stream vs agent log; they are the same file.
- **Q2 (binary safety of the log file):** wire format is **binary WebSocket frames** for stream data, matching coder's `reconnectingpty`. Client sets `ws.binaryType = 'arraybuffer'` and does `terminal.write(new Uint8Array(event.data))`. JSON text frames remain for control messages only (`terminal_closed`, `resize_ack`, `mode`). Case 5.1/5.2 assertions: bytes arrive byte-identical (no U+FFFD substitution); inspect with `ws.binaryType` set to `'arraybuffer'`.
- **Q3 (replay size tuning):** default **256 KiB**, exposed as `terminal_replay_bytes` config setting (global, per-session optional). Matches today's PTY ring buffer; no regression. §3.3 case stays as written. Add a variant where the setting is raised to 1 MiB and replay still fits.
