# Teams API

Manage persistent agent teams — groups of agents that collaborate on a shared message board. Teams track their members as slots with lifecycle state, so they can be stopped, resurrected, or relaunched without losing their configuration.

---

## Concepts

### Team Lifecycle

Teams move through three states:

| Status | Description |
|--------|-------------|
| `running` | Team is active. Members have live sessions on the board. |
| `sleeping` | Team is paused. Sessions are killed and the board is paused, but everything is recoverable via wake. |
| `stopped` | Team is terminated. Members are marked stopped with timestamps. Can be resurrected or deleted. |

```
running ──sleep──▶ sleeping ──wake──▶ running
   │                                     ▲
   └──stop──▶ stopped ──resurrect────────┘
                  │
                  └──delete──▶ (removed)
```

### Slot-Based Members

Each team has a fixed set of **member slots** — one per agent defined in the team config. A slot is a persistent position, not a live session:

- When the team launches, each slot gets a `session_id` pointing to its live session.
- When the team sleeps, sessions are killed but slots keep their `session_id` — waking reuses it to preserve history.
- When the team stops, slots are marked `stopped` with a `stopped_at` timestamp.
- On resurrect, the slots that were active when the team stopped are relaunched with fresh sessions.

This means `session_id` gets updated on restart, but the slot identity (agent name, config) is stable.

---

## Team object

```json
{
  "id": 1,
  "name": "api-team",
  "status": "running",
  "working_dir": "/home/user/project",
  "is_worktree": 0,
  "created_at": "2025-03-11T10:00:00+00:00",
  "updated_at": "2025-03-11T10:30:00+00:00",
  "stopped_at": null,
  "config": { /* full team config JSON from launch */ },
  "members": [
    {
      "id": 1,
      "team_id": 1,
      "agent_name": "Backend Lead",
      "session_id": "550e8400-e29b-41d4-a716-446655440000",
      "status": "active",
      "created_at": "2025-03-11T10:00:00+00:00",
      "stopped_at": null,
      "agent_config": { /* per-agent config */ }
    }
  ]
}
```

### Member statuses

| Status | Description |
|--------|-------------|
| `active` | Member has a running session. |
| `sleeping` | Session killed, recoverable via team wake. |
| `stopped` | Terminated. `stopped_at` records when. |

---

## List teams

```
GET /api/teams/all
```

### Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `status` | string | (all) | Filter by status: `running`, `sleeping`, or `stopped`. |

### Response

```json
{
  "teams": [
    {
      "id": 1,
      "name": "api-team",
      "status": "running",
      "working_dir": "/home/user/project",
      "is_worktree": 0,
      "created_at": "2025-03-11T10:00:00+00:00",
      "updated_at": "2025-03-11T10:30:00+00:00",
      "member_count": 2,
      "active_count": 2
    }
  ]
}
```

---

## Get a team

```
GET /api/teams/detail/{name}
```

Returns one team with its full member list and config.

Returns `{"error": "team not found"}, 404` if no team matches the name.

---

## Resurrect a stopped team

```
POST /api/teams/detail/{name}/resurrect
```

Brings a stopped team back to life by relaunching the agents that were active when the team was stopped. Uses each member's stored `agent_config` to recreate sessions.

**How it works:**
1. Finds members whose `stopped_at` matches the team's `stopped_at` (the agents active at shutdown).
2. Relaunches each with a new session on the same board.
3. Updates member slots with new `session_id` and status `active`.
4. Sets team status back to `running`.

### Response

```json
{
  "ok": true,
  "board": "api-team",
  "agents": [
    {
      "name": "Backend Lead",
      "session_id": "new-uuid",
      "session_name": "claude-new-uuid"
    }
  ]
}
```

### Errors

| Status | Body | Cause |
|--------|------|-------|
| 404 | `{"error": "team not found"}` | No team with that name. |
| 400 | `{"error": "can only resurrect stopped teams (current status: ...)"}` | Team must be stopped to resurrect. |
| 400 | `{"error": "no members eligible for resurrection"}` | No member slots were active when the team stopped. |

---

## Delete a stopped team

```
DELETE /api/teams/detail/{name}
```

Permanently deletes a stopped team and all its member records.

### Response

```json
{"ok": true}
```

### Errors

| Status | Body | Cause |
|--------|------|-------|
| 404 | `{"error": "team not found"}` | No team with that name. |
| 400 | `{"error": "can only delete stopped teams"}` | Only stopped teams can be deleted. |

---

## Related endpoints

Teams are also managed through the session endpoints:

| Action | Endpoint | Description |
|--------|----------|-------------|
| Launch | `POST /api/sessions/launch-team` | Create and start a new team. See [Team Configuration](team-config.md). |
| Sleep | `POST /api/sessions/live/team/{boardName}/sleep` | Pause team, kill sessions, pause board. |
| Wake | `POST /api/sessions/live/team/{boardName}/wake` | Resume sleeping team, relaunch sessions. |
| Reset | `POST /api/sessions/live/team/{boardName}/reset` | Kill and relaunch all agents with original config. |
| Sleep status | `GET /api/sessions/live/team/{boardName}/sleep-status` | Check if team is sleeping. |

---

## Example: Full team lifecycle

```bash
# 1. Launch a team
curl -X POST http://localhost:8420/api/sessions/launch-team \
  -H "Content-Type: application/json" \
  -d '{
    "board_name": "api-team",
    "working_dir": "/home/user/project",
    "agents": [
      { "name": "Lead", "role": "orchestrator", "prompt": "Coordinate the team." },
      { "name": "Dev", "prompt": "Implement features." }
    ]
  }'

# 2. List running teams
curl http://localhost:8420/api/teams/all?status=running

# 3. Get team detail
curl http://localhost:8420/api/teams/detail/api-team

# 4. Sleep the team (pause without losing state)
curl -X POST http://localhost:8420/api/sessions/live/team/api-team/sleep

# 5. Wake it back up
curl -X POST http://localhost:8420/api/sessions/live/team/api-team/wake

# 6. Stop the team (terminate)
# (done via killing all sessions on the board)

# 7. Resurrect — bring back the agents that were running
curl -X POST http://localhost:8420/api/teams/detail/api-team/resurrect

# 8. Or delete the stopped team
curl -X DELETE http://localhost:8420/api/teams/detail/api-team
```


## Agent availability

**Team view** combines team metadata (agent count, directory and branch),
observed activity/readiness, assigned work, reminders, unassigned work and health
findings. It replaces the separate Agent availability and Team details menu
entries. User-changeable configuration belongs in **Team settings**; contextual
actions remain with the team/agent controls. The availability API and state
definitions below are unchanged by this layout.

`GET /api/board/{project}/status` returns a routing snapshot for a local
team/board. Agents can read it with `coral-board status` (current board) or
`coral-board status --board NAME`. The original
`GET /api/teams/detail/{name}/availability` remains a compatibility alias. Open **Team view** from the team's three-dot menu to view
it, including task titles and unassigned work. Use **Refresh** for a new snapshot.

The response contains `board`, compatibility `team`, UTC `observed_at`, `summary` counts, `agents`, and
`unassigned_tasks`. Each agent includes `name`, optional `session_id`,
`subscriber_id`, `agent_type`, `role`, `availability`, boolean `available`,
`reason`, and open `tasks`. Task entries contain `id`, `scope` (`personal` or
`board`), `title`, and `status`; IDs must be interpreted with their scope.

| Availability | Meaning |
|---|---|
| `available` | Confirmed idle, live, subscribed agent with no active or pending assigned work |
| `busy` | In-progress personal/board task, or reported runtime activity |
| `queued` | Has pending assigned work |
| `needs_input` | Waiting for user input or approval |
| `sleeping` | Session is asleep |
| `offline` | No observed local runtime/session |
| `unknown` | No confirmed idle signal, or remote state cannot be observed locally |
| `unavailable` | Terminal or no active board subscription |

Blocked and draft tasks are shown but do not by themselves reserve an idle agent.
Sleeping/offline/input states take precedence over task occupancy; inspect
`tasks` to see assignments in those states. Unassigned tasks are listed once at
team level. Completed/cancelled tasks are omitted. Runtime activity uses Coral's
session event state. For Codex, explicit transcript turn-start/completion events
also supply state when newer than hook events, including sessions with missing
hooks. The main session list, WebSocket updates, and individual session views
use this same detection. An explicit unfinished turn remains working even when
terminal output is quiet. Newer input/approval hooks take precedence. Absent idle evidence is
treated conservatively.

This read-only endpoint does not claim tasks or reserve agents. State can change
before routing; use the atomic task claim APIs to start work. The snapshot spans
runtime and task stores and is not a transaction across them. Responses disable
caching. An unknown/empty board returns empty arrays; storage or runtime discovery
failure returns HTTP 500 rather than reporting agents as free. Local registered
sessions and active board subscribers are included; stopped unsubscribed members
are not a historical team roster.


## Team working modes

Choose **Team settings** from a team's three-dot menu in the sidebar, then
use **Workflow instructions** to edit presets and **Team working mode** to select the active preset and extra guidance. In v1.3.6 and earlier, this entry is named
**Team working mode**; the Team view/Team settings consolidation is a later
UI change.
The setting is stored per board, not globally and not per worker. It supplies
instructions when a task is first claimed; it is not a repository isolation or
queue enforcement mechanism. See [Task queue flow](task-workflows.md) for the
engine-enforced lifecycle and permissions.

| Setting | Default | Effect and practical use |
|---|---|---|
| `mode: none` (None) | Selected | The shipped default adds no checkout convention. A team-local override can add instructions. Useful for research or teams that already specify repository conventions elsewhere. It does not remove base task instructions. |
| `mode: shared_checkout` (Shared checkout) | Off | Asks agents to coordinate file ownership, preserve teammates' changes and commit only their own work. Useful when everyone works in one checkout; agents must still coordinate overlapping edits. |
| `mode: worktrees` (Worktrees) | Off | Asks agents to use an isolated Git worktree/branch from an agreed base, reuse their task checkout, leave other checkouts untouched and publish the exact commit. Useful for parallel implementation; someone must still integrate and verify the combined result. |
| `dependency_guidance` (Include dependent-queue guidance) | `false` | Appends guidance to connect stages as separate dependent tasks with explicit prerequisites and named outputs. It does not create tasks or require every job to use Build/Test/Release. Operator/Orchestrator planning permissions still apply. |
| `custom_instructions` | Empty string | Appends team conventions after the mode and dependency guidance. Leading/trailing whitespace is trimmed; maximum 4096 UTF-8 bytes, not characters. Example: “Run make check before publishing a commit.” |
| `instructions` in the response | Generated | Read-only composed guidance: mode text, optional dependency guidance, then custom text, separated by newlines. A caller-provided value is ignored. |

The mode choices are mutually exclusive; dependency guidance and custom text
are independent. Selecting None with custom text still adds that custom text.
To add no team instructions, reset None to its shipped empty default, select it, disable the checkbox, and clear custom
text. Required outputs, dependency conditions, active-slot capacity and planning
permissions remain enforced regardless of these settings.

For a small documentation correction, `none` with no dependency guidance may be
sufficient. For two developers in one checkout, choose `shared_checkout` and
agree which files each owns. For parallel branches, choose `worktrees` and
specify the integration base and acceptance checks in custom instructions.
Enable dependency guidance when a separately assigned verifier needs a specific
published result. These are conventions, not automatic worktree creation,
branch merging, artifact validation or mandatory workflow stages.

Personal queues do not inherit a team board's working mode. Per-task additional
workflow instructions are separate: they are stored with the task at creation;
the team instructions are appended on its first claim. Changing a team's mode
does not launch agents or rewrite their original session prompts.

```text
GET /api/board/{project}/working-mode
PUT /api/board/{project}/working-mode
```

PUT replaces the setting:

```json
{
  "mode": "worktrees",
  "dependency_guidance": true,
  "custom_instructions": "Run make check before publishing a commit."
}
```

Both endpoints return these fields plus generated `instructions`. Mode values
are the built-in IDs `none`, `shared_checkout`, and `worktrees`, or an existing custom preset ID on this board. Omitted fields reset to defaults;
caller-supplied generated `instructions` are ignored. Invalid modes or oversized
custom instructions return HTTP 400 without changing the saved setting.
`coral-board status` also includes the current `working_mode`.

On a task's **first claim**, Coral snapshots the mode in `workflow.team_mode`
and appends its generated text to `workflow.instructions`, in the same
transaction as the claim. The CLI, claim API, current/detail responses, and task
dashboard therefore show the same instructions. Later setting changes affect
unclaimed tasks, not an existing claim. Reassignment/reclaim preserves the
original snapshot. A new retry task receives the setting in effect when it is
first claimed. Existing active and completed tasks are not retroactively changed.


### Editable workflow presets

Here, a **workflow** means a team instruction preset. It is separate from launch
workflows (`coral-board workflow`) and from task dependencies and release stages.
Users and agents may create or edit these presets. This permission does not grant
workers permission to create or reassign team tasks; those operations still
require Operator or a registered Orchestrator.

**New preset** creates a custom preset. Give it a name and instructions, then
save the preset. Select it as the working mode and save team settings to use it.
Editing a built-in saves an override on this team only. **Reset built-in** removes
that preset's override and restores the shipped instructions; it preserves
custom presets, the selected mode, extra team guidance, global settings, and all
historical task snapshots. Empty preset instructions are allowed. Edits to the
currently selected preset apply to subsequent first claims immediately, without
requiring the mode to be saved again. Existing claimed tasks retain their snapshot.

```text
GET  /api/board/{project}/working-mode/presets
POST /api/board/{project}/working-mode/presets
PUT  /api/board/{project}/working-mode/presets/{id}
POST /api/board/{project}/working-mode/presets/{id}/reset
```

The list response contains `presets` and `working_mode`. Each preset has `id`,
`name`, `builtin`, `default_instructions`, `instructions`, and `overridden`.
`default_instructions` is the shipped text for built-ins (empty for custom
presets); `instructions` is the current effective preset text. `overridden`
distinguishes an explicitly empty built-in override from the shipped default.
Create accepts `{ "id": "review", "name": "Review", "instructions": "Review changes." }`;
edit accepts `name` and `instructions` (a built-in keeps its shipped name).
Create returns HTTP 201, duplicates return 409, and invalid edits/reset requests
return 400. IDs match `[a-z][a-z0-9_-]{0,63}`. Names must be 1–80 UTF-8 bytes,
and instructions at most 4096 UTF-8 bytes, after trimming surrounding whitespace.
Edits replace the preset content; concurrent edits use the last saved value.
There is no delete operation. Reset supports built-ins only.

Agents can use the CLI, specifying `--board` outside a joined board:

```bash
coral-board working-mode --presets
coral-board working-mode --create review --name 'Review' --instructions-file review.txt
coral-board working-mode --edit review --name 'Careful review' --instructions 'Inspect changes and run relevant checks.'
coral-board working-mode --mode review --dependency-guidance=true
coral-board working-mode --custom-instructions ''
coral-board working-mode --edit worktrees --instructions 'Use the agreed branch base.'
coral-board working-mode --reset worktrees
```

CLI mode updates preserve omitted fields; explicit empty custom instructions
clear the field and `--dependency-guidance=false` disables the extra guidance.
Preset editing and active-mode selection are separate operations.

### Inspecting role and task prompts

Team settings displays **Orchestrator**, **Agent / worker**, and **Task defaults**.
The read-only API is `GET /api/settings/prompt-inspection?board={project}`.
Role entries include `system_default`, `action_default`, the current global
`override`, `effective_system`, `effective_action`, `scope`, and `applicability`.
The task entry includes `default_instructions`, `scope`, and `applicability`.

Role previews use the same builders as a new board session, without any
agent-specific base prompt. System and action fragments are separate launch
channels. A nonempty global `default_prompt_orchestrator` or
`default_prompt_worker` override replaces both respective role fragments. The
system preview includes the board introduction; the action preview resolves
`{board_name}`. These values are global and read-only in Team settings; editing a
workflow preset does not edit a role prompt or change an existing agent session.

The task default is stored at task creation, with any per-task additional
instructions. The effective team guidance is selected preset text, optional
dependency guidance, then extra team instructions. It is appended to the task on
first claim. A preview cannot include unknown per-task additions; inspect task
detail/current for the actual stored instructions of a particular task.


### Board health monitor setting

The **Global settings** section of Team settings exposes **Run global board
health monitor**. Unlike the working-mode
fields above, this is the **global** `board_health_monitor` user setting, stored
through `GET /api/settings` and `PUT /api/settings` as the strings `"true"` or
`"false"`. It is not a per-team convention and is not snapshotted into tasks.
The default is off (absent or not `"true"`). Saving it from one team's dialog
changes the global value visible from other teams' dialogs.

The server reads this switch at startup. A changed value takes effect on the
next server start; it does not start or stop the background monitor immediately.
When enabled at startup, the monitor scans board health every 10 minutes and
can report inactivity/reminder/escalation findings. It does not automatically
reassign tasks, accept completion or repair dependency graphs. Observed health
findings and ordinary board/task notifications are separate features.

Saving Team settings writes the per-board mode and the global health setting
through separate requests, not one atomic transaction. If saving reports an
error, the feedback identifies whether working mode, global health, or both
failed. Reopen the dialog to inspect the persisted values. The first-claim
snapshot rule applies only to working-mode guidance, not to the health switch.
