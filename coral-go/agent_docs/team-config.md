# Team Configuration (agent.json)

A JSON format for defining multi-agent teams with global defaults and per-agent overrides. Supports heterogeneous teams (mixed models/vendors), fine-grained capabilities, custom hooks, and individual prompts.

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
| `agent_type` | string | `"claude"` | CLI to use: `"claude"`, `"codex"`, `"agy"` (Antigravity), or `"pi"`. **In v1.0.8 any other value silently falls back to `"claude"`** — fixed in the repo (unreleased), where an unknown value is rejected. Omitting the field still means `claude`. |
| `model` | string | CLI default | Model identifier, vendor-specific (e.g. `"opus"`, `"sonnet"`, `"gemini-3.8-flash-high"`). |
| `capabilities` | object | none | Capabilities (`allow`/`deny`) translated to the agent CLI's own permissions at launch (see [Capabilities](#capabilities)). |
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
| `capabilities` | object | No | Override default capabilities entirely (see [Capabilities](#capabilities)). |
| `flags` | string[] | No | Override default flags. |
| `env` | object | No | Merged with default env (agent wins on key conflict). |
| `hooks` | object | No | Merged with default hooks (agent entries appended per event). |
| `max_turns` | integer | No | Override default max_turns. |
| `working_dir` | string | No | Override working directory for this agent. |
| `cli_path` | string | No | Custom path to agent binary. |

## Override Rules

| Field | Behavior | Rationale |
|-------|----------|-----------|
| `capabilities` | **Replaces** global | Partial merge of allow/deny lists creates confusing interactions. |
| `flags` | **Replaces** global | Flag conflicts (e.g. `--verbose` vs `--quiet`) are unresolvable via merge. |
| `env` | **Merges** with global (agent wins) | Env vars are independent key-value pairs; merge is safe. |
| `hooks` | **Merges** with global (appended per event) | Team-wide hooks should always fire; agent adds extras. |
| All other fields | **Replaces** global | Scalar values, no merge needed. |

## Capabilities

Capabilities use Coral's abstract capability system. They are translated to each agent CLI's native permission format at launch time. Coral does not add an enforcement layer of its own: an agent is restricted only as far as its CLI's own permissions restrict it.

The field name is `capabilities` (an object with `allow` and `deny` lists). An unknown key in the config is ignored — the config is parsed with standard JSON, which does not reject unknown fields — so a misspelled field name, or the old `permissions` / `permissions_preset` keys, which nothing reads, applies no restriction at all.

```json
{
  "capabilities": {
    "allow": ["file_read", "file_write", "shell"],
    "deny": []
  }
}
```

### Capability reference

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

Coral grants no permission on its own. The dashboard's presets that use the message board list `shell:coral-board *`, which only Claude enforces (see below).

### What each CLI enforces

Capabilities are translated to each CLI's own permissions at launch, and that is all the enforcement there is: Coral adds no permission layer. What each CLI keeps:

| agent_type | What is enforced |
|------------|------------------|
| `claude` | `allow` becomes pre-approvals and `deny` becomes deny rules in Claude Code's settings (`file_read` -> `Read`, `Glob`, `Grep`; `shell:npm *` -> `Bash(npm *)`). A tool that isn't allowed still runs after Claude asks, and in Bypass Permissions mode (the dashboard's default) nothing asks, so `allow` restricts nothing; only `deny` blocks. `git_write`'s git patterns don't limit git while `shell` is allowed, and they match command prefixes only. |
| `codex` | Only a sandbox (`read-only`, `workspace-write`, or none) and whether it asks (`on-request`: the model decides, and sandboxed commands run without asking; `never`). Shell patterns and `deny` entries aren't enforced; a `deny` entry only changes the mode. `shell`, `file_read`, `file_write` and `git_write` together run Codex without a sandbox. |
| `agy`, `gemini` | Only a mode (`--mode plan`, `--mode accept-edits`, or `--dangerously-skip-permissions`). Shell patterns and `deny` entries aren't enforced; a `deny` entry only changes the mode, and with `shell` it gives `--mode accept-edits`, which approves edits without asking. (`gemini` launches the `agy` binary.) |

When capabilities set a mode, they win over the launch dialog's permission flag for Codex and Antigravity.

**Default permission mode (#5836).** A Claude launch with no mode chosen (no `--permission-mode` or bypass flag, no saved `default_permission_mode`) starts in `--permission-mode auto`; the launch form, the team form and quick launch show Auto for Claude until another is picked, and a saved default is the person's choice for every agent type. Other CLIs start as they did: Codex's Auto (`--sandbox workspace-write -a never`) blocks the localhost board, Antigravity has no safe mode between its edits-only one and skip-permissions, and Pi has none. A task run with `auto_accept` adds `--permission-mode auto` for Claude. Through Coral Hub the default applies only when the owner lets agents skip permission prompts ("Coral Hub can turn on auto-accept" on, and Launch mode, the rule an explicit `--permission-mode auto` meets); otherwise a Hub launch or team member with no mode asks (`default`). A restart, resume, wake or team reset restores the session's own mode and never gets the default.

The dashboard's built-in presets get these flags (from `agent_presets.js`, pinned by `internal/agent/testdata/preset_enforcement.golden`):

| Preset | Codex | Antigravity |
|--------|-------|-------------|
| Lead Developer | `--dangerously-bypass-approvals-and-sandbox` | `--dangerously-skip-permissions` |
| QA Engineer | `--sandbox workspace-write -a never` | `--dangerously-skip-permissions` |
| Orchestrator | `--dangerously-bypass-approvals-and-sandbox --search` | `--dangerously-skip-permissions` |
| Frontend Dev | `--sandbox workspace-write -a on-request --search` | `--mode accept-edits` |
| Backend Dev | `--dangerously-bypass-approvals-and-sandbox` | `--dangerously-skip-permissions` |
| DevOps Engineer | `--dangerously-bypass-approvals-and-sandbox` | `--dangerously-skip-permissions` |
| Security Reviewer | `--sandbox read-only -a on-request --search` | `--mode plan` |
| Technical Writer | `--sandbox workspace-write -a on-request` | `--mode accept-edits` |
| Content Writer | `--sandbox workspace-write -a on-request --search` | `--mode accept-edits` |
| SEO Strategist | `--sandbox read-only -a on-request --search` | `--mode plan` |
| Design Director | `--sandbox workspace-write -a on-request --search` | `--mode accept-edits` |
| Research Analyst | `--sandbox read-only -a on-request --search` | `--mode plan` |
| Writer & Editor | `--sandbox workspace-write -a on-request` | `--mode accept-edits` |
| Scheduler & Planner | `--sandbox workspace-write -a on-request` | `--mode accept-edits` |

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

**Coral-managed (all agents):** `StepComplete`, `StepFailed` -- these are fired by Coral's workflow runner after a step finishes. They work for Claude, Antigravity (`agy`), Codex, and shell steps.

**Cross-agent support for Stop:** For Antigravity and Codex agents, `Stop` hooks are fired by Coral's runner after the agent process exits (since these CLIs have no native hooks system).

### Merge Behavior

Hooks defined in `defaults` apply to all agents. Per-agent hooks are **appended** per event (both fire). Coral system hooks (`coral-hook-task-sync`, `coral-hook-agentic-state`, `coral-hook-message-check`) are always injected into Claude sessions and cannot be overridden.

## Model Specification

The `model` field is vendor-specific and passed through to the agent CLI via `--model`:

| agent_type | Model examples |
|------------|---------------|
| `claude` | `opus`, `sonnet`, `haiku`, `claude-sonnet-4-6` |
| `agy` | `gemini-3.8-flash-high`, `gemini-3.8-flash-medium`, `gemini-3.1-pro-high`, `claude-sonnet-4-6` |
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
7. `capabilities`, if given, is an object with `allow` and `deny` string lists; the capability strings themselves are **not** validated — an unrecognised one is passed through to the agent CLI as-is rather than rejected
8. At most one agent should have `role: "orchestrator"` (warning, not error)

## API Endpoints


### Launch Team Inline

```
POST /api/sessions/launch-team
```

**Request Body:** Full team config JSON (same schema as a `.json` file).

### List Persisted Team Runs

```
GET /api/teams/all
```

This lists teams that have been launched and persisted in Coral's database; it is not a saved-config file API.

### Get Persisted Team Detail

```
GET /api/teams/detail/{name}
```

Coral does not expose an HTTP endpoint that saves a team definition to
`~/.coral/teams`. Write the JSON file there directly, import one with
`POST /api/teams/import`, or pass the full definition to the launch endpoint.

## Examples

### Minimal

Two agents with all defaults (Claude, no special capabilities):

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

### With Defaults and Capabilities

```json
{
  "name": "api-team",
  "working_dir": "/home/user/api",
  "defaults": {
    "agent_type": "claude",
    "model": "sonnet",
    "capabilities": {
      "allow": ["file_read", "file_write", "shell", "git_write", "agent_spawn"]
    }
  },
  "agents": [
    {
      "name": "Orchestrator",
      "role": "orchestrator",
      "prompt": "Coordinate API development.",
      "model": "opus",
      "capabilities": {
        "allow": ["file_read", "file_write", "shell", "git_write", "agent_spawn", "web_access", "notebook"]
      }
    },
    {
      "name": "Backend Dev",
      "prompt": "Implement API endpoints and migrations."
    },
    {
      "name": "QA",
      "prompt": "Write tests and review code.",
      "capabilities": {
        "allow": ["file_read", "shell"]
      }
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
    "capabilities": {
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
      "capabilities": {
        "allow": ["file_read", "file_write", "shell", "git_write", "agent_spawn", "web_access", "notebook"]
      }
    },
    {
      "name": "Backend Dev",
      "prompt": "Implement API endpoints and database migrations.",
      "capabilities": {
        "allow": ["file_read", "file_write", "shell", "git_write", "agent_spawn"]
      }
    },
    {
      "name": "Frontend Dev",
      "prompt": "Build React components and pages.",
      "agent_type": "agy",
      "model": "gemini-3.8-flash-high",
      "capabilities": {
        "allow": ["file_read", "file_write", "shell:npm *", "shell:npx *", "web_access"]
      },
      "env": {
        "BROWSER": "none"
      }
    },
    {
      "name": "Security Reviewer",
      "prompt": "Audit all changes for OWASP top 10 vulnerabilities.",
      "capabilities": {
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
   - `capabilities`: **replace** global
   - `flags`: **replace** global
   - `env`: **merge** with global (agent values win on key conflict)
   - `hooks`: **merge** with global (agent entries appended per event)
   - All other fields: **replace** global
3. Inject Coral system hooks (always added, never overridden)
4. Translate capabilities to agent-native format
5. Build launch command
