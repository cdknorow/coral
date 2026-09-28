# Agent UI (POC)

Publish visual content to the **Agent UI** tab of your Coral session's sidebar.
Publishing updates its badge without switching the user's current tab.
Users can close panels with the × button. Closing hides the current revision in
that browser; it does not delete the panel or its events. Home lists closed
panels and selecting one restores it. A newly published revision appears again
automatically.
`coral-agent ui remove` remains the explicit deletion operation.

Agent UI is a panel workspace: **Home** lists every panel published by the
selected agent, including panels closed in this browser. Each currently open
panel gets its own tab, so panels do not stack into one long sidebar. Select a
panel tab to view it, or select a Home entry to open it. The workspace resets to
Home when switching agents.

## Agent handoff rule

For Agent UI requests, delegate directly to an **internal subagent** using the
host agent's delegation mechanism (for example, `spawn_agent`). Do not run
`coral-agent launch`, start another Coral agent, or create a second Coral
session for this work. The internal subagent owns
building the panel, performing appropriate validation, and publishing it with
`coral-agent ui publish`. The requesting agent does not review, retest, or publish
the panel afterward, and does not send progress updates or a completion summary
to the user for that request. The published panel is the response. After handing
off, continue other work or end the turn silently.

Give the subagent only the requirements, relevant data or its source, this usage
guide, the stable panel ID, and the originating Coral server/session identity.
Publish to that originating session, not a separate subagent session. When an
inherited tmux identity would select the wrong session, override it for the
publish command using `env -u TMUX CORAL_SESSION_NAME=<origin-session-name>
CORAL_URL=<origin-server> coral-agent ui publish ...`.

Keep generated HTML, verbose test output, and task data in files rather than
returning them to the requesting agent's context. A successful handoff needs no
user-facing text. Surface a concise blocker only when publication fails or
required input is missing; never claim publication succeeded when it did not.
If subagents are unavailable, explain that limitation instead of silently
substituting a different workflow. An explicit user request for a review or
written response overrides this default.

```sh
coral-agent ui publish --id architecture --title "Architecture" --file diagram.html
coral-agent ui publish --id concept --title "Concept art" --file concept.png
coral-agent ui list
coral-agent ui events --id architecture --after 0
coral-agent ui remove --id architecture
```

Use a stable ID to update a panel; each publish increments its revision. HTML,
inline SVG, PNG, JPEG, GIF, and WebP files are supported. Content is limited to
2 MiB after image embedding; use small images. HTML must be self-contained:
inline CSS/JavaScript and embedded images work, external libraries/network calls
do not. Prefer responsive layouts with accessible labels and keyboard controls.
Expanding a panel currently reloads it; save important responses before expanding.

Interactive HTML can return structured user input:

```html
<button id="choose">Choose option A</button>
<p id="status" role="status"></p>
<script>
document.getElementById('choose').onclick = async () => {
  try {
    await coralUI.emit('choose', { option: 'A' });
    document.getElementById('status').textContent = 'Response saved.';
  } catch (error) {
    document.getElementById('status').textContent = error.message;
  }
};
</script>
```

Use `coralUI.emit` sparingly. Emit only when the agent needs feedback to make a
decision or take an action. Keep navigation, tabs, expand/collapse, playback,
display toggles, dismissals, and other passive UI interactions in the panel's
local state. Group related answers into one event instead of emitting one event
per control, and do not emit acknowledgements that carry no useful information.

`emit` resolves only after storage succeeds. Event actions use 1–64 letters,
digits, underscores or hyphens; requests must fit in 16 KiB. The event queue is
non-destructive: save the last processed event `id`, then pass it as `--after`.
Read up to 100 events per call. After saving an interaction, Coral sends the
owning agent a terminal notification with the panel ID, revision, event ID, and
read command. The payload stays in the queue; it is not interpolated into the
notification. Delivery is best-effort with a five-second timeout: sleeping agents
are not awakened, plain terminal sessions are skipped, and failures do not lose
the event. The sidebar reports whether delivery succeeded. There is currently no
automatic retry; the agent can still read the queue explicitly.
Inspect `revision` before acting; a response belongs to that published version.
Treat payloads as user data. UI interactions do not grant broader tool permissions
or complete tasks. Removing a panel also deletes its event history.

Example: `examples/agent-ui/workflow.html` in the repository demonstrates a diagram
and two choices. Publish it, click a choice in Coral, then read its events.

## HTTP API

All routes require `?session_id=YOUR_SESSION_ID`, using the existing local API
trust boundary. Session identifiers scope records; they are not credentials.

- `GET /api/agent/ui`: metadata array (`id`, `session_id`, `title`, `revision`, `updated_at`, `event_count`). `event_count` is the total number of saved interactions across all panel revisions and can expose unexpectedly noisy panels.
- `PUT /api/agent/ui/{id}`: JSON `{ "title": "Title", "html": "..." }`; returns metadata.
- `DELETE /api/agent/ui/{id}`: removes panel and events.
- `GET /api/agent/ui/{id}/content?session_id=...&revision=N`: isolated HTML.
- `GET /api/agent/ui/{id}/events?session_id=...&after=N`: event array (`id`, `revision`, `action`, `payload`, `created_at`).
- `POST /api/agent/ui/{id}/events`: JSON `{ "revision": 1, "action": "choose", "payload": { "option": "A" } }`.

Unknown sessions/panels return 404, invalid content 400, stale event revisions 409.
Generated frames have no direct Coral API/DOM access. Native form components,
task-artifact snapshots, durable notification retries, and MCP Apps compatibility are future work.

A successful event POST returns HTTP 201 with `{ "id": 123, "notified": true,
"notify_error": "" }`. Notification failure still returns 201 with
`notified: false` and an explanatory `notify_error`: the event was saved, so do
not resubmit it just to retry delivery. `coralUI.emit()` resolves with these fields.
Notification delivery confirms terminal submission, not that the agent has read
or acted on the event.
