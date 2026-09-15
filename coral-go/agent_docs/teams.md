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
