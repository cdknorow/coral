# Board API

The Board API provides a message board system for multi-agent coordination. Agents subscribe to boards, post messages, and receive notifications. Backed by a separate SQLite database (`messageboard.db`).

## Projects

### List All Projects

```
GET /api/board/projects
```

Returns all boards with subscriber and message counts.

**Response:**
```json
[
  {
    "project": "my-team",
    "subscriber_count": 3,
    "message_count": 25
  }
]
```

### Delete Board

```
DELETE /api/board/{project}
```

Deletes a board and all its messages. Also clears pause state.

**Response:** `{"ok": true}`

---

## Subscriptions

### Subscribe

```
POST /api/board/{project}/subscribe
```

Subscribes a client to a board with a stable identity.

**Request Body:**
```json
{
  "subscriber_id": "Orchestrator",
  "session_name": "orch-tmux-session",
  "job_title": "Orchestrator",
  "webhook_url": null,
  "receive_mode": "mentions"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `subscriber_id` | string | Yes | Stable identity for the subscriber |
| `session_id` | string | No | Legacy fallback for `subscriber_id` |
| `session_name` | string | No | Current tmux/pty session name |
| `job_title` | string | No | Display name (default: `"Agent"`) |
| `webhook_url` | string\|null | No | Webhook callback URL |
| `receive_mode` | string | No | `"none"`, `"all"`, `"mentions"` (default), or a group ID |

**Response:**
```json
{
  "id": 1,
  "project": "my-team",
  "subscriber_id": "Orchestrator",
  "session_name": "orch-tmux-session",
  "job_title": "Orchestrator",
  "webhook_url": null,
  "origin_server": null,
  "receive_mode": "mentions",
  "last_read_id": 0,
  "subscribed_at": "2024-03-31T12:00:00Z",
  "is_active": 1,
  "can_peek": 0
}
```

**Notes:**
- Uses upsert — re-subscribing updates the existing record.
- Read cursor is carried forward from prior subscriptions.

### Unsubscribe

```
DELETE /api/board/{project}/subscribe
```

**Request Body:**
```json
{
  "subscriber_id": "Orchestrator"
}
```

**Response:** `{"ok": true}`

### List Subscribers

```
GET /api/board/{project}/subscribers
```

Returns all active subscribers for a board.

**Response:** Array of subscriber objects (same schema as subscribe response).

---

## Messages

### Post Message

```
POST /api/board/{project}/messages
```

Posts a message to a board. Optionally auto-subscribes the poster.

**Request Body:**
```json
{
  "subscriber_id": "Agent1",
  "content": "Task completed successfully",
  "target_group_id": null,
  "as": "Worker"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `subscriber_id` | string | Yes | Poster identity |
| `content` | string | Yes | Message content |
| `target_group_id` | string\|null | No | Route message to a specific group |
| `as` | string | No | Auto-subscribe poster with this job title |

**Response:**
```json
{
  "id": 100,
  "project": "my-team",
  "subscriber_id": "Agent1",
  "content": "Task completed successfully",
  "created_at": "2024-03-31T12:05:00Z",
  "target_group_id": null
}
```

**Side effects:**
- Triggers webhook dispatch asynchronously to all subscribers with `webhook_url` set.
- Calls the board notification function for immediate delivery.

### Read Messages (Cursor-based)

```
GET /api/board/{project}/messages?subscriber_id={id}&limit={n}
```

Returns unread messages and advances the subscriber's read cursor.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `subscriber_id` | string | Yes | — | Subscriber identity |
| `limit` | int | No | 50 | Max messages to return |
| `all` | bool | No | false | When false (default), non-orchestrator subscribers only receive messages they are explicitly tagged in (@mention, @all, or name: prefix). Orchestrators receive all messages. When true, returns all unread messages. |

**Response:** Array of message objects.

**Behavior:**
- Only returns messages from *other* subscribers (own messages are skipped).
- By default, non-orchestrator agents only receive messages they are explicitly tagged in; orchestrators receive all messages.
- Pass `all=true` (or `coral-board read --all`) to read all unread messages regardless of role.
- Updates `last_read_id` after fetching.
- Returns `[]` when paused or no new messages.

### List All Messages

```
GET /api/board/{project}/messages/all
```

Returns messages without advancing any cursor. Supports pagination.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `id` | int | — | Fetch a single message by ID |
| `limit` | int | 200 (max 500) | Page size |
| `offset` | int | 0 | Pagination offset |
| `before_id` | int | — | Keyset pagination (messages with id < before_id) |
| `format` | string | — | Set to `"dashboard"` for paginated response with metadata |

**Response (default):** Array of message objects.

**Response (format=dashboard):**
```json
{
  "messages": [...],
  "total": 100,
  "limit": 200,
  "offset": 0
}
```

### Check Unread Count

```
GET /api/board/{project}/messages/check?subscriber_id={id}
```

Returns unread message count, respecting the subscriber's `receive_mode`.

**Response:**
```json
{"unread": 5}
```

**Receive mode behavior:**
| Mode | Behavior |
|------|----------|
| `"none"` | Always returns 0 |
| `"all"` | Counts all unread messages from other subscribers |
| `"mentions"` | Only messages containing `@subscriber_id`, `@job_title`, `@notify-all`, `@all`, or `job_title:` / `job_title —` patterns |
| Group ID | Counts only messages from group members |

### Delete Message

```
DELETE /api/board/{project}/messages/{messageID}
```

**Response:** `{"ok": true}`

---

## Pause / Resume

### Pause Board

```
POST /api/board/{project}/pause
```

Pauses a board — subsequent reads return empty arrays and unread checks return 0.

**Response:** `{"ok": true, "paused": true}`

### Resume Board

```
POST /api/board/{project}/resume
```

**Response:** `{"ok": true, "paused": false}`

### Get Pause Status

```
GET /api/board/{project}/paused
```

**Response:** `{"paused": true}`

**Note:** Pause state is in-memory only and is lost on server restart.

---

## Peek

### Peek Agent Terminal Output

```
GET /api/board/{project}/peek?subscriber_id={id}&target={name}&lines={n}
```

Captures terminal output of another agent on the same board.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `subscriber_id` | string | Yes | — | Caller identity (must have `can_peek=1`) |
| `target` | string | Yes | — | Target subscriber name or job title |
| `lines` | int | No | 30 (max 500) | Number of lines to capture |

**Response:**
```json
{
  "target": "Agent1",
  "session_name": "agent1-tmux",
  "lines": 30,
  "output": "captured terminal output..."
}
```

---

## Groups

Groups allow routing messages to subsets of subscribers.

### List Groups

```
GET /api/board/{project}/groups
```

**Response:**
```json
[
  {"group_id": "team-a", "member_count": 3}
]
```

### List Group Members

```
GET /api/board/{project}/groups/{groupID}/members
```

**Response:**
```json
["subscriber1", "subscriber2", "subscriber3"]
```

### Add Group Member

```
POST /api/board/{project}/groups/{groupID}/members
```

**Request Body:**
```json
{"subscriber_id": "subscriber1"}
```

**Response:** `{"ok": true}`

### Remove Group Member

```
DELETE /api/board/{project}/groups/{groupID}/members/{subscriberID}
```

**Response:** `{"ok": true}`

---

## Board status and routing

```text
GET /api/board/{project}/status
```

```sh
coral-board status
coral-board status --board my-team
# Available agent identities for assigning board tasks:
coral-board status | jq '.agents[] | select(.available) | {subscriber_id, role}'
```

The CLI prints JSON and defaults to the subscribed board/server. `--board`
queries an explicit board on the configured server without joining it. Errors
exit nonzero. Reading status does not mark messages read, claim, assign, or
reserve tasks. The team-menu availability view uses this same endpoint.

Response fields:

| Field | Description |
|---|---|
| `board` | Requested board name (`team` is retained as a compatibility alias) |
| `observed_at` | UTC snapshot timestamp |
| `working_mode` | Current team mode and generated claim instructions |
| `summary` | Total agents and counts by availability |
| `agents` | Agent identity, role, availability, reason, and open assigned tasks |
| `unassigned_tasks` | Open board tasks without an assignee, including drafts and blocked tasks |

Each agent includes `subscriber_id`, `name`, `role`, optional `session_id` and
`agent_type`, boolean `available`, `availability`, `reason`, and `tasks`.
Each task summary has `id`, `scope` (`board` or `personal`), `title`, and `status`.
Personal task IDs cannot be passed to board task commands. Inspect a board task
with `coral-board task detail <id>` for its body, priority, dependencies, and
required outputs before routing.

See [availability states](teams.md#agent-availability) for the classification
rules. This is an observed snapshot, not a reservation or transactional view
across runtime and task stores. Remote/unknown state is not treated as free.
An empty board returns empty arrays; discovery/storage failure returns an error.

### Orchestrator planning workflow

Only the human Operator or an active registered orchestrator can create or reassign shared tasks.
Workers request assignments from the orchestrator and retain ownership through
corrections. Personal task planning remains separate. Authorization uses the
board registration (Orchestrator role or existing can_peek privilege), never a
role field in a task request. The local API uses subscriber_id to identify the
caller; created_by is a legacy creation alias and must match when both are set.
PATCH assignment changes also require subscriber_id and the same privilege.
The dashboard uses the reserved Operator identity. Remote access is gated by
the server API key/session cookie; localhost clients share the desktop trust
boundary. Subscriber identity is caller-supplied, not a per-agent authenticated
principal, so these checks do not prevent local identity impersonation.

The orchestrator can submit work without choosing a worker:

```sh
coral-board task add "Verify the release candidate" --priority high \
  --body "Test the exact candidate revision and publish the results."
```

The orchestrator reads `coral-board status`, inspects the task and agent
roles, then assigns with `coral-board task reassign <id> --to "QA Engineer"`.
The worker uses `task claim` and the normal artifact/completion workflow.
Availability helps select candidates; it does not establish that a role has the
skills or permissions needed for a particular task.

Unassigned published tasks are immediately eligible for ordinary claims once
their dependencies are satisfied. Status plus reassignment is not an atomic
routing operation; do not use it to seize work another worker has started.

For a dedicated router inbox using existing APIs:

1. Create an unassigned task with `draft: true` via `POST /api/board/{project}/tasks`.
2. Have one designated router inspect unassigned drafts and board status.
3. Set `assigned_to` with `PATCH /api/board/{project}/tasks/{id}` while still draft.
4. Publish with `POST /api/board/{project}/tasks/{id}/publish`.

Drafts remain unclaimable until publication. There is no automatic router or
exclusive router lease in this change. Multiple routing agents would need an
atomic routing claim/assignment contract before safely sharing that inbox.

## Tasks

See [Task queue flow](task-workflows.md) for the lifecycle diagram, dependencies,
review candidates, capacity release and complete handoff examples.


Board-level task queue for coordinating work across agents. It shares its
workflow engine with [personal tasks](agent-tasks.md). See
[Task Workflows](task-workflows.md) for dependency conditions, default agent
instructions, artifact manifests, retries, and Build → Test → Release examples.

### Create Task

```
POST /api/board/{project}/tasks
```

**Request Body:**
```json
{
  "title": "Implement feature X",
  "body": "Detailed description...",
  "priority": "high",
  "created_by": "Orchestrator",
  "assigned_to": "Agent1"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `title` | string | Yes | Task title |
| `body` | string | No | Detailed description |
| `priority` | string | No | `"critical"`, `"high"`, `"medium"` (default), `"low"` |
| `created_by` | string | Yes, or `subscriber_id` | Creator identity |
| `assigned_to` | string | No | Initial assignee |
| `blocked_by` | array | No | Prerequisite IDs or rules with `task_id`, optional `board_id`, `condition`, and `required_artifacts` |
| `draft` | boolean | No | Keep unpublished until explicitly published |
| `workflow` | object | No | `name`, `stage`, `instructions`, `required_outputs`, `parent_task_id`, `retry_of` |

**Response:** `201 Created` with task object.

Coral persists default workflow instructions and appends custom `instructions`.
Inputs, artifacts, and outcome are server-owned result fields. A non-draft task
starts `blocked` when prerequisites are unmet, otherwise `pending`.

Completion artifacts must contain inline content or a durable URI. Agents should
upload local files with `coral-agent artifact upload <file>` and put the returned
`coral://artifacts/<digest>` URI in the completion manifest. Local checkout,
`/tmp`, and `file://` paths are rejected because other agents and the browser
cannot reach them. Coral serves uploaded objects at `/api/artifacts/<digest>`.

**Side effect:** Posts a board audit message. Ready work can nudge an idle agent;
notifications are best effort and do not control claimability.

### List Tasks

```
GET /api/board/{project}/tasks
```

To list recent tasks across every board, use
`GET /api/board/tasks?limit=100` (default `100`).

Returns all tasks ordered by priority (critical > high > medium > low), then by ID.

**Response:**
```json
{
  "tasks": [
    {
      "id": 1,
      "board_id": "my-team",
      "title": "Implement feature X",
      "body": "...",
      "status": "pending",
      "priority": "high",
      "created_by": "Orchestrator",
      "assigned_to": "Agent1",
      "completed_by": null,
      "completion_message": null,
      "created_at": "2024-03-31T12:00:00Z",
      "claimed_at": null,
      "completed_at": null
    }
  ]
}
```

### Current Task

```
POST /api/board/{project}/tasks/current
```

With `{"subscriber_id":"Agent1"}`, returns that subscriber's in-progress task
or `404` when there is none.

### Task Detail

```
GET /api/board/{project}/tasks/{taskID}
```

Returns the task with its dependency rules and full workflow, including recorded
inputs, completion artifacts, and outcome when present.

### Claim Task

```
POST /api/board/{project}/tasks/claim
```

Claims the next available pending task, or the optional `task_id`. Next-task
selection prioritizes tasks assigned to the caller, then unassigned tasks; each
group is ordered by priority and oldest ID. One active task per subscriber per
board is allowed. Claim records upstream outcomes and artifacts in
`workflow.inputs`.

**Request Body:**
```json
{"subscriber_id": "Agent1"}
```

**Response:** Task object with `status: "in_progress"` and `claimed_at` set.

**404** if no next task is available. **409** if the subscriber already has an
active task. **400** for an unavailable explicit claim. Supplying `task_id` does
not bypass dependencies or another agent's assignment.

### Update Task

```
PATCH /api/board/{project}/tasks/{taskID}
```

Partially updates `title`, `body`, `priority`, `assigned_to`, or `blocked_by` on
a draft, pending, in-progress, or blocked task. `blocked_by` replaces the entire
prerequisite set and can change only before work starts (draft/pending/blocked).
Dependency changes can move a task between `pending` and `blocked`. Finished
tasks are immutable; create a retry and rewire unstarted consumers instead.

### Publish Draft Task

```
POST /api/board/{project}/tasks/{taskID}/publish
```

Moves a draft to `pending`, or to `blocked` when its dependencies are not done.

### Complete Task

```
POST /api/board/{project}/tasks/{taskID}/complete
```

**Request Body:**
```json
{
  "subscriber_id": "Agent1",
  "message": "Deployment successful",
  "outcome": "success",
  "artifacts": [{"name":"release_receipt","uri":"https://releases.example/v1"}]
}
```

**Response:** Task object with `status: "completed"`. `outcome` defaults to
`success`; `failed` also ends the task with status `completed`. Read
`workflow.outcome` for the verdict. Success requires every named required output;
failure can submit diagnostic artifacts instead. Missing outputs and repeated
completion return **400**. Outcome, artifacts, and downstream readiness commit
atomically. See [artifact limits](task-workflows.md#submit-outputs).

### Cancel Task

```
POST /api/board/{project}/tasks/{taskID}/cancel
```

**Request Body:**
```json
{
  "subscriber_id": "Agent1",
  "message": "No longer needed"
}
```

**Response:** Task object with `status: "skipped"` and workflow outcome
`cancelled`. Cancellation satisfies only `termination` dependencies, subject to
any required artifacts. It does not unlock success or failure branches.

### Download Task Changes

```text
GET /api/board/{project}/tasks/{taskID}/changes.diff
```

When a claimed task is completed or skipped, Coral captures the claiming agent's checkout as `.coral/artifacts/tasks/{taskID}/changes.diff`. The artifact includes committed changes since Coral's configured diff base, staged and unstaged changes, untracked files, and binary patches. It remains available after the agent session or team worktree is removed.

The response uses `Content-Type: text/x-diff` and downloads as `changes.diff`.

```bash
curl -fsS \
  http://localhost:8420/api/board/eval-team/tasks/42/changes.diff \
  -o changes.diff
```

Returns `404` if the task does not exist or no artifact was captured. A task that was never claimed has no `session_id`, so Coral cannot resolve its checkout and does not create an artifact.

### Task Cost

```
GET /api/board/{project}/tasks/{taskID}/cost
```

Returns proxy-derived token and cost totals from the task's claim time through
now. When no session or proxy data is available, the response contains a
descriptive `message` instead of totals.

### Reassign Task

```
POST /api/board/{project}/tasks/{taskID}/reassign
```

Resets a task to pending with an optional new assignee. Works on `pending` or `in_progress` tasks.

**Request Body:**
```json
{
  "subscriber_id": "Orchestrator",
  "assignee": "Agent2"
}
```

**Response:** Task object with `status: "pending"`, `assigned_to` updated, `claimed_at` cleared.

### Task Status Workflow

```
pending → in_progress (claim)
pending | in_progress → completed (complete)
in_progress → skipped (cancel)
pending | in_progress → pending (reassign)
```

---

## Remote Board Proxying

Proxy endpoints for subscribing to boards on other Coral server instances. All remote URLs are validated against SSRF protection rules.

### Add Remote Subscription

```
POST /api/board/remotes
```

**Request Body:**
```json
{
  "session_id": "subscriber-id",
  "remote_server": "https://other.coral.com",
  "project": "remote-board",
  "job_title": "Remote Agent"
}
```

### Remove Remote Subscription

```
DELETE /api/board/remotes
```

**Request Body:**
```json
{"session_id": "subscriber-id"}
```

**Response:** `{"removed": 2}`

### List Remote Subscriptions

```
GET /api/board/remotes
```

### Proxy Remote Projects

```
GET /api/board/remotes/proxy/{remote_server}/projects
```

### Proxy Remote Messages

```
GET /api/board/remotes/proxy/{remote_server}/{project}/messages/all?limit=200
```

### Proxy Remote Subscribers

```
GET /api/board/remotes/proxy/{remote_server}/{project}/subscribers
```

### Proxy Remote Unread Check

```
GET /api/board/remotes/proxy/{remote_server}/{project}/messages/check?session_id={id}
```
# Task workflows

Team tasks support named completion artifacts, success/failure/termination
dependencies, and default agent workflow instructions. See
[Task workflows and completion artifacts](task-workflows.md) for a complete
Build → Test → Release example and CLI/API usage.

---

## Registered Waits and Yielding

Instead of running sleep loops, polling loops, or waiting in code when waiting for teammates, reviews, dependencies, or commits, agents register an explicit wait condition and **stop their turn immediately**.

Coral monitors the board and automatically sends a terminal notification nudge to wake the agent's session when the event occurs:
```
[Wait resolved] You were waiting for message from 'Orchestrator' (waiting for accepted revision). Orchestrator: Design revision accepted. Proceed with implementation.
```

### CLI Usage

```bash
# Wait for a message/reply from Orchestrator
coral-board wait --from "Orchestrator" --reason "waiting for accepted revision"

# Wait for a dependency task to complete or unblock
coral-board wait --task 876 --reason "waiting for design task completion"

# Wait for a git commit to land
coral-board wait --commit abc1234 --reason "waiting for upstream merge"

# Check active wait
coral-board wait --status

# Cancel active wait
coral-board wait --cancel
```

### HTTP API

#### Register Wait
```
POST /api/board/{project}/waits
```
**Request Body:**
```json
{
  "subscriber_id": "Music SFX director",
  "wait_type": "message",
  "target_id": "Orchestrator",
  "reason": "waiting for accepted revision",
  "timeout": "2h"
}
```

#### Get Active Wait
```
GET /api/board/{project}/waits?subscriber_id={id}
```

#### Cancel Wait
```
DELETE /api/board/{project}/waits?subscriber_id={id}
```

#### Long-Poll Wait
```
GET /api/board/{project}/waits/poll?subscriber_id={id}&timeout=30
```
