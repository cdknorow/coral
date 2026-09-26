# Agent Configuration Spec

This document specifies how Coral configures and launches each agent type, and defines the work needed to bring Codex and Gemini to feature parity with Claude.

## Overview

When Coral launches an agent session, it needs to:

1. **Inject a system prompt** — protocol instructions, board usage, role-specific behavior
2. **Inject settings** — permissions, environment variables, hooks, custom config
3. **Pass the action prompt** — the user's task or board action instructions
4. **Support resume** — continue a previous session with full context
5. **Translate permissions** — map Coral capabilities to agent-native permission flags

Each agent CLI has different mechanisms for these. This spec documents the current state and target state for all three.

---

## Claude (Reference Implementation — Complete)

### System Prompt
- **Mechanism:** `systemPrompt` field in a JSON settings file passed via `--settings <path>`
- **Content:** Protocol file + board system prompt (CLI usage instructions + role tail)
- **File:** Temp file at `/tmp/coral_settings_{sessionID}.json`

### Settings Injection
- **Mechanism:** `--settings <path>` flag pointing to a merged JSON settings file
- **Merging order:** `~/.claude/settings.json` < `{workdir}/.claude/settings.json` < `{workdir}/.claude/settings.local.json`
- **Hooks deep-merged:** All hooks from all layers combined, then Coral hooks appended
- **Coral hooks injected:**
  - `PostToolUse` → `coral-hook-task-sync` (on TaskCreate|TaskUpdate), `coral-hook-agentic-state`, `coral-hook-message-check`
  - `Stop` → `coral-hook-agentic-state`
  - `Notification` → `coral-hook-agentic-state`
- **Environment variables** injected via settings `env` map:
  - `CORAL_SESSION_NAME` — tmux session name for board state lookup
  - `CORAL_SUBSCRIBER_ID` — stable board identity (role name)
  - `PATH` — prepended with app bundle MacOS dir (for hook binaries)
- **Permissions** injected via settings `permissions` map:
  - `allow` / `deny` arrays of Claude tool name patterns (e.g. `"Bash(git push *)"`, `"Read"`)
  - Always includes `"Bash(coral-board *)"` in allow list

### Action Prompt
- **Mechanism:** Positional CLI argument via `"$(cat '/tmp/coral_prompt_{id}.txt')"`
- **Content:** User prompt + board action prompt (orchestrator/worker instructions)

### Resume
- **Mechanism:** `--resume <sessionID>` flag (replaces `--session-id`)
- **Preparation:** `PrepareResume()` copies the session's `.jsonl` file and metadata directory from other project dirs to the target project dir so Claude can find it

### Permissions
- **CLI flags:** `--dangerously-skip-permissions` (when full shell access, no deny list)
- **Settings-based:** Granular tool patterns in `permissions.allow` / `permissions.deny`
- **Capability mapping:**
  - `file_read` → `["Read", "Glob", "Grep"]`
  - `file_write` → `["Write", "Edit"]`
  - `shell` → `["Bash"]`
  - `shell:<pattern>` → `["Bash(<pattern>)"]`
  - `web_access` → `["WebFetch", "WebSearch"]`
  - `git_write` → `["Bash(git push *)", "Bash(git commit *)", ...]`
  - `agent_spawn` → `["Agent"]`
  - `notebook` → `["NotebookEdit"]`

### Launch Command Shape
```
claude --resume <id> --settings /tmp/coral_settings_<id>.json [flags] "$(cat '/tmp/coral_prompt_<id>.txt')"
```
or (new session):
```
claude --session-id <id> --settings /tmp/coral_settings_<id>.json [flags] "$(cat '/tmp/coral_prompt_<id>.txt')"
```

---

## Codex (Current State → Target State)

### CLI Reference
```
codex [OPTIONS] [PROMPT]
codex resume [OPTIONS] [SESSION_ID] [PROMPT]
```

Key flags:
- `-c, --config <key=value>` — Override config.toml values (dotted paths, TOML-parsed values)
- `-m, --model <MODEL>` — Model override
- `-p, --profile <PROFILE>` — Config profile from config.toml
- `-s, --sandbox <MODE>` — `read-only`, `workspace-write`, `danger-full-access`
- `-a, --ask-for-approval <POLICY>` — `untrusted`, `on-request`, `never`
- `--full-auto` — Alias for `-a on-request --sandbox workspace-write`
- `--dangerously-bypass-approvals-and-sandbox` — No sandbox, no approvals
- `-C, --cd <DIR>` — Working directory
- `--search` — Enable web search tool
- `--add-dir <DIR>` — Additional writable directories

Config file: `~/.codex/config.toml`

### System Prompt

**Current:** Protocol + board system prompt + action prompt are all concatenated and written to a single temp file, passed as the positional `[PROMPT]` argument. No separation between system instructions and user prompt.

**Constraints:**
- Codex reads instructions from `~/.codex/instructions.md` (global) and `CODEX.md` in the project root, but we **cannot** write a shared `CODEX.md` because multiple agents may run in the same working directory simultaneously
- The `codex_hooks` feature is listed as "under development" — not yet available

**Solution:** Codex supports overriding the system prompt via the `-c` config flag with the `developer_instructions` key:
```bash
codex -c developer_instructions="$(cat /tmp/coral_codex_instructions_{id}.md)" "action prompt here"
```

This gives us proper separation — system context via `developer_instructions`, action prompt as the positional argument — structurally equivalent to Claude's `systemPrompt` in `--settings`.

**Implementation plan:**
- Write system instructions (protocol + board system prompt + role instructions) to a per-session temp file: `/tmp/coral_codex_instructions_{id}.md`
- Inject via `-c developer_instructions="$(cat '/tmp/coral_codex_instructions_{id}.md')"`
- Pass the action prompt separately as the positional `[PROMPT]` argument (via temp file + `$(cat)` as today)

### Settings Injection

**Current:** Only env vars (`CORAL_SESSION_NAME`, `CORAL_SUBSCRIBER_ID`) set as shell prefix. No config merging.

**Target:** Use `-c key=value` flags to inject Coral-specific config overrides:
- `-c model="<model>"` — if model override is specified
- Sandbox and approval modes via `-c` or dedicated flags
- Environment variables via shell prefix (Codex has no env injection in config)

**Implementation plan:**
- Read `~/.codex/config.toml` to understand current user config (informational, don't modify)
- Build `-c` flag list for Coral overrides
- Continue using shell env var prefix for `CORAL_SESSION_NAME`, `CORAL_SUBSCRIBER_ID`
- Prepend app bundle bin dir to PATH via shell prefix (for coral-board, hook binaries)

### Action Prompt

**Current:** Combined with system prompt in single temp file. Needs separation.

**Target:** Action prompt passed as a separate positional `[PROMPT]` argument, independent of system instructions:
- New session: `codex -c developer_instructions="$(cat '...')" [flags] "$(cat '/tmp/coral_codex_prompt_{id}.txt')"`
- Resume: `codex resume <id> -c developer_instructions="$(cat '...')" [flags] "$(cat '/tmp/coral_codex_prompt_{id}.txt')"`

**Implementation plan:**
- Write action prompt (user task + board action instructions) to its own temp file
- Pass via `"$(cat '/tmp/coral_codex_prompt_{id}.txt')"` as the positional argument
- On resume, both system instructions and action prompt are re-injected

### Resume

**Current:** `codex resume <sessionID>` as positional. No `PrepareResume()` implementation. ✅ Codex manages its own session storage in `~/.codex/sessions/`, so file copying is likely unnecessary.

**Target:** Verify that Codex can resume sessions launched from different working directories. If not, implement a `PrepareResume()` that handles this edge case.

**Implementation plan:**
- Test resume behavior across working directories
- Codex stores sessions by date (`YYYY/MM/DD/rollout-*.jsonl`), not by project dir, so resume should work without file copying
- Add `PrepareResume` as no-op unless testing reveals issues

### Permissions

**Current:** Only maps to `--full-auto` when `CapShell` allowed and no deny list.

**Target:** Full capability mapping using Codex's granular sandbox and approval modes:

| Coral Capability | Codex Mapping |
|---|---|
| `shell` (no deny) | `--sandbox workspace-write -a never` (automatic, sandboxed execution) |
| `shell` + deny list | `--sandbox workspace-write -a on-request` |
| `file_read` only | `--sandbox read-only -a on-request` |
| `file_read` + `file_write` | `--sandbox workspace-write -a on-request` |
| Full access (no restrictions) | `--dangerously-bypass-approvals-and-sandbox` |
| `web_access` | `--search` |

**Implementation plan:**
- Update `TranslateToCodexPermissions()` to return structured flags
- Add sandbox mode and approval policy fields to `CodexPermissions`
- Map Coral capabilities to the appropriate combination

### Hooks

**Current:** None. Codex has no native hook system.

**Target:** No native hooks possible. Coral's background services (git poller, message check poller) already provide hook-like functionality for all agents. The `coral-hook-agentic-state` and `coral-hook-message-check` hooks are Claude-specific optimizations — Codex sessions will rely on Coral's polling-based detection instead.

### Launch Command Shape (Target)

New session:
```
CORAL_SESSION_NAME="codex-<uuid>" CORAL_SUBSCRIBER_ID="<role>" codex -c developer_instructions="$(cat '/tmp/coral_codex_instructions_<id>.md')" --sandbox workspace-write -a never [flags] "$(cat '/tmp/coral_codex_prompt_<id>.txt')"
```

Resume:
```
CORAL_SESSION_NAME="codex-<uuid>" CORAL_SUBSCRIBER_ID="<role>" codex resume <sessionID> -c developer_instructions="$(cat '/tmp/coral_codex_instructions_<id>.md')" --sandbox workspace-write -a never [flags] "$(cat '/tmp/coral_codex_prompt_<id>.txt')"
```

Two temp files per session:
- `/tmp/coral_codex_instructions_<id>.md` — system instructions (protocol, board CLI usage, role)
- `/tmp/coral_codex_prompt_<id>.txt` — action prompt (user task, board action instructions)

---

## Gemini (Current State → Target State)

### CLI Reference
```
gemini [options] [query..]
```

Key flags:
- `-m, --model <MODEL>` — Model override
- `-p, --prompt <PROMPT>` — Non-interactive (headless) mode
- `-i, --prompt-interactive <PROMPT>` — Execute prompt, stay interactive
- `-s, --sandbox` — Run in sandbox
- `-y, --yolo` — Auto-approve all actions
- `--approval-mode <MODE>` — `default`, `auto_edit`, `yolo`, `plan`
- `-r, --resume <SESSION>` — Resume session (`"latest"` or index number)
- `--allowed-tools <TOOLS>` — Tools allowed without confirmation
- `-e, --extensions <LIST>` — Extensions to use
- `--include-directories <DIRS>` — Additional workspace directories

Environment variables:
- `GEMINI_SYSTEM_MD` — Path to system prompt markdown file

Config: `~/.gemini/settings.json`

Hooks: `gemini hooks` subcommand for managing hooks

### System Prompt

**Current:** Protocol + board system prompt written to temp `.md` file, injected via `GEMINI_SYSTEM_MD` env var. ✅ This works.

**Target:** Keep `GEMINI_SYSTEM_MD` mechanism — it's the correct approach. Ensure content matches what Claude gets (protocol + board instructions + role tail).

**Implementation plan:** No changes needed for system prompt injection. Already at parity.

### Settings Injection

**Current:** No settings merging. Only env vars for session name and subscriber ID.

**Target:** Read and merge Gemini's settings where relevant. Gemini's `~/.gemini/settings.json` is simple (auth config). Unlike Claude, there's no deep settings merging needed since Gemini's config is minimal.

**Implementation plan:**
- Inject environment variables via shell prefix (already done)
- Add app bundle bin dir to PATH prefix (for coral-board, hook binaries)
- Pass relevant flags directly (`--approval-mode`, `--sandbox`, `--model`, etc.)

### Action Prompt

**Current:** Passed as inline positional argument with quote escaping: `"escaped prompt text"`.

**Target:** Use temp file approach like Claude and Codex for robustness with long prompts:
- Write action prompt to temp file
- Pass via `"$(cat '/tmp/coral_gemini_prompt_{id}.txt')"`

**Implementation plan:**
- Switch from inline string to `writeTempFile` + `FormatPromptFileArg` pattern
- Handles special characters and long prompts more reliably

### Resume

**Current:** `SupportsResume() = false`. Gemini's `--resume` flag is not used.

**Target:** Enable resume support. Gemini CLI supports `--resume <session>` where session is `"latest"` or an index number.

**Challenge:** Gemini uses session index numbers, not UUIDs. Coral tracks sessions by UUID. We need to:
1. Determine if Gemini supports resume by session ID or only by index
2. If index-only, either map Coral session IDs to Gemini indices or use `--resume latest` as a pragmatic fallback

**Implementation plan:**
- Set `SupportsResume() = true`
- Investigate Gemini session storage in `~/.gemini/tmp/` to understand session ID format
- If Gemini stores sessions with identifiable IDs, map them. Otherwise use `--resume latest` when the session is the most recent one for that project
- Implement `PrepareResume()` if file preparation is needed

### Permissions

**Current:** Stub — passes through capabilities with no translation.

**Target:** Map Coral capabilities to Gemini's approval modes:

| Coral Capability | Gemini Mapping |
|---|---|
| Full access (no restrictions) | `--yolo` or `--approval-mode yolo` |
| `shell` + `file_write` | `--approval-mode auto_edit` |
| `file_read` only | `--approval-mode plan` (read-only mode) |
| Default | `--approval-mode default` (prompt for each action) |
| `web_access` | No flag needed (Gemini has extensions) |
| Specific tools allowed | `--allowed-tools <tool1> <tool2>` |

**Implementation plan:**
- Update `TranslateToGeminiPermissions()` to return structured flags
- Add approval mode, sandbox, and allowed tools fields
- Map Coral capabilities to the appropriate combination

### Hooks

**Current:** None.

**Target:** Investigate Gemini's native hook system (`gemini hooks` CLI). If it supports post-tool-use or stop hooks similar to Claude, wire Coral hooks through it.

**Implementation plan:**
- Run `gemini hooks --help` to understand the hook system
- If hooks can be injected per-session (not just globally), add Coral hooks for:
  - Task sync on tool use
  - Agentic state updates
  - Message board check
- If hooks are global-only, skip and rely on Coral's polling-based detection

### Launch Command Shape (Target)

New session:
```
GEMINI_SYSTEM_MD="/tmp/coral_gemini_sys_<id>.md" CORAL_SESSION_NAME="gemini-<uuid>" CORAL_SUBSCRIBER_ID="<role>" gemini --approval-mode yolo [flags] "$(cat '/tmp/coral_gemini_prompt_<id>.txt')"
```

Resume:
```
GEMINI_SYSTEM_MD="/tmp/coral_gemini_sys_<id>.md" CORAL_SESSION_NAME="gemini-<uuid>" CORAL_SUBSCRIBER_ID="<role>" gemini --resume latest --approval-mode yolo [flags] "$(cat '/tmp/coral_gemini_prompt_<id>.txt')"
```

---

## Comparison Matrix

| Feature | Claude | Codex (Current) | Codex (Target) | Gemini (Current) | Gemini (Target) |
|---|---|---|---|---|---|
| **System prompt** | `--settings` JSON `systemPrompt` | Combined in prompt arg | `-c developer_instructions="$(cat ...)"` | `GEMINI_SYSTEM_MD` env var | Same (already good) |
| **Settings merge** | 3-layer JSON merge + hooks | None | `-c` flag overrides | None | Direct flags |
| **Coral hooks** | PostToolUse, Stop, Notification | None | None (use polling) | None | Investigate native hooks |
| **Env vars** | Via settings `env` map | Shell prefix | Shell prefix | Shell prefix | Shell prefix |
| **PATH injection** | Via settings `env.PATH` | Not done | Shell prefix | Not done | Shell prefix |
| **Action prompt** | Temp file `$(cat)` | Temp file `$(cat)` ✅ | Temp file `$(cat)` ✅ | Inline escaped string | Temp file `$(cat)` |
| **Resume** | `--resume <id>` + file copy | `resume <id>` (no prep) | Verify, add prep if needed | Not supported | `--resume latest` or index |
| **Permissions** | Granular tool patterns | `--full-auto` only | `--sandbox` + `-a` modes | Stub (no-op) | `--approval-mode` + flags |
| **Session ID** | `--session-id <uuid>` | Not passed | Not available (Codex manages internally) | Not passed | Not available |

---

## Implementation Order

### Phase 1: Codex Parity
1. **System prompt separation** — Inject via `-c developer_instructions="$(cat ...)"`, action prompt as separate positional arg
2. **Permissions upgrade** — Map capabilities to `--sandbox` and `-a` modes
3. **PATH injection** — Add app bundle bin dir to shell prefix
4. **Action prompt for resume** — Pass prompt to `codex resume <id> [PROMPT]`

### Phase 2: Gemini Parity
1. **Resume support** — Enable `SupportsResume()`, implement `--resume` flag usage
2. **Action prompt robustness** — Switch from inline to temp file `$(cat)` pattern
3. **Permissions** — Map capabilities to `--approval-mode` and `--sandbox`
4. **PATH injection** — Add app bundle bin dir to shell prefix
5. **Hooks investigation** — Check if Gemini's native hooks can carry Coral hooks

### Phase 3: Validation
1. Launch each agent type with system prompt and verify it appears in agent context
2. Launch with board and verify board CLI instructions are in system prompt
3. Resume a session for each agent type
4. Verify permissions restrict agent behavior as expected
5. Verify `coral-board` is accessible from each agent's PATH

---

## Files to Modify

| File | Changes |
|---|---|
| `internal/agent/codex.go` | Rewrite `BuildLaunchCommand` — separate system/action prompts, add `-c` flags, granular permissions |
| `internal/agent/gemini.go` | Enable resume, switch to temp file prompt, add permission flags, PATH prefix |
| `internal/agent/permissions.go` | Expand `CodexPermissions` and `GeminiPermissions` structs with sandbox/approval fields |
| `internal/agent/agent_test.go` | Update tests for new launch command shapes |

---

## Open Questions

1. **Gemini resume by ID:** Can `--resume` accept a session identifier, or only index numbers? Need to inspect `~/.gemini/tmp/` session storage.
2. **Gemini hooks:** What hook events does `gemini hooks` support? Can they be configured per-session or only globally?
3. **Codex resume prep:** Does `codex resume <id>` work regardless of current working directory, or does it need to be in the original project dir?
4. **Codex hooks timeline:** The `codex_hooks` feature is "under development" — monitor for availability in future Codex releases.
