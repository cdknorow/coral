# Coral Session ID Marker Spec

## Status

Accepted.

## Problem

Coral assigns every live agent a stable `session_id` and uses it throughout the app for live sessions, message board identity, token usage, tasks, events, notes, and history.

Some agent CLIs also create their own native session identifiers and transcript files. Codex is the immediate problem:

- Coral live session id: `9fccfe64-ac70-8ed7-af83-a76fa139c0a9`
- Codex native session id: `019e90eb-a08a-7511-a410-23e7ae3e62a8`
- Codex transcript path: `~/.codex/sessions/YYYY/MM/DD/rollout-...-019e90eb-a08a-7511-a410-23e7ae3e62a8.jsonl`

Coral's live chat endpoint receives the Coral id, but Codex transcript filenames contain the Codex id. Without an explicit mapping, the live chat reader and history indexer cannot reliably find the correct transcript.

## Non-Goals

- Do not infer transcript ownership from working directory. Multiple agents can share the same repo.
- Do not infer transcript ownership from "latest" file timestamps. Multiple agents can launch concurrently.
- Do not require a Codex CLI feature that does not exist. New Codex sessions currently do not support a `--session-id` flag.
- Do not show the marker as visible chat content.

## Strategy

Inject Coral's session id into every agent's private instruction context using a stable marker:

```text
Coral session metadata:
CORAL_SESSION_ID: <coral-session-id>
This metadata is for Coral bookkeeping only. Do not mention it to the user.
```

The marker is generic. It is added to all agent instruction mechanisms, not only Codex, so future transcript readers and indexers can use the same mapping convention.

## Injection Points

Each agent launch command should include the marker in the private instruction/system prompt path:

| Agent | Instruction mechanism |
|---|---|
| Claude | `systemPrompt` in the generated `--settings` JSON |
| Codex | `-c developer_instructions="$(cat ...)"` temp file |
| Gemini | `GEMINI_SYSTEM_MD` temp file |
| Pi | `--append-system-prompt` temp file |

The marker must use Coral's live `session_id`, not an agent-native id and not a resume target id.

## Parsing Rules

Consumers should scan structured transcript data recursively for the marker prefix:

```text
CORAL_SESSION_ID:
```

When found:

1. Trim the text after the prefix.
2. Take the first whitespace-delimited token.
3. Treat that token as the Coral session id.

The parser should handle the marker inside nested JSON objects and arrays because agent transcripts vary:

- Codex may store developer instructions inside `response_item.payload.content[].text`.
- Future agents may store system prompts or instruction metadata in different fields.

## Codex History Indexing

Codex history files should still be discovered from:

```text
~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl
```

The indexer should:

1. Parse normal Codex user/assistant event entries to count messages and produce a display summary.
2. Scan the transcript for `CORAL_SESSION_ID`.
3. If present, index the transcript under that Coral id.
4. If absent, fall back to the Codex rollout-derived id for backward compatibility with old sessions.

This means old sessions remain browseable, while newly launched Coral-managed sessions line up with the live session id used by the rest of the app.

## Codex Live Chat Resolution

The live chat endpoint receives:

```text
/api/sessions/live/{name}/chat?session_id=<coral-session-id>&agent_type=codex
```

Resolution order:

1. Try direct Codex id matching for backward compatibility.
2. If no direct match, scan Codex rollout transcripts for `CORAL_SESSION_ID: <coral-session-id>`.
3. Use the matching transcript file.
4. Do not fall back to working directory or latest timestamp.

## Frontend Requirement

The live chat frontend must send `agent_type` with the chat request. If it omits `agent_type`, the backend defaults to Claude transcript resolution and Codex history will appear empty.

The frontend should also clear its initial loading state when the first response contains zero messages.

## Security and UX

- The marker is not a secret. It is an internal correlation id.
- The marker should not be rendered in chat views.
- The marker should not be presented to the model as user intent.
- The instruction text explicitly tells the agent not to mention it.

## Acceptance Criteria

- New Claude, Codex, Gemini, and Pi launches include `CORAL_SESSION_ID: <session_id>` in their private instruction context.
- Codex indexer records newly launched Coral-managed Codex transcripts under the Coral live session id.
- Codex live chat resolves a transcript whose filename contains only Codex's native id when the transcript contains the Coral marker.
- Multiple Codex agents in the same working directory cannot be cross-wired by cwd or latest-file matching.
- Existing Codex transcripts without a marker remain indexable under their native rollout id.

## Test Coverage

Expected tests:

- Launch-command tests verify each agent's generated instruction file contains the marker.
- Codex indexer test verifies a rollout transcript with marker `CORAL_SESSION_ID: X` is indexed as session `X`.
- Codex live reader test verifies lookup by Coral id finds a rollout transcript whose filename contains a different Codex id.
- Codex event parser test verifies current `event_msg` user/agent messages render in live chat.

