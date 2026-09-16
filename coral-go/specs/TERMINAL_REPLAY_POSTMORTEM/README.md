# Terminal Replay Snapshot Experiment — Postmortem

**Status:** Rejected and reverted
**Experiment date:** 2026-09-15
**Scope:** Historical rendering when reconnecting to tmux-backed xterm sessions
**Related specs:** `TERMINAL_UNIFIED_STREAM`, `XTERM_FLICKER`

## Summary

Coral attempted to improve historical terminal rendering by replacing the
tmux backend's raw `pipe-pane` replay with a `tmux capture-pane` snapshot. The
idea was reasonable: the raw log is a stream of terminal operations, while
tmux already maintains an interpreted screen and scrollback grid.

The experiment fixed the first observed symptom in isolation, but introduced
more severe rendering failures:

1. Captured rows rendered diagonally because tmux emitted LF separators and
   xterm did not implicitly return the cursor to column zero.
2. Converting every separator to CRLF turned soft-wrapped rows into permanent
   hard lines, preserving historical narrow widths.
3. Joining tmux soft-wrapped rows fixed that width symptom but did not remove
   obsolete full-screen redraws already stored in tmux history.
4. TUI resize/redraw activity caused complete Claude screens and prompts to
   appear multiple times in the replay.

The core failure was treating `capture-pane` as a canonical conversation
history. It is not. It is a snapshot of tmux's current grid plus tmux
scrollback, and that scrollback may contain obsolete intermediate TUI frames.
No combination of newline conversion and soft-wrap joining can determine
which historical rows were meaningful output and which were superseded
redraws.

The complete experiment was reverted. Coral again uses the raw replay path
defined by `TERMINAL_UNIFIED_STREAM`.

## Original Problem

On WebSocket connection, the tmux backend reads the final portion of its
`pipe-pane` log and writes those bytes into a new xterm instance. The replay is
prefixed with clear-screen and clear-scrollback escape sequences.

This has a known limitation: a byte suffix can begin midway through a terminal
update. The missing prefix may have established cursor position, color state,
screen mode, or content that a later escape sequence modifies. The resulting
xterm can therefore show:

- a partial older response above the final response;
- content wrapped at an earlier terminal width;
- stale text that was later overwritten;
- incorrect state when replay begins inside an ANSI sequence or redraw.

The experiment tried to replace this incomplete operation stream with tmux's
interpreted state.

## Attempted Design

The implementation made four related changes.

### 1. Replay from `capture-pane`

`TmuxBackend.Replay` stopped reading the last configured number of bytes from
the `pipe-pane` log. It instead called `tmux capture-pane -p -e` with a
scrollback depth estimated from `terminal_replay_bytes`.

The intended benefit was that tmux would resolve cursor movements, erasures,
and overwritten cells before Coral sent content to xterm.

### 2. Resize before capture

The browser added its xterm rows and columns to the WebSocket query string.
The server resized tmux before attaching the live tail and capturing replay.
This was intended to make tmux reflow its grid to the browser's current width.

### 3. Normalize captured newlines

After the first screenshot exposed staircase-shaped output, captured LF
separators were converted to CRLF before being sent to xterm.

### 4. Join soft-wrapped rows

After CRLF normalization preserved old narrow widths, `capture-pane -J` was
added so rows marked by tmux as soft wrapped would be returned as one logical
line. A redundant resize sent from the browser's WebSocket `onopen` handler was
also removed to reduce post-snapshot TUI redraws.

## What Failed

### Failure 1: `capture-pane` output is text, not a terminal byte stream

The unified terminal WebSocket sends replay and live output through the same
binary channel, but the two payloads had different semantics after this
change:

- live data remained raw PTY operations;
- replay became a textual rendering of tmux grid rows.

`capture-pane` separates rows with LF. In normal xterm mode, LF moves the
cursor down but does not necessarily perform carriage return. Writing this
text directly caused each new row to begin at the column where the prior row
ended, producing diagonal or staircase rendering.

CRLF normalization repaired this symptom, but it exposed the next mismatch.

### Failure 2: Physical rows are not logical lines

Plain `capture-pane` output places a newline after every captured grid row.
Some rows end because the application emitted a newline; others end only
because the terminal reached its right edge. Converting every separator to
CRLF erased that distinction and converted soft wraps into hard lines.

The result was internally aligned but permanently formatted at a historical
narrow width. Widening the Coral panel could not reflow those hard lines.

The tmux `-J` option joins rows tmux marks as wrapped and improved this case,
but it could not solve historical redraw pollution.

### Failure 3: Tmux scrollback contains superseded TUI frames

Claude Code is an interactive terminal UI. It redraws prompts, headers,
instructions, progress, and response regions. Resize events can cause large
parts of the display to be repainted.

Tmux's grid correctly represents the current visible screen, but its
scrollback may also contain earlier physical screen rows displaced by those
redraws. From tmux's perspective these are valid historical rows. From a user
perspective many are obsolete versions of the same UI.

Consequently, a capture containing scrollback showed repeated Claude headers,
prompts, instructions, and response paragraphs. `capture-pane` had resolved
cell overwrites on the current screen, but it had no semantic basis for
removing superseded frames already pushed into history.

This is the decisive reason the approach failed. Newline and wrap metadata can
repair row geometry, but cannot identify duplicate semantic content.

### Failure 4: Snapshot and live stream had no atomic boundary

The server attached the live log tail and then captured the snapshot. This
ordering avoids losing output, but output produced during capture may be
present in both places:

1. the output is incorporated into the tmux snapshot;
2. the same raw bytes are queued for the live subscriber;
3. xterm renders the snapshot and then renders those bytes again.

Reversing the order would replace duplication with a loss window between
capture and live attachment. Tmux does not provide Coral with an atomic
"capture this grid and continue the pipe from the corresponding byte offset"
operation.

Resizing immediately before capture made this boundary more active because a
TUI commonly redraws in response to `SIGWINCH`. Removing one redundant resize
reduced the opportunity but could not make the two data sources atomic.

### Failure 5: Existing sessions remained polluted

Once resize redraws had entered tmux scrollback, changing later capture flags
could not remove them safely. Clearing tmux history would also delete valid
user history. This made iterative visual testing confusing: a corrected build
could continue showing artifacts accumulated by an earlier build unless the
session was restarted.

## Why the Automated Tests Passed

The tests covered important mechanics but not the full behavioral model:

- a line overwritten in the current grid did not reappear;
- LF was converted to CRLF;
- tmux soft-wrapped rows were joined;
- browser dimensions were applied before replay;
- replay and live frames still used the expected WebSocket types.

These tests used simple shell output. They did not model a long-lived TUI that
redraws multiple screen regions, responds asynchronously to resize, pushes
obsolete frames into scrollback, and emits output concurrently with capture.

The tests therefore demonstrated that each local transformation worked, but
not that `capture-pane` scrollback was a valid replacement for a terminal
history stream. The missing test was an end-to-end reconnect scenario using a
redrawing TUI across repeated resize and agent-switch cycles.

## Timeline and Commits

The experimental implementation and fixes were:

- `2f72bd1` — initial coherent terminal history replay
- `aac963d` — merge of the initial implementation
- `38fa531` — normalize tmux snapshot newlines
- `ae3947c` — preserve soft wraps and remove redundant initial resize

They were reverted in reverse order:

- `25e2f8f` — revert soft-wrap changes
- `3b8dfa5` — revert newline normalization
- `4369b00` — revert the initial merged implementation

The reverts intentionally restore the earlier raw `pipe-pane` replay behavior,
including its known limitations.

## Lessons

### Do not mix representations without an explicit protocol boundary

A rendered grid snapshot and an incremental PTY event stream are not directly
interchangeable. A future protocol must identify the snapshot format, define
cursor and wrap semantics, and define exactly where incremental events begin.

### Terminal history is not conversation history

Terminal scrollback records visual behavior. Interactive applications may
redraw the same semantic content many times. If Coral wants a clean historical
conversation, agent JSONL or another semantic transcript is a better source
than terminal scrollback.

### Wrap state is data

The distinction between hard line endings and soft terminal wraps must survive
serialization. Plain strings with newline separators cannot represent it.

### Snapshot-to-stream handoff must be atomic

Any future snapshot design needs a sequence number, byte offset, paused source,
or backend primitive that binds the snapshot to the exact first live event.
Choosing between "attach then snapshot" and "snapshot then attach" merely
chooses between possible duplication and possible loss.

### Test with real TUI behavior

Shell `echo` and `printf` tests are insufficient for terminal reconstruction.
Regression coverage needs repeated redraws, cursor addressing, erasure,
alternate-screen transitions, resize signals, scrollback overflow, and output
during reconnect.

## Safer Future Directions

### Option A: Persist terminal-emulator checkpoints

Feed the complete raw stream into a server-side VT emulator and periodically
serialize its full state, including:

- visible cells and attributes;
- cursor and terminal modes;
- primary and alternate buffers;
- scrollback;
- hard-line versus soft-wrap metadata;
- the exact raw-log byte offset represented by the checkpoint.

On reconnect, restore the checkpoint and replay raw bytes strictly after its
offset. This is the most direct architectural solution, but it adds a terminal
emulator implementation and checkpoint compatibility concerns.

### Option B: Stream from a real tmux client attachment

Investigate attaching a PTY client to the persistent tmux session and relaying
the bytes tmux emits to initialize and update that client, rather than scraping
`capture-pane`. This may let tmux perform its normal client synchronization.
It still requires careful multi-client sizing and proof that reconnect output
has a lossless handoff to ongoing updates.

### Option C: Use semantic agent transcripts for historical viewing

Keep xterm for the live interactive terminal, but render completed or older
agent output from Claude/Codex JSONL messages. This avoids TUI redraw artifacts
and provides clean reflowable text. It will not reproduce arbitrary shell or
full-screen program output, so Coral would need to label the view as a
transcript rather than an exact terminal recording.

### Option D: Keep raw replay and improve checkpoint boundaries modestly

Retain the current architecture, but record safe reset/checkpoint markers when
Coral controls session creation or detects a complete terminal reset. Replay
could begin at the newest known-safe marker rather than an arbitrary byte
offset. This is less complete than a terminal emulator but preserves one data
representation from replay through live streaming.

## Recommendation

Do not reintroduce `capture-pane` scrollback as the WebSocket replay seed.

For a near-term user-facing improvement, prefer a semantic transcript for
historical agent output while retaining raw xterm streaming for live control.
For exact terminal restoration, prototype a stateful terminal-emulator
checkpoint with an explicit raw-log offset and test it against a real
redrawing agent TUI before changing the production replay path.

