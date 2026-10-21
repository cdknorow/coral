# Team Configuration Format

## Overview

A JSON format for defining agent teams with global defaults and per-agent overrides. Supports heterogeneous teams (mixed models/vendors), fine-grained permissions, custom hooks, and individual prompts — all in a single declarative file.

## Design Principles

1. **Global defaults, agent overrides** — Set team-wide config once, override per agent only where needed. Agent-level values replace (not merge with) global values for the same key.
2. **Agent-agnostic capabilities** — Permissions use Coral's abstract capability system (`file_read`, `shell`, etc.), translated to native formats at launch time.
3. **Vendor-neutral model specification** — `agent_type` selects the CLI (claude, gemini, codex), `model` selects the specific model within that vendor.
4. **File-based and API-compatible** — Same format works as a `.json` file on disk or as a `POST /api/sessions/launch-team` request body.
5. **Preset-friendly** — Reference built-in permission presets by name instead of listing capabilities manually.

## Schema

```jsonc
{
  // ─── Team Metadata ───────────────────────────────────────────────
  "name": "backend-refactor",              // Team name (used as board_name if board_name not set)
  "board_name": "backend-refactor",        // Message board name (optional, defaults to name)
  "working_dir": "/path/to/project",       // Shared working directory
  "board_type": "coral",                   // Board implementation: "coral" (default)

  // ─── Global Defaults ─────────────────────────────────────────────
  // These apply to ALL agents unless overridden at the agent level.
  "defaults": {
    "agent_type": "claude",                // CLI to use: "claude" | "gemini" | "codex"
    "model": "opus",                       // Model identifier (vendor-specific)
    "permissions": {                       // Coral-level capability permissions
      "allow": ["file_read", "file_write", "shell"],
      "deny": []
    },
    "flags": ["--verbose"],                // Extra CLI flags passed to every agent
    "env": {                               // Extra environment variables
      "NODE_ENV": "development"
    },
    "hooks": {                             // Agent hooks (Claude settings.json format)
      "PostToolUse": [
        {
          "matcher": "Bash",
          "hooks": [
            { "type": "command", "command": "echo 'tool used'" }
          ]
        }
      ]
    },
    "max_turns": 100,                      // Max conversation turns before auto-stop
    "working_dir": null                    // Per-agent working_dir override base (rare)
  },

  // ─── Agent Definitions ───────────────────────────────────────────
  "agents": [
    {
      "name": "Orchestrator",
      "role": "orchestrator",              // Role: "orchestrator" | any string (workers)
      "prompt": "You coordinate the backend refactor. Break work into tasks and assign to team members.",

      // Override global defaults for this agent:
      "agent_type": "claude",              // (optional) Override vendor/CLI
      "model": "opus",                     // (optional) Override model
      "permissions": {                     // (optional) REPLACES global permissions entirely
        "allow": ["file_read", "web_access", "agent_spawn"],
        "deny": ["file_write", "shell"]
      },
      "permissions_preset": null,          // (optional) Use a named preset instead of inline permissions
      "flags": [],                         // (optional) REPLACES global flags
      "env": {},                           // (optional) MERGED with global env (agent wins on conflict)
      "hooks": {},                         // (optional) MERGED with global hooks (agent entries appended)
      "max_turns": 200,                    // (optional) Override global max_turns
      "working_dir": null,                 // (optional) Override working directory for this agent
      "cli_path": null                     // (optional) Custom path to agent binary
    },
    {
      "name": "Lead Developer",
      "role": "lead_dev",
      "prompt": "You implement the core refactoring tasks assigned by the Orchestrator.",
      "permissions_preset": "lead_dev"
      // Everything else inherits from defaults
    },
    {
      "name": "QA Engineer",
      "role": "qa",
      "prompt": "You review code, write tests, and verify implementations.",
      "agent_type": "gemini",              // This agent uses Gemini instead of Claude
      "model": "gemini-2.5-pro",
      "permissions_preset": "qa"
    },
    {
      "name": "Security Reviewer",
      "role": "security",
      "prompt": "You audit code for security vulnerabilities.",
      "permissions": {
        "allow": ["file_read", "shell:grep *", "shell:rg *", "web_access"],
        "deny": ["file_write", "git_write"]
      }
    }
  ]
}
```

## Override Rules

The merge strategy is intentionally simple — no deep recursive merging of permission lists that would be hard to reason about.

| Field | Override Behavior | Rationale |
|-------|------------------|-----------|
| `agent_type` | Agent replaces global | An agent IS a specific CLI |
| `model` | Agent replaces global | Model is tightly coupled to agent |
| `permissions` | Agent **replaces** global entirely | Partial merge of allow/deny lists creates confusing interactions. If you override, you own the full permission set. |
| `permissions_preset` | Expands to `permissions`, same replace rule | Syntactic sugar for common profiles |
| `flags` | Agent **replaces** global | Flag conflicts (e.g. `--verbose` vs `--quiet`) are unresolvable via merge |
| `env` | Agent **merges** with global (agent wins) | Env vars are independent key-value pairs; merge is safe and expected |
| `hooks` | Agent **merges** with global (agent entries appended per event) | Hooks are additive — team-wide hooks should always fire, agent adds extras |
| `max_turns` | Agent replaces global | Scalar value, no merge needed |
| `working_dir` | Agent replaces global | Path, no merge needed |
| `cli_path` | Agent-only (no global equivalent) | Per-agent binary override |
| `prompt` | Agent-only (no global equivalent) | Each agent needs its own instructions |
| `role` | Agent-only (no global equivalent) | Defines orchestrator vs worker behavior |

### Why permissions replace rather than merge

Consider: global allows `["file_read", "file_write", "shell"]`, and you want a read-only QA agent. With merge, you'd need to figure out which global allows to remove. With replace, you just declare what the agent can do:

```jsonc
// Clear and complete — no need to mentally diff against global
"permissions": { "allow": ["file_read"], "deny": ["file_write", "shell"] }

// Or just use a preset
"permissions_preset": "qa"
```

## Permission Presets

Built-in presets (defined in `internal/agent/permissions.go`):

| Preset | Allow | Deny |
|--------|-------|------|
| `full_access` | file_read, file_write, shell, git_write, agent_spawn, web_access, notebook | — |
| `lead_dev` | file_read, file_write, shell, git_write, agent_spawn | — |
| `devops` | file_read, file_write, shell, git_write | — |
| `frontend_dev` | file_read, file_write, shell:npm *, shell:npx *, web_access | — |
| `orchestrator` | file_read, agent_spawn, web_access | — |
| `qa` | file_read | file_write, shell |
| `read_only` | file_read, web_access | — |

Presets are expanded at launch time. If both `permissions` and `permissions_preset` are set, `permissions` wins.

## Capabilities Reference

Abstract capabilities that get translated to each agent CLI's native permission format:

| Capability | Description | Claude Translation | Codex Translation |
|------------|-------------|-------------------|-------------------|
| `file_read` | Read files, search, glob | Read, Glob, Grep | (allow list) |
| `file_write` | Write and edit files | Write, Edit | (allow list) |
| `shell` | Run arbitrary shell commands | Bash | --full-auto |
| `shell:<pattern>` | Run specific shell commands | Bash(\<pattern\>) | (allow list) |
| `web_access` | Fetch URLs, web search | WebFetch, WebSearch | (allow list) |
| `git_write` | Push, commit, branch, merge, rebase | Bash(git push *), etc. | (allow list) |
| `agent_spawn` | Spawn sub-agents | Agent | (allow list) |
| `notebook` | Edit Jupyter notebooks | NotebookEdit | (allow list) |

All agents automatically get `Bash(coral-board *)` permission for message board communication.

## Resolution Algorithm

When launching an agent, resolve its effective configuration:

```
1. Start with defaults (or empty if no defaults block)
2. For each field the agent defines:
   a. permissions/permissions_preset → resolve preset, then REPLACE
   b. flags → REPLACE
   c. env → MERGE (agent values win on key conflict)
   d. hooks → MERGE (agent entries appended per event)
   e. All other fields → REPLACE
3. Inject Coral system hooks (coral-hook-task-sync, etc.) — always added, never overridden
4. Translate permissions to agent-native format via TranslatePermissions()
5. Build launch command via agent.BuildLaunchCommand()
```

## Hooks

Hooks follow the Claude Code settings.json hook format. They fire on agent lifecycle events.

```jsonc
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Bash",                 // Optional: only fire for matching tools
        "hooks": [
          { "type": "command", "command": "my-linter --check" }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          { "type": "command", "command": "notify-team 'agent stopped'" }
        ]
      }
    ],
    "Notification": [
      {
        "hooks": [
          { "type": "command", "command": "log-notification" }
        ]
      }
    ]
  }
}
```

**Hook events:** `PostToolUse`, `Stop`, `Notification` (matching Claude Code's hook system).

**Coral system hooks** (`coral-hook-task-sync`, `coral-hook-agentic-state`, `coral-hook-message-check`) are always injected regardless of configuration. They cannot be overridden or removed by team config.

For agents that don't support hooks natively (Gemini, Codex), hook definitions are stored but not injected. Coral's background services provide equivalent functionality via polling.

## Model Specification

The `model` field is vendor-specific and passed through to the agent CLI:

| agent_type | model examples | How it's passed |
|------------|---------------|-----------------|
| claude | `opus`, `sonnet`, `haiku`, `claude-sonnet-4-6` | `--model` flag |
| gemini | `gemini-2.5-pro`, `gemini-2.5-flash` | `--model` flag |
| codex | `o3`, `o4-mini`, `gpt-4.1` | `--model` flag |

If `model` is omitted, the agent CLI uses its own default.

## File Storage

Team config files are stored at `~/.coral/teams/` with `.json` extension:

```
~/.coral/teams/
  backend-refactor.json
  frontend-redesign.json
  security-audit.json
```

Teams can also be launched inline via the API without a file on disk.

## API Integration

### Launch from file
```
POST /api/sessions/launch-team-file
{ "file": "backend-refactor" }
```
Loads `~/.coral/teams/backend-refactor.json` and launches.

### Launch inline (current API, extended)
```
POST /api/sessions/launch-team
{ ...full team config JSON... }
```

### Save team config
```
POST /api/teams
{ ...full team config JSON... }
```

### List saved teams
```
GET /api/teams
```

### Get team config
```
GET /api/teams/{name}
```

## Migration from Current Format

The current `launch-team` request body:
```jsonc
{
  "board_name": "my-team",
  "working_dir": "/path",
  "agent_type": "claude",
  "flags": ["--verbose"],
  "agents": [
    { "name": "Agent 1", "prompt": "...", "capabilities": { "allow": [...] } }
  ]
}
```

Maps to the new format as:
```jsonc
{
  "name": "my-team",
  "working_dir": "/path",
  "defaults": {
    "agent_type": "claude",
    "flags": ["--verbose"]
  },
  "agents": [
    { "name": "Agent 1", "prompt": "...", "permissions": { "allow": [...] } }
  ]
}
```

The old format should continue to work — the server normalizes it to the new structure internally. `capabilities` is accepted as an alias for `permissions` at the agent level.

## Minimal Example

Simplest possible team — two agents with defaults:

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

This uses all defaults: Claude, no special permissions, no hooks, no flags.

## Heterogeneous Team Example

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

## Validation Rules

At launch time, the server validates:

1. `name` or `board_name` must be non-empty
2. `working_dir` must be non-empty and exist on disk
3. `agents` must contain at least one entry
4. Each agent must have a non-empty `name`
5. `agent_type` (global or per-agent) must be a registered type or empty (defaults to "claude")
6. `permissions_preset` must reference a known preset name
7. `permissions.allow` and `permissions.deny` entries must be valid capability strings
8. At most one agent should have `role: "orchestrator"` (warning, not error)
9. Agent names must be unique within the team
