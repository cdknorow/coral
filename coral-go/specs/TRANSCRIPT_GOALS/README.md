# Transcript-Derived Goals

**Status:** Planned
**Depends on:** [Agent Bar Tweaks](../AGENT_BAR_TWEAKS.md) D1 (identity chain, `first_prompt` in both payload builders)
**Replaces:** the `PULSE:SUMMARY` prompt injection in `requestGoal` / `refreshGoal`

## Overview

Give every live agent a stable **name** and a current **goal** in the sidebar
without ever typing into the agent's terminal. A background service reads the
agent's own JSONL transcript, sends a condensed slice to a cheap model through
the CLI the user already has installed (`claude -p` or `codex exec`), and
stores a one-line result. The agent is never interrupted and its context is
never polluted.

Cost is on the order of $0.003 per generation (Claude Haiku 4.5, $1 / MTok
input) and generations are gated to a few per hour per agent.

## Problem

The only way Coral obtains a goal for a standalone agent today is the sparkle
button, which types

```
Emit a ||PULSE:SUMMARY <your current goal>|| line now to update the dashboard with your current goal.
```

into the agent's terminal (`internal/server/frontend/static/controls.js`,
`requestGoal` / `refreshGoal`). That:

- spends an agent turn and adds noise to its context;
- can land mid-task, or on top of a permission prompt where it is misread as
  an answer;
- is easy to fire by accident — the sparkle sits beside the row label
  (see AGENT_BAR_TWEAKS D1 for making the label passive);
- is the *whole* mechanism, not a fallback. `coral-go/agent_docs/` never
  instructs agents to emit `PULSE:SUMMARY`, so without the injection there is
  no goal at all and the sidebar reads `Agent / No goal yet`.

Coral already does the right thing for *ended* sessions:
`internal/background/summarizer.go` condenses the transcript and calls
`claude --print --model haiku` to fill the history Summary tab. Nobody wired
that up for live sessions. This spec does.

## Design

### Output

One CLI call returns a strict JSON object:

```json
{"name": "Store Refactor", "goal": "Move session queries behind the repository interface"}
```

| Field | Rule |
|---|---|
| `name` | 2–3 words. What you would call this agent. Written **once**, on the first successful generation, never overwritten. Identity must not drift. |
| `goal` | ≤ 12 words, imperative, present tense: what the agent is working toward *right now*. Refreshed. |

Parse strictly. On parse failure or empty fields, keep the previous values and
count it as a failure for backoff.

### Input

- The first user prompt, always (already exposed as `first_prompt`, see
  AGENT_BAR_TWEAKS D1).
- The **last ~6 KB** of the condensed transcript. Reuse `condenseMessages` /
  `extractContent` from `summarizer.go` (export them). Tool results are
  already excluded by `extractContent`; keep it that way — they dominate
  byte count and rarely say what the agent is *for*.
- Never the whole transcript. The history summarizer's 30 KB head+tail is
  for a 300-word summary; a one-line goal needs the tail.

### Prompt

Identical text for both CLIs; Codex additionally gets the schema via
`--output-schema`.

```
You label a running AI coding agent for a dashboard. Read the agent's first
instruction and the most recent part of its transcript, then answer with JSON
only, no prose, matching exactly:

{"name": "<2-3 word label for this agent>", "goal": "<what it is working toward right now, <= 12 words, imperative, present tense>"}

Rules:
- "name" is a stable label for the whole job (e.g. "Store Refactor",
  "Flaky WS Tests"), not the current step.
- "goal" is the current step or objective, not a summary of everything done.
- If the transcript shows the agent is waiting for the user, say so in "goal"
  (e.g. "Waiting for approval to delete the old migration").
- Do not mention the dashboard, the transcript, or yourself.

FIRST INSTRUCTION:
<first_prompt>

RECENT TRANSCRIPT:
<tail>
```

### Transport: CLI only (decided)

No API key to manage, works for subscription users, and billing stays on
whatever the user already runs. A direct SDK client is **out of scope**.

**Selection.** Setting `goal_generator_cli` = `auto` (default) | `claude` |
`codex` | `off`.

In `auto`: use the CLI the session itself runs on when it is `claude` or
`codex` (auth and cost stay aligned with the agent) → else `claude` → else
`codex` → else the extractive fallback (first user prompt as the goal, no
name). Gemini and Pi sessions therefore summarize via `claude` or `codex`
when either is installed.

**Binary resolution** — exactly as launch does, through one shared helper:
`cli_path_<type>` from settings (`agent.CLIPathSettingKey`) →
`exec.LookPath` → `agent.FindCLIInCommonPaths`. Do not add another bare
`exec.LookPath("claude")`; there are already three.

**Commands** (flags verified against the installed binaries on 2026-09-15):

```
claude -p --no-session-persistence --model haiku <prompt>

codex exec --ephemeral --skip-git-repo-check -s read-only -C <session working dir> \
           --output-schema <goal.schema.json> -o <tmpfile> [-m <model>] <prompt>
```

- `claude`: `-p` is `--print`; the model's reply is stdout. `--model haiku`
  resolves to the current Haiku (Claude Haiku 4.5 today) — do not pin a
  dated model ID. Same flags the summarizer and workflow runner already use.
- `codex`: `exec` is the non-interactive subcommand. **`-p` on Codex means
  `--profile`, not print.** `--ephemeral` skips session persistence,
  `-s read-only` forbids writes, `--output-schema` enforces the JSON shape,
  and `-o` writes only the final message to a file so progress output on
  stdout is never parsed. Model: the CLI's configured default unless
  `goal_model_codex` is set; a mini-tier model is sufficient and recommended.
- Both: `cmd.Dir` = the session's working directory (Codex also gets `-C`),
  20 s context timeout, `executil.Command` like the summarizer, process group
  set so a timeout kills children.

`goal.schema.json` (embedded with `go:embed`):

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["name", "goal"],
  "properties": {
    "name": {"type": "string", "minLength": 1, "maxLength": 40},
    "goal": {"type": "string", "minLength": 1, "maxLength": 120}
  }
}
```

### Storage

Add to `session_meta` via the `migrations` list in
`internal/store/connection.go`:

| Column | Type | Purpose |
|---|---|---|
| `auto_name` | `TEXT DEFAULT ''` | Write-once label |
| `auto_goal` | `TEXT DEFAULT ''` | Refreshed goal |
| `goal_source_offset` | `INTEGER DEFAULT 0` | Transcript bytes consumed by the last generation; skip when unchanged |
| `goal_generated_at` | `TEXT` | Interval gating |
| `goal_failed_at` | `TEXT` | Backoff |
| `is_goal_user_edited` | `INTEGER DEFAULT 0` | When 1, `auto_goal` is never written (mirrors `is_user_edited` for notes) |

`editGoal` today persists only as a `goal` agent event via
`POST /api/sessions/live/{name}/events`. That handler sets
`is_goal_user_edited = 1` when the event's source is the user; no frontend
change needed for the guard.

### Triggers

1. **First generation.** A live, non-terminal, non-sleeping session with
   ≥ 1 user and ≥ 1 assistant message and no `display_name` / `auto_name`.
2. **Refresh.** Transcript has grown ≥ 4 KB past `goal_source_offset`
   **and** ≥ 5 min since `goal_generated_at` **and** the session is
   `working` or `done`.
3. **On demand.** The sparkle calls `POST /api/sessions/live/{name}/goal`
   (body: `agent_type`, `session_id`). Bypasses the interval, still respects
   `is_goal_user_edited`, still subject to the concurrency guard. Returns
   `202` with `{"goal_pending": true}`; the result arrives on the normal
   WebSocket tick.

The service polls every 30 s (same cadence as `BatchSummarizer`), evaluates
triggers for every live session, and runs at most the allowed concurrency.

### Guards

- Global concurrency 2; per-session in-flight lock.
- 20 s CLI timeout; on timeout or non-zero exit, set `goal_failed_at` and
  back off 15 min for that session.
- Skip terminals and sleeping sessions; skip sessions whose CLI cannot be
  resolved (log once per session, not per tick).
- `goal_generator_cli = off` disables the service and the endpoint returns
  `409`.

### Payload and resolution

In **both** session payload builders — the HTTP list (`routes/sessions.go`
`List`) and the WebSocket tick (`routes/websocket.go`) — add:

- `summary = pulse_summary || auto_goal` (PULSE still wins when an agent
  emits one; keep `pulse.ExtractSummary` — only the injection goes away)
- `auto_name`
- `goal_pending` (true while a generation is in flight)

Frontend identity chain (AGENT_BAR_TWEAKS D1):
`display_name → auto_name → first_prompt → "Agent"`. Row label, avatar
initials, right-pane header and the `Sending to:` placeholder all use it.

The client `coral_diff` merge in `websocket.js` must not drop these fields.
It currently replaces the session object and preserves an allowlist
(`commands`, `icon`, `token_*`, `context_pct`); switch to
`{...old, ...changed}` so fields the WS omits survive by default.

### UI

- `requestGoal` / `refreshGoal` call the endpoint. **No terminal write.**
- The goal line shows a subtle spinner while `goal_pending` is true.
- "No goal yet" is passive text; the sparkle is the only trigger (D1).
- Settings → Agents: `goal_generator_cli` selector and `goal_model_codex`
  text field.

### Teams and workflows

Board agents already carry `board_job_title` as their label; goal generation
applies unchanged (`auto_name` is still written, used only when there is no
title). Workflow-step agents run `--print` and have no live transcript to
watch; skip sessions with `workflow_run_id`.

## Implementation Plan

Backend and frontend are independent; the backend can ship first with the
sparkle still on the old path.

### Phase A — Backend

- `internal/background/goalcli.go` (new): `runGoalCLI(ctx, cli, bin,
  workDir, prompt) (name, goal string, err error)` covering both commands;
  shared binary resolver; strict JSON decode; embedded `goal.schema.json`.
- `internal/background/goals.go` (new): `GoalGenerator` — trigger
  evaluation, gating, concurrency, backoff, transcript tail via the exported
  `condenseMessages`.
- `internal/background/summarizer.go`: export `condenseMessages` /
  `extractContent`; leave its own CLI call in place.
- `internal/store/connection.go`: migrations above.
- `internal/store/sessions.go`: `GetGoalMeta`, `SetAutoGoal`,
  `SetAutoNameOnce`, `SetGoalUserEdited`, `SetGoalPending`.
- `internal/startup/startup.go`: `safeGo(ctx, "goal_generator", ...)` next
  to `batch_summarizer`.
- `internal/server/routes/sessions.go` + `routes/websocket.go`: resolution
  and new fields in **both** builders; `POST /api/sessions/live/{name}/goal`;
  events handler sets `is_goal_user_edited` on user-sourced `goal` events.
- Settings keys `goal_generator_cli`, `goal_model_codex`.

Tests:
- `goalcli_test.go`: command construction for both CLIs (argv exactly as
  above), strict decode accepts the schema and rejects prose / extra keys /
  empty strings; `-o` file read for Codex.
- `goals_test.go`: gating table (first-run, 4 KB / 5 min refresh, backoff,
  user-edited, sleeping, terminal, workflow); `auto_name` written once.
- `routes/sessions_test.go`: endpoint returns `202` + `goal_pending`,
  `409` when off; `List` and the WS payload both carry `auto_name`,
  `auto_goal`-as-`summary`, `goal_pending`.

### Phase B — Frontend

- `controls.js`: `requestGoal` / `refreshGoal` → endpoint.
- `render.js`: identity chain; spinner on `goal_pending`; passive label.
- `websocket.js`: spread-merge.
- `modals.js` / settings template: the two settings.
- `tests/frontend/agent_bar_tweaks.test.js`: assert the sparkle sends **no**
  `PULSE` text to the terminal, and run one scenario with the WebSocket
  **unblocked** so the diff-merge path is exercised (the existing test blocks
  it, which is how the `first_prompt` drop went unnoticed).

### Verification

Launch an agent with a prompt and touch nothing. Within ~1 min the row shows
a name and a goal. The agent's terminal contains no `Emit a ||PULSE` text.
The sparkle regenerates without writing to the terminal. A hand-edited goal
is never overwritten. Kill the `claude` binary from `PATH`: the row falls
back to the first prompt and the log shows one skip line, not one per tick.

## Open Questions

- Should `auto_name` ever refresh (e.g. when the user clears it), or is
  write-once the right contract for the lifetime of a session?
- Is 5 min / 4 KB the right refresh gate, or should `done` transitions force
  a refresh immediately so the goal reads as finished?
- Should Gemini sessions prefer `gemini` non-interactive mode once its flags
  are verified, for the same "same CLI as the agent" reason?
