# Browser plugin and shared review

Status: proposed implementation plan. October 4, 2026.

## Goal

Let agents open and operate a browser inside Coral while the user watches the same page, takes control, and gives feedback directly on what they see. Present this in a popup using the existing Team Activity / Team view dialog pattern. Feedback should remain useful after the page changes or the agent restarts.

The proposed MVP is a shared Chromium page for local development apps and Coral-generated panels, with a feedback rail and explicit control handoff. A provider-neutral Coral CLI exposes navigation and inspection to Claude, Codex, and other CLI agents. This document specifies new work; the browser manager and review popup have not been implemented.

## Product experience

Add **Browser** to the selected agent's actions and expose active browser sessions from Team view. Opening the popup restores the selected agent's review. An agent opening a page adds a badge or Coral notification without stealing the user's focus.

Desktop layout:

```text
Browser review · Team / Agent                        Expand   Close
Page title · URL                 Back   Forward   Reload   Stop
Agent controlling               Watch   Take control   Comment
+-----------------------------------+---------------------------+
|                                   | Feedback                  |
| Shared browser page               | Open / Addressed / All    |
|                                   |                           |
| Element or region annotation      | Screenshot + comment      |
|                                   | Agent response            |
+-----------------------------------+---------------------------+
```

Reuse the Team view shell's visual treatment, focus trapping, Escape behavior, and focus restoration. On mobile, use a full-height dialog with the feedback rail below the viewport or on a separate tab. Resize the display without silently changing the browser's emulated viewport; offer explicit viewport presets.

The popup supports four actions:

- **Watch:** receive the agent's current page without sending input.
- **Take control:** pause agent input, acquire the control lease, and interact with the same page.
- **Comment:** capture a stable view and select an element, region, or whole page before writing feedback.
- **Return control:** release the user lease and allow the agent to continue.

Keep controller identity, connection state, page URL, and pending actions visible. Closing the popup detaches the viewer; **End browser session** explicitly closes the browser. Closing must preserve submitted comments. Prompt before discarding an unsent comment draft.

## Shared page architecture

Coral owns a dedicated Chromium process/profile and connects to its browser automation interface. The agent and user operate the same page target. Coral sends frames to the popup and routes permitted pointer, keyboard, and navigation actions back to that target.

A separate iframe navigation creates a separate page instance with its own runtime state. Therefore the managed browser viewport is the primary shared-control surface. Existing Agent UI iframe previews remain available for lightweight panel viewing. Opening a panel for shared review loads its pinned revision in the managed browser with the existing sandbox restrictions intact.

Do not promise that arbitrary external sites can be embedded in an iframe. The initial scope is local apps and generated panels. External navigation, authenticated account workflows, multiple tabs, uploads, downloads, and cross-server viewing are later extensions.

Suggested components:

| Component | Responsibility |
|---|---|
| Browser manager | Process lifecycle, isolated profile, page target, resource budget, ownership |
| Chromium adapter | Navigation, frame capture, accessibility/DOM snapshots, input |
| Review API | Authorization, request validation, revisions, leases, idempotency |
| Review store | Browser metadata, immutable feedback, delivery and response history |
| Review popup | Shared viewport, controller state, annotations, feedback rail |
| Agent CLI adapter | Stable commands shared by different agent providers |

Reuse the existing artifact store for screenshots, session/team scoping for identity, and durable Agent UI event patterns for feedback. Reuse the safe prompt-delivery path for a short notification containing a feedback ID. Do not treat terminal delivery as acknowledgment or successful work.

“Plugin” here means an optional Coral capability behind these interfaces. The MVP does not require a Chrome extension, provider-specific browser tool, or arbitrary third-party plugin loader. An MCP adapter can expose the same operations later.

## Browser actions and control

Proposed CLI surface:

```sh
coral-agent browser open --url http://127.0.0.1:3000 --task 123
coral-agent browser snapshot --browser <id>
coral-agent browser navigate --browser <id> --url <url> --revision <n>
coral-agent browser click --browser <id> --ref <element-ref> --revision <n>
coral-agent browser type --browser <id> --ref <element-ref> --text <text> --revision <n>
coral-agent browser scroll --browser <id> --dy 500 --revision <n>
coral-agent browser feedback --browser <id> --after <cursor>
coral-agent browser feedback respond --id <feedback-id> --message <text>
coral-agent browser close --browser <id>
```

Commands resolve the originating session using Coral's existing identity mechanism. A browser ID alone does not grant access. Return structured JSON with browser/page identity, revision, action status, and actionable errors. Snapshots include a screenshot artifact URI, URL/title, viewport, and bounded accessible element descriptions with revision-scoped references.

Use a server-owned exclusive control lease. Agent actions and user input enter one serialized queue. Taking control closes admission to agent actions, waits for the current action to settle, then grants the user lease. Timeouts or uncertain in-flight actions must remain visibly uncertain; do not claim that handoff succeeded while an earlier action can still fire.

Reject commands from the wrong controller and stale page/element references. Navigation and DOM changes invalidate references when they can no longer be resolved reliably. Include a page epoch and document revision; checking URL alone is insufficient. Never replay pending clicks or form submissions after reconnect or restart. A request ID prevents duplicate execution; an unknown delivery result requires inspection before a new action.

## Feedback contract

Commenting briefly pauses agent actions and captures a frame plus page state. The selection overlay operates on that captured frame so the comment cannot silently attach to a different page after navigation. Other viewers see that feedback capture is in progress. Cancel releases the temporary hold; a user control lease remains with the user until explicitly returned.

Store each submission with:

- Feedback ID and idempotent submission ID.
- Team, agent session, browser session, page epoch, and optional task ID.
- URL, title, document revision, capture timestamp, and frame ID.
- Screenshot artifact URI, viewport dimensions, device scale, and scroll offset.
- Optional normalized region bounds or element reference, accessible name, and bounding box.
- User text, author, creation time, and delivery state.
- Discussion events and resolution state.

Persist the screenshot and feedback before notifying the agent. If capture fails, offer an explicitly labeled text-only comment rather than implying screenshot evidence exists. If persistence fails, retain the draft and show Retry. Once stored, a notification failure does not erase feedback or create a duplicate on retry.

Notifications should render as a **Coral notification** in chat and carry the feedback ID plus concise context. The agent fetches the durable record and image through Coral's artifact tools. Preserve the screenshot media type and a usable filename extension when downloading.

Use separate states for delivery and resolution: `pending`, `delivered`, or `delivery_unknown` describe notification transport; `open`, `addressed`, or `resolved` describe the discussion. Agents may mark feedback addressed and attach evidence. The user resolves or reopens it. Feedback does not automatically approve work or complete the linked task.

## Proposed API and storage

All routes require authenticated access and verified team/session membership. Names below are proposed, not existing endpoints.

| Method | Route | Purpose |
|---|---|---|
| POST | `/api/browser/sessions` | Open or restore a review using a request ID |
| GET | `/api/browser/sessions` | List reviews within an authorized scope |
| GET | `/api/browser/sessions/{id}` | Lifecycle, page, controller, and capabilities |
| POST | `/api/browser/sessions/{id}/actions` | Submit a revision-checked action |
| POST | `/api/browser/sessions/{id}/control` | Acquire or release control |
| GET | `/api/browser/sessions/{id}/snapshot` | Capture screenshot and inspection data |
| GET | `/api/browser/sessions/{id}/stream` | Authenticated live frames and state events |
| POST | `/api/browser/sessions/{id}/feedback` | Store an idempotent comment |
| GET | `/api/browser/sessions/{id}/feedback?after=N` | Read durable feedback incrementally |
| POST | `/api/browser/feedback/{id}/events` | Respond, mark addressed, resolve, or reopen |
| DELETE | `/api/browser/sessions/{id}` | End runtime without deleting feedback |

Separate persisted review metadata from ephemeral process handles. Suggested tables are `browser_sessions`, `browser_feedback`, and `browser_feedback_events`, plus bounded action/idempotency records. Screenshot retention follows artifact references; ending a browser must not orphan feedback screenshots. Provide explicit deletion/retention rules before shipping cleanup.

Use ordered event cursors and reconnect snapshots. Bound frame buffering and discard superseded frames for slow viewers. Durable comments must not be dropped with transient frames. On Coral restart, mark interrupted runtimes disconnected; reopen a new page epoch only by an explicit action. Preserve feedback but do not promise to restore JavaScript state or replay unconfirmed input.

## Security and resource boundaries

- Keep browser debugging transport private to Coral; never expose its debugging port through remote access or return debugger credentials to agents or pages.
- Use a dedicated temporary profile with no access to the user's normal browser cookies, extensions, or passwords. Keep Chromium's sandbox enabled.
- Start with explicitly registered local development origins and Coral panel routes. Validate navigation targets, redirects, popups, subresources, and browser-initiated network requests against policy. Block arbitrary file URLs, internal metadata services, Coral administrative routes, and unapproved network destinations. Test actual enforcement; top-level URL validation alone is insufficient.
- Preserve generated-panel CSP/sandbox limits and mediate required panel capabilities without giving the page general Coral credentials.
- Treat page text, DOM content, and comments as untrusted data. Captured content cannot grant new tool permissions.
- Existing remote-access defaults and authentication remain in force. An authorized viewer is not automatically an authorized controller.
- Disable clipboard access, file pickers, downloads, microphone, camera, and persistent credentials in the MVP. Redact password values from inspection data and avoid automatic secret logging. Screenshots may contain sensitive visible data and must retain scoped access.

Proposed conservative defaults: one active browser per team and two per server, one page per browser, launch on demand, and no browser launch when listing sleeping agents. Pause frame capture when there are no viewers unless an explicit snapshot is requested. Begin at a maximum of five frames per second and tune from measurements.

After five idle minutes with no controller, viewer, or agent action, close the runtime and preserve review metadata. Measure child-process memory and apply a configurable budget; refuse new sessions under pressure and terminate an over-budget session with a visible explanation. Do not claim a hard memory cap until platform enforcement is verified. Browser automation must not make Chrome a dependency of ordinary Coral startup or the regular static Linux binaries. Detect an installed supported browser and report setup instructions when unavailable; do not silently download one.

## Implementation phases

### Phase 1 Shared browser viewing

Implement the optional manager and adapter, local-origin policy, single-page open/snapshot/navigation, authenticated frame transport, and Team view-style popup. Add controller/status placeholders and lifecycle cleanup. Demonstrate that the agent snapshot and displayed page refer to the same runtime.

### Phase 2 Durable feedback

Add capture/annotation, screenshot artifacts, persistent feedback, cursor reads, Coral chat notification, and user resolution. Implement duplicate submission handling and restart recovery before adding interactive control.

### Phase 3 Shared control

Add the lease and action queue, pointer/keyboard routing, agent element actions, Take control / Return control, stale-reference errors, and mobile coordinates. The MVP ships when phases 1–3 meet the acceptance criteria.

### Later extensions

Evaluate multiple tabs, external sites and accounts, controlled uploads/downloads, session recordings, cross-server viewing, and an MCP adapter independently. None is necessary for local app review.

## Acceptance criteria

- Agent and user observe the same navigation, form state, scrolling, and viewport; no duplicate iframe session is mistaken for a shared page.
- Popup follows Team view interaction patterns, restores focus, supports keyboard operation, and works at mobile widths. Provide accessible page inspection alongside the visual stream.
- Take control prevents later agent input until the lease is returned; queued, timed-out, and disconnected actions cannot execute unexpectedly.
- Feedback remains tied to its captured frame through navigation, agent replacement, and Coral restart. Element references are never silently reused on a new document.
- Repeated submissions create one feedback record. Transport failure, unknown delivery, storage failure, and stale revision each have distinct recoverable UI states.
- Cross-team reads, stream subscriptions, feedback writes, and control attempts are rejected. Redirects and subresources cannot bypass the local-origin policy.
- Missing Chrome, browser crash, renderer hang, slow viewer, idle expiration, and memory pressure produce bounded cleanup and clear status.
- An inactive or sleeping agent produces no browser process and no frame polling. Existing artifact previews and Agent UI events continue working.

Verify with deterministic fixtures and isolated local apps, one browser at a time. Include a real browser end-to-end test for shared state, handoff races, feedback persistence, and restart. Keep process/concurrency limits explicit and record peak memory. Mocked route tests alone cannot establish shared browser state or navigation policy enforcement.

## Existing integration points

- `coral-go/internal/server/frontend/static/team_availability.js`: popup shell and Team view navigation.
- `coral-go/internal/server/frontend/static/agent_ui.js`: panel UI and interaction patterns.
- `coral-go/internal/server/frontend/static/live_chat.js`: Coral notification presentation.
- `coral-go/internal/server/routes/agent_ui_request.go`: durable request/delivery semantics to reuse where applicable.
- `coral-go/internal/ptymanager/` and `coral-go/internal/tmux/prompt.go`: guarded agent notification transport.
- `specs/AGENT_GENERATED_UI.md`: existing panel specification; browser review extends its presentation options.

Before implementation, prototype the Chromium adapter and network policy, confirm supported browser discovery on Linux/macOS, and measure frame latency and memory. The proposed resource defaults and transport encoding should be finalized from that prototype.
