"""FastAPI router for the inter-agent message board."""

from __future__ import annotations

import asyncio
import logging
import uuid
from typing import Any, Optional

import httpx
from fastapi import APIRouter, HTTPException, Query
from pydantic import BaseModel

from coral.messageboard.store import MessageBoardStore

log = logging.getLogger(__name__)

router = APIRouter()

# Set by app.py during create_app()
store: MessageBoardStore = None  # type: ignore[assignment]

# In-memory set of paused projects (operator can pause/resume reads)
_paused_projects: set[str] = set()


# ── UUID helpers (Task 4) ────────────────────────────────────────────────

def _name_to_uuid(name: str) -> str:
    """Deterministic UUID from a board/project name."""
    return str(uuid.uuid5(uuid.NAMESPACE_URL, f"coral-board:{name}"))


async def _resolve_project(project_or_uuid: str) -> str:
    """Resolve a project path param that may be a UUID or a name.

    Checks known projects first (UUID lookup), falls back to treating the
    param as a literal project name.
    """
    # Fast path: if it doesn't look like a UUID, skip the lookup
    try:
        uuid.UUID(project_or_uuid)
    except ValueError:
        return project_or_uuid

    # It parses as a UUID — check if any known project maps to it
    projects = await store.list_projects()
    for p in projects:
        if _name_to_uuid(p["project"]) == project_or_uuid:
            return p["project"]

    # No match — return as-is (could be a name that happens to look like a UUID)
    return project_or_uuid


# ── Subgent-compatible response enrichment ───────────────────────────────

def _enrich_message(msg: dict[str, Any]) -> dict[str, Any]:
    """Add Subgent-compatible fields to a message dict."""
    msg.setdefault("board", msg.get("project", ""))
    # org_id intentionally omitted — Subgent uses UUID FK; empty string would cause 400/500
    msg.setdefault("deleted_at", None)
    msg.setdefault("is_injected", False)
    msg.setdefault("target_group_id", None)
    # job_title should already be present from the store query
    msg.setdefault("job_title", "Unknown")
    return msg


# ── Request models ───────────────────────────────────────────────────────

class SubscribeRequest(BaseModel):
    session_id: str
    job_title: str
    webhook_url: str | None = None
    receive_mode: str = "mentions"
    check_mode: str | None = None  # Subgent alias for receive_mode
    locked: bool | None = None  # Accepted, ignored


class UnsubscribeRequest(BaseModel):
    session_id: str


class PostMessageRequest(BaseModel):
    session_id: str
    content: str
    target_group_id: str | None = None


class CreateProjectRequest(BaseModel):
    name: str


class CreateBoardRequest(BaseModel):
    name: str


# ── Project/Board management endpoints (Task 1) ─────────────────────────
# These MUST come before /{project}/... routes to avoid path conflicts.

@router.get("/projects")
async def list_projects():
    projects = await store.list_projects()
    # Enrich with id/name fields for Subgent compatibility
    for p in projects:
        p["id"] = _name_to_uuid(p["project"])
        p["name"] = p["project"]
    return projects


@router.post("/projects")
async def create_project(body: CreateProjectRequest):
    # In Coral's flat model, project = board. Just return the UUID mapping.
    return {"id": _name_to_uuid(body.name), "name": body.name}


@router.get("/projects/{project_id}/boards")
async def list_boards_for_project(project_id: str):
    project = await _resolve_project(project_id)
    board_uuid = _name_to_uuid(project)
    return [
        {
            "id": board_uuid,
            "name": project,
            "board_id": board_uuid,
            "board": project,
        }
    ]


@router.post("/projects/{project_id}/boards")
async def create_board_for_project(project_id: str, body: CreateBoardRequest):
    # In Coral's flat model, creating a board is a no-op — just return the UUID.
    return {"id": _name_to_uuid(body.name), "name": body.name}


# ── Subscriber endpoints ────────────────────────────────────────────────

@router.get("/{project}/subscribers")
async def list_subscribers(project: str):
    project = await _resolve_project(project)
    return await store.list_subscribers(project)


@router.post("/{project}/subscribe")
async def subscribe(project: str, body: SubscribeRequest):
    project = await _resolve_project(project)

    # check_mode is Subgent's alias for receive_mode (Task 3)
    receive_mode = body.check_mode if body.check_mode is not None else body.receive_mode

    if body.webhook_url:
        from coral.api.webhooks import _validate_url
        url_error = _validate_url(body.webhook_url, "generic")
        if url_error:
            raise HTTPException(status_code=400, detail=f"Invalid webhook_url: {url_error}")

    result = await store.subscribe(
        project, body.session_id, body.job_title, body.webhook_url,
        receive_mode=receive_mode,
    )

    # Enrich response for Subgent compatibility (Task 3)
    result["check_mode"] = result.get("receive_mode", receive_mode)
    # org_id intentionally omitted — Subgent uses UUID FK
    result["board"] = project
    return result


@router.delete("/{project}/subscribe")
async def unsubscribe(project: str, body: UnsubscribeRequest):
    project = await _resolve_project(project)
    removed = await store.unsubscribe(project, body.session_id)
    if not removed:
        raise HTTPException(status_code=404, detail="Subscriber not found")
    return {"ok": True}


# ── Message endpoints ───────────────────────────────────────────────────

@router.post("/{project}/messages")
async def post_message(project: str, body: PostMessageRequest):
    project = await _resolve_project(project)
    message = await store.post_message(
        project, body.session_id, body.content,
        target_group_id=body.target_group_id,
    )

    # Enrich response (Task 2)
    _enrich_message(message)

    # Fire-and-forget webhook dispatch
    asyncio.create_task(_dispatch_webhooks(project, body.session_id, message))

    return message


@router.get("/{project}/messages")
async def read_messages(project: str, session_id: str, limit: int = 50):
    project = await _resolve_project(project)
    if project in _paused_projects:
        return []
    messages = await store.read_messages(project, session_id, limit)
    return [_enrich_message(m) for m in messages]


@router.get("/{project}/messages/check")
async def check_unread(project: str, session_id: str):
    project = await _resolve_project(project)
    if project in _paused_projects:
        return {"unread": 0}
    count = await store.check_unread(project, session_id)
    return {"unread": count}


@router.get("/{project}/messages/all")
async def list_messages(
    project: str,
    limit: int = 200,
    offset: int = 0,
    before_id: Optional[int] = Query(None),
    format: Optional[str] = Query(None),
):
    project = await _resolve_project(project)
    from coral.config import BOARD_MAX_LIMIT
    limit = min(limit, BOARD_MAX_LIMIT)
    messages = await store.list_messages(project, limit, offset, before_id=before_id)
    enriched = [_enrich_message(m) for m in messages]

    # Default: bare array (Subgent-compatible). format=dashboard returns wrapped object.
    if format == "dashboard":
        total = await store.count_messages(project)
        return {"messages": enriched, "total": total, "limit": limit, "offset": offset}

    return enriched


@router.delete("/{project}/messages/{message_id}")
async def delete_message(project: str, message_id: int):
    project = await _resolve_project(project)
    removed = await store.delete_message(message_id)
    if not removed:
        raise HTTPException(status_code=404, detail="Message not found")
    return {"ok": True}


# ── Pause/Resume ────────────────────────────────────────────────────────

@router.post("/{project}/pause")
async def pause_reads(project: str):
    project = await _resolve_project(project)
    _paused_projects.add(project)
    return {"ok": True, "paused": True}


@router.post("/{project}/resume")
async def resume_reads(project: str):
    project = await _resolve_project(project)
    _paused_projects.discard(project)
    return {"ok": True, "paused": False}


@router.get("/{project}/paused")
async def get_paused(project: str):
    project = await _resolve_project(project)
    return {"paused": project in _paused_projects}


@router.delete("/{project}")
async def delete_project(project: str):
    project = await _resolve_project(project)
    _paused_projects.discard(project)
    await store.delete_project(project)
    return {"ok": True}


# ── Group management ─────────────────────────────────────────────────────

class GroupMemberRequest(BaseModel):
    session_id: str


@router.get("/{project}/groups")
async def list_groups(project: str):
    project = await _resolve_project(project)
    groups = await store.list_groups(project)
    # Enrich with Subgent-compatible fields
    for g in groups:
        g["id"] = g["group_id"]
        g["name"] = g["group_id"]
        g["board"] = project
    return groups


@router.get("/{project}/groups/{group_id}/members")
async def list_group_members(project: str, group_id: str):
    project = await _resolve_project(project)
    session_ids = await store.list_group_members(project, group_id)
    # Return as objects with session_id for Subgent compatibility
    return [{"session_id": sid, "group_id": group_id} for sid in session_ids]


@router.post("/{project}/groups/{group_id}/members")
async def add_group_member(project: str, group_id: str, body: GroupMemberRequest):
    project = await _resolve_project(project)
    await store.add_to_group(project, group_id, body.session_id)
    return {"ok": True, "group_id": group_id, "session_id": body.session_id}


@router.delete("/{project}/groups/{group_id}/members/{session_id}")
async def remove_group_member(project: str, group_id: str, session_id: str):
    project = await _resolve_project(project)
    await store.remove_from_group(project, group_id, session_id)
    return {"ok": True}


# ── Webhook dispatch ─────────────────────────────────────────────────────

async def _dispatch_webhooks(
    project: str, sender_session_id: str, message: dict[str, Any]
) -> None:
    targets = await store.get_webhook_targets(project, sender_session_id)
    if not targets:
        return

    # Look up sender's job_title
    subscribers = await store.list_subscribers(project)
    sender_title = "Unknown"
    for s in subscribers:
        if s["session_id"] == sender_session_id:
            sender_title = s["job_title"]
            break

    payload = {
        "project": project,
        "message": {
            "id": message["id"],
            "session_id": message["session_id"],
            "job_title": sender_title,
            "content": message["content"],
            "created_at": message["created_at"],
        },
    }

    async with httpx.AsyncClient(timeout=5.0) as client:
        tasks = []
        for target in targets:
            tasks.append(_send_webhook(client, target["webhook_url"], payload))
        await asyncio.gather(*tasks, return_exceptions=True)


async def _send_webhook(
    client: httpx.AsyncClient, url: str, payload: dict[str, Any]
) -> None:
    try:
        await client.post(url, json=payload)
    except Exception:
        log.debug("Webhook delivery failed for %s", url, exc_info=True)
