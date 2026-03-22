"""FastAPI router for the inter-agent message board."""

from __future__ import annotations

import asyncio
import logging
from typing import Any

import httpx
from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from coral.messageboard.store import MessageBoardStore

log = logging.getLogger(__name__)

router = APIRouter()

# Set by app.py during create_app()
store: MessageBoardStore = None  # type: ignore[assignment]

# In-memory set of paused projects (operator can pause/resume reads)
_paused_projects: set[str] = set()


# ── Request models ───────────────────────────────────────────────────────

class SubscribeRequest(BaseModel):
    session_id: str
    job_title: str
    webhook_url: str | None = None
    receive_mode: str = "mentions"
    check_mode: str | None = None  # Alias for receive_mode


class UnsubscribeRequest(BaseModel):
    session_id: str


class PostMessageRequest(BaseModel):
    session_id: str
    content: str
    target_group_id: str | None = None


# ── Endpoints ────────────────────────────────────────────────────────────

@router.get("/projects")
async def list_projects():
    return await store.list_projects()


@router.get("/{project}/subscribers")
async def list_subscribers(project: str):
    return await store.list_subscribers(project)


@router.post("/{project}/subscribe")
async def subscribe(project: str, body: SubscribeRequest):
    if body.webhook_url:
        from coral.api.webhooks import _validate_url
        url_error = _validate_url(body.webhook_url, "generic")
        if url_error:
            raise HTTPException(status_code=400, detail=f"Invalid webhook_url: {url_error}")
    # check_mode is an alias for receive_mode
    receive_mode = body.check_mode or body.receive_mode
    return await store.subscribe(
        project, body.session_id, body.job_title, body.webhook_url,
        receive_mode=receive_mode,
    )


@router.delete("/{project}/subscribe")
async def unsubscribe(project: str, body: UnsubscribeRequest):
    removed = await store.unsubscribe(project, body.session_id)
    if not removed:
        raise HTTPException(status_code=404, detail="Subscriber not found")
    return {"ok": True}


@router.post("/{project}/messages")
async def post_message(project: str, body: PostMessageRequest):
    message = await store.post_message(
        project, body.session_id, body.content,
        target_group_id=body.target_group_id,
    )

    # Fire-and-forget webhook dispatch
    asyncio.create_task(_dispatch_webhooks(project, body.session_id, message))

    return message


@router.get("/{project}/messages")
async def read_messages(project: str, session_id: str, limit: int = 50):
    if project in _paused_projects:
        return []
    return await store.read_messages(project, session_id, limit)


@router.get("/{project}/messages/check")
async def check_unread(project: str, session_id: str):
    if project in _paused_projects:
        return {"unread": 0}
    count = await store.check_unread(project, session_id)
    return {"unread": count}


@router.get("/{project}/messages/all")
async def list_messages(
    project: str, limit: int = 200, offset: int = 0,
    before_id: int | None = None, format: str | None = None,
):
    from coral.config import BOARD_MAX_LIMIT
    limit = min(limit, BOARD_MAX_LIMIT)
    messages = await store.list_messages(project, limit, offset, before_id=before_id)
    # Default: bare array for CLI consumers. format=dashboard wraps with metadata.
    if format == "dashboard":
        total = await store.count_messages(project)
        return {"messages": messages, "total": total, "limit": limit, "offset": offset}
    return messages


@router.delete("/{project}/messages/{message_id}")
async def delete_message(project: str, message_id: int):
    removed = await store.delete_message(message_id)
    if not removed:
        raise HTTPException(status_code=404, detail="Message not found")
    return {"ok": True}


@router.post("/{project}/pause")
async def pause_reads(project: str):
    _paused_projects.add(project)
    return {"ok": True, "paused": True}


@router.post("/{project}/resume")
async def resume_reads(project: str):
    _paused_projects.discard(project)
    return {"ok": True, "paused": False}


@router.get("/{project}/paused")
async def get_paused(project: str):
    return {"paused": project in _paused_projects}


@router.delete("/{project}")
async def delete_project(project: str):
    _paused_projects.discard(project)
    await store.delete_project(project)
    return {"ok": True}


# ── Group management ─────────────────────────────────────────────────────

class GroupMemberRequest(BaseModel):
    session_id: str


@router.get("/{project}/groups")
async def list_groups(project: str):
    return await store.list_groups(project)


@router.get("/{project}/groups/{group_id}/members")
async def list_group_members(project: str, group_id: str):
    return await store.list_group_members(project, group_id)


@router.post("/{project}/groups/{group_id}/members")
async def add_group_member(project: str, group_id: str, body: GroupMemberRequest):
    await store.add_to_group(project, group_id, body.session_id)
    return {"ok": True}


@router.delete("/{project}/groups/{group_id}/members/{session_id}")
async def remove_group_member(project: str, group_id: str, session_id: str):
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
