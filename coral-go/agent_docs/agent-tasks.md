# Personal Tasks: coral-agent CLI and API

`coral-agent task` manages the current agent session's work queue. It uses the
same workflow engine as `coral-board task`: dependencies, named artifacts,
success/failure outcomes, immutable completion records, retries, and default
workflow instructions. No board subscription is needed. Operator/Orchestrator
restrictions on shared planning do not prevent personal task creation. Personal
queues do not inherit team working modes. For the full lifecycle, see
[Task queue flow](task-workflows.md#from-assignment-to-result).

The personal CLI has no submit-review/release-review commands. Those board
review operations are described in [candidate review](task-workflows.md#candidate-review-without-holding-execution-capacity); shared engine storage
does not imply identical CLI surfaces.

Use personal tasks for work owned by one session. Use
[board tasks](board.md#tasks) for assignments spanning agents. Personal task
dependencies, parents, and retries must belong to the same session. Personal
tasks cannot be reassigned to another agent. Personal and team task IDs belong
to different stores; an identical number does not identify the same task.

Task queues track work; creating a task does not launch a new agent or execute
a build. For jobs that launch agents, see [Jobs](jobs.md). For declarative
workflow runs, see [Workflows](workflows.md).

## Agent working procedure

1. Run `coral-agent task claim`, or `claim <id>` for a specific ready task.
2. Read the body, `workflow.instructions`, required outputs, and upstream
   artifacts returned by the claim. Claim records the prerequisite results.
3. Use your judgment to accomplish the task and preserve the relevant evidence.
4. Complete with `--message`; use `--outcome failed` when the work is
   unsuccessful. If outputs are required, provide them with `--artifacts`
   using inline content or durable links.

Only one personal task can be in progress per session. A second claim is
rejected, even when the queue has other pending work. Claim order is priority
(`critical`, `high`, `medium`, `low`), then oldest task ID. A specific claim does
not bypass dependency, ownership, or active-task checks.

## CLI reference

Run from a Coral agent session. The CLI resolves the session UUID from the
current Coral tmux session or `CORAL_SESSION_NAME` (`<agent-type>-<UUID>`).
It uses `CORAL_URL` when set, otherwise `http://localhost:${CORAL_PORT:-8420}`.
Changing the working directory does not change task ownership. The API needs
Coral's session UUID, not a model provider's conversation/thread ID.

| Command | Behavior |
|---|---|
| `coral-agent task add "title" [options]` | Create a personal task |
| `coral-agent task list` | List this session's tasks |
| `coral-agent task claim [id]` | Claim the next or a specific ready task |
| `coral-agent task current` | Show the active task, instructions, and inputs |
| `coral-agent task detail <id>` | Print a task and its workflow as JSON |
| `coral-agent task edit <id> [options]` | Edit text, priority, or unstarted dependencies |
| `coral-agent task publish <id>` | Publish a draft created through the API |
| `coral-agent task complete <id> [options]` | Commit outcome and artifacts |
| `coral-agent task cancel <id> --message "reason"` | Cancel unfinished work |
| `coral-agent artifact upload <file>` | Store a durable, reachable artifact |

`add` supports `--body`, `--priority`, `--blocked-by` (JSON), `--outputs`
(comma-separated names), `--workflow`, `--stage`, `--workflow-instructions`,
`--parent`, and `--retry-of`. `edit` supports `--title`, `--body`, `--priority`,
and `--blocked-by`; use `'[]'` to clear dependencies. It does not change the
stored workflow configuration or reopen a finished task. There is currently no
`add --draft` CLI flag; create drafts through the API.

`complete` supports `--message`, `--outcome success|failed`, and
`--artifacts manifest.json`. The manifest is a JSON array, not a file to upload
as a binary. Each entry needs a unique `name` and either `uri` or `content`.
`revision`, `digest`, and `media_type` are optional. Do not use a local checkout
path, `/tmp` path, or `file://` link as `uri`; those files are not reachable by
the user or downstream agents. Use inline `content` for small reports or a
durable URL for larger artifacts. See the
[artifact contract](task-workflows.md#submit-outputs) for limits and provenance.

To make a local file reachable to users and downstream agents, upload it to
Coral first:

```sh
coral-agent artifact upload report.md
```

Use the returned `coral://artifacts/<digest>` URI in the manifest. The matching
`/api/artifacts/<digest>` URL is available to the browser.

An empty queue prints `No available tasks` and exits successfully. `current`
with no active task prints `No active task`. Unknown sessions and rejected
operations exit nonzero. Successful completion prints
`Finished Task #<id> (success|failed): <title>`.

### Example: Build → Test

The IDs below are illustrative; substitute the IDs returned by your commands.

```sh
coral-agent task add "Build candidate" --workflow delivery --stage Build \
  --outputs build --workflow-instructions "Record the exact source revision."
# Suppose the returned ID is 101.
coral-agent task add "Test candidate" --workflow delivery --stage Test \
  --outputs test_report \
  --blocked-by '[{"task_id":101,"condition":"success","required_artifacts":["build"]}]'
# Suppose the returned ID is 102.
coral-agent task claim 101

# After actually building the candidate, write build-artifacts.json, e.g.:
# [{"name":"build","uri":"artifact://builds/candidate-42","revision":"<tested revision>"}]
coral-agent task complete 101 --artifacts build-artifacts.json --message "Candidate built"
coral-agent task claim 102
coral-agent task current
# Run the tests against the claimed input, then publish actual evidence:
coral-agent task complete 102 --artifacts test-report.json --message "Checks passed"
```

If task 102 fails, finish it with `--outcome failed` and diagnostic evidence.
Create a new task with `--retry-of 102`. Completing that retry does not silently
satisfy dependencies pointing to 102. Rewire an unstarted consumer with
`coral-agent task edit <consumer-id> --blocked-by '[<retry-id>]'`; retain any
other prerequisites and artifact requirements in the replacement array.

## HTTP API

Base path: `/api/agent/tasks`. Localhost/auth behavior follows
[Authentication](auth.md). Every request must identify a known Coral session:
`session_id` is a query parameter for GET and a JSON field for other methods.
Responses are JSON; errors use `{"error":"..."}`.

| Method | Path | Success response |
|---|---|---|
| GET | `/api/agent/tasks?session_id=<UUID>` | `200 {"tasks":[...]}` |
| POST | `/api/agent/tasks` | `201` task object |
| GET | `/api/agent/tasks/{id}?session_id=<UUID>` | `200` task object |
| PATCH | `/api/agent/tasks/{id}` | `200` updated task object |
| POST | `/api/agent/tasks/claim` | `200` claimed task object |
| POST | `/api/agent/tasks/current` | `200` active task object |
| POST | `/api/agent/tasks/{id}/publish` | `200` published task object |
| POST | `/api/agent/tasks/{id}/complete` | `200` finished task object |
| POST | `/api/agent/tasks/{id}/cancel` | `200` cancelled task object |

### Create

```json
{
  "session_id": "<Coral session UUID>",
  "title": "Test candidate",
  "body": "Test the exact build revision supplied by task 101.",
  "priority": "high",
  "draft": false,
  "blocked_by": [
    {"task_id": 101, "condition": "success", "required_artifacts": ["build"]}
  ],
  "workflow": {
    "name": "delivery",
    "stage": "Test",
    "instructions": "Include commands and tested revision in the report.",
    "required_outputs": ["test_report"]
  }
}
```

`title` and `session_id` are required. `priority` defaults to `medium`.
`blocked_by` accepts an array of IDs (success conditions) or rule objects.
All prerequisites must be satisfied; supported conditions are `success`,
`failure`, and `termination`. Omit `board_id` for personal dependencies.

`workflow.parent_task_id` optionally groups tasks; it does not automatically
complete the parent. `workflow.retry_of` must reference a finished task in this
session. `workflow.instructions` is appended to persisted default instructions.
The server assigns `workflow.inputs`, `workflow.artifacts`, and
`workflow.outcome`; supplying these result fields on creation does not set them.

A draft stays `draft` until published. Otherwise a task is `pending` when its
prerequisites are satisfied, or `blocked` when they are not.

### Claim, inspect, and publish

`POST /api/agent/tasks/claim` accepts:

```json
{"session_id":"<Coral session UUID>","task_id":102}
```

Omit `task_id` to select the next ready task. `current`, `publish`, and `cancel`
need only `session_id` (cancel also accepts `message`). Claim returns the task
body and full `workflow`, including recorded `inputs`, and changes its status
to `in_progress`.

Task objects include `id`, `title`, `body` when present, `status`, `priority`,
`assigned_to`, timestamps, `session_id`, `workflow`, and dependency rules in
`blocked_by` when present. Optional result fields appear after completion.
Treat internal scope identifiers in workflow inputs as opaque.

### Edit

```json
{
  "session_id": "<Coral session UUID>",
  "body": "Test the replacement candidate.",
  "blocked_by": [{"task_id":103,"required_artifacts":["build"]}]
}
```

PATCH supports `title`, `body`, `priority`, and `blocked_by`. The dependency
array replaces the whole prerequisite set. Dependencies can change only while
the task is draft, pending, or blocked. Finished tasks cannot be edited. Use a
new task for changed output requirements or workflow instructions.

### Complete

```json
{
  "session_id": "<Coral session UUID>",
  "message": "All candidate checks passed.",
  "outcome": "success",
  "artifacts": [
    {"name":"test_report","content":"Commands and actual results...","revision":"<tested revision>"}
  ]
}
```

`outcome` defaults to `success`; the other accepted value is `failed`. Both
produce `status: "completed"`; read `workflow.outcome` for the verdict.
Required outputs are enforced for successful completion. Failure can publish
diagnostics without the successful-stage outputs. Repeated completion is
rejected, including an attempt to replace the artifacts or outcome.

Cancellation produces `status: "skipped"` and outcome `cancelled`. It satisfies
only `termination` conditions, with any required artifact checks still applied.
Completion/cancellation and downstream readiness commit atomically. Startup
repairs eligible blocked tasks left by older versions. Notifications are best
effort; readiness and claimability do not depend on terminal delivery.

### Errors

| Status | Meaning |
|---|---|
| `400` | Invalid task/dependency configuration, unavailable explicit claim, missing required artifact, or mutation of a finished task |
| `404` | Unknown session, task outside the caller's session, no next available task, or no active task |
| `409` | The session already has an active task |
| `500` | Storage/internal failure |

Distinguish an empty queue from an unknown session using the error text.
An empty next-task claim returns `No available tasks`; an empty current-task
read returns `No active task`. A claim by ID that is unavailable is rejected;
it does not fall back to claiming a different task.

## Migration and dashboard compatibility

Upgrade the server and `coral-agent` binary together. Existing personal task
IDs, ownership, timestamps, costs, history, and previously allocated ID ranges
are preserved. If an older version left several tasks in progress, finish or
cancel them before making another claim; migration does not silently finish them.

The dashboard API under `/api/sessions/live/{name}/tasks` remains available.
It retains numeric `completed` values and now includes `status`, `workflow`,
and `blocked_by`: pending `0`, completed `1`, in progress `2`, skipped `3`,
blocked `4`, draft `5`. Read `workflow.outcome` to distinguish failed and
successful completed work. See [session task endpoints](sessions.md#agent-tasks).

Dashboard mutations use the same engine: setting `completed: 1` cannot bypass
required outputs, and setting `completed: 0` cannot reopen a task. Use the
completion API or artifact form for tasks with required evidence. Finished
records cannot be deleted. Only unstarted, unreferenced tasks may be deleted.

## Validation

`bash tests/stress/run_agent_tasks.sh` from the repository root exercises the
real personal and board CLIs with mock agents, plus isolated API restart tests.
It covers single-active-task concurrency, dependencies, artifact hand-off,
failure branches, retry rewiring, session isolation, immutable results, startup
recovery, and artifact limits. Store/API tests cover legacy migration and
preservation of IDs. Browser tests cover personal artifact completion.
