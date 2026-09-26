# Hooks

Hooks let you run shell commands at specific points in an agent's lifecycle or workflow step execution. They're defined in Claude Code's settings.json format and work across team agents and workflow steps.

---

## Hook Format

A hooks object maps event names to arrays of hook groups. Each group has an optional `matcher` (tool name filter) and a `hooks` array of commands to run.

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [
          { "type": "command", "command": "echo 'file modified'" }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          { "type": "command", "command": "curl -X POST https://hooks.example.com/done" }
        ]
      }
    ],
    "StepComplete": [
      {
        "hooks": [
          { "type": "command", "command": "echo 'step finished successfully'" }
        ]
      }
    ]
  }
}
```

---

## Events

### Agent-Native Events (Claude only)

These events are handled natively by Claude Code via the settings.json hooks system. They fire inside the agent process and can intercept tool execution.

| Event | When it fires | Notes |
|-------|---------------|-------|
| `PreToolUse` | Before a tool executes | Exit code 2 blocks the tool call |
| `PostToolUse` | After a tool executes | Use `matcher` to filter by tool name |
| `Stop` | When the agent session stops | Also fired by Coral runner for Antigravity/Codex |
| `Notification` | On notification events | Claude-only |
| `SubagentStop` | When a subagent completes | Claude-only |

### Coral-Managed Events (all agent types + shell steps)

These events are managed by Coral's workflow runner. They work for all step types (shell, Claude, Antigravity, Codex) and fire after the step process exits.

| Event | When it fires | Notes |
|-------|---------------|-------|
| `StepComplete` | After a step finishes successfully | Works for shell and all agent types |
| `StepFailed` | After a step fails | Works for shell and all agent types |

---

## Event Support by Agent Type

| Event | Claude | Antigravity (agy) | Codex | Shell Steps |
|-------|--------|-------------------|-------|-------------|
| `PreToolUse` | Native | -- | -- | -- |
| `PostToolUse` | Native | -- | -- | -- |
| `Stop` | Native | Runner-managed | Runner-managed | -- |
| `Notification` | Native | -- | -- | -- |
| `SubagentStop` | Native | -- | -- | -- |
| `StepComplete` | Runner-managed | Runner-managed | Runner-managed | Runner-managed |
| `StepFailed` | Runner-managed | Runner-managed | Runner-managed | Runner-managed |

**Native** means the agent CLI handles the hook internally (full tool-level granularity).
**Runner-managed** means Coral's workflow runner executes the hook command after the process exits.

---

## Using Hooks in Workflows

Add a `hooks` field to any step definition. The runner routes events based on step type and agent type.

```json
{
  "name": "build-and-notify",
  "steps": [
    {
      "name": "test",
      "type": "shell",
      "command": "go test ./...",
      "hooks": {
        "StepFailed": [
          {
            "hooks": [
              { "type": "command", "command": "curl -X POST $SLACK_WEBHOOK -d '{\"text\": \"Tests failed\"}'" }
            ]
          }
        ]
      }
    },
    {
      "name": "fix",
      "type": "agent",
      "prompt": "Fix the failing tests.",
      "agent": { "agent_type": "claude" },
      "hooks": {
        "PostToolUse": [
          {
            "matcher": "Edit|Write",
            "hooks": [
              { "type": "command", "command": "echo 'file changed'" }
            ]
          }
        ],
        "StepComplete": [
          {
            "hooks": [
              { "type": "command", "command": "curl -X POST $SLACK_WEBHOOK -d '{\"text\": \"Fix applied\"}'" }
            ]
          }
        ]
      }
    }
  ]
}
```

### Environment Variables

All hooks have access to these environment variables:

| Variable | Description |
|----------|-------------|
| `CORAL_WORKFLOW_NAME` | Workflow name |
| `CORAL_WORKFLOW_RUN_ID` | Run ID |
| `CORAL_WORKFLOW_STEP` | Current step index (0-based) |
| `CORAL_WORKFLOW_STEP_DIR` | Step artifact directory path |
| `CORAL_WORKFLOW_RUN_DIR` | Run artifact directory path |
| `CORAL_WORKFLOW_REPO_PATH` | Workflow repo path |
| `CORAL_STEP_STATUS` | Step outcome: `completed` or `failed` (StepComplete/StepFailed only) |

---

## Using Hooks in Team Agents

Hooks can be set at the team defaults level (apply to all agents) or per-agent.

```json
{
  "name": "monitored-team",
  "working_dir": "/home/user/project",
  "defaults": {
    "hooks": {
      "Stop": [
        {
          "hooks": [
            { "type": "command", "command": "notify-team 'agent stopped'" }
          ]
        }
      ]
    }
  },
  "agents": [
    {
      "name": "Orchestrator",
      "role": "orchestrator",
      "hooks": {
        "PostToolUse": [
          {
            "matcher": "Bash",
            "hooks": [
              { "type": "command", "command": "log-command-usage" }
            ]
          }
        ]
      }
    },
    {
      "name": "Developer",
      "prompt": "Implement features."
    }
  ]
}
```

The Orchestrator gets both the global `Stop` hook and its own `PostToolUse` hook (merged). The Developer gets only the global `Stop` hook.

### Merge Rules

- **Defaults + agent**: Agent hooks are **appended** per event (both fire)
- **Workflow default_agent + step**: Step hooks **replace** the default (only step hooks fire)
- **Coral system hooks**: Always injected, cannot be overridden

---

## Using Hooks in the Launch API

Pass hooks when launching a single agent session:

```bash
curl -X POST http://localhost:8420/api/sessions/launch \
  -H "Content-Type: application/json" \
  -d '{
    "working_dir": "/home/user/project",
    "agent_type": "claude",
    "hooks": {
      "Stop": [
        {
          "hooks": [
            { "type": "command", "command": "echo agent stopped" }
          ]
        }
      ]
    }
  }'
```

---

## Coral System Hooks

Coral automatically injects these hooks into every Claude session. They cannot be removed or overridden by user-defined hooks.

| Hook | Event | Purpose |
|------|-------|---------|
| `coral-hook-task-sync` | PostToolUse (TaskCreate\|TaskUpdate) | Syncs task changes to Coral |
| `coral-hook-agentic-state` | PostToolUse, Stop, Notification | Updates agent state in Coral UI |
| `coral-hook-message-check` | PostToolUse | Checks for new board messages |

---

## Validation Rules

When creating or updating workflows, hooks are validated:

1. Event names must be one of: `PreToolUse`, `PostToolUse`, `Stop`, `Notification`, `SubagentStop`, `StepComplete`, `StepFailed`
2. Each event value must be an array of hook groups
3. Each hook group must contain a `hooks` array with at least one entry
4. Each hook entry must have `type: "command"` and a non-empty `command`
5. `matcher` is optional (pipe-separated tool names, e.g. `"Edit|Write"`)
6. Max 10 hook groups per event
7. Max 50 total hook groups across all events

### Agent-Type Warnings

Configuring Claude-only events (`PreToolUse`, `PostToolUse`, `Notification`, `SubagentStop`) on non-Claude agent steps produces a validation warning but does not block creation.

---

## Execution Details

- **Timeout**: Each hook command has a 60-second execution timeout (matches Claude Code's default)
- **Best-effort**: Hook failures are logged but do not fail the step
- **Hook commands** run in a `sh -c` subshell with the step's environment variables
- **Order**: Hooks within a group fire sequentially; multiple groups fire in array order
