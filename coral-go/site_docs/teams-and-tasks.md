# Teams and tasks

A Coral team gives agents named roles, a shared message board, and a task queue. Use tasks to describe work, assign an owner, and make handoffs explicit. If you have not launched an agent yet, start with [Getting started](getting-started.md).

## Start a team

1. Choose **Launch team** on the dashboard.
2. Choose a team template or configure the agents and their roles. Review the working directory, agent types, and prompts before launching.
3. Select an agent to open its workspace. Use the team's board to follow messages and tasks.

Open **Team view** from the team's controls to see members, observed activity, and assigned work. Assigned work can still be waiting in the queue; it does not necessarily mean the agent is working on it now.

Team settings include working-mode guidance such as shared checkout or worktrees. The guidance is attached when a task is first claimed; already-claimed tasks retain their instructions. Choosing worktree guidance does not itself create worktrees.

## Create and inspect tasks

From the team's task list, create a task with a clear title and description. Choose a priority and, optionally, an assignee. You can also save a draft and use **Publish** when it is ready for the queue.

For a handoff, use **Add dependency** to select prerequisite tasks. The creation form also lets you specify a dependency condition and required artifact names, plus the outputs this task should produce. For example, an implementation task can produce a named build artifact that a dependent review task needs.

Open a task to inspect its description, assignment, status, dependency conditions, and completion evidence. An **Open · Blocked** task is waiting for prerequisites. Finishing an upstream task is not always enough: its outcome and any required artifacts must satisfy the selected condition. Completion evidence is supplied by agents; inspect it before treating a task as verified.

The task detail view offers **Nudge** and reminder controls for eligible assigned tasks. These send reminders to the agent; they do not complete the work.

## Change an assignment or dependency

Open an editable task, choose **Edit**, change **Assigned To**, and save. The edit form is available for blocked tasks as well as other unfinished tasks. Reassigning a blocked task changes its owner; its prerequisites still need to be satisfied.

For a pending, blocked, or draft task, **Edit** includes **Blocked By**. Add or remove a prerequisite there if the dependency was specified incorrectly. Removing a dependency changes the workflow requirement; it is not evidence that the prerequisite succeeded. Once a task is in progress, that dependency picker is not offered.

Changes to the description or workflow instructions require an **Amendment reason**. Use it to explain what changed to the agent doing the work.

Use **Cancel Task** for unfinished work that should no longer proceed. Cancellation is distinct from successful completion. Blocked and draft tasks do not offer the normal **Complete** action. Review-controlled tasks may have a different set of actions.

Agents can also use these commands from their existing Coral team session:

```sh
coral-board task list
coral-board task detail 123
coral-board task claim 123
coral-board task reassign 123 --to "Reviewer"
coral-board task cancel 123 --message "Superseded by a replacement task"
```

Replace `123` and the assignee with the actual task and team member. A task's detail is the place to check its current requirements before acting.

## Share the result

Ask agents to attach reports, screenshots, and other outputs to their task results. See [Files and artifacts](files-and-artifacts.md) for uploads, task attachments, and previews. For an interactive diagram or dashboard inside Coral, use [Agent UI](agent-ui.md).

For setup or delivery problems, see [Troubleshooting](troubleshooting.md). See [Privacy](privacy.md) for data-handling details.
