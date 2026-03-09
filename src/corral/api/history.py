"""API routes for historical session data."""

from __future__ import annotations

import asyncio
import logging
from typing import Optional, TYPE_CHECKING

from fastapi import APIRouter, Query

from corral.session_manager import (
    load_history_sessions,
    load_history_session_messages,
)

if TYPE_CHECKING:
    from corral.store import CorralStore

log = logging.getLogger(__name__)

router = APIRouter()

# Module-level dependency, set by web_server.py during app setup
store: CorralStore = None  # type: ignore[assignment]


@router.get("/api/sessions/history")
async def get_history_sessions(
    page: int = Query(1, ge=1),
    page_size: int = Query(50, ge=1, le=200),
    q: Optional[str] = Query(None),
    tag_id: Optional[int] = Query(None),
    source_type: Optional[str] = Query(None),
):
    """Paginated history sessions from the index, with search/tag/source filters."""
    result = await store.list_sessions_paged(page, page_size, q, tag_id, source_type)

    if result["total"] == 0 and not q and not tag_id and not source_type:
        # Cold start — index hasn't run yet; trigger immediate index and fall back
        from fastapi import Request
        # Access app.state.indexer via the store's parent app
        indexer = getattr(_app, "state", None) and getattr(_app.state, "indexer", None) if _app else None
        if indexer:
            try:
                await indexer.run_once()
                result = await store.list_sessions_paged(
                    page, page_size, q, tag_id, source_type
                )
            except Exception:
                pass

        # If still empty, fall back to old file-scan method
        if result["total"] == 0:
            sessions = load_history_sessions()
            metadata = await store.get_all_session_metadata()
            for s in sessions:
                meta = metadata.get(s["session_id"])
                if meta:
                    s["tags"] = meta["tags"]
                    s["has_notes"] = meta["has_notes"]
                else:
                    s["tags"] = []
                    s["has_notes"] = False
            return {"sessions": sessions, "total": len(sessions), "page": 1, "page_size": len(sessions)}

    return result


@router.post("/api/indexer/refresh")
async def trigger_indexer_refresh():
    """Trigger an immediate re-index."""
    indexer = getattr(_app.state, "indexer", None) if _app else None
    if not indexer:
        return {"error": "Indexer not available"}
    result = await indexer.run_once()
    return result


@router.get("/api/sessions/history/{session_id}")
async def get_history_session_detail(session_id: str):
    """Get all messages for a historical session."""
    messages = load_history_session_messages(session_id)
    if not messages:
        return {"error": f"Session '{session_id}' not found"}
    return {"session_id": session_id, "messages": messages}


@router.get("/api/sessions/history/{session_id}/git")
async def get_history_session_git(session_id: str):
    """Return git commits that occurred during a historical session's time range."""
    snapshots = await store.get_git_snapshots_for_session(session_id)
    return {"session_id": session_id, "commits": snapshots}


@router.get("/api/sessions/history/{session_id}/tasks")
async def get_history_session_tasks(session_id: str):
    """Get tasks for a historical session (read-only)."""
    return await store.list_tasks_by_session(session_id)


@router.get("/api/sessions/history/{session_id}/agent-notes")
async def get_history_session_agent_notes(session_id: str):
    """Get agent notes for a historical session (read-only)."""
    return await store.list_notes_by_session(session_id)


@router.get("/api/sessions/history/{session_id}/events")
async def get_history_session_events(session_id: str, limit: int = Query(200, ge=1, le=500)):
    """Get activity events for a historical session (read-only)."""
    return await store.list_events_by_session(session_id, limit)


@router.get("/api/sessions/history/{session_id}/notes")
async def get_session_notes(session_id: str):
    """Get notes and auto-summary for a session. Triggers auto-summarization if empty."""
    notes = await store.get_session_notes(session_id)

    if not notes["notes_md"] and not notes["auto_summary"]:
        try:
            from corral.background_tasks import AutoSummarizer
            summarizer = AutoSummarizer(store)
            asyncio.create_task(summarizer.summarize_session(session_id))
            notes["summarizing"] = True
        except ImportError:
            notes["summarizing"] = False

    return notes


@router.put("/api/sessions/history/{session_id}/notes")
async def save_session_notes(session_id: str, body: dict):
    """Save user-edited markdown notes for a session."""
    notes_md = body.get("notes_md", "")
    await store.save_session_notes(session_id, notes_md)
    return {"ok": True}


@router.post("/api/sessions/history/{session_id}/resummarize")
async def resummarize_session(session_id: str):
    """Force re-generate auto-summary for a session."""
    try:
        from corral.background_tasks import AutoSummarizer
        summarizer = AutoSummarizer(store)
        summary = await summarizer.summarize_session(session_id)
        return {"ok": True, "auto_summary": summary}
    except ImportError:
        return {"error": "claude-agent-sdk not installed"}
    except Exception as e:
        return {"error": str(e)}


# ── Tags on historical sessions ────────────────────────────────────────────


@router.get("/api/sessions/history/{session_id}/tags")
async def get_session_tags(session_id: str):
    return await store.get_session_tags(session_id)


@router.post("/api/sessions/history/{session_id}/tags")
async def add_session_tag(session_id: str, body: dict):
    tag_id = body.get("tag_id")
    if tag_id is None:
        return {"error": "tag_id is required"}
    await store.add_session_tag(session_id, int(tag_id))
    return {"ok": True}


@router.delete("/api/sessions/history/{session_id}/tags/{tag_id}")
async def remove_session_tag(session_id: str, tag_id: int):
    await store.remove_session_tag(session_id, tag_id)
    return {"ok": True}


# App reference for accessing app.state.indexer — set by web_server.py
_app = None
