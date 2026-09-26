# Task Queue Behavior

Updated: 2026-09-26

This is the implemented queue contract. Use the [personal CLI/API reference](../../agent_docs/agent-tasks.md), [board API reference](../../agent_docs/board.md#tasks), and [workflow guide](../../agent_docs/task-workflows.md) for commands and request examples.

## Scope and ownership

`coral-board task` coordinates agents on a team board. `coral-agent task` manages one session's personal queue without a board subscription. Both use the same workflow engine, with separate stores and ID namespaces.

Board tasks support assignment and dependencies on other local boards. Personal tasks cannot be reassigned, and dependencies, parent references, and retries must stay within the same session. Creating a task tracks work; it does not launch an agent or execute a pipeline.

## Claiming and inspection

- `task claim` selects ready work; `task claim <id>` selects a specific available task.
- Each subscriber may have one active task per board. Each personal session may have one active personal task. Another claim returns HTTP 409, including an explicit claim.
- Board selection prefers caller-assigned tasks before unassigned tasks. Within each group, priority is critical, high, medium, low, then oldest ID. Personal selection uses the same priority order and oldest ID.
- Only pending, eligible tasks can be claimed. Explicit claims cannot bypass dependencies or ownership; unavailable explicit claims return 400. An empty next-task queue returns 404.
- Claim is atomic and records upstream task IDs, outcomes, and artifacts in `workflow.inputs`. Concurrent callers cannot both claim the same task.
- `task current` reads the active task; `task detail <id>` reads a task and its workflow evidence. Claim/current output includes instructions and input evidence.

## Lifecycle and dependencies

| Status | Meaning |
|---|---|
| `draft` | Unpublished; cannot be claimed |
| `blocked` | Published but prerequisites are unmet |
| `pending` | Ready to claim |
| `in_progress` | Claimed |
| `completed` | Finished; `workflow.outcome` is `success` or `failed` |
| `skipped` | Cancelled; `workflow.outcome` is `cancelled` |

Publishing a draft makes it pending or blocked according to its prerequisites. All dependency rules must be satisfied:

- `success` (default): successful completion, plus any required artifact names.
- `failure`: failed completion, plus any required artifact names.
- `termination`: success, failure, or cancellation, plus any required artifact names.

Short-form dependency IDs mean success. Cancellation does not satisfy success or failure. A branch whose condition cannot become true remains blocked until an operator rewires or cancels it. Cycles and duplicate prerequisites are rejected; HTTP dependency depth is limited to 32.

Dependency edits replace the prerequisite set and are allowed only for draft, pending, or blocked tasks. Started tasks retain their input contract. Text and priority can still be edited during work. Finished tasks cannot be edited, reopened, or completed again.

## Workflow instructions and outputs

Each new task persists default instructions explaining how to claim one task, consume upstream evidence, use separate Build/Test/Release stages, report honest outcomes, publish named outputs, and wait for readiness notifications. Custom workflow instructions are appended. Agents see these instructions on claim, current/detail, and in the dashboard.

Use a separate task for each stage; do not mutate a completed Build into Test. `workflow.name`, `stage`, and `parent_task_id` organize work. A parent reference is not a dependency or automatic completion rule.

Completion accepts an outcome, optional message, and named artifacts. Each artifact has a unique name and either a URI or inline content; media type, revision, and digest are optional. Successful completion requires all declared outputs. Failure may instead publish diagnostic evidence. Coral stores evidence, but does not upload/fetch URI targets, run verification, or validate supplied revision/digest claims.

Limits are 32 artifacts per completion, 32 required output names per task, 32 required artifact names per dependency, 128 UTF-8 bytes per name, 4 KiB per URI, and 64 KiB inline content per artifact.

Create a new task with `retry_of` pointing to a terminal task to retry work. Explicitly reconnect unstarted consumers to the new task, retaining other prerequisite conditions and artifact requirements. Old results and claimed inputs remain intact.

## Atomic readiness and recovery

Outcome, artifacts, and newly satisfied downstream state changes commit in the same SQLite transaction. Cancellation uses the same transactional readiness path. A downstream task can be claimed immediately after its prerequisite completion succeeds; terminal notification delivery is not required.

Readiness notifications are queued persistently. Startup also repairs eligible tasks left blocked by older versions and processes queued readiness notices. Notifications remain best effort: there is no exactly-once or guaranteed terminal-delivery contract. Agents should inspect current/available work when resuming rather than treating a notification as ownership of a task.

## Notifications and audit

Board task lifecycle operations post audit messages under `Coral Task Queue`. These messages remain visible in board reads but are excluded from unread-message counts to avoid redundant board notifications.

Ready assigned board work nudges an idle assignee. Ready unassigned work can nudge an idle subscriber; the orchestrator is excluded from the unassigned-worker pool. Busy assignees are not interrupted by creation nudges. On board completion, the next-task check can nudge the completing agent if more eligible work exists.

Dependency readiness also triggers notifications for personal and board queues. Personal dashboard creation nudges are opt-in (`notify: true`) and apply only to newly created pending tasks. Personal queues do not promise every board-specific notification behavior.

Nudges direct agents to `coral-board task claim` or `coral-agent task claim`. They are a prompt to inspect the queue, not a reservation; another eligible agent may have claimed board work before the recipient acts.

## Personal task migration and compatibility

Legacy personal task IDs, session ownership, timestamps, history, costs, and previously allocated ID ranges are preserved. Existing active tasks remain active; when an older session has multiple active tasks, finish or cancel them before claiming more.

Dashboard/history responses retain `completed` codes: pending 0, completed 1, active 2, skipped 3, blocked 4, draft 5. They also expose workflow/status information. Dashboard and hook mutations use the shared lifecycle rules: completing cannot bypass required outputs, resetting to 0 cannot reopen work, and finished records cannot be deleted. Only unstarted, unreferenced personal tasks may be deleted. Legacy display sorting does not change claim priority.

## Implementation and validation

- Shared engine: `internal/board/store.go`, `internal/board/task_workflow.go`.
- Personal migration/projection: `internal/store/task_workflows.go`.
- HTTP: `internal/server/routes/board.go`, `internal/server/routes/agent_tasks.go`, and session compatibility handlers.
- CLI: `cmd/coral-board/main.go`, `cmd/coral-agent/main.go`.

From the repository root, run `bash tests/stress/run_agent_tasks.sh`. It builds an isolated server and CLIs, uses mock agents, and checks personal/team dependencies, artifacts, failures, retries, session isolation, and notifications. Its isolated API regressions cover atomic readiness, abrupt restart recovery, legacy blocked-task repair, and the 32/33-artifact boundary. See the [integration guide](../../agent_docs/task-workflows.md#integration-coverage) for prerequisites, port overrides, and retained logs.

Store and route tests cover migration and lifecycle enforcement. Frontend tests exercise artifact completion and dependency visibility. These checks validate queue behavior without requiring model calls or restarting the live server.
