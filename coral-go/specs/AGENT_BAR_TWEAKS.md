# Agent Bar Tweaks

**Status:** Shipped

## Overview

A set of targeted changes to the Agents sidebar and the live-session command
toolbar. None of them are structural — the sidebar layout, grouping, and
navigation stay as they are. The goal is to make the sidebar answer the two
questions an operator running many agents actually has:

1. **Which agent needs me right now?**
2. **What is each agent doing?**

Today it answers neither. Every row is labelled "Agent", the goal line is
hidden on all but the selected row, and the most actionable number on screen
(context usage) is unlabelled.

Reference screenshot: 8 sessions across 3 folders, all standalone (no teams).
Every agent row reads `Agent`, the avatars read `CO CO CO` under a `coral-go`
header, and two agents show a solid red `100%` bar with no explanation.

## Problem

### P1. Rows carry no identity

`_renderSessionItem` (`internal/server/frontend/static/render.js`) falls back
to the literal string `"Agent"` / `"Terminal"` when `display_name` is empty.
The goal/summary line is only rendered when the row is the active session:

```js
const goalText = (isActive && s.summary) ? escapeHtml(s.summary) : null;
```

So for every non-selected row the only distinguishing mark is the avatar —
and `_renderAvatar` derives its initials and colour from
`display_name || board_job_title || name`, where `name` is the **folder**.
Under a `coral-go` group header every agent gets `CO`. The avatar duplicates
the header instead of identifying the agent.

Knock-on effects:

- Right-pane header shows `Agent -- 0048997…` (a session id, not a name).
- Command input placeholder reads `Sending to: Agent` (`sessions.js`,
  `selectLiveSession`), which tells the user nothing.
- The "Generate goal" sparkle button only appears on hover, so most users never
  discover that a goal can be produced.

### P2. Mobile-only banner leaks onto desktop (bug)

`_renderSessionItem` always emits:

```html
<div class="session-mobile-banner">Nothing yet — open the terminal</div>
```

for `not_started` agents (and "Waiting for your input" for
`waiting_for_input`). The only CSS for `.session-mobile-banner` is scoped under
`#mobile-session-list` in `css/mobile.css`. On desktop the div has no rule at
all, so it renders as unstyled body text beneath the label. The same state is
simultaneously shown by the `CHECK TERMINAL` pill and the avatar status dot —
three signals for one fact.

`.session-status-chip` and `.session-activity-text` in `css/session.css` are
already `display:none` on desktop; the banner row and meta row were missed.

### P3. The context bar reads as progress

`_renderTokenLine` renders `context_pct` as a bar plus a bare percentage. The
only explanation is a `title` tooltip. A red `100%` next to an agent reads as
"finished", when it actually means "context window exhausted — Claude is about
to auto-compact or start degrading". For someone operating agents this is the
single most useful number in the sidebar, and it is unlabelled. (An earlier
draft also proposed a sidebar action here; that was removed by operator
decision — see D3.)

### P4. The Mode button hides the state it controls

`renderQuickActions` (`controls.js`) renders a button labelled **Mode** that
calls `cycleModeToggle()` — Default → Plan → Accept Edits. The current mode is
not shown anywhere in Coral's UI; the user has to read the terminal.
`detectCurrentMode()` already exists and returns the current mode.

### P5. No "needs attention" rollup

`needsAttention` is computed per row but never aggregated. With a dozen agents
the yellow pill scrolls off-screen and there is no count anywhere. The Agents
nav tab is a plain label.

### P6. Density

`.session-group-item` has 14px vertical padding around a ~40px avatar. Eight
sessions fill the full height of a laptop display. Team users with 10–20
agents will spend most of their time scrolling.

### P7. "Chat" means three things

With the top nav tab renamed to **Chats** (history), the live-session panel
also has a **Chat** tab (transcript) and a **Board Chat** tab (team messages).

## Design

### D1. Identity on every row

Row layout becomes:

```
[avatar]  <name or "Agent">                    [attention pill] [⋮]
          <goal / summary, one line, ellipsized>
          [ctx ▓▓▓▓░░ 69%]
```

- Render `s.summary` on every row, not just the active one. Drop the
  `isActive &&` guard.
- When `summary` is empty, fall back to the first user prompt, truncated to
  one line. `first_prompt` must be present in **both** session payload
  builders — the HTTP list (`routes/sessions.go` `List`) and the WebSocket
  tick (`routes/websocket.go`) — and the client `coral_diff` merge in
  `websocket.js` must not drop it. That merge currently replaces the session
  object and preserves only an allowlist (`commands`, `icon`, `token_*`,
  `context_pct`); prefer `{...old, ...changed}` so fields the WS does not
  send survive by default, rather than growing the allowlist per field.
- When neither exists and the agent is not a terminal, show the goal
  (sparkle) button inline and always visible, next to a muted "No goal yet"
  label. The label is **plain text, not a click target** — it sits where the
  user clicks to select the row, and a mis-click must not fire an action.
- Avatar initials: when `display_name` is empty, derive initials from
  `auto_name` (D8) then `board_job_title`, then the folder/terminal name —
  never from `summary`, which changes as the agent works and would make the
  avatar drift, and never from `first_prompt`, which is a sentence or a path
  and yields nonsense initials (revised in task #85). The role emoji follows
  the same chain. Keep the folder-based colour (keyed on the folder/session
  name) so agents in the same folder still share a hue.
- Row label, right-pane header, terminal label and `Sending to:` placeholder
  use the same `display_name → auto_name → board_job_title → "Agent"`
  (or `"Terminal"`) resolution. `first_prompt` is **goal text only**: it is
  the secondary fallback for the goal line (`summary → first_prompt`) and is
  never a primary name, title or initials source.

Automatic naming and goal generation are D8.

### D2. One signal per state on desktop

- Add to `css/session.css`:
  ```css
  .session-mobile-banner-row,
  .session-mobile-meta { display: none; }
  ```
  Mobile already re-enables these under `#mobile-session-list`.
- Keep the `CHECK TERMINAL` / `NEEDS INPUT` pill as the desktop indicator.
  The avatar dot remains as a secondary, glanceable cue.

### D3. Context bar: label it (informational only)

- Label: `ctx 69%` instead of bare `69%`. Tooltip unchanged.
- Thresholds unchanged (≥50 warning, ≥80 error); label colour follows the
  threshold.
- At 100%, colour is unchanged but the label reads `ctx full`.
- The context display is **informational only**. It renders no button, link,
  or click target at any level, including ≥80% and full. Users compact a
  session by typing `/compact` directly in that session's chat/terminal, which
  is unchanged by this spec.
- History: an earlier revision added a sidebar **Compact** button at ≥80%. It
  was removed by operator decision (task #75) because accidental activation
  was too easy, and `/compact` is one keystroke away in chat.

### D4. Mode button shows current mode

- Button label becomes the current mode: `Default`, `Plan`, `Accept`.
- Keep the same click behaviour (advance one step).
- Refresh the label whenever the terminal buffer is re-scanned (same hook that
  `detectCurrentMode()` reads from). If the mode cannot be detected, fall back
  to `Mode`.
- Tooltip: "Current: Plan. Click to switch to Accept Edits (Shift+Tab)."

### D5. Attention rollup is badge-only

- Add an attention count badge to the **Agents** nav tab.
- Count sessions where `waiting_for_input`, `stuck`, or `not_started` is true.
- Do **not** sort, promote, or otherwise reorder attention-needed sessions.
  Preserve the existing session and group order exactly; the badge is the
  rollup, while the existing row indicators identify the affected sessions.


### D6. Density

- Avatar 40px → 28px. Initials font 13px → 11px.
- `.session-group-item` padding 14px → 9px vertical.
- Goal line 12px, muted, single line.
- Net row height ~68px → ~48px. No compact toggle; the goal line (D1) carries
  enough information that the larger avatar no longer earns its space.

### D7. Naming

- Live-session panel tab `Chat` → **Transcript**. Board chat unchanged.
- No change to the top nav in this spec. Whether **Tokens** and **Docs** stay
  top-level is a separate decision.

### D8. Transcript-derived goals (replaces PULSE injection)

Moved to its own spec: **[Transcript Goals](TRANSCRIPT_GOALS/)**, owned by a
separate team. Summary of the contract this spec depends on:

- The sparkle no longer types `Emit a ||PULSE:SUMMARY …||` into the agent's
  terminal; it calls `POST /api/sessions/live/{name}/goal`.
- A background service derives `{name, goal}` from the transcript via
  `claude -p` or `codex exec` and stores `session_meta.auto_name` (write-once)
  and `auto_goal` (refreshed).
- Both payload builders expose `auto_name`, `goal_pending`, and
  `summary = pulse_summary || auto_goal`.
- Frontend identity chain becomes
  `display_name → auto_name → board_job_title → "Agent"` (D1); the prompt
  remains the goal-line fallback only.

## Implementation Plan

Phases are independent; 1–3 are the highest leverage and are all inside
`_renderSessionItem` plus one CSS block.

### Phase 1 — Bug fix + identity (D1, D2)

Files:
- `internal/server/frontend/static/render.js` — `_renderSessionItem`,
  `_renderAvatar`, `_getInitials`
- `internal/server/frontend/static/sessions.js` — placeholder text in
  `selectLiveSession`
- `internal/server/frontend/static/css/session.css` — hide mobile rows
- `internal/server/frontend/static/css/mobile.css` — verify mobile still
  overrides to `display:block` (it scopes under `#mobile-session-list`;
  confirm specificity beats the new desktop rule)
- `internal/server/routes/sessions.go` (`List`) **and**
  `internal/server/routes/websocket.go` — add `first_prompt` to both payload
  builders; `internal/jsonl/reader.go` — cache the first prompt per session
  rather than holding every listed session's full transcript in memory
- `internal/server/frontend/static/websocket.js` — `coral_diff` merge must
  preserve `first_prompt` (spread-merge, see D1)

Verify: every row shows a second line; no raw "Nothing yet" text on desktop;
mobile list unchanged.

### Phase 2 — Context bar (D3)

Files:
- `render.js` — `_renderTokenLine` (label text, `ctx full` at 100%)
- `css/session.css` — `.context-bar-label` threshold colours

No `controls.js` changes: there is no sidebar-specific compact plumbing.

Verify: rows at ≥80% and 100% show `ctx N%` / `ctx full` in the error colour
with no clickable control inside the context bar; typing `/compact` in chat
still sends to the active session as before.

### Phase 3 — Mode label (D4)

Files:
- `controls.js` — `renderQuickActions`, `cycleModeToggle`, and a small
  `refreshModeLabel()` called from the terminal update path

Verify: label tracks Shift+Tab pressed directly in the terminal, not only
clicks on the button.

### Phase 4 — Attention rollup + density (D5, D6)

Files:
- `render.js` — `renderLiveSessions` (badge count only; preserve order),
  `_renderSessionItem`
- `templates/index.html` — badge span inside `#nav-tab-agents`
- `app.js` or `websocket.js` — update the badge on each live-sessions tick
- `css/session.css` — avatar and padding sizes
- `css/layout.css` — nav tab badge

Verify: the badge counts `waiting_for_input | stuck | not_started`, and the
rendered session order is identical to the payload order.

### Phase 5 — Naming (D7)

Files:
- `templates/includes/views/live_session.html` — tab label and `title`

### Phase 6 — Transcript-derived goals (D8)

See [Transcript Goals](TRANSCRIPT_GOALS/) → Implementation Plan (Phases A/B).
The only touchpoints inside this spec's files are `render.js` (identity
chain), `controls.js` (sparkle → endpoint) and `websocket.js` (spread-merge),
all listed there.

## Open Questions

- ~~Should the summary fallback prefer the *latest* user prompt over the
  first?~~ Resolved by D8: `first_prompt` stays as the stable *goal-line*
  fallback (not an identity source, per task #85); the *current* goal comes
  from a periodic Haiku call over the tail of the transcript, with no prompt
  injected into the agent.
