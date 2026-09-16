# Agent Bar Tweaks — spec updates and review findings

For the team implementing `specs/AGENT_BAR_TWEAKS.md`. Everything below is
relative to the version of the spec you started from (commit `e38383b`).
The spec file itself has been updated; this is the changelog plus the review
findings on your current working-tree diff.

## Action items (in priority order)

1. **Fix: `first_prompt` is dropped on the first WebSocket tick.** Real bug,
   visible in the current build. See Finding 1.
2. **Fix: initials must not derive from `summary`.** Spec change; see D1.
3. **Fix: "No goal yet" label must be passive text.** Spec change; see D1.
4. **Fix: `tests/frontend/agent_bar_tweaks.test.js` is racy in the suite.**
   Passes 46/46 standalone, fails in `run.sh`. See Finding 2.
5. **Change: cache only the first prompt, not whole transcripts.** See
   Finding 3.
6. **Stop: do not extend `requestGoal` / `refreshGoal`.** The PULSE
   injection is being replaced by a separate team — see D8. Your only change
   there is item 3.

Not yours: `go test ./internal/server/routes/` fails on `TestUpdateCheck`
(`system_test.go:80`). It fails identically on a clean `HEAD` worktree —
pre-existing, unrelated.

## Tracking

| Item | Landed in | Notes |
|---|---|---|
| 1. `first_prompt` survives WS ticks | #72 (client), #76 (both payload builders) | Client now uses generic spread-merge (task #81): omitted fields preserve, explicit `null`/`""`/value overwrite, new sessions taken intact |
| 2. Initials never from `summary` | #72, #81, #85 | Shared `sessionIdentitySource`: `display_name → auto_name → board_job_title`, then folder/terminal name. #85 removed `first_prompt` from identity after screenshots showed paths/sentences as names and initials |
| 3. "No goal yet" is passive text | #81 | Label has no handler/title/pointer; click bubbles to select the row. Sparkle is the sole trigger, 24×24, `aria-label` |
| 4. Suite race | #72, #75 | HTTP stub before navigation + pre-navigation `/ws/coral` constructor stub; runner refuses :8420 and occupied ports |
| 5. Cache only the first prompt | #71, #76 | Backend |
| 6. Do not extend `requestGoal` | — | Unchanged apart from item 3 |

Identity chain `display_name → auto_name → board_job_title → Agent/Terminal`
is applied to row label (+title), avatar initials, header, terminal label and
the `Sending to:` placeholder (#81, revised #85), ready for D8's `auto_name`.
`first_prompt` is the goal-line fallback (`summary → first_prompt`) only. The real-socket
browser e2e suggested under Finding 2 is deferred; the merge path is covered
by driving `handleCoralMessage` directly.

## Spec changes

### Status

`Planned` → `In Progress`. Index row in `specs/README.md` updated; a new
row for **Transcript Goals** was added beneath it.

### D1 — Identity on every row (corrected)

What changed and why:

- **`first_prompt` must be in both payload builders.** The original text
  said "add `first_prompt` to the live-sessions response" — singular. There
  are two builders: the HTTP list (`routes/sessions.go` `List`) and the
  WebSocket tick (`routes/websocket.go`, ~line 330). You added it to the
  first only. See Finding 1.
- **Client merge must preserve it.** `websocket.js` `coral_diff` does
  `sessions[idx] = changed` and keeps only an allowlist (`commands`, `icon`,
  `token_*`, `context_pct`). Switch to `{...old, ...changed}` so fields the
  WS omits survive by default. Every previous field on that allowlist got
  there by hitting this same bug.
- **Initials: `display_name → auto_name → first_prompt`, never `summary`.**
  *(Revised by task #85: `first_prompt` was dropped from identity too, in
  favour of `board_job_title`; see Tracking.)*
  The original text said "summary's first two words". That was wrong:
  summary changes as the agent works, so the avatar would drift
  (`DT` today, `RE` tomorrow). Identity must be stable. `auto_name` comes
  from D8; until it exists, use `first_prompt`.
- **"No goal yet" is plain text, not a click target.** Your implementation
  made both the sparkle and the label call `requestGoal`. The label sits
  where users click to select a row, and `stopPropagation` means a mis-click
  sends a prompt to the agent *and* doesn't select the row. This is why
  "Emit a ||PULSE:SUMMARY…" started appearing in agent terminals more
  often. Sparkle stays the only trigger, with a visible hover state.
- **Identity chain** everywhere (row label, avatar, right-pane header,
  `Sending to:` placeholder): `display_name → auto_name → first_prompt →
  "Agent"`.

### D3 — Context bar (changed by operator, task #75)

Already in your tree: the sidebar **Compact** button was removed as too easy
to hit accidentally. The context display is informational only. Noted here
for completeness; no action.

### D5 — Attention rollup is badge-only

Already in your tree (badge on the Agents tab, no reordering). No action.

### D8 — Transcript-derived goals (new, owned by another team)

The PULSE prompt injection is being replaced. Full spec:
`specs/TRANSCRIPT_GOALS/README.md`. A background service will derive
`{name, goal}` from each agent's transcript via `claude -p` / `codex exec`
and store `session_meta.auto_name` (write-once) and `auto_goal`.

The contract your code must honor so their work drops in cleanly:

- Both payload builders will gain `auto_name`, `goal_pending`, and
  `summary = pulse_summary || auto_goal`. Your spread-merge (D1) is what
  keeps those alive on the client.
- The sparkle will call `POST /api/sessions/live/{name}/goal` instead of
  writing to the terminal. **That change is theirs** (their Phase B), so
  leave `requestGoal` / `refreshGoal` as they are apart from making the label
  passive.
- `resolveSessionIdentity` should already read `auto_name` in its chain so
  it lights up the moment the backend ships.

### Phase 1 file list (corrected)

Replaces the "if `first_prompt` is not in the payload" bullet:

- `internal/server/routes/sessions.go` (`List`) **and**
  `internal/server/routes/websocket.go` — `first_prompt` in both builders
- `internal/jsonl/reader.go` — cache the first prompt per session (Finding 3)
- `internal/server/frontend/static/websocket.js` — spread-merge

### Open Questions

The "latest vs first user prompt" question is resolved by D8: `first_prompt`
is the stable identity fallback; the *current* goal comes from the transcript
service. The Compact confirm question is moot after task #75.

## Review findings on the current diff

### Finding 1 — `first_prompt` dropped on the first WS tick (bug)

- `routes/websocket.go` builder has no `first_prompt`.
- `websocket.js` `coral_diff` replaces the object; `first_prompt` is not on
  the preserve allowlist.
- `websocket.js` ~line 175 sets `state.currentSession.first_prompt =
  s.first_prompt || ''`, actively clearing it.

Effect: an unnamed agent with no summary shows its first prompt for one
poll, then reverts to "No goal yet"; initials flip back to the folder's;
header and placeholder fall back to "Agent". In the last screenshot,
*Frontend Dev* at `ctx 79%` and *QA Engineer* at `29%` showing "No goal yet"
is this bug — agents that far into their context were certainly given a
prompt.

Your test didn't catch it because it blocks the WebSocket entirely.

### Finding 2 — frontend test is racy inside the suite

Standalone: 46/46. In `tests/frontend/run.sh`: dies at "renders all fixture
rows — got 1", and the one row is the terminal session from
`terminal_scroll.test.js`. Two causes:

- `terminal_scroll.test.js:222` fires `DELETE` but never waits for the
  session to disappear from `/api/sessions/live`.
- `app.js:616` (`pollStartupStatus`) and `:666` (init) call
  `loadLiveSessions()`; that fetch resolves *after* the fixture is injected
  and overwrites it. Blocking `*/ws/coral*` is not enough.

Fix: stub `fetch` for `/api/sessions/live` before injecting (you already do
this pattern for `/send`), and have the scroll test poll until its session
is gone. Also add one scenario with the WebSocket **unblocked** so the
diff-merge path is exercised — that is the scenario that would have caught
Finding 1.

### Finding 3 — `FirstUserPrompt` pins every transcript in memory

`FirstUserPrompt` → `ReadAllMessages` is incrementally cached (cheap per
call), but it now loads and holds the *entire transcript* of every listed
session in `SessionReader`'s cache, which is only cleared by `ClearSession`.
Previously only transcripts someone opened were cached. Store the first
prompt in `sessionCache` (or a small separate map) and stop retaining the
full message slice for sessions nobody has opened. It also scans all cached
messages on every list call; a stored value makes that O(1).

### Finding 4 — good, keep

- Team header line `5 agents · 11.3M tokens · 22m` — not in the spec,
  exactly the rollup an operator wants.
- `ctx 79%` labels and colour thresholds read unambiguously.
- Density: rows ~46–69 px with nothing cramped.
- Mode button tracking Shift+Tab from the terminal buffer via
  `coral:terminal-updated` is the right mechanism.
- Backend `FirstUserPrompt` test and the routes serialization test are
  solid; keep them, just move the storage per Finding 3.
