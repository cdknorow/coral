# Pi Coding Agent Support

**Status:** Proposed
**Owner:** —
**Depends on:** none
**Reference:** [pi-mono/packages/coding-agent](https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent)

## Summary

Add support for the Pi coding agent (`pi`) as a new agent type in Coral. Pi is an open-source terminal-based coding agent by Mario Zechner (badlogic) that supports multiple LLM providers (Anthropic, OpenAI, Google, etc.) and has no built-in permission system — it runs full-auto by design.

## Pi CLI Interface

**Binary:** `pi`
**Install:** `npm install -g @mariozechner/pi-coding-agent`
**Config directory:** `~/.pi/agent/`
**Session storage:** `~/.pi/agent/sessions/--<path>--/<timestamp>_<uuid>.jsonl`

### Key Flags

| Flag | Description |
|------|-------------|
| `--model <pattern>` | Model pattern or ID (supports `provider/id` syntax) |
| `--provider <name>` | Provider name (anthropic, openai, google, etc.) |
| `--append-system-prompt <text>` | Append to system prompt (Coral's board instructions) |
| `--session <path\|id>` | Use specific session file or partial UUID |
| `-c` / `--continue` | Continue most recent session |
| `--session-dir <dir>` | Custom session storage directory |
| `--no-session` | Ephemeral mode |
| `--tools <list>` | Allowlist specific tools |
| `--no-context-files` | Disable AGENTS.md/CLAUDE.md loading |
| `--thinking <level>` | off, minimal, low, medium, high, xhigh |
| `-e <source>` | Load extension (repeatable) |

### No Permission Modes

Pi has no built-in permission/autonomy modes. It runs all tools without confirmation by design. Users are expected to run in containers or add permission gates via extensions. Coral does not need to translate permission capabilities for Pi.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `PI_CODING_AGENT_DIR` | Override config directory (default `~/.pi/agent`) |
| `PI_SKIP_VERSION_CHECK` | Skip version check at startup |
| `PI_TELEMETRY` | `0` to disable telemetry |

### Session Format

JSONL with tree structure (version 3). Each entry has `id` (8-char hex) and `parentId` for in-place branching. Entry types:
- `session` — header with UUID, cwd, version
- `message` — conversation messages (user, assistant, toolResult roles)
- `compaction` — context summarization
- `model_change`, `thinking_level_change`
- `custom`, `custom_message` — extension state/messages

Sessions are identified by UUID stored in the JSONL header. Partial UUIDs work with `--session`.

## Implementation Plan

### Files to Create

**`internal/agent/pi.go`** — Pi agent implementation:

```go
type PiAgent struct{}

func (a *PiAgent) AgentType() string      { return at.Pi }
func (a *PiAgent) SupportsResume() bool   { return true }
func (a *PiAgent) HistoryBasePath() string {
    return filepath.Join(homeDir(), ".pi", "agent", "sessions")
}
func (a *PiAgent) HistoryGlobPattern() string {
    return "*/*.jsonl"
}
```

**`BuildLaunchCommand` design:**

```
pi --append-system-prompt "$(cat <prompt-file>)" \
   --model <model> \
   "<user prompt>"
```

Key decisions:
- System prompt injected via `--append-system-prompt` (like Gemini's env var approach, not Claude's settings file)
- No `--session-id` flag — Pi uses `--session <path>` or `--continue`
- No permission flags needed (Pi is always full-auto)
- Model passed via `--model <pattern>`
- Prompt as positional argument (same as Claude)
- No settings file injection (Pi doesn't support `--settings`)

### Files to Modify

**`internal/agenttypes/types.go`** — Add `Pi = "pi"` constant

**`internal/agent/agent.go`** — Add to `GetAgent()` factory and `agentCLIs` map:
```go
case at.Pi:
    return &PiAgent{}

// In agentCLIs:
at.Pi: {Binary: "pi", InstallCommand: "npm install -g @mariozechner/pi-coding-agent"},
```

**`internal/agent/permissions.go`** — Add no-op permission translation:
```go
func TranslateToPiPermissions(caps *Capabilities) PiPermissions {
    // Pi has no permission system — always full-auto
    return PiPermissions{}
}
```

**`internal/jsonl/reader.go`** — Add Pi transcript parser:
- `resolvePiTranscript()` — locate JSONL file by session UUID
- `parsePiEntry()` — parse Pi's message format (similar to Claude's JSONL but different entry types)

**`pkg/frontend/static/modals.js`** — Add Pi to agent type dropdowns and permission flag map (no-op flag since Pi has no permission modes)

### Resume Support

Pi supports resume via `--continue` (most recent) and `--session <id>` (specific session). Implementation:
- `SupportsResume() = true`
- `PrepareResume()` — no-op (Pi handles session discovery internally)
- Resume command: `pi --session <sessionID> --append-system-prompt "$(cat <prompt>)"`

### Proxy Support

Pi respects standard HTTP proxy env vars (`HTTP_PROXY`, `HTTPS_PROXY`) via `undici`'s `EnvHttpProxyAgent`. Coral's MITM proxy can be injected via environment variables in the launch command.

### PULSE Protocol

Pi does not natively emit PULSE markers (`||PULSE:STATUS||`, `||PULSE:SUMMARY||`). Options:
1. **Extension approach:** Write a Pi extension that emits PULSE markers to stdout — cleanest but requires shipping an extension file
2. **Regex fallback:** Parse Pi's TUI output for status indicators — fragile
3. **Accept degraded status:** Pi sessions show as "Working" without granular status — simplest, start here

**Recommendation:** Start with option 3 (no PULSE), add a Pi extension later if demand warrants it.

## Scope

### In Scope
- Launch Pi agents from Coral UI (single agent + team launch)
- Terminal streaming (same unified protocol as all other agents)
- Session list display with agent type "pi"
- Model selection via `--model` flag
- Board integration via `--append-system-prompt`
- History indexing of Pi JSONL sessions

### Out of Scope (future work)
- PULSE status/summary parsing (requires Pi extension)
- Pi extension management from Coral UI
- Pi-specific settings UI (thinking level, tool allowlist)
- Pi skill management

## Tradeoffs

### What we gain
- Support for a multi-provider coding agent (use GPT-4, Gemini, Claude all from one agent type)
- No permission complexity (Pi is always full-auto)
- Simple integration — Pi's CLI is straightforward with minimal required flags

### What we give up
- No granular status tracking without PULSE (session shows as "Working" perpetually)
- No permission translation (Pi doesn't have permission modes — could surprise users expecting safety rails)
- Session history format is different (tree-based JSONL vs linear)

### Risk
- Pi is a newer project — CLI interface may change more frequently than Claude/Gemini/Codex
- No built-in safety rails could be a liability for teams that expect permission controls
