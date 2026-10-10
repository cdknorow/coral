# Mobile App Simplification Spec

## Goal

Refocus the mobile app on the three things that matter on a phone:

1. Live agents
2. Agent group chat plus text input
3. Individual agent conversation plus text input

Mobile should not be a compressed version of the desktop app. It should be a narrow, fast client for checking on live agents and sending messages.

## Current State Findings

### 1. Mobile is still a thin layer over the desktop IA

The current mobile implementation is not a distinct product surface. It hides or clones desktop views rather than defining a mobile-first flow.

Observed in:

- `pkg/frontend/static/mobile.js`
- `pkg/frontend/templates/index.html`
- `pkg/frontend/templates/includes/views/live_session.html`
- `pkg/frontend/templates/includes/views/message_board.html`
- `pkg/frontend/templates/includes/views/history_session.html`

Evidence:

- Mobile navigation still switches between desktop-level views: agents, live session, history session, board, scheduler.
- The live session view is the desktop live session template with pieces hidden by CSS.
- The message board and history views are also desktop views shown conditionally on mobile.

Impact:

- Product scope is unclear on mobile.
- UI behavior depends on desktop DOM structure.
- Every desktop feature increase risks spilling into mobile unintentionally.

### 2. Mobile navigation is internally inconsistent

The code supports a bottom tab bar, but the mobile CSS hides that tab bar entirely.

Evidence:

- `templates/index.html` renders a 3-tab mobile nav: Agents, Chat, Board.
- `static/mobile.js` still manages `_currentMobileTab` and `switchMobileTab(...)`.
- `static/css/mobile.css` explicitly sets `.mobile-tab-bar { display: none; }` at mobile width.

Impact:

- There is no stable, visible primary navigation model.
- The codebase contains dead or half-dead navigation logic.
- Users are likely relying on accidental transitions instead of explicit information architecture.

### 3. Mobile agent list is duplicated from the sidebar instead of owning its own rendering

The mobile agent list is created by cloning the desktop sidebar session list and re-binding handlers.

Evidence:

- `syncMobileAgentList()` clones `#live-sessions-list`.
- Click handlers are recovered from inline `onclick` strings and executed with `eval(...)`.

Impact:

- Mobile correctness depends on sidebar markup staying compatible.
- This is fragile and hard to evolve.
- It makes mobile rendering and interaction logic indirect and difficult to reason about.

### 4. Mobile currently exposes too much surface area

The app still includes or routes toward:

- Welcome screen team presets
- Session history
- Scheduler detail view
- Full message board project list and management actions
- Desktop live-session metadata and panel system
- Desktop command toolbar behaviors

Impact:

- The mobile app is trying to serve setup, orchestration, browsing, analytics, history, and monitoring in one constrained surface.
- This dilutes the primary use case: check live agents and message them.

### 5. The live session view is still terminal-centric, not conversation-centric

The current live session is optimized around terminal capture plus the desktop command pane. On mobile, some desktop elements are hidden, but the underlying mental model is still "remote terminal session."

Evidence:

- `live_session.html` centers the capture/xterm stack and desktop command pane.
- Mobile CSS mostly hides the side panels and compresses the command UI rather than changing the screen model.
- Individual conversation history exists in `#live-history-messages`, but it lives as one tab in a larger desktop panel system.

Impact:

- The phone UI does not clearly separate:
  - what the agent said
  - what I said
  - what the team is discussing
  - what raw terminal output exists
- The most useful mobile action, "send a message to an agent," is visually secondary to terminal output.

### 6. Group chat exists twice, in two different shapes

There is a full message board view and a board chat panel rendered inside the live session side panel.

Evidence:

- `includes/views/message_board.html` is a full-page board experience.
- `static/render.js` renders `showBoardChatTab(...)` into `#agentic-panel-board`.

Impact:

- The same concept appears in two different UI patterns.
- Mobile does not have a single obvious "group chat" surface.
- This raises maintenance cost and increases behavioral drift.

### 7. Mobile behavior relies heavily on CSS suppression instead of reduced markup

Large parts of the desktop experience remain mounted in the mobile DOM and are just hidden at small widths.

Evidence:

- Mobile CSS hides the sidebar, agentic state panel, toolbars, metadata, overflow actions, resize handles, and subscribers panel.
- The underlying templates still render them.

Impact:

- Mobile complexity stays high even when the visual UI looks simpler.
- It is easy to create regressions by changing desktop markup or class names.
- Performance and maintainability suffer.

## Product Direction

The mobile app should be redefined as a **live companion**, not a full control center.

### Primary mobile jobs

1. See which agents are live right now
2. Open a team and read/write the group conversation
3. Open an individual agent and read/write the direct conversation

### Non-goals for mobile v1

- Session history browsing
- Scheduler management
- File browsing and git diff workflows
- Notes, activity charts, tasks, commits, stats
- Theme customization and advanced settings beyond auth/connectivity
- Full terminal power-user controls
- Launch-complex-team workflows

These can remain desktop-only until proven essential on phones.

## Target Information Architecture

### Surface 1: Live Agents

This is the mobile home screen.

Contents:

- Search or filter optional, but not required for v1
- List of live agents grouped by team
- Each row shows:
  - agent name
  - team name
  - agent type badge
  - latest status
  - waiting-for-input state if relevant
  - unread indicators for direct chat and team chat if available

Primary actions from each row:

- Open agent conversation
- Open team conversation

Secondary actions:

- None in row for v1

This screen should not include:

- launch presets
- welcome marketing content
- desktop section clones
- history mixed into the same list

### Surface 2: Team Conversation

This is the group chat view for a board/team.

Contents:

- Header with team name and back button
- Scrollable message feed
- Composer pinned to bottom
- Optional lightweight participant strip, only if it materially helps

Capabilities:

- Read all board messages
- Send a text message
- Mention agents if already supported cleanly

This screen should not include:

- export, delete, pause-read admin controls
- project-management side panels
- subscribers side rail
- desktop split-pane board layout

### Surface 3: Agent Conversation

This is the individual conversation with one live agent.

Contents:

- Header with agent name, team name, status, and back button
- Conversation feed between user and agent
- Composer pinned to bottom
- Optional collapsible "Live Output" section if terminal output must remain accessible

Capabilities:

- Read the user/agent conversation
- Send a text prompt
- Send a "with team reminder" variant only if it proves necessary and understandable

This screen should not default to a raw terminal view.

Terminal output, if kept at all, should be demoted behind a secondary affordance such as:

- segmented control: `Conversation | Live Output`
- collapsible drawer
- "View terminal output" sheet

The default should be conversation.

## Navigation Model

Use a simple, explicit 3-destination navigation model:

1. `Live Agents`
2. `Team Chat`
3. `Agent Chat`

Recommended behavior:

- Root route is `Live Agents`
- Tapping an agent row opens `Agent Chat`
- Tapping a team affordance opens `Team Chat`
- Back always returns to `Live Agents`

Recommendation:

- Keep a visible bottom tab bar only if it is truly needed.
- For the three screens above, a stack-based flow is simpler than the current half-tab, half-hidden-view model.

Preferred mobile navigation for v1:

- `Live Agents` as root list
- push `Team Chat`
- push `Agent Chat`

That means the current bottom-tab implementation should be removed unless product explicitly wants persistent tab switching.

## UX Requirements

### Live Agents list

- Must load quickly
- Must support pull-to-refresh
- Must preserve scroll position when returning from a conversation
- Must clearly show waiting-for-input states
- Must make the team relationship obvious

### Team Chat

- New messages should auto-scroll when user is already at bottom
- Composer must stay usable with iOS and Android keyboards
- Input font size must avoid mobile browser zoom
- Mentions, if present, must feel native and not overload the composer

### Agent Chat

- Conversation should be visually distinct from terminal output
- User and agent messages should be styled as chat bubbles or clearly separated blocks
- Composer must be the primary action
- Agent status should remain visible but compact

## Technical Recommendations

### 1. Stop cloning desktop DOM into mobile

Replace `syncMobileAgentList()` cloning with a dedicated renderer that maps `state.liveSessions` directly into mobile cards/rows.

Do not depend on:

- cloned sidebar markup
- inline `onclick`
- `eval(...)`

### 2. Introduce mobile-specific routes or view state

The mobile app should own its own view model, for example:

- `mobileRoute = { screen: 'agents' }`
- `mobileRoute = { screen: 'team', boardName }`
- `mobileRoute = { screen: 'agent', sessionId }`

Do not reuse desktop "show/hide arbitrary views" logic as the primary mobile navigation mechanism.

### 3. Separate conversation data from terminal rendering

Mobile agent chat should bind to message history first, not terminal capture first.

If conversation history is already available through existing live/history message structures, reuse that path. If not, add a dedicated live conversation feed API or state shape.

### 4. Keep group chat implementation single-source

Choose one board chat rendering path for mobile and standardize on it.

Recommended source of truth:

- team chat message feed + composer as a standalone mobile view

Avoid maintaining both:

- a full board desktop view adapted for mobile
- a side-panel board chat view adapted for mobile

### 5. Reduce mobile markup at template level

Do not render large desktop-only regions on mobile and then hide them with CSS.

Prefer:

- smaller mobile-specific templates
- conditional rendering boundaries
- shared data/state, separate presentation

## Proposed Phases

### Phase 1: Scope reduction

- Remove mobile entry points to history, scheduler, and welcome preset surfaces
- Remove hidden/dead tab logic that is no longer part of the mobile model
- Define the three supported mobile screens in code

### Phase 2: Dedicated mobile list

- Build a mobile-native live agents list renderer
- Group by team by default
- Add unread/waiting/status indicators

### Phase 3: Dedicated team chat screen

- Build a single-column team chat screen with pinned composer
- Reuse existing board messaging backend
- Strip admin and desktop management actions

### Phase 4: Dedicated agent chat screen

- Make conversation the default agent view
- Demote terminal output behind a secondary affordance
- Keep composer pinned and obvious

### Phase 5: Cleanup

- Delete obsolete mobile CSS and hidden desktop affordances
- Remove `eval(...)`-based mobile event rebinding
- Remove duplicated mobile/desktop board pathways where possible

## Success Criteria

The mobile app is successful when:

- A user can understand the app in under 10 seconds
- The first screen is clearly "live agents"
- Opening a team chat or agent chat feels immediate and obvious
- Sending a message is the easiest action on the screen
- There is no visible trace of scheduler/history/files/tasks/notes desktop complexity on mobile

## Open Questions

1. Does mobile need to launch new agents at all, or should that remain desktop-only?
2. Should team chat and agent chat expose attachments/images in v1?
3. Should live terminal output be accessible on mobile, or fully omitted?
4. Do we need unread counts separately for team chat and direct agent chat?
5. Is the mobile app intended mainly for monitoring, messaging, or both equally?

## Recommendation

Treat this as a product-boundary correction, not a styling pass.

The current mobile app is best understood as "desktop Coral with selective hiding." The right next step is to define a much smaller mobile product and rebuild the mobile shell around that reduced scope.
