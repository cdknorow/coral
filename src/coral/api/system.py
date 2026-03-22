"""API routes for system-level operations: settings, tags, filesystem."""

from __future__ import annotations

import os
import logging
from typing import TYPE_CHECKING

from fastapi import APIRouter

if TYPE_CHECKING:
    from coral.store import CoralStore

log = logging.getLogger(__name__)

router = APIRouter()

# Module-level dependency, set by web_server.py during app setup
store: CoralStore = None  # type: ignore[assignment]

# Keys that should never be returned to the frontend via GET /api/settings
_SENSITIVE_KEYS: set[str] = set()


@router.get("/api/system/status")
async def system_status():
    """Return system startup status."""
    from coral.web_server import app
    return {"startup_complete": getattr(app.state, "startup_complete", False)}


@router.get("/api/system/update-check")
async def update_check():
    """Return update availability info from cached check."""
    from coral.web_server import app
    info = getattr(app.state, "update_info", None)
    if info is None or not info.current:
        return {"available": False, "current": "unknown"}
    result = {
        "available": info.available,
        "current": info.current,
    }
    if info.available:
        result["latest"] = info.latest
        result["release_notes"] = info.release_notes
        result["release_url"] = info.release_url
        result["upgrade_command"] = "pip install --upgrade agent-coral"
    return result


@router.get("/api/settings")
async def get_settings():
    """Return all global user settings, filtering out sensitive keys."""
    settings = await store.get_settings()
    if _SENSITIVE_KEYS:
        settings = {k: v for k, v in settings.items() if k not in _SENSITIVE_KEYS}
    return {"settings": settings}


@router.get("/api/settings/default-prompts")
async def get_default_prompts():
    """Return the hardcoded default prompt templates for Reset to Default."""
    from coral.tools.session_manager import DEFAULT_ORCHESTRATOR_PROMPT, DEFAULT_WORKER_PROMPT
    from coral.agents.base import DEFAULT_ORCHESTRATOR_SYSTEM_PROMPT, DEFAULT_WORKER_SYSTEM_PROMPT
    return {
        "default_prompt_orchestrator": DEFAULT_ORCHESTRATOR_PROMPT,
        "default_prompt_worker": DEFAULT_WORKER_PROMPT,
        "default_system_prompt_orchestrator": DEFAULT_ORCHESTRATOR_SYSTEM_PROMPT,
        "default_system_prompt_worker": DEFAULT_WORKER_SYSTEM_PROMPT,
        "team_reminder_orchestrator": "Remember to coordinate with your team and check the message board for updates",
        "team_reminder_worker": "Remember to work with your team",
    }


@router.put("/api/settings")
async def put_settings(body: dict):
    """Upsert one or more global user settings."""
    for key, value in body.items():
        await store.set_setting(str(key), str(value))
    return {"ok": True}


@router.get("/api/filesystem/list")
async def list_filesystem(path: str = "~"):
    """List directories at a given path for the directory browser.

    Restricted to the user's home directory to prevent arbitrary filesystem browsing.
    """
    expanded = os.path.realpath(os.path.expanduser(path))
    home_dir = os.path.realpath(os.path.expanduser("~"))

    # Only allow browsing within the user's home directory
    if not expanded.startswith(home_dir + os.sep) and expanded != home_dir:
        return {"error": "Access restricted to home directory", "entries": []}

    if not os.path.isdir(expanded):
        return {"error": "Not a directory", "entries": []}

    entries = []
    try:
        for name in sorted(os.listdir(expanded), key=str.lower):
            full = os.path.join(expanded, name)
            if os.path.isdir(full) and not name.startswith("."):
                entries.append(name)
    except PermissionError:
        return {"error": "Permission denied", "entries": []}

    return {"path": expanded, "entries": entries}


@router.get("/api/tags")
async def list_tags():
    """List all tags."""
    return await store.list_tags()


@router.post("/api/tags")
async def create_tag(body: dict):
    """Create a new tag."""
    name = body.get("name", "").strip()
    if not name:
        return {"error": "Tag name is required"}
    color = body.get("color", "#58a6ff")
    try:
        tag = await store.create_tag(name, color)
        return tag
    except Exception as e:
        return {"error": str(e)}


@router.delete("/api/tags/{tag_id}")
async def delete_tag(tag_id: int):
    """Delete a tag."""
    await store.delete_tag(tag_id)
    return {"ok": True}


# ── Folder Tags ────────────────────────────────────────────────────────

@router.get("/api/folder-tags")
async def get_all_folder_tags():
    """Return all folder tags as {folder_name: [tags...]}."""
    return await store.get_all_folder_tags()


@router.get("/api/folder-tags/{folder_name}")
async def get_folder_tags(folder_name: str):
    """Return tags for a specific folder."""
    return await store.get_folder_tags(folder_name)


@router.post("/api/folder-tags/{folder_name}")
async def add_folder_tag(folder_name: str, body: dict):
    """Add a tag to a folder."""
    tag_id = body.get("tag_id")
    if not tag_id:
        return {"error": "tag_id is required"}
    await store.add_folder_tag(folder_name, int(tag_id))
    return {"ok": True}


@router.delete("/api/folder-tags/{folder_name}/{tag_id}")
async def remove_folder_tag(folder_name: str, tag_id: int):
    """Remove a tag from a folder."""
    await store.remove_folder_tag(folder_name, tag_id)
    return {"ok": True}
