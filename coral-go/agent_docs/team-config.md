# Team Configuration (agent.json)

A JSON format for defining multi-agent teams with global defaults and per-agent overrides. Supports heterogeneous teams (mixed models/vendors), fine-grained permissions, custom hooks, and individual prompts.

Team config files are stored at `~/.coral/teams/<name>.json` and can also be launched inline via the API.

## Schema Overview

### Top-Level Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes* | Team name (used as `board_name` if not set separately). *At least one of `name` or `board_name` required. |
| `board_name` | string | No | Message board name. Defaults to `name`. |
| `working_dir` | string | Yes | Shared working directory for all agents. |
| `board_type` | string | No | Board implementation. Default: `"coral"`. |
| `defaults` | object | No | Global defaults applied to all agents (see below). |
| `agents` | array | Yes | Agent definitions. At least one required. |

### Defaults Object

All fields are optional. Values apply to every agent unless overridden at the agent level.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `agent_type` | string | `"claude"` | CLI to use: `"claude"`, `"codex"`, `"gemini"`, or `"pi"`. **In v1.0.8 any other value silently falls back to `"claude"`** — fixed in the repo (unreleased), where an unknown value is rejected. Omitting the field still means `claude`. |
| `model` | string | CLI default | Model identifier, vendor-specific (e.g. `"opus"`, `"sonnet"`, `"gemini-2.5-pro"`). |
| `permissions` | object | none | Coral-level capability permissions (see [Permissions](#permissions)). |
| `flags` | string[] | `[]` | Extra CLI flags passed to every agent. |
| `env` | object | `{}` | Extra environment variables (string key-value pairs). |
| `hooks` | object | `{}` | Agent hooks in Claude Code settings.json format (see [Hooks](#hooks)). |
| `max_turns` | integer | none | Max conversation turns before auto-stop. |
| `working_dir` | string | top-level | Per-agent working directory override base (rarely needed). |

### Agent Definition

Each entry in the `agents` array defines one agent.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Agent name. Must be unique within the team. |
| `role` | string | No | `"orchestrator"` for team lead, or any custom string for workers. |
| `prompt` | string | No | Agent-specific instructions/goal. |
| `agent_type` | string | No | Override default CLI. |
| `model` | string | No | Override default model. |
| `permissions` | object | No | Override default permissions entirely (see [Permissions](#permissions)). |
| `permissions_preset` | string | No | Use a named preset instead of inline permissions (see [Presets](#permission-presets)). |
| `flags` | string[] | No | Override default flags. |
| `env` | object | No | Merged with default env (agent wins on key conflict). |
| `hooks` | object | No | Merged with default hooks (agent entries appended per event). |
| `max_turns` | integer | No | Override default max_turns. |
| `working_dir` | string | No | Override working directory for this agent. |
| `cli_path` | string | No | Custom path to agent binary. |

## Override Rules

| Field | Behavior | Rationale |
|-------|----------|-----------|
| `permissions` / `permissions_preset` | **Replaces** global | Partial merge of allow/deny lists creates confusing interactions. |
| `flags` | **Replaces** global | Flag conflicts (e.g. `--verbose` vs `--quiet`) are unresolvable via merge. |
| `env` | **Merges** with global (agent wins) | Env vars are independent key-value pairs; merge is safe. |
| `hooks` | **Merges** with global (appended per event) | Team-wide hooks should always fire; agent adds extras. |
| All other fields | **Replaces** global | Scalar values, no merge needed. |

## Permissions

Permissions use Coral's abstract capability system. They are translated to each agent CLI's native format at launch time.

```json
{
  "permissions": {
    "allow": ["file_read", "file_write", "shell"],
    "deny": []
  }
}
```

### Capabilities

| Capability | Description |
|------------|-------------|
| `file_read` | Read files, search, glob |
| `file_write` | Write and edit files |
| `shell` | Run arbitrary shell commands |
| `shell:<pattern>` | Run specific shell commands (e.g. `shell:npm *`, `shell:grep *`) |
| `web_access` | Fetch URLs, web search |
| `git_write` | Push, commit, branch, merge, rebase |
| `agent_spawn` | Spawn sub-agents |
| `notebook` | Edit Jupyter notebooks |

All agents automatically receive `Bash(coral-board *)` permission for message board communication.

### Permission Presets

Built-in presets that can be referenced by name via `permissions_preset`:

| Preset | Allow | Deny |
|--------|-------|------|
| `full_access` | file_read, file_write, shell, git_write, agent_spawn, web_access, notebook | -- |
| `lead_dev` | file_read, file_write, shell, git_write, agent_spawn | -- |
| `devops` | file_read, file_write, shell, git_write | -- |
| `frontend_dev` | file_read, file_write, shell:npm *, shell:npx *, web_access | -- |
| `orchestrator` | file_read, agent_spawn, web_access | -- |
| `qa` | file_read | file_write, shell |
| `read_only` | file_read, web_access | -- |

If both `permissions` and `permissions_preset` are set on an agent, `permissions` wins.

### Agent-Native Translation

Capabilities are translated to each CLI's native permission format at launch:

| agent_type | Translation |
|------------|-------------|
| `claude` | Mapped to Claude Code tool permissions (e.g. `file_read` -> `Read`, `Glob`, `Grep`) |
| `codex` | Mapped to sandbox_mode, approval_policy flags |
| `gemini` | Mapped to approval_mode, sandbox flags |

## Hooks

Hooks run shell commands at agent lifecycle events. They follow the Claude Code settings.json format. See [Hooks](hooks.md) for the full reference.

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          { "type": "command", "command": "echo 'tool used'" }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          { "type": "command", "command": "notify-team 'agent stopped'" }
        ]
      }
    ]
  }
}
```

### Supported Events

**Agent-native (Claude only):** `PreToolUse`, `PostToolUse`, `Stop`, `Notification`, `SubagentStop` -- these are injected into Claude's settings.json and handled by the agent process. `PreToolUse` and `PostToolUse` provide tool-level granularity with `matcher` filtering.

**Coral-managed (all agents):** `StepComplete`, `StepFailed` -- these are fired by Coral's workflow runner after a step finishes. They work for Claude, Gemini, Codex, and shell steps.

**Cross-agent support for Stop:** For Gemini and Codex agents, `Stop` hooks are fired by Coral's runner after the agent process exits (since these CLIs have no native hooks system).

### Merge Behavior

Hooks defined in `defaults` apply to all agents. Per-agent hooks are **appended** per event (both fire). Coral system hooks (`coral-hook-task-sync`, `coral-hook-agentic-state`, `coral-hook-message-check`) are always injected into Claude sessions and cannot be overridden.

## Model Specification

The `model` field is vendor-specific and passed through to the agent CLI via `--model`:

| agent_type | Model examples |
|------------|---------------|
| `claude` | `opus`, `sonnet`, `haiku`, `claude-sonnet-4-6` |
| `gemini` | `gemini-2.5-pro`, `gemini-2.5-flash` |
| `codex` | `o3`, `o4-mini`, `gpt-4.1` |

If `model` is omitted, the agent CLI uses its own default.

## Validation Rules

At launch time, the server validates:

1. `name` or `board_name` must be non-empty
2. `working_dir` must be non-empty and exist on disk
3. `agents` must contain at least one entry
4. Each agent must have a non-empty `name`
5. Agent names must be unique within the team
6. `agent_type` must be a registered type or empty (defaults to `"claude"`)
7. `permissions_preset` must reference a known preset name
8. `permissions.allow` and `permissions.deny` entries must be valid capability strings
9. At most one agent should have `role: "orchestrator"` (warning, not error)

## API Endpoints


### Launch Team Inline

```
POST /api/sessions/launch-team
```

**Request Body:** Full team config JSON (same schema as a `.json` file).

### Save Team Config

```
POST /api/teams
```

**Request Body:** Full team config JSON. Saved to `~/.coral/teams/<name>.json`.

### List Saved Teams

```
GET /api/teams
```

### Get Team Config

```
GET /api/teams/{name}
```

## Examples

### Minimal

Two agents with all defaults (Claude, no special permissions):

```json
{
  "name": "quick-fix",
  "working_dir": "/home/user/project",
  "agents": [
    { "name": "Orchestrator", "role": "orchestrator", "prompt": "Fix the login bug" },
    { "name": "Developer", "prompt": "You fix bugs assigned by the Orchestrator" }
  ]
}
```

### With Defaults and Presets

```json
{
  "name": "api-team",
  "working_dir": "/home/user/api",
  "defaults": {
    "agent_type": "claude",
    "model": "sonnet",
    "permissions_preset": "lead_dev"
  },
  "agents": [
    {
      "name": "Orchestrator",
      "role": "orchestrator",
      "prompt": "Coordinate API development.",
      "model": "opus",
      "permissions_preset": "orchestrator"
    },
    {
      "name": "Backend Dev",
      "prompt": "Implement API endpoints and migrations."
    },
    {
      "name": "QA",
      "prompt": "Write tests and review code.",
      "permissions_preset": "qa"
    }
  ]
}
```

### Heterogeneous Team

Mixed-vendor team with full customization:

```json
{
  "name": "full-stack-team",
  "working_dir": "/home/user/webapp",
  "defaults": {
    "agent_type": "claude",
    "model": "sonnet",
    "permissions": {
      "allow": ["file_read", "file_write", "shell"],
      "deny": []
    },
    "env": {
      "NODE_ENV": "development",
      "LOG_LEVEL": "debug"
    }
  },
  "agents": [
    {
      "name": "Orchestrator",
      "role": "orchestrator",
      "prompt": "You coordinate the full-stack feature build.",
      "model": "opus",
      "permissions_preset": "orchestrator"
    },
    {
      "name": "Backend Dev",
      "prompt": "Implement API endpoints and database migrations.",
      "permissions_preset": "lead_dev"
    },
    {
      "name": "Frontend Dev",
      "prompt": "Build React components and pages.",
      "agent_type": "gemini",
      "model": "gemini-2.5-pro",
      "permissions_preset": "frontend_dev",
      "env": {
        "BROWSER": "none"
      }
    },
    {
      "name": "Security Reviewer",
      "prompt": "Audit all changes for OWASP top 10 vulnerabilities.",
      "permissions": {
        "allow": ["file_read", "web_access"],
        "deny": ["file_write", "shell", "git_write"]
      },
      "hooks": {
        "PostToolUse": [
          {
            "matcher": "Grep",
            "hooks": [
              { "type": "command", "command": "log-security-search" }
            ]
          }
        ]
      }
    }
  ]
}
```

### With Hooks

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
      "prompt": "Coordinate the team.",
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

The Orchestrator agent gets both the global `Stop` hook and its own `PostToolUse` hook (merged). The Developer agent gets only the global `Stop` hook.

## Resolution Algorithm

When launching an agent, the effective configuration is resolved as:

1. Start with `defaults` (or empty if no defaults block)
2. For each field the agent defines:
   - `permissions` / `permissions_preset`: resolve preset, then **replace** global
   - `flags`: **replace** global
   - `env`: **merge** with global (agent values win on key conflict)
   - `hooks`: **merge** with global (agent entries appended per event)
   - All other fields: **replace** global
3. Inject Coral system hooks (always added, never overridden)
4. Translate permissions to agent-native format
5. Build launch command
