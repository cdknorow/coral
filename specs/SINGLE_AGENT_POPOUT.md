# Single-Agent Popout

**Status:** In Progress
**Branch:** `feature/single-agent-popout`
**Owners:** backend (Lead Developer, #128), frontend (Frontend Dev, #129), spec + harness (QA, #130)

## Overview

A dedicated, refresh-safe browser entrypoint that renders exactly one agent's
existing workspace (terminal/transcript pane, right tools pane, quick-action
strip, command input) with no global chrome. Opened from the Agents sidebar
kebab or the workspace header as a plain new-window anchor.

## Contract (reconciled from tasks #124, #125, #126)

- **URL** `GET /agent/{sessionID}`. The path carries only the immutable
  session UUID. The shell renders for any canonical UUID (200); malformed
  ids are rejected at the route (400). Routing identity comes from
  `GET /api/sessions/{sessionID}/resolve`, which returns the exact record
  (`session_id, state: active|sleeping|finished|not_found, agent_type, name,
  tmux_session` (null unless active), `display_name, auto_name,
  board_job_title, board_project, icon, working_directory, status,
  waiting_for_input, stuck, not_started`); unknown ids return 200 with
  `state=not_found`. `/api/sessions/live` may enrich display state but never
  establishes routing or fallback. Any `agent_type` supplied
  in query/path is a consistency assertion (mismatch -> 400), never a routing
  key. Malformed UUID -> 400 at the route.
- **Exact targeting.** Every attach/send/keys/resize/capture path used by the
  popout resolves the exact `(agent_type, session_id)` record and rejects
  name-only or mismatched requests. The existing fuzzy name fallback
  (`tmux.Client.FindPane` when `session_id` is empty) is never reachable
  from the popout. `/ws/terminal/{name}` verifies the route name belongs to
  the requested session before attaching.
- **Shell.** Reuse `live_session.html` DOM (never clone ids). Hidden: top nav,
  Agents sidebar and resizer, tablet toggle, mobile tab bar and agent list,
  welcome screen. Kept: terminal/capture pane, `#agentic-state` tools pane,
  pane split and command-pane resize, fullscreen/panel toggle, quick-action
  strip, command input, waiting banner, overlays. Slim header = identity
  (`display_name -> auto_name -> board_job_title -> Agent/Terminal`), type
  badge, state pill, "Open in Coral" anchor, panel toggle. The session id may
  appear only in the URL, the boot attribute `body[data-target-session-id]`
  and action hrefs; never in visible text, tooltips/titles, `document.title`
  or user copy. tmux name and folder never appear in the header.
- **Lifecycle.** Exact live match -> render/attach. Sleeping -> overlay +
  Wake (explicit, non-destructive), no attach until awake. History-only ->
  Ended, transcript link to the main app, input disabled. Removal /
  `terminal_closed` -> Ended within one tick, input and resize disabled, no
  reconnect loop. Valid unknown id -> Not found (Retry / Open Coral), zero
  send/resize/attach calls. WS/server loss -> Reconnecting, same id only.
  Restart (new UUID) is **never auto-followed**: the popout stays Ended and
  offers an explicit "open restarted agent" link; the main app's same-name
  adoption (`sameNameCount === 1`) is disabled in popout mode.
- **Excluded in v1.** Kill, restart, rename, icon, launch, team actions,
  workflows/jobs, board chat (unless an identity endpoint ships), drag
  reorder, notifications for other sessions, cross-window focus coordination.
- **Opening.** Synchronous anchors: `<a href="/agent/<uuid>" target="_blank"
  rel="noopener noreferrer">` in the awake and sleeping kebab menus and in
  the workspace header. Works with popup blockers; the opened window has no
  `window.opener` and boots from the URL alone. Native app: in-app window
  when the bridge supports internal routes, else same-origin system browser.
- **Multi-window.** Same browser profile: a `BroadcastChannel` per session id
  elects one interactive owner (focus/first-input claim, release on
  `pagehide`); only the owner sends keyboard input, terminal resize and
  capture width sync; viewers stream output and show a "click to take
  control" chip. When the owner closes, ownership is released but not
  auto-reclaimed: the remaining window claims explicitly on click/keystroke.

### Documented v1 limitation: cross-browser ownership

`BroadcastChannel` only spans one browser profile. Two different browsers,
or the native app plus a browser, viewing the same session both stream
output and both may send input and resize. v1 does not add a server lease
(disconnect/focus/timeout semantics would materially increase failure
modes). Mitigations that ship in v1: the server rejects any request whose
name does not belong to the session id, and resize is last-writer-wins with
no echo. Acceptance for the cross-browser case asserts **no crash and no
cross-session targeting**, not a single interactive owner.

## Acceptance checklist (A = QA #126, U = Frontend #124; numbering is the test oracle)

## Server / route
 1. GET /agent/<valid uuid> renders the bundle in agent entry mode; registered before GET /; auth/license/CORS/origin middleware unchanged. [A1,#125]
 2. GET /agent/not-a-uuid (and path traversal / empty) -> 400 at the route; no template render. [A3,#125]
 3. agent_type in query/path that mismatches the record -> 400; never used for targeting. [A5 amended]
 4. Terminal attach (/ws/terminal), send, keys, resize verify the route name belongs to the requested session_id; name-only or wrong-id requests are rejected; never reach the fuzzy name fallback. Go test: two sessions in one folder, request A's id -> A's pane; no id -> rejected. [A5,A6,#125 hardening]
 5. Server-side last-resize-wins guard with no echo (documented cross-browser behaviour). [A9 variant 2]
 6. Refresh on /agent/<id> re-renders from server data only; window.opener not required; boot data is escaped. [A2,#125]
 7. Remote host without cookie -> /auth redirect that returns to /agent/<id> after login; localhost renders directly; no supporter-reminder diversion. [A7,#125]

## Shell
 8. Rendered popout has no .top-bar, .sidebar, #sidebar-resize-handle, .mobile-tab-bar, #mobile-agent-list, #welcome-screen; .layout fills 100vh; no hidden chrome reserves space at any width. [A1,U4]
 9. Slim header shows identity (display_name -> auto_name -> board_job_title -> Agent/Terminal), type badge, state pill; no session id / tmux name / folder in any DOM text or attribute except location. [U3 amended]
10. document.title = "<glyph> <identity> · Coral" tracks working / needs input / sleeping / ended. [U3]
11. Header actions: Open in Coral (noopener anchor to the main app, plain link/new tab) and the existing panel toggle (aria-pressed, accessible name). [U2,U5]
12. Pane split, command-pane height and panel collapse work and persist under popout-scoped keys; main-app keys unchanged afterwards. [A16,U5]
13. Quick-action strip: Mode label/aria tracks the buffer; Esc/arrows/Enter/macros send raw keys to the exact session. [A15]
14. Terminal-only session renders with 'Mode' fallback and no goal affordance. [U12]

## Opening
15. Kebab 'Open Agent Tab' is the FIRST actionable item of the awake and sleeping row menus (label, title and aria-label exactly 'Open Agent Tab'; the workspace-header opener uses the same accessible text), absent for done rows; href exactly /agent/<uuid> (no query), target _blank, rel noopener noreferrer; workspace header carries the same anchor. [U1,U2]
16. Works with popups blocked (anchor navigation); opened window has window.opener === null. [A17,U2]
17. Native app: in-app window when the bridge supports internal routes, else same-origin system-browser fallback (operator default). [#124 decision a]

## Identity / lifecycle
18. Boot selects the exact session_id after the live list loads; brief poll for index lag; never selects by name. [A2,#124]
19. Valid unknown id -> not-found card with Retry / Open Coral; zero send/resize/attach calls; no terminal socket. [A3]
20. History-only id -> Ended state, transcript link to main app, input disabled. [A4]
21. Kill from main app -> terminal_closed -> Ended within one tick; input/resize disabled; no reconnect loop. [A8]
22. Restart in main app -> popout stays Ended with an explicit 'open restarted agent' link; never streams the new pane until clicked; name-adoption (sameNameCount) disabled in popout mode. [A13,U7]
23. Sleeping -> overlay + Wake (explicit gesture); no terminal attach until awake; waking clears overlay without reload. [A11]
24. WS/server loss -> lost/reconnecting pill, disconnected badge, single reconnect per generation, scrollback replayed once, same id only. [A10]
25. Rename/auto_name/summary change in main window -> header, placeholder, document.title update on next tick. [A12,U6]
26. Command targeting: body asserts (agent_type, session_id) of the exact record; placeholder 'Sending to: <identity>'. [A14]
27. Needs-input toast only for the popout's own session; other sessions' toasts suppressed. [A18]

## Multi-window
28. Same browser profile: main window + popout both stream; second is viewer with the chip; first click/keystroke claims ownership; only the owner's resize/input/syncPaneWidth reach the server; ownership released on pagehide. [U8 v1]
29. Same browser resize: 1440px main + 900px popout -> pane width stable for 5s, resize request count <= 2 after settle. [A9 v1]
30. Cross-browser / native+browser: documented contention; assert no crash, no cross-session targeting, server guard (5) applied. [A9 v2,U8 v2]

## Mobile / accessibility
31. 390px: workspace renders directly (no overlay promotion, no tab bar), header sticky 36px, quick-action strip visible and horizontally scrollable with 44px targets, panel toggle opens .mobile-panel-overlay, textarea 16px. [A19,U9]
32. Header identity/pill region aria-live=polite announces state changes; #waiting-banner stays assertive; focus lands in #command-input on load; Esc/modal handling intact. [U10,A19]

## Main-app regression (same implementation task)
33. Existing suites green: acf, terminal_scroll, agent_bar (sidebar/nav ordering, badge, D1 identity untouched). [A20]
34. NEW: main app '#chat/<sid>' refresh restores the live session (router chat restore currently a no-op). [U11,A20]
35. NEW: main app renders without errors when #nav-tab-agents-badge / #terminal-header-label are absent (null-guarded writers). [A20]

## Test isolation / safety
36. All browser cases via tests/frontend/run.sh with a fresh CORAL_TEST_PORT and the :8420 guard; fixture cases use the agent_bar pattern (pre-navigation fetch + WS stub); real-session cases use the terminal_scroll pattern (launch terminal in isolated home, kill, poll until absent). [A20]
37. Refresh is side-effect free apart from terminal attach (no wake/resize on load without a gesture); no kill/restart/rename endpoints reachable from the popout UI. [destructive-state rule]

## Selector / hook contract (final, from #129)

`body[data-entry-mode="agent"][data-target-session-id]`, `body.popout-mode`;
`#popout-header-identity` (aria-live=polite), `#terminal-type-badge`,
`#terminal-state-pill[data-state=loading|working|idle|waiting|check|stuck|your-turn|sleeping|ended|reconnecting|not-found]`
(pill text: Working / Idle / Needs input / Check terminal / Stuck / Your turn / Sleeping / Ended, the same
vocabulary and resolver as the dashboard rows; terminal actions stay enabled in working, idle, waiting, check, stuck and your-turn)
(`idle` = attached, not working, not waiting; neutral pill text "Idle" and a neutral title glyph),
`#popout-open-main-btn` (anchor to `/#chat/<uuid>`, `target=_blank`,
`rel=noopener noreferrer`), `#popout-panel-toggle-btn[aria-pressed]`,
`#terminal-open-window-link` (main app only), `.overflow-menu-item.overflow-menu-open-window`
(awake + sleeping kebabs only), `#popout-viewer-chip`, `#popout-not-found`,
`#popout-ended`, `#popout-restarted-link`, `window._coralPopout`
(`isPopout, targetSessionId, getState, isOwner, claimOwnership, releaseOwnership`),
layout keys suffixed `:popout`. Existing hooks (`_coralSetLiveSessions`,
`_coralHandleWsMessage`, `_coralGetLiveSessions`, `selectLiveSession`) keep
working in popout mode; `#chat` hash push is suppressed.

## Test plan

- `tests/frontend/agent_popout.test.js` (registered in `tests/frontend/run.sh`):
  fixture-driven shell/state/identity cases via the pre-navigation fetch +
  WebSocket stub pattern (`agent_bar_tweaks.test.js`), real isolated terminal
  sessions for attach / ended-on-kill / sleeping / multi-tab / resize via the
  `terminal_scroll.test.js` launch/kill pattern. Runs only through `run.sh`
  (fresh `CORAL_TEST_PORT`, production-port guard, temp home).
- Go: route handler tests (valid/invalid UUID, 400/404 shapes, boot data
  escaping, auth inheritance), exact-targeting tests with two sessions in one
  folder, `WSTerminal` name-belongs-to-id rejection, resize guard.
- Main-app regression: existing `acf`, `terminal_scroll`, `agent_bar` suites,
  plus `#chat/<sid>` refresh restore and null-target render checks.
- Never point tests at `:8420`; refresh must be side-effect free apart from
  the terminal attach.

## Files

Backend: `internal/server/server.go` (route, validator, template mode/id),
`internal/server/routes/sessions.go` + `websocket.go` (exact resolution,
rejections, resize guard), tests. Frontend: `templates/index.html`,
`templates/includes/views/live_session.html`, `static/app.js`,
`static/state.js`, `static/sessions.js`, `static/router.js`,
`static/websocket.js`, `static/render.js`, `static/controls.js`,
`static/xterm_renderer.js`, `static/capture.js`, `static/sidebar.js`,
`css/layout.css`, `css/output.css`, `css/mobile.css`, `css/session.css`.
Spec + harness: `specs/SINGLE_AGENT_POPOUT.md` (repo root), `tests/frontend/agent_popout.test.js`,
`tests/frontend/run.sh`.
