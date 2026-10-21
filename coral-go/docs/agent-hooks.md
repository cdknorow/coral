# Per-Agent Hooks Configuration

## Overview

Allow workflow steps and team agent configs to define custom Claude Code hooks (PreToolUse, PostToolUse, Stop, etc.) that get merged into the agent's settings.json at launch time.

## Problem

Today, Coral injects a fixed set of hooks (`coralHooks` in claude.go) into every Claude session. There's no way for workflow steps or team agent configs to define custom hooks. This means:
- A workflow step can't trigger a webhook on completion
- A team agent can't have a PostToolUse hook that validates output format
- There's no way to run custom scripts at agent lifecycle events (PreToolUse, Stop, etc.)

## Design Principles

1. **Extend the existing merge pipeline** -- hooks flow through the same `buildMergedSettings` -> temp settings.json path. No new injection mechanism.
2. **Additive by default** -- User-defined hooks are appended alongside Coral system hooks, never replacing them.
3. **Same schema as Claude Code** -- The hooks JSON format matches Claude Code's settings.json exactly. No Coral-specific abstraction layer.
4. **Step-level overrides workflow-level** -- Same merge pattern as agent config (capabilities, tools, MCP servers).

## Claude Code Hooks Format (Reference)

Claude Code settings.json supports these hook events:
- `PreToolUse` -- Before a tool executes (can block with exit code 2)
- `PostToolUse` -- After a tool executes
- `Stop` -- When the agent session stops
- `Notification` -- On notification events
- `SubagentStop` -- When a subagent completes

Each event contains an array of hook groups:

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [
          {"type": "command", "command": "echo 'file was modified'"}
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {"type": "command", "command": "curl -X POST https://hooks.example.com/done"}
        ]
      }
    ]
  }
}
```

## Schema Changes

### AgentStepConfig (workflow_runner.go)

Add a `hooks` field to AgentStepConfig:

```go
type AgentStepConfig struct {
    AgentType    string            `json:"agent_type,omitempty"`
    Model        string            `json:"model,omitempty"`
    Capabilities json.RawMessage   `json:"capabilities,omitempty"`
    Tools        []string          `json:"tools,omitempty"`
    MCPServers   json.RawMessage   `json:"mcpServers,omitempty"`
    Flags        []string          `json:"flags,omitempty"`
    Hooks        json.RawMessage   `json:"hooks,omitempty"`  // NEW
}
```

### LaunchParams (agent.go)

Add a `Hooks` field:

```go
type LaunchParams struct {
    // ... existing fields ...
    Hooks map[string]interface{} `json:"hooks,omitempty"`
}
```

### Team Agent Config (sessions DB)

Add a `hooks_json` TEXT column to the agent config stored in the DB, same pattern as `tools` and `mcp_servers`.

## Merge Order

The hooks merge follows a 5-level priority (all additive):

```
1. ~/.claude/settings.json            (user global)
2. {repo}/.claude/settings.json       (project)
3. {repo}/.claude/settings.local.json (project local)
4. Coral system hooks (coralHooks)    (always injected)
5. Per-agent hooks                    (workflow step / team agent config)
```

All levels are **appended**, not overwritten. This ensures:
- User hooks still fire
- Coral system hooks (task-sync, agentic-state, message-check) are always present
- Per-agent hooks layer on top

### Implementation in buildMergedSettings

Add a new parameter to `buildMergedSettings`:

```go
func buildMergedSettings(workingDir string, agentHooks map[string]interface{}) map[string]interface{} {
    // ... existing merge of global/project/local ...
    // ... existing Coral hooks injection ...

    // Append per-agent hooks (level 5)
    if agentHooks != nil {
        for event, groups := range agentHooks {
            if groupList, ok := groups.([]interface{}); ok {
                mergedHooks[event] = append(mergedHooks[event], groupList...)
            }
        }
    }

    merged["hooks"] = mergedHooks
    return merged
}
```

### Implementation in BuildLaunchCommand

Pass LaunchParams.Hooks through to buildMergedSettings:

```go
func (c *Claude) BuildLaunchCommand(params LaunchParams) string {
    merged := buildMergedSettings(params.WorkingDir, params.Hooks)
    // ... rest unchanged ...
}
```

### Implementation in workflow runner

In `executeAgentPrint` and `executeAgentInteractive`, unmarshal the AgentStepConfig.Hooks and pass to LaunchParams:

```go
var hooks map[string]interface{}
if cfg.Hooks != nil {
    json.Unmarshal(cfg.Hooks, &hooks)
}
launchParams.Hooks = hooks
```

### mergeAgentConfig update

The existing `mergeAgentConfig` function needs to handle Hooks the same way as other fields -- step-level **replaces** workflow default:

```go
if stepCfg.Hooks != nil {
    merged.Hooks = stepCfg.Hooks
}
```

**Design decision:** Step hooks **replace** (not merge with) default_agent hooks. Rationale: deep-merging hook arrays across two levels of agent config plus the 3 settings levels plus Coral hooks creates too many layers of complexity and makes debugging which hooks fire very difficult. If a step needs the default hooks plus its own, it should include both explicitly.

## Example: Workflow with Custom Hooks

```json
{
  "name": "email-triage",
  "default_agent": {
    "agent_type": "claude",
    "model": "claude-sonnet-4-6",
    "hooks": {
      "Stop": [{
        "hooks": [{"type": "command", "command": "curl -X POST $CORAL_WEBHOOK_URL -d '{\"workflow\": \"$CORAL_WORKFLOW_NAME\"}'"}]
      }]
    }
  },
  "steps": [
    {
      "name": "triage",
      "type": "agent",
      "prompt": "Triage the inbox and classify emails...",
      "agent": {
        "hooks": {
          "PostToolUse": [{
            "matcher": "Write",
            "hooks": [{"type": "command", "command": "echo \"Agent wrote a file: step-level hook fired\""}]
          }]
        }
      }
    }
  ]
}
```

In this example:
- The default_agent Stop hook fires for all agent steps (unless overridden)
- Step "triage" overrides hooks entirely -- gets PostToolUse on Write but NOT the default Stop hook
- Coral system hooks (task-sync, agentic-state, message-check) still fire on all steps regardless

## Example: Team Agent with Custom Hooks

```json
{
  "agent_type": "claude",
  "model": "claude-sonnet-4-6",
  "capabilities": {"allow": ["file_read", "file_write"]},
  "hooks": {
    "PreToolUse": [{
      "matcher": "Bash",
      "hooks": [{"type": "command", "command": "/usr/local/bin/validate-shell-command"}]
    }],
    "Stop": [{
      "hooks": [{"type": "command", "command": "coral-hook-notify-slack"}]
    }]
  }
}
```

## Validation Rules

Add to the API handler validation (routes/workflow.go and routes/sessions.go):

1. **hooks must be a valid JSON object** -- Keys must be known event names: PreToolUse, PostToolUse, Stop, Notification, SubagentStop
2. **Each event value must be an array of hook groups** -- Each group must have a `hooks` array with at least one entry
3. **Each hook entry must have `type: "command"` and a non-empty `command`** -- This is the only hook type Claude Code supports today
4. **matcher is optional** -- If present, must be a non-empty string (pipe-separated tool names)
5. **No shell injection validation** -- Hook commands are defined by the workflow author (trusted), same trust model as shell step commands
6. **Max hook groups per event: 10** -- Prevent accidental explosion
7. **Max total hooks across all events: 50** -- Safety cap

## Security Considerations

- **Hooks run with the agent's permissions** -- They execute in the same process context as the Claude CLI session. Same trust boundary as shell steps.
- **Coral system hooks cannot be overridden** -- The merge is additive. Per-agent hooks can't remove coralHooks entries. This ensures task-sync and agentic-state always function.
- **PreToolUse exit code 2 can block tools** -- A per-agent PreToolUse hook could block legitimate tool calls. This is by design (it's how Claude Code hooks work) but should be documented.
- **Non-interactive (--print) mode** -- Hooks behavior in --print mode must be verified before shipping. This is a prerequisite for the implementation. If hooks don't fire in --print mode, this feature only works for interactive agent steps.
- **Hook timeout** -- Claude Code applies a default 60-second timeout per hook execution. Use this default for v1; no custom timeout config needed.

## Gemini and Codex Hook Support

### The Problem

Gemini and Codex CLIs have **no native hooks system**. Unlike Claude Code which supports PreToolUse/PostToolUse/Stop/Notification events via settings.json, Gemini and Codex provide no mechanism to run custom commands at agent lifecycle events.

However, workflow authors expect a consistent hooks experience regardless of agent type. A workflow that notifies Slack on step completion shouldn't need different configuration for Claude vs Gemini steps.

### Approach: Coral-Managed Wrapper Hooks

For Gemini and Codex agents, Coral implements hooks at the **runner level** rather than delegating to the agent CLI. The runner wraps agent execution and fires hooks at the appropriate lifecycle points.

#### Supported Events by Agent Type

| Event | Claude | Gemini | Codex | Implementation |
|-------|--------|--------|-------|----------------|
| `PreToolUse` | Native (settings.json) | Not supported | Not supported | Claude-only; no equivalent for Gemini/Codex |
| `PostToolUse` | Native (settings.json) | Not supported | Not supported | Claude-only; no equivalent for Gemini/Codex |
| `Stop` | Native (settings.json) | **Coral-managed** | **Coral-managed** | Runner fires after agent process exits |
| `Notification` | Native (settings.json) | Not supported | Not supported | Claude-only |
| `SubagentStop` | Native (settings.json) | Not supported | Not supported | Claude-only |
| `StepComplete` | **Coral-managed** | **Coral-managed** | **Coral-managed** | NEW event: runner fires after any step completes |
| `StepFailed` | **Coral-managed** | **Coral-managed** | **Coral-managed** | NEW event: runner fires after any step fails |

#### Design Rationale

- **PreToolUse/PostToolUse** require deep integration with the agent's tool execution loop. There's no way to intercept individual tool calls from outside the Gemini/Codex process. These remain Claude-only.
- **Stop** is implementable at the runner level -- the runner knows when the agent process exits and can fire hooks at that point.
- **StepComplete/StepFailed** are new Coral-level events that work uniformly across all agent types and shell steps. These are the recommended cross-agent hook points.

#### Runner-Level Hook Execution

Add a `fireHooks` method to the workflow runner:

```go
// fireHooks executes hook commands for a given event.
// For Claude agents, native hooks handle PreToolUse/PostToolUse/Stop/Notification.
// For Gemini/Codex agents and Coral-level events, the runner executes hooks directly.
func (r *WorkflowRunner) fireHooks(ctx context.Context, hooks map[string]interface{}, event string, env []string) error {
    eventHooks, ok := hooks[event]
    if !ok {
        return nil
    }
    groups, ok := eventHooks.([]interface{})
    if !ok {
        return nil
    }
    for _, g := range groups {
        group, ok := g.(map[string]interface{})
        if !ok {
            continue
        }
        hookList, ok := group["hooks"].([]interface{})
        if !ok {
            continue
        }
        for _, h := range hookList {
            hook, ok := h.(map[string]interface{})
            if !ok {
                continue
            }
            command, _ := hook["command"].(string)
            if command == "" {
                continue
            }
            cmd := exec.CommandContext(ctx, "sh", "-c", command)
            cmd.Env = append(os.Environ(), env...)
            cmd.Run() // Best-effort, don't fail the step on hook error
        }
    }
    return nil
}
```

#### Hook Dispatch Logic

In the workflow runner's step execution methods:

```go
func (r *WorkflowRunner) executeAgentStep(ctx context.Context, step StepDef, ...) {
    cfg := mergeAgentConfig(defaultAgent, step.Agent)
    var hooks map[string]interface{}
    if cfg.Hooks != nil {
        json.Unmarshal(cfg.Hooks, &hooks)
    }

    // For Claude: pass hooks to LaunchParams (native handling)
    // For Gemini/Codex: runner manages Stop hooks
    if cfg.AgentType == "claude" {
        launchParams.Hooks = hooks
    }

    // ... execute agent ...

    // For Gemini/Codex: fire Stop hooks after agent exits
    if cfg.AgentType != "claude" && hooks != nil {
        r.fireHooks(ctx, hooks, "Stop", stepEnv)
    }

    // For ALL agent types: fire Coral-level events
    if err == nil {
        r.fireHooks(ctx, hooks, "StepComplete", stepEnv)
    } else {
        r.fireHooks(ctx, hooks, "StepFailed", stepEnv)
    }
}
```

#### Environment Variables Available to Hooks

All hooks (both native Claude and Coral-managed) have access to:

| Variable | Description |
|----------|-------------|
| `CORAL_WORKFLOW_NAME` | Workflow name |
| `CORAL_WORKFLOW_RUN_ID` | Run ID |
| `CORAL_STEP_NAME` | Current step name |
| `CORAL_STEP_INDEX` | Current step index (0-based) |
| `CORAL_STEP_STATUS` | Step status (completed/failed/killed) |
| `CORAL_STEP_EXIT_CODE` | Exit code (shell steps) or "" (agent steps) |
| `CORAL_STEP_DIR` | Step artifact directory path |
| `CORAL_RUN_DIR` | Run artifact directory path |

### Validation: Agent-Specific Hook Warnings

The API validation should warn (not reject) when hooks are configured for events that won't fire on non-Claude agents:

```go
func validateHooksForAgentType(hooks map[string]interface{}, agentType string) []string {
    var warnings []string
    if agentType != "claude" {
        claudeOnly := []string{"PreToolUse", "PostToolUse", "Notification", "SubagentStop"}
        for _, event := range claudeOnly {
            if _, ok := hooks[event]; ok {
                warnings = append(warnings, fmt.Sprintf(
                    "%s hooks are only supported for Claude agents (configured agent_type: %s)",
                    event, agentType))
            }
        }
    }
    return warnings
}
```

Warnings are returned in the API response but don't block workflow creation. This allows workflows to define hooks that work when the agent_type is later changed to Claude.

### Example: Cross-Agent Workflow with Hooks

```json
{
  "name": "multi-agent-pipeline",
  "steps": [
    {
      "name": "analyze",
      "type": "agent",
      "prompt": "Analyze the codebase...",
      "agent": {
        "agent_type": "gemini",
        "hooks": {
          "Stop": [{
            "hooks": [{"type": "command", "command": "echo 'Gemini analysis complete'"}]
          }],
          "StepComplete": [{
            "hooks": [{"type": "command", "command": "curl -X POST $WEBHOOK_URL"}]
          }]
        }
      }
    },
    {
      "name": "implement",
      "type": "agent",
      "prompt": "Implement the changes...",
      "agent": {
        "agent_type": "claude",
        "hooks": {
          "PostToolUse": [{
            "matcher": "Edit|Write",
            "hooks": [{"type": "command", "command": "echo 'file changed'"}]
          }],
          "Stop": [{
            "hooks": [{"type": "command", "command": "echo 'Claude implementation complete'"}]
          }],
          "StepComplete": [{
            "hooks": [{"type": "command", "command": "curl -X POST $WEBHOOK_URL"}]
          }]
        }
      }
    }
  ]
}
```

In this example:
- The Gemini step gets Stop (Coral-managed) and StepComplete (Coral-managed) hooks
- The Claude step gets PostToolUse (native), Stop (native), and StepComplete (Coral-managed) hooks
- Both steps fire the same webhook on completion via StepComplete

### Shell Step Hooks

Shell steps also support Coral-level hooks (StepComplete, StepFailed). Since shell steps have no agent lifecycle, only these two events apply:

```json
{
  "name": "run-tests",
  "type": "shell",
  "command": "go test ./...",
  "hooks": {
    "StepFailed": [{
      "hooks": [{"type": "command", "command": "curl -X POST $SLACK_WEBHOOK -d '{\"text\": \"Tests failed\"}'"}]
    }]
  }
}
```

This requires adding a `Hooks` field to StepDef (not just AgentStepConfig):

```go
type StepDef struct {
    // ... existing fields ...
    Hooks json.RawMessage `json:"hooks,omitempty"` // NEW: Coral-level hooks for any step type
}
```

For agent steps, `step.Hooks` provides Coral-level events (StepComplete/StepFailed) while `step.Agent.Hooks` provides agent-level events (Stop, PreToolUse, etc.). If only one hooks field is desired, consolidate into `step.Hooks` and let the runner route events appropriately based on step type and agent type.

**Recommendation:** Use a single `hooks` field on StepDef. The runner inspects event names and routes:
- Agent-native events (PreToolUse, PostToolUse, Stop, Notification, SubagentStop) -> passed to LaunchParams for Claude, Coral-managed for Gemini/Codex Stop only
- Coral events (StepComplete, StepFailed) -> always Coral-managed

## Decisions

| Question | Decision | Rationale |
|----------|----------|-----------|
| Merge or replace at step level? | **Replace** | Simpler to reason about. Avoids N-level deep merge complexity. |
| Hook timeout config? | **Skip for v1** | Use Claude Code's default 60s timeout. |
| --print mode hooks? | **Verify first** | Prerequisite: confirm Claude CLI fires hooks in --print mode before shipping. |
| Gemini/Codex hooks? | **Coral-managed wrapper** | Runner fires Stop/StepComplete/StepFailed. PreToolUse/PostToolUse are Claude-only. |
| Single or dual hooks field? | **Single field on StepDef** | Runner routes events by type. Simpler schema, consistent UX. |

## Files to Modify

| File | Change |
|------|--------|
| `internal/agent/agent.go` | Add `Hooks` field to LaunchParams |
| `internal/agent/claude.go` | Add `agentHooks` param to buildMergedSettings, merge at level 5 |
| `internal/background/workflow_runner.go` | Add `Hooks` to StepDef, add `fireHooks` method, route events by agent type, update mergeAgentConfig |
| `internal/server/routes/workflow.go` | Add hooks validation in createWorkflow/updateWorkflow, agent-type warnings |
| `internal/server/routes/sessions.go` | Accept hooks in launch API for team agents |
| `internal/store/sessions.go` | Add hooks_json column to session config storage |
