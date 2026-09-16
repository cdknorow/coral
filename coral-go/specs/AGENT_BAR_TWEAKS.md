# Agent Bar Tweaks

**Status:** Planned

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
single most useful number in the sidebar, and it is both unlabelled and
passive: it warns, but offers no action.

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
  one line. (Available from the JSONL reader; if not already in the session
  payload, add `first_prompt` to the live-sessions response.)
- When neither exists and the agent is not a terminal, show the goal button
  inline and always visible (not hover-only) with a muted "No goal yet" label.
- Avatar initials: when `display_name` is empty, derive initials from the
  summary's first two words rather than the folder name. Keep the folder-based
  colour so agents in the same folder still share a hue.
- Right-pane header and `Sending to:` placeholder use the same
  `display_name → summary → "Agent"` resolution.

Out of scope for this spec: automatic naming via LLM. The summary fallback
gets most of the value with none of the latency.

### D2. One signal per state on desktop

- Add to `css/session.css`:
  ```css
  .session-mobile-banner-row,
  .session-mobile-meta { display: none; }
  ```
  Mobile already re-enables these under `#mobile-session-list`.
- Keep the `CHECK TERMINAL` / `NEEDS INPUT` pill as the desktop indicator.
  The avatar dot remains as a secondary, glanceable cue.

### D3. Context bar: label it, make it actionable

- Label: `ctx 69%` instead of bare `69%`. Tooltip unchanged.
- Thresholds unchanged (≥50 warning, ≥80 error).
- At ≥80%, the label becomes a button: **Compact** (or the label itself is
  clickable with a `title="Send /compact"`). Clicking sends `/compact` to that
  session via the existing `sendQuickCommand` path, regardless of whether the
  session is the active one. Show a toast on send.
- At 100%, colour is unchanged but the label reads `ctx full`.

### D4. Mode button shows current mode

- Button label becomes the current mode: `Default`, `Plan`, `Accept`.
- Keep the same click behaviour (advance one step).
- Refresh the label whenever the terminal buffer is re-scanned (same hook that
  `detectCurrentMode()` reads from). If the mode cannot be detected, fall back
  to `Mode`.
- Tooltip: "Current: Plan. Click to switch to Accept Edits (Shift+Tab)."


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
- If `first_prompt` is not in the payload: `internal/server/...` live-sessions
  handler and the session struct

Verify: every row shows a second line; no raw "Nothing yet" text on desktop;
mobile list unchanged.

### Phase 2 — Context bar (D3)

Files:
- `render.js` — `_renderTokenLine`
- `controls.js` — expose a `compactSession(name, agentType, sessionId)` that
  targets a specific session, not just the active one
- `css/session.css` — `.context-bar-label` button styling at ≥80%

Verify: click Compact on a non-active row, confirm `/compact` lands in that
session's terminal.

### Phase 3 — Mode label (D4)

Files:
- `controls.js` — `renderQuickActions`, `cycleModeToggle`, and a small
  `refreshModeLabel()` called from the terminal update path

Verify: label tracks Shift+Tab pressed directly in the terminal, not only
clicks on the button.

### Phase 4 — Attention rollup + density (D5, D6)

Files:
- `render.js` — `renderLiveSessions` (sort), `_renderSessionItem`
- `templates/index.html` — badge span inside `#nav-tab-agents`
- `app.js` or `websocket.js` — update the badge on each live-sessions tick
- `css/session.css` — avatar and padding sizes
- `css/layout.css` — nav tab badge

### Phase 5 — Naming (D7)

Files:
- `templates/includes/views/live_session.html` — tab label and `title`

## Open Questions

- Should the Compact action be gated behind a confirm on the first use? It
  is non-destructive but does interrupt a working agent.
- Should the summary fallback prefer the *latest* user prompt over the first?
  First is stable; latest is more current.
   Ideally summary fallback can be a call to to get a short summary from the agent, similar to /btw what are you doing? in claude. We could send a short transcript to a haiku level model asking for a summary based on the last X works periodically. 
