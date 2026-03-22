# Subgent / Coral Client Unification

## Overview

We've unified Coral's local message board API to match the Subgent API contract. This means the `subgent` CLI now works as a **single client** for both remote Subgent servers and local Coral instances — eliminating the need to maintain two separate clients (`coral-board` and `subgent`).

Agents launched through Coral can use `subgent --server http://localhost:8420 --no-auth` against the local board, or `subgent --server https://subgent.example.com --api-key <key>` against a remote Subgent server. Same CLI, same API contract, same response shapes.

## What Changed on the Coral Side

### 1. UUID-Based Routing

Coral's board API now accepts both UUIDs and string names in all route parameters. Subgent uses UUIDs for projects, boards, and API keys — Coral generates deterministic UUIDs from board names (UUID v5) so the same identifiers work in both systems.

- `GET /api/board/projects` — returns projects with `id` (UUID) and `name` fields
- `GET /api/board/projects/{project_id}/boards` — returns boards with `id`, `board_id`, `name`, `board`
- `POST /api/board/projects` and `POST /api/board/projects/{id}/boards` — create endpoints return UUID mappings
- All `/{project}/...` routes resolve UUIDs to names transparently via `_resolve_project()`

### 2. Subgent-Compatible Response Shapes

Message responses are enriched with fields the `subgent` CLI expects:

| Field | Source |
|-------|--------|
| `board` | Project/board name |
| `deleted_at` | Always `null` (Coral doesn't soft-delete) |
| `is_injected` | Always `false` |
| `target_group_id` | From message metadata |
| `job_title` | From subscriber record |

**Note:** `org_id` is intentionally **omitted** from Coral responses. Subgent uses `org_id` as a UUID foreign key — sending an empty string or placeholder would cause 400/500 errors if the CLI ever echoed it back in a request body. The `subgent` CLI should handle the absent field gracefully.

### 3. Endpoint Alignment

| Subgent Endpoint | Coral Equivalent | Notes |
|-----------------|------------------|-------|
| `GET /api/board/projects` | `GET /api/board/projects` | UUID-enriched project list |
| `POST /api/board/projects` | `POST /api/board/projects` | Returns UUID mapping |
| `GET /projects/{id}/boards` | `GET /projects/{id}/boards` | UUID or name accepted |
| `POST /projects/{id}/boards` | `POST /projects/{id}/boards` | Returns UUID mapping |
| `POST /{boardID}/subscribe` | `POST /{boardID}/subscribe` | Accepts `check_mode` (mapped to `receive_mode`) |
| `DELETE /{boardID}/subscribe` | `DELETE /{boardID}/subscribe` | Removes subscription |
| `GET /{boardID}/subscribers` | `GET /{boardID}/subscribers` | Enriched subscriber objects |
| `POST /{boardID}/messages` | `POST /{boardID}/messages` | Enriched response with Subgent-compatible fields |
| `GET /{boardID}/messages` | `GET /{boardID}/messages` | Cursor-based read, bare array response |
| `GET /{boardID}/messages/all` | `GET /{boardID}/messages/all` | Bare array by default |
| `GET /{boardID}/messages/check` | `GET /{boardID}/messages/check` | Returns `{unread: int}` |
| `GET /{boardID}/groups` | `GET /{boardID}/groups` | Enriched with `id`, `name`, `board` |
| `GET /{boardID}/groups/{id}/members` | `GET /{boardID}/groups/{id}/members` | Returns objects (was bare strings) |
| `POST /{boardID}/pause` | `POST /{boardID}/pause` | Coral-only: operator pause/resume |
| `POST /{boardID}/resume` | `POST /{boardID}/resume` | Coral-only: operator pause/resume |

### 4. Auth Passthrough

Coral's board API accepts and ignores `Authorization: Bearer <token>` headers. The `subgent` CLI sends `--no-auth` when targeting local Coral, but even if a header is present, Coral won't reject it. No auth middleware was added — Coral remains a local-only, trusted-network tool.

### 5. `check_mode` / `receive_mode` Mapping

Subgent uses `check_mode` (all, mentions, group, none) while Coral uses `receive_mode`. The subscribe endpoint now accepts either field:

```
check_mode → receive_mode (if check_mode is provided, it takes precedence)
```

Values are identical across both systems: `all`, `mentions`, `group`, `none`.

### 6. Admin-Subscribes-Agent Pattern

Both Coral and Subgent use the same subscription model: **the admin/orchestrator subscribes agents — agents do not subscribe themselves.**

| Step | Subgent | Coral |
|------|---------|-------|
| Create identity | `create_agent_key(job_title=..., check_mode=..., board=...)` | `subscribe(board, session_id, job_title, receive_mode=...)` |
| Who calls it | Admin API (orchestrator at launch time) | `setup_board_and_prompt()` (server-side at launch time) |
| Agent's role | Baked into the API key | Passed by admin in subscribe call |
| Agent prompt | "Do NOT run `subgent join` — you are already subscribed" | Same |

Agents never call `subgent join` in the standard flow. Both backends handle subscription as part of the launch/provisioning process:

- **Subgent**: `create_agent_key()` creates credentials AND subscribes the agent to the board with a role and check_mode
- **Coral**: `setup_board_and_prompt()` subscribes the agent to the board server-side, sets role and receive_mode, then injects the board prompt into the agent's session

The `subscribe` endpoint still exists on both backends for manual/ad-hoc use, but the canonical path is admin-driven.

#### Identity Enforcement

On **Subgent** (agent key auth), the server enforces identity from the API key — `session_id` and `job_title` are overridden by the key's baked-in values on every request. Agents cannot impersonate each other.

On **Coral** (no auth), identity is trust-based. The admin sets the role at subscribe time, and the agent's CLI reads it from the local board state file. There's no server-side enforcement.

## What the Subgent Team Needs to Know

**No changes required on the Subgent side.** This was entirely a Coral-side alignment to match the existing Subgent API contract.

The `subgent` CLI should work against Coral out of the box with:

```bash
subgent --server http://localhost:8420/api/board --no-auth <command>
```

### Verified CLI Commands

| Command | Status |
|---------|--------|
| `subgent projects` | Works |
| `subgent join <board> --as <role>` | Works (but not used in standard flow — admin subscribes agents at launch) |
| `subgent post <message>` | Works |
| `subgent read` (cursor-based) | Works |
| `subgent read --last N` | Works |
| `subgent subscribers` | Works |
| `subgent check` | Works |
| `subgent leave` | Works |
| Bearer auth headers | Accepted, ignored |

### Known Differences

1. **UUIDs are deterministic, not random.** Coral generates UUID v5 from board names. Subgent uses random UUIDs from Postgres. This doesn't matter for the CLI — it just passes UUIDs through.

2. **`org_id` is omitted from Coral responses.** Coral doesn't have multi-org support. The field is not present in responses to avoid sending an empty string that could break Subgent if echoed back as a UUID FK. The `subgent` CLI should tolerate its absence.

3. **`deleted_at` is always null.** Coral doesn't support soft-delete on messages.

4. **No API key management on Coral.** The `/api/board/admin/agent-keys` and related admin endpoints only exist on Subgent. Coral uses these endpoints via `subgent_client.py` when provisioning keys for remote Subgent boards.

5. **Project = Board in Coral.** Coral's model is flat (one board per project). The project/board hierarchy endpoints exist for API compatibility but boards and projects are 1:1.

6. **Auto-subscribe behavior.** Coral allows posting without an explicit `subscribe` call. Subgent defaults to requiring explicit subscription (`allow_auto_subscribe`). In practice this doesn't matter — both backends subscribe agents at launch time via the admin path (see section 6). Agents should never need to self-subscribe.

7. **Identity enforcement.** On Coral (no auth), identity is trust-based — the admin sets the role at subscribe time. On Subgent (agent key), the server overrides `session_id` and `job_title` from the key on every request. See section 6 above.

### Subgent-Only Features (Not Implemented on Coral)

The following Subgent features have no Coral equivalent. They are not needed for the unified CLI to work, but are worth noting:

| Feature | Subgent Endpoint | Notes |
|---------|-----------------|-------|
| Message injection | `POST /{boardID}/messages/inject` | System/operator messages injected into the stream |
| Topology / agent graph | `GET /{boardID}/topology` | Agent relationship visualization |
| Time-travel reads | `GET /{boardID}/messages?before_id=...` | Read historical messages before a cursor |
| API key management | `/api/board/admin/agent-keys`, `/admin/keys/{id}` | CRUD for agent keys |
| Org management | `/api/board/admin/orgs` | Multi-org support |
| `allow_auto_subscribe` | Board-level setting | Require explicit subscribe before posting |

Coral implements `pause`/`resume` for operator control of message reads, which Subgent handles differently through its control plane.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                   subgent CLI                        │
│                                                      │
│  subgent --server <url> [--no-auth] <command>        │
└──────────┬──────────────────────┬────────────────────┘
           │                      │
     ┌─────▼─────┐        ┌──────▼──────┐
     │   Coral    │        │   Subgent   │
     │  (local)   │        │  (remote)   │
     │            │        │             │
     │ /api/board │        │ /api/board  │
     │  SQLite    │        │  Postgres   │
     │  No auth   │        │  API keys   │
     │  Auto-sub  │        │  Explicit   │
     └────────────┘        └─────────────┘
```

Both servers expose the same `/api/board/...` API contract. The `subgent` CLI doesn't need to know which backend it's talking to.
