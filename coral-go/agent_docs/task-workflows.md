# Task workflows and completion artifacts

Team workflows use separate board tasks for each stage. A completed Build task
does not become a Test task. Each task keeps its owner, instructions, inputs and
completion evidence. Team tasks (`coral-board task`) and personal tasks
(`coral-agent task`) use the same workflow engine and lifecycle rules.

Personal tasks are scoped to the current agent session. They support the same
dependency conditions, required outputs, immutable artifacts, outcomes, retries,
and default instructions. One personal task may be in progress at a time.
Dependencies and retry/parent references must stay within that session; use a
team board for work spanning agents. Personal tasks cannot be reassigned.

Existing personal task IDs, session ownership, history, and timestamps are
preserved automatically. Previously active tasks remain active; finish them
before claiming another. Repeated completion and reopening a finished personal
task are now rejected, just as for board tasks. Create a new retry instead.
Finished task records cannot be deleted; unstarted tasks can be deleted only
when no other task references them.

For personal workflows, substitute `coral-agent task` in the examples below and
omit `--assignee`. Use `coral-agent task edit <id> --blocked-by '[123]'` to rewire an
unstarted task. `detail <id>` reads any task in the session; `current` reads the
active task. The dashboard displays workflow status and supports artifact
completion for personal tasks too.

See [Personal Tasks: CLI and API](agent-tasks.md) for the complete command and
HTTP reference, session identity, error responses, and migration behavior.

Every new personal or board task stores Coral's default workflow instructions. They explain
how to consume upstream evidence, use separate Build/Test/Release stages, report
failures, publish named outputs, and wait for dependency notifications instead of
polling. `--workflow-instructions` appends project-specific instructions. Claim,
current, detail, and the dashboard show the resulting instructions.

Teams can add [working-mode instructions](teams.md#team-working-modes) for shared
checkouts or worktrees, optional dependency guidance, and custom conventions.
These are snapshotted on first claim and retained through reassignment.

## Build → Test → Release

Create each task and substitute the returned IDs in the following commands:

```sh
coral-board task add "Build release candidate" --assignee "Developer" \
  --workflow "Ship candidate" --stage Build --outputs build \
  --body "Implement the agreed change and produce a versioned build." \
  --workflow-instructions "Use the repository's documented build command. Publish the exact Git revision and artifact digest."

# Suppose Build returned task #101.
coral-board task add "Verify candidate" --assignee "QA" \
  --workflow "Ship candidate" --stage Test --outputs test_report \
  --body "Test the exact build provided by task #101. Record the commands, result and tested revision." \
  --blocked-by '[{"task_id":101,"condition":"success","required_artifacts":["build"]}]'

# Suppose Test returned task #102. Release consumes both records.
coral-board task add "Release verified candidate" --assignee "Release Engineer" \
  --workflow "Ship candidate" --stage Release --outputs release_receipt \
  --body "Release the verified build after required operator approval. Check that test evidence identifies the same revision. Publish the release URL and version." \
  --blocked-by '[{"task_id":101,"required_artifacts":["build"]},{"task_id":102,"required_artifacts":["test_report"]}]'
```

Agents can use `coral-board task claim 101` to select a specific available task,
or `task claim` to take the next task. Blocked tasks and tasks assigned to another
agent cannot be claimed. One agent may hold one active task per board.

## Submit outputs

Save a JSON artifact manifest, for example `build-artifacts.json`:

```json
[
  {
    "name": "build",
    "uri": "https://artifacts.example/releases/candidate-42.tar.gz",
    "media_type": "application/gzip",
    "revision": "full-git-commit-or-tree-id",
    "digest": "sha256:artifact-content-digest"
  }
]
```

```sh
coral-board task complete 101 --message "Candidate built" --artifacts build-artifacts.json
coral-board task current
coral-board task detail 101
```

Each artifact needs a unique name and either a durable `uri` or inline `content`.
Local checkout paths, `/tmp` files, and `file://` links are not artifacts: users
and downstream agents cannot reach another agent's filesystem. Put small reports
in `content`, or publish large files to durable storage and include its URL.

Coral also provides durable local storage for agent-produced files:

```sh
result=$(coral-agent artifact upload report.md)
# Copy the returned `uri` (coral://artifacts/<digest>) into the task manifest.
```

The upload endpoint is `POST /api/agent/artifacts?session_id=...` with the file
bytes as the body and `X-Artifact-Name` plus optional `X-Artifact-Media-Type`
headers. Coral returns a digest, a `coral://` URI, and a browser URL. Retrieve
the artifact with `GET /api/artifacts/<digest>`. Uploads are limited to 64 MiB;
the content is immutable and addressed by its SHA-256 digest.
In Coral chat and task details, the returned `coral://` URI opens in the Files
preview panel; users do not need filesystem access to the agent's checkout.
Small reports can use `content`; large logs/builds should use durable external
storage. Coral stores the manifest and inline text in the board database; it
does not upload files referenced by a URI, fetch them, execute tests, or verify
the supplied revision/digest. Evidence is agent-reported. Use trusted verification
steps before releasing.

Limits: 32 artifacts per completion, 32 required output names per task and
32 required artifact names per dependency, 64 KiB of inline content per artifact,
4 KiB per URI, and 128 UTF-8 bytes per artifact name. Required output names must
be present before a successful completion is accepted. Failure reports may omit
successful-stage outputs and instead include diagnostic artifacts.

```sh
coral-board task complete 102 --outcome failed --message "Regression found" --artifacts failure-report.json
```

In the dashboard, create tasks with workflow/stage names, required output names,
dependency conditions, and additional workflow instructions. Complete a task with
an outcome and a named artifact, or attach a JSON manifest for multiple outputs.
Task details show instructions, dependency conditions, inputs, and results.

## Dependency conditions

- `success` (default): prerequisite finished successfully and required artifacts exist.
- `failure`: prerequisite finished with `outcome: failed` and required artifacts exist.
- `termination`: prerequisite succeeded, failed, or was cancelled; any required artifacts must still exist.

All dependencies must be satisfied. For branching alternatives, create separate
tasks with the corresponding conditions. Short form `--blocked-by '[101,102]'`
means success dependencies. Conditions can reference another local board with
`board_id`; they do not span remote Coral installations.

Cancellation does **not** satisfy success dependencies, including old shorthand
dependencies. A branch whose condition cannot become true stays blocked so the
operator can inspect, rewire, or cancel it. Completion/failure remains represented
by task status `completed` plus `workflow.outcome` (`success` or `failed`);
cancellation uses existing status `skipped` and outcome `cancelled`.
When cancellation leaves a non-`termination` downstream dependency unsatisfied,
Coral posts a `[Task #N stalled]` notice addressed to the Orchestrator. The notice
names the cancelled prerequisite and tells the Orchestrator to update or rewire
the downstream task; Coral does not silently rewrite that dependency.

## Retries and evidence history

Terminal results cannot be overwritten or reopened. Create a new task with
`--retry-of 101`, then explicitly update unstarted downstream dependencies to the
new task. Use the dashboard dependency editor or PATCH the task's `blocked_by`.
Existing dependency conditions/artifact requirements are preserved for retained
dependencies in the dashboard; newly selected prerequisites default to success.
Started tasks cannot change their dependencies. A new build therefore cannot
silently replace the build referenced by an old test or release.

`--parent ID` groups tasks under an existing task on the same board. It is a
grouping reference, not an automatic dependency or aggregate completion rule;
declare prerequisites explicitly. `--workflow` is a descriptive grouping name.

## API

`POST /api/board/{board}/tasks` accepts `workflow` with `name`, `stage`,
`instructions`, `required_outputs`, `parent_task_id`, and `retry_of`. Coral always
prepends its default instructions and ignores caller-supplied results/inputs.
`blocked_by` accepts task IDs or objects with `task_id`, optional `board_id`,
`condition`, and `required_artifacts`. Dependency depth is limited to 32 through
the HTTP API; cycles and duplicate prerequisites are rejected.

`POST /api/board/{board}/tasks/claim` accepts optional `task_id` along with
`subscriber_id`. `GET /api/board/{board}/tasks/{id}` reads any task and its evidence.
`POST /api/board/{board}/tasks/{id}/complete` accepts `subscriber_id`, optional
`message`, `outcome` (`success` by default or `failed`), and an `artifacts` array.
Completion and artifacts commit atomically. Claim records upstream task IDs,
outcomes, and artifacts; completed tasks retain those input records.

Downstream readiness commits in the same transaction as completion or
cancellation. Terminal notifications are best effort and do not control whether
a task can be claimed. On startup, Coral repairs eligible tasks left blocked by
older versions and processes queued readiness notifications.

## Integration coverage

From the repository root, run `bash tests/stress/run_agent_tasks.sh`. The harness
builds an isolated server and real CLIs, launches two `mock-agent` processes,
and drives their task commands without model calls. It covers solo tasks plus
Build → Test → Release hand-offs, terminal notifications, required artifacts,
default instructions, failed/cancelled dependencies, explicit retries, and
concurrent completion submissions with immutable results. The same command also
runs the API regressions for atomic readiness, abrupt restart recovery, legacy
blocked-task repair, and the 32/33-artifact boundary. These checks use a second
isolated server so restarting it does not disrupt the live mock agents.

Requires Go, tmux, curl, Python 3 and lsof. Override the default test port with
`CORAL_TEST_PORT=18474`; an occupied port causes the harness to stop. Failed runs
retain their temporary data and server log; `KEEP_TEST_DATA=1` retains successful
runs too. Mock sessions and the test server are cleaned up on exit.

To run just the API checks, build a dev binary and
run `CORAL_BIN=/path/to/coral python3 tests/stress/test_task_workflow_api.py` from
the repository root. This covers immediate readiness, restart recovery, and
required-artifact limits. Standalone runs retain the test database and server
log; runs through the stress harness follow its retention settings and include
the API checks in its final pass/fail summary.

## Why a completed prerequisite can still block a task

Readiness requires both the dependency condition and every named artifact. A
successful task that publishes `diagnostic` and `verification` does not satisfy
a dependency requiring `candidate` and `verification`. Refreshing the queue
cannot supply the missing evidence.

Dependency responses include `satisfied`, upstream `outcome`, `missing_artifacts`,
and `blocked_reason`. A claim with no available work includes up to 20
`blocked_tasks` for the caller (or unassigned tasks), with IDs, titles and readable
reasons; both task CLIs print them. Explicit blocked-task claims also include the
explanation. The dashboard marks unmet dependencies even if the upstream task
shows completed. These diagnostics do not change task state or bypass checks.

If the artifact name in the dependency is wrong, explicitly correct the
unstarted downstream task's dependency. If evidence really is missing, create a
new producer task and rewire the unstarted consumer to that result. Completed
artifacts remain immutable. Do not remove dependencies merely to force a claim.

When an upstream task declares `required_outputs`, Coral validates dependency
creation and edits against that contract. Requiring an undeclared output is
rejected before the consumer is created or changed. Legacy producers with no
declared output contract remain accepted for compatibility and are checked
against their actual immutable artifacts at readiness time.
