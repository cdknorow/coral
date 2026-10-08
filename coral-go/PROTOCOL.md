# Coral Agent Protocol

You are operating inside **Coral**, a multi-agent orchestration system.

## Orchestrator: Task Management

The orchestrator assigns work to agents using the `coral-board task` API. Tasks auto-notify the assigned agent — no separate `coral-board post` is needed.

### Creating and Assigning Tasks

```bash
coral-board task add "Task title" \
  --assignee "Agent Name" \
  --priority high \
  --body "Detailed instructions for the agent"
```

- `--assignee` auto-assigns the task and sends an @mention notification to that agent.
- `--priority` sets urgency: `critical`, `high`, `medium` (default), `low`.
- `--body` provides detailed instructions the agent will see when they view the task.

### Task Lifecycle

| Command | Description |
|---|---|
| `coral-board task add "title" --assignee "Agent"` | Create and assign a task |
| `coral-board task list` | List all tasks with status |
| `coral-board task claim` | Agent claims the next unassigned task |
| `coral-board task complete <id> --message "note"` | Mark a task as done |
| `coral-board task cancel <id> --message "reason"` | Cancel a stuck or unnecessary task |
| `coral-board task reassign <id> --to "Agent"` | Reassign a task (omit `--to` to make it unassigned) |

### Handling Stuck Tasks

If an agent claims a task but doesn't work on it (or abandons it), the orchestrator can:

1. **Reassign** to a specific agent: `coral-board task reassign 15 --to "Frontend Dev"`
2. **Release** back to the pool: `coral-board task reassign 15` (no `--to`, becomes unassigned)
3. **Cancel** if no longer needed: `coral-board task cancel 15 --message "replaced by #17"`

### Breaking Down Large Tasks

Large tasks should be broken into smaller, sequential parts assigned to the same agent. This gives the orchestrator visibility into progress and lets agents work in focused increments.

**Example:** Instead of one monolithic task:
```bash
coral-board task add "Implement entire workflow engine" --assignee "Lead Developer" --priority high --body "..."
```

Break it into sequential steps:
```bash
coral-board task add "Workflow Runner: step directory setup and env vars" \
  --assignee "Lead Developer" --priority high \
  --body "Create workflow_runner.go with directory creation and environment variable injection. See spec §Execution Engine."

coral-board task add "Workflow Runner: shell step execution" \
  --assignee "Lead Developer" --priority high \
  --body "Add shell step execution with stdout/stderr capture, exit codes, and process groups. Builds on previous task."

coral-board task add "Workflow Runner: agent step execution and kill" \
  --assignee "Lead Developer" --priority high \
  --body "Add agent step execution via FireOneshot, concurrency pool, and kill semantics. Builds on previous tasks."
```

**Guidelines:**
- Each sub-task should be completable independently and produce working code.
- Later tasks can reference earlier ones ("Builds on previous task").
- The agent claims and completes them in order — `task claim` returns assigned tasks by priority then ID.
- This gives the orchestrator natural checkpoints to review progress and adjust direction.

### Best Practices

1. **Use `--assignee` instead of posting separately.** The task API handles notifications automatically.
2. **Use `--body` for detailed instructions.** Include file paths, spec references, and acceptance criteria.
3. **Use `coral-board peek "Agent Name"`** to check on an agent's progress without interrupting them.
4. **Break large tasks into sequential sub-tasks.** Assign all parts to the same agent for incremental progress and review checkpoints.
5. **Parallelize independent tasks.** Create multiple tasks at once for different agents when work is independent.
6. **Wait for completion notifications.** Don't poll `coral-board read` — Coral sends notifications when agents post or complete tasks.

## Operator Commands

| Action | Command |
|---|---|
| Launch coral | `launch-coral <worktree-dir>` |
| Open dashboard | `coral` |
| Attach to tmux | `tmux attach -t claude-agent-1` |
| Switch window | `Ctrl+b n` (next) / `Ctrl+b p` (previous) |
| Detach tmux | `Ctrl+b d` |

---

## Attribution

When creating git commits, include this co-author line in the commit message:

```
Co-Authored-By: Coral <noreply@coral.com>
```

When creating pull requests, include the following line at the end of the PR description:

```
🤖 Generated with [Coral](https://github.com/cdknorow/coral)
```
