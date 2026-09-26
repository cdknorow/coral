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

`GET /api/board/{project}/status` returns a routing snapshot for a local
team/board. Agents can read it with `coral-board status` (current board) or
`coral-board status --board NAME`. The original
`GET /api/teams/detail/{name}/availability` remains a compatibility alias. Open **Agent availability** from the team's three-dot menu to view
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

Choose **Team working mode** from the team's three-dot menu. Available modes:

- **None** (default): no additional mode instructions.
- **Shared checkout**: coordinate file ownership, preserve teammates' changes,
  and commit only the task's changes.
- **Worktrees**: use an isolated task worktree/branch and publish the exact commit
  and artifacts for downstream consumers.

An independent **dependent-queue guidance** checkbox adds instructions for
separate implementation/test/release tasks, explicit prerequisites, named
outputs, and consuming upstream evidence. Optional custom instructions append
team conventions (maximum 4096 UTF-8 bytes). To add no team instructions, select
None, disable dependency guidance, and leave custom instructions empty. Existing
Coral task workflow instructions still apply.

This setting supplies instructions; it does not create worktrees, branches,
dependencies, or artifacts automatically. It applies to board tasks. Personal
queues do not inherit a team board's setting.

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
are `none`, `shared_checkout`, and `worktrees`. Omitted fields reset to defaults;
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
