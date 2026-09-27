# Agent-generated UI in Coral

Status: proposed design with an initial POC. September 26, 2026.

## Goal

Let any CLI-based agent publish images, diagrams, and interactive interfaces to
Coral's sidebar, and receive structured user responses. Agents continue using
their existing CLI harness; Coral owns presentation, persistence, and delivery.

## Research and design direction

- [A2UI](https://a2ui.org/) describes UI as data drawn from a host-controlled
  component catalog. It is a good fit for native forms, choices, tables, and
  progress cards, including consistent accessibility and mobile behavior.
- [MCP Apps](https://apps.extensions.modelcontextprotocol.io/api/) delivers HTML
  resources in sandboxed frames with host-mediated bidirectional communication.
  This is a useful model for interactive diagrams, charts, and custom mini-apps.
- [AG-UI](https://github.com/ag-ui-protocol/ag-ui/blob/main/docs/ag_ui.md) addresses
  event and state exchange between an agent and a frontend. That concern is
  separate from the rendering format.

Use two complementary renderers: native components for common interactions and
isolated HTML for custom visuals. The POC implements the latter, plus local image
publication. It borrows these architectural patterns; it does not claim protocol
compatibility. Existing global Custom Views remain separate: their current
same-origin script permissions are inappropriate for generated agent panels.

## User experience

One **Agent UI** tab shows panels belonging to the selected session. Publication
updates a badge without stealing focus. Panels have a trusted title, revision,
expand control, and visible interaction status. Updates replace the matching
panel by stable ID; unchanged panels retain their interactive state. Switching
agents must immediately hide the previous agent's content. Panels survive reloads
and server restarts. Expanding or collapsing a panel reloads its frame in this
POC; important choices should be submitted before changing its size. The sidebar is usable at mobile widths. Each panel has a close control, including
in expanded view. Dismissal is stored per session/panel/revision in browser local
storage and survives reloads. Closed revisions do not contribute to unread counts.
Home reopens closed panels; a republished revision becomes visible again.
Closing is not deletion: panel contents and response events remain intact.
The Agent UI panel is a workspace with a Home tab listing all created panels and
one tab per open panel. Switching agents returns to Home; selecting a Home entry
opens that panel without stacking all frames vertically.

Later: unread state persisted per user, pinning, task links, native choice/form
cards, and optional promotion of a published revision to a task artifact.
Publishing UI alone never completes a task or grants an approval.

## Agent handoff policy

Agent UI generation is delegated end to end: the subagent builds, validates, and
publishes directly to the originating Coral session. The requesting agent hands
off minimal context and does not perform a second review, publish step, progress
narration, or completion summary. The panel itself is the user-facing result.
Generated source and verbose output remain in files. Only failures or missing
required inputs need to be surfaced; explicit user requests can override this
policy. See `coral-go/agent_docs/agent-ui.md` for the operational rule and session
identity handling.

## CLI and HTTP contract (POC)

```
coral-agent ui publish --id architecture --title "Architecture" --file diagram.html
coral-agent ui publish --id concept --title "Concept art" --file concept.png
coral-agent ui list
coral-agent ui events --id architecture --after 0
coral-agent ui remove --id architecture
```

The CLI resolves its session through the same mechanism as `coral-agent task`.
HTML files may contain inline SVG, CSS, and JavaScript. Raster images are embedded
as data URLs by the CLI. No arbitrary server-side file reads or remote downloads.

All endpoints require `session_id` as a query parameter:

| Method | Route | Meaning |
|---|---|---|
| GET | `/api/agent/ui` | Panel metadata for a session |
| PUT | `/api/agent/ui/{id}` | Create/replace `{title, html}`; returns metadata |
| DELETE | `/api/agent/ui/{id}` | Remove panel and its events |
| GET | `/api/agent/ui/{id}/content` | Isolated HTML document |
| GET | `/api/agent/ui/{id}/events?after=N` | Up to 100 events after a cursor |
| POST | `/api/agent/ui/{id}/events` | Record `{revision, action, payload}` |

IDs are 1–64 ASCII letters, digits, underscores or hyphens; titles 1–160 bytes;
HTML up to 2 MiB; event requests up to 16 KiB. Revisions increment atomically.
Event IDs are ordered database cursors. Consumers persist their last processed
cursor; reading is non-destructive. Events contain panel revision and timestamp.
Posting against an outdated revision returns 409; unknown sessions/panels 404.
Removing a panel is destructive to its event history. Reusing its ID starts a new
panel at revision 1. Task-linked immutable snapshots are a later feature.

## Interactive bridge

The injected browser helper exposes:

```js
await coralUI.emit('choose', { option: 'A' });
```

The Promise resolves after Coral durably records the event and rejects on failure.
The host validates the sending frame against its mounted panel and revision,
limits payloads, and forwards only interaction events. The host renders success
or failure outside the frame. The agent reads the queue through the CLI. After storage succeeds, the POC sends
a notice through Coral's existing terminal notification transport, containing the
panel ID, revision, event ID, and read command. Payloads and panel titles are not
included in the notice. Delivery has a five-second timeout independent of the
request context. Sleeping agents and plain terminals are skipped. HTTP 201 and
the bridge result include `notified` and `notify_error`; failure to notify does
not turn a saved interaction into a failed submission. Terminal submission is
not an acknowledgment that the agent processed the event. Durable retries,
waking sleeping agents, and processing acknowledgments are future work. Treat action payloads as user data, never as
instructions with elevated authority.

## Isolation and trust boundary

Serve generated HTML with CSP and an iframe sandbox allowing scripts but not
same-origin access, forms, popups, top navigation, or downloads. Disallow network
connections, external scripts/styles/images, nested frames, objects, and base URL
changes; support inline code/styles and embedded images. No API tokens or secrets
are injected. The frame cannot directly access Coral's DOM, storage, or APIs.
Only validated messages from a currently mounted frame can create events. Labels
outside the frame are rendered as text. These restrictions also apply when the
content URL is opened directly.

Session IDs provide routing/ownership scoping, matching existing local CLI APIs;
they are **not authentication credentials**. This POC assumes Coral's existing
trusted local API boundary. Multi-user authorization, event spam quotas, hostile
CPU/memory use, and iframe self-navigation need separate hardening before opening
publication to untrusted remote producers. Native components are the preferred
longer-term interface for sensitive approvals.

## POC acceptance and follow-ups

- Publish, replace, list, view, interact, consume events, and remove via real API.
- Preserve panels/events across database reopen; enforce session scoping and sizes.
- Reject stale-revision interactions; keep other sessions and revisions isolated.
- Verify in Chrome that parent DOM/network access is denied, interactions return,
  updates preserve unchanged frames, and mobile/expand behavior works.
- Include a self-contained diagram/choice demo and agent-facing instructions.

The POC uses bounded polling for sidebar discovery. Follow-ups include websocket
notifications, revision history/artifact promotion, durable notification retries and
idempotency, native JSON components, capability negotiation, and an MCP Apps
adapter. Do not couple persistence or ownership to one transport or agent vendor.

## POC implementation and validation

Implemented in `coral-go/cmd/coral-agent/ui.go`, the agent UI routes/store, and
`coral-go/internal/server/frontend/static/agent_ui.js`. Usage is documented in
`coral-go/agent_docs/agent-ui.md`. Demo: `examples/agent-ui/workflow.html`.

Validation commands (isolated test infrastructure; no model API calls):

```sh
cd coral-go
go test ./...
ONLY=agent_ui.test.js CORAL_TEST_PORT=18467 CDP_PORT=19227 bash ../tests/frontend/run.sh
```

The browser test launches a temporary terminal session, publishes through the
actual CLI, clicks a real isolated iframe button, checks recorded events through
the CLI, and verifies DOM/network isolation, revision updates, mobile expansion,
session switching, and removal. Route/store tests cover stale revisions, limits,
unknown sessions, cross-session reads, event cursors, and database reopen.
