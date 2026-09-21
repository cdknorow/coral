# Agent List Compact Overview

**Status:** Consensus (rev 4, 2026-09-18) — implemented under task #150, pending QA
**Scope:** the Agents tab sidebar (`#live-sessions-list`) and its mobile clone
(`#mobile-session-list`). Builds on `AGENT_BAR_TWEAKS.md` (D1–D7, shipped) and
must not change the popout (`SINGLE_AGENT_POPOUT.md`) or any routing.

## Goal

Make the Agents list scan like a list of *agents*, not a dashboard of
metrics: two short lines per agent, one small header per team/folder, and
every number moved to where the operator looks when they have already picked
an agent. The reference is Codex-style scanability (dense, text-first rows);
no branding, icons or colours are copied.

## Row contract (desktop)

```
┌ ● Frontend Dev                              ⋮ ┐   line 1: 13px/16px
│   Working on the compact list spec…           │   line 2: 12px/14px, muted
└───────────────────────────────────────────────┘   row: 40px (36px without line 2)
```

- **Line 1**: status dot (8px, `getDotClass` states) + identity from
  `resolveSessionIdentity` (D1 chain) + right-aligned attention pill (only when
  `sessionNeedsAttention`) + kebab (unchanged menu, "Open Agent Tab" first).
- **Line 2**: `sessionGoalText` (summary → first_prompt), single line, ellipsis.
  Empty goal keeps today's sparkle + passive "No goal yet" (D1). Line 2 renders
  on every live row **including sleeping rows** (D-E: the goal is what tells a
  sleeping agent from its siblings); only terminal and ended rows drop it
  (36px row).
- **Near-full context signal (D-C, ≥80% only)**: a small `ctx 87%` / `ctx full`
  pill on line 1 (same thresholds, colours and explicit-null suppression as
  AGENT_BAR_TWEAKS D3; hidden when `context_pct` is null or <80) plus the dot
  tinted `--warning`/`--error`. Below 80% nothing is shown in the row. Pill
  priority when space is tight (tablet 280px overlay, M2): attention pill wins,
  the ctx pill collapses to the dot tint alone; identity never truncates below
  ~12 characters.
- **Removed from rows** (default list): avatar (`_renderAvatar`), context bar
  (`_renderTokenLine`), branch/dir chip (`.agent-dir-chip`), token totals,
  elapsed time (`formatStaleness` / `.session-activity-text`), inline status
  text (`.session-inline-status`). None of this data is dropped from the
  payload; see "Where the metadata goes".
- **Dimensions** (as implemented): `.session-group-item` is `box-sizing:
  border-box`, padding `5px 10px 5px 14px`, `min-height: 36px`; line 1
  (`.session-name-row`) is 16px tall; line 2 (`.session-goal`) is 12px text on a
  14px line with no top margin: 5 + 16 + 14 + 5 = **40px** with a goal line;
  rows without one (terminal/ended) are held at the 36px minimum. The kebab
  button is 16px tall (padding 0 5px, font 14px) so line 1 never grows; its
  24×24 hit target comes from an `::after` overlay (`inset: -4px -2px`). Team
  rows inside `.board-card-agents` use the same vertical padding with 12px left.
- **Typography**: identity 13px/600 `--text-primary`; goal 12px/400
  `--text-muted`; attention pill 10px/700 uppercase (reuse `.badge.waiting-badge`
  colours, no pulse animation in the list).
- **Truncation**: identity `max-width: calc(100% - 64px)` (dot 8 + gaps + pill +
  kebab), ellipsis; goal full-width ellipsis; both carry `title=` with the full
  text (title text never includes ids).

## Group headers

- **Team card** (`.session-board-card`): header becomes `name` + small count
  (`.session-group-count`, e.g. `4`) + sleeping moon when the whole team
  sleeps + chevron + group kebab. Remove from the header: `.board-card-dir`,
  `.board-card-branch-line`, `.board-card-subline` token/time roll-up
  (`.team-token-usage`). Header height 28px (was ~56px with three sub-lines).
- **Folder header** (`.session-group-header`): name + count; drop
  `groupDirLine` / `groupBranchLine`. Height 24px.
- **Directory sub-groups** (`.agent-subgroup-header`): unchanged (they only
  appear for ≥3 distinct dirs and carry the short path, which *is* the
  grouping key).
- No reordering of teams, folders or agents (AGENT_BAR_TWEAKS D5 no-sort rule).
- **D-G (operator decision)**: the team header roll-up line
  (`5 agents · 11.3M tokens · 22m`, recorded as "good, keep" in
  `AGENT_BAR_TWEAKS_UPDATE.md` Finding 4) is removed from the header and moves
  to the Team details disclosure, which must be reachable without hover on
  every form factor (team kebab item "Team details"). If the operator prefers
  to keep the roll-up in the header, the header is 40px instead of 28px and
  everything else in this spec is unchanged.

## Where the metadata goes (nothing becomes undiscoverable)

| Removed from list | Still shown | Affordance |
|---|---|---|
| ctx % / bar (<80%) | selected-agent workspace header `#session-token-usage` + row tooltip; ≥80% also as the line-1 pill above | tooltip row "Context: 29% of 1M"; `context_pct` null → "Context: unknown" (never 0% / full) |
| tokens in/out/cache/cost | tooltip (exists) + Analytics tab | unchanged |
| branch / repo path | workspace `#session-branch` chip shown in the live terminal header **in dashboard mode only** (`body:not(.popout-mode)`, so the popout slim-header contract is untouched; the chip shows the branch name only, never a path) + tooltip Branch row (exists) + team **details** disclosure | see Team details |
| elapsed / last activity | tooltip "Last action" row (exists) + mobile meta pill (kept on phone) | unchanged |
| team dir / branch / token roll-up | **Team details** popover from the team kebab ("Team details") and on header hover (desktop) | new small popover reusing `.session-tooltip` styling |
| avatar emoji/initials | workspace header (identity + type badge) | unchanged |

**Selected-agent header (task #156):** the terminal header identity line shows
the resolved identity only. The session UUID and its ` -- ` separator are gone
from the visible text and are not moved into a title, aria-label or tooltip;
the id remains in application state, API/WS routing, URLs and Session Info.
The dashboard-only branch chip shows the **branch name only**, in its visible
text and in its tooltip (so an ellipsized chip still reveals the full branch);
never the repo slug, a filesystem path or an id. Repo metadata stays in the
existing details and row-tooltip surfaces. The chip takes the freed flex space (no fixed max-width), so a branch such as
`feature/agent-list-compact` renders in full at common desktop widths. When
the header is tight (e.g. 1024px) the identity has priority: the chip yields
first (shrink factor 1000, 48px floor, ellipsis) and the identity keeps at
least 120px while it has more to show; the actions on the right never shrink. The popout header is unchanged (it never
showed the id and still hides the chip).

Hover tooltips remain desktop-only; the Team details disclosure is reachable
through the kebab item on desktop, tablet and phone, so nothing depends on
hover.

## States

| State | Row treatment |
|---|---|
| default | as above |
| hover | `background: var(--bg-hover)`; kebab and drag grip fade in (existing) |
| focus | rows are focusable list items (`tabindex="0"`, no button role, see Accessibility D-F); `:focus-visible` 2px `--accent` outline inset (drawn inside the 2px selected border, not clipped); Enter/Space selects |
| selected (`.active`) | 2px `--accent` left border + `rgba(255,255,255,.04)` background (existing colours); identity stays 600 |
| needs input (`waiting_for_input`) | filled amber dot; pill "Needs input" (quiet amber surface, sentence case); amber row tint; name full-strength |
| check terminal (`not_started`) | filled amber dot; pill "Check terminal"; amber row tint |
| stuck (`stuck`) | filled red dot; pill "Stuck" (error surface); row tint switches to the error tint (`.is-stuck`); outranks needs input |
| your turn (`awaiting_user`; servers without the field: the stop-derived `done`) | hollow NEUTRAL ring (`--text-primary`, no hue); quieter neutral pill "Your turn"; no row tint; never green, never a check mark |
| sleeping | muted moon glyph in the dot slot (no amber); identity `--text-secondary`; goal line kept (40px, D-E); kebab shows Wake variant (existing) |
| ended (killed/history rows only, `.session-done`) | muted check glyph, never green; identity line-through `--text-muted`; goal hidden (36px); click opens history (existing) |
| working | filled GREEN dot (`--success`); no pill; nothing animates |
| idle | empty dot slot (`visibility: hidden`, slot kept); no pill |

### State resolver and overlays (task #167)

One resolver (`render.js` `deriveSessionState` / `SESSION_STATES`) picks a single
winner with the priority **Ended > Sleeping > Stuck > Needs input > Check
terminal > Your turn > Working > Idle**, and every surface uses its words: row
pill, row `aria-label`, tooltip State row, phone status chip, workspace header
dot and the popout pill. Selection, context and unread are overlays, never
winners:

- **Selection** keeps the accent edge and blue surface and never hides the glyph or pill.
- **Context** (>=80%): `ctx N%` pill only when no state pill owns line 1; the red
  ring on the dot slot survives on every glyph (also on the otherwise hidden idle slot).
- **Unread** board messages: neutral count chip at the end of line 2
  (`.session-unread-chip`), also on selected rows; never an attention colour. The
  phone keeps its meta pill instead.
- **aria-label**: `<identity>, <state>` plus `, context 87%` and `, 3 unread` when
  those overlays are shown; glyphs are `aria-hidden`.

**Aggregation** (`sessionCountsTowardAttention`): the Agents nav badge and each
team/folder header's `.group-attention-count` (red `.stuck` variant when any
member is Stuck; shown on collapsed groups too) always count Needs input, Check
terminal and Stuck. **Your turn** and **unread board messages**
(`board_unread > 0`, coerced to a finite non-negative integer) share one
operator-facing rule: they count only when nobody else will act on them, i.e.
no `board_project`, or `board_is_orchestrator === true` (explicit backend flag,
never a name match). An ordinary team member's unread stays visible as the
neutral row chip / phone metadata but never counts. Ended and sleeping sessions
never count. Ended rows render no line 2 at all (36px), even when they had a
goal or unread messages. Needs-input toasts fire on a later false -> true
transition only: the first snapshot after load seeds silently, and the agent
the operator is typing into never toasts itself.
Operator switch: `localStorage['coral-count-your-turn'] = 'false'` disables
Your-turn counting.

## Responsive

- **≥1024px**: as specified. Sidebar width unchanged (320px, resizable).
- **768–1023px (tablet overlay)**: same rows; the overlay sidebar is 280px so
  identity `max-width` shrinks naturally.
- **≤767px (phone, `#mobile-session-list` clone)**: rows stay cards
  (existing 12px radius) but drop the avatar too; line 1 = dot + identity + pill,
  line 2 = goal (2-line clamp allowed on phone, existing rule); the mobile meta
  row keeps **only** the status chip and unread pill (drop the elapsed text);
  banner row unchanged (D2). Min row height 56px for touch; kebab 44×44.
- Reduced motion: no new animations; the existing waiting pulse is not used in
  rows.

## Accessibility

- **Row pattern (D-F)**: the row stays an `<li>` list item and is made
  focusable with `tabindex="0"`; it does **not** get `role="button"` because it
  contains other controls (kebab button, "Open Agent Tab" anchor, sparkle),
  which would be nested interactive content inside a button. Selection is
  driven by a **delegated** keydown/click listener on both list containers
  (`#live-sessions-list` and the cloned `#mobile-session-list`, because
  `cloneNode` drops `addEventListener` handlers): Enter/Space on the row selects
  (Space `preventDefault` so it neither scrolls nor starts a drag); events whose
  target is inside `.sidebar-kebab-wrapper`, `.overflow-menu-open-window` or
  `.sidebar-goal-btn` are ignored by the row handler. Kebab, anchor and sparkle
  remain separate tab stops. Escape closes an open kebab.
- Row `aria-label="<identity>, <state>"` with the popout vocabulary
  (Working / Idle / Needs input / Check terminal / Stuck / Sleeping / Ended);
  `aria-current="true"` on the selected row. Labels are rewritten on every
  render (rename, sleeping, attention, ended ticks) and, because a tick
  re-renders the list, focus is restored to the row with the same
  `data-session-id` after render so keyboard users are not dropped.
- Status is never colour-only: attention has pill text; sleeping/ended have
  text treatment; working/idle are in the aria-label.
- Drag-to-reorder (`draggable`, `moveSessionUp/Down`) is unchanged and starts
  only from pointer drag; keyboard selection never initiates it.
- Group headers: the chevron control carries `aria-expanded`.
- **Team details**: it is interactive (focusable contents, Escape/outside
  close), so it is an anchored **disclosure/popover with dialog semantics**
  (`role="dialog"`, `aria-labelledby` the team name, focus moved into it on
  open and returned to the invoking kebab/header on close), not a tooltip.
  Contents: a definition list (Directory, Branch, Agents, Tokens, Last
  activity); no session ids.
- Reduced motion: no new animation; the waiting pulse is not used in rows.

## DOM / CSS / file impact (implementation plan, not done)

- `static/render.js`
  - `_renderSessionItem`: drop `${avatar}`, the full `_renderTokenLine(s)` bar,
    `dirChip`, `.session-inline-status`; add the ≥80% ctx pill (new
    `_renderCtxPill(s)` reusing D3 thresholds + null suppression); keep
    `.session-mobile-banner-row` and a reduced `.session-mobile-meta` (chip +
    unread only); add `tabindex="0"`, `aria-label`, `aria-current`; status dot
    becomes `<span class="session-dot ${dotClass}">` on line 1 (class exists for
    mobile at `mobile.css:807`).
  - `renderLiveSessions`: install one delegated keydown/click handler per list
    container (idempotent); restore focus by `data-session-id` after render.
  - Team header (≈1706–1735): remove `teamDirLine`, `branchLine`,
    `teamSubline`; add `.session-group-count`; add kebab item "Team details".
  - Folder header (≈1820, 1889): remove `groupDirLine`/`groupBranchLine`.
  - `buildSessionTooltip`: add Context row; keep Tokens/Branch/Last action.
  - New `showTeamDetails(boardName)`: anchored disclosure with dialog
    semantics (focus in / Escape / outside close / focus return), fed by the
    same data the removed header lines used.
  - `_renderTokenLine` / `_renderAvatar` remain for the workspace header and
    tooltips (no deletion).
- `static/css/session.css`: new row metrics (`.session-group-item` padding
  5/10/5/14, `box-sizing: border-box`, `min-height: 36px`; `.session-name-row`
  16px; `.session-goal` 12px/14px, margin 0; `.session-dot` 8px; kebab 16px +
  24px `::after` hit area), `.session-state-pill.session-attention-pill`,
  `.session-ctx-pill`, `.session-group-count`, `.team-details-popover`,
  `.terminal-branch-chip`; header markup no longer emits
  `.board-card-dir/.board-card-branch-line/.board-card-subline` (rules kept).
- `static/css/mobile.css`: hide `.agent-avatar` in `#mobile-session-list`,
  hide `.session-activity-text`, min-height 56px, kebab 44px.
- `templates/includes/views/live_session.html`: show `#session-branch` (branch
  name only) in the terminal header left group, gated to dashboard mode by CSS
  (`body.popout-mode #session-branch { display:none }`), so
  `SINGLE_AGENT_POPOUT.md` needs no change.
- `AGENT_BAR_TWEAKS.md`: D1 avatar bullets and D6 numbers are marked
  **superseded by this spec** when it ships (C1); D3 stays authoritative for
  thresholds/colours/null handling, now rendered as the ≥80% pill.
- Tests: `tests/frontend/agent_bar_tweaks.test.js` — replace the superseded
  assertions in the same commit (C1): `avatar is 28px`, `initials font is 11px`,
  `initials …` (PF/RW/CO/ZE), `same-folder agents share an avatar hue`,
  `different folder … hue`, `row vertical padding is 9px`, `row with goal + ctx
  bar … <= 70px`, `row … <= 54px`, `ctx label is "ctx N%"` (<80 case) with the
  new row/pill/header/keyboard assertions; keep identity, no-sort, badge, D2
  mobile, mode-label, merge and WS-stub coverage untouched. The agent_popout
  suite must stay 108+/108+ (the popout renders no list and its header is
  gated).

## Acceptance checklist (QA)

1. Desktop row with goal = 40px ±1, without goal = 36px ±1; no `.agent-avatar`,
   `.session-context-bar`, `.agent-dir-chip`, `.session-inline-status`,
   `.session-activity-text` inside `#live-sessions-list` rows.
2. Line 1 shows dot + D1 identity + kebab; attention rows show exactly one pill
   with text; identity/goal have `title` with full text and no uuid.
3. Team header = name + count (+ sleeping moon); no dir/branch/token text; 28px.
   Folder header = name + count; 24px. Order of teams/folders/agents unchanged
   across ticks (no-sort).
4. Tooltip on a row shows Context %, Tokens, Branch, Last action; "Team
   details" kebab item opens a popover with dir/branch/agents/tokens/last
   activity and closes on Escape.
5. Selected agent's workspace header shows branch chip + token usage (metadata
   remains one click away).
6. States: hover/focus-visible/selected/attention/sleeping/ended render per the
   table; sleeping rows keep their goal line (40px with goal, 36px only when the
   agent has no goal text); terminal and ended rows are 36px; Agents-nav badge
   count unchanged.
7. Keyboard: Tab reaches rows, Enter/Space selects, Escape closes kebab;
   `aria-label` announces identity + state.
8. Phone 390px: cards without avatar, 2-line goal clamp, status chip + unread
   pill only, kebab 44×44, banner row visible for attention rows.
9. Popout (`/agent/<uuid>`) unaffected: no list rendered, 108+ harness green.
10. Existing suites green: acf, terminal_scroll, agent_bar (updated per C1), popout.
11. No-sort across ticks that flip attention/sleeping/ended (payload order preserved; existing order assertion).
12. Tooltip Context row: unknown model → "Context: unknown"; known → percent of window; ≥80% rows also show the line-1 ctx pill, <80% rows show none.
13. `aria-label` tracks rename and state ticks in place; the focused row keeps focus (restored by session id) after a re-render.
14. Cloned phone row: Enter selects, kebab opens, "Open Agent Tab" is the first kebab item (#133); Enter/Space on the kebab, anchor or sparkle do not select the row.
15. Popout suite green after the dashboard-only branch chip (chip absent in popout; leak scan clean).
16. Drag reorder still works on desktop rows; Space/Enter on a row do not trigger drag or page scroll.
17. Reduced motion: no new animation; waiting pulse absent in rows.
18. Team details: opens from the kebab on desktop/tablet/phone, has dialog semantics, traps nothing beyond itself, closes on Escape/outside click and returns focus to the invoker; contains no session ids.
19. Sleeping rows keep their goal line (40px); terminal and ended rows are 36px.

## Decisions (rev 2)

Resolved by Lead Developer + QA review (2026-09-17):

- **D-A** No avatar anywhere in the list; identity colour/emoji survive only in
  the workspace header. (agreed)
- **D-B** Explicit state pills "Needs input" / "Check terminal" / "Stuck"
  (same vocabulary as the mobile chip and tooltip). (agreed)
- **D-D** Team details is an anchored keyboard/touch-reachable disclosure with
  dialog semantics, not a workspace tab and not a tooltip. (agreed)
- **D-E** Goal line stays on sleeping rows; only terminal/ended rows are 36px.
  (QA proposal, adopted)
- **D-F** Row a11y pattern: focusable list item + delegated handlers, kebab /
  anchor / sparkle as separate tab stops; no `role="button"` on the row.
  (Lead Developer correction, adopted)

Open for the operator:

- **D-C** In-row context signal at ≥80%. Lead Developer: none in rows (tooltip,
  workspace header and nav badge suffice). QA: keep a minimal ≥80% signal since
  near-full context is attention-class and the tooltip is unreachable on touch
  until an agent is selected. **This spec adopts the ≥80% pill + dot tint**
  (it keeps D3's thresholds and null handling and adds no number below 80%);
  flip to "none" by deleting `_renderCtxPill` if the operator prefers.
- **D-G** Remove the team header roll-up (moves to Team details) vs keep it in
  a 40px header. This spec proposes removal.
