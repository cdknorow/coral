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


_SENSITIVE_KEYS = {"subgent_admin_key"}


@router.get("/api/settings")
async def get_settings():
    """Return all global user settings (sensitive keys filtered out)."""
    settings = await store.get_settings()
    filtered = {k: v for k, v in settings.items() if k not in _SENSITIVE_KEYS}
    return {"settings": filtered}


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


_SUBGENT_KEYS = ("subgent_admin_url", "subgent_admin_key", "subgent_org_id")


def _mask_key(key: str) -> str:
    """Mask a sensitive key, showing only prefix and last 4 chars."""
    if not key or len(key) < 8:
        return "****"
    # Show prefix up to first underscore group + last 4 chars
    prefix = key[:8] if len(key) > 12 else key[:4]
    return f"{prefix}...{key[-4:]}"


@router.get("/api/settings/subgent")
async def get_subgent_settings():
    """Return saved Subgent server configuration (admin_key masked)."""
    settings = await store.get_settings()
    admin_url = settings.get("subgent_admin_url", "")
    admin_key = settings.get("subgent_admin_key", "")
    org_id = settings.get("subgent_org_id", "default")
    return {
        "admin_url": admin_url,
        "org_id": org_id,
        "key_configured": bool(admin_key),
        "admin_key_masked": _mask_key(admin_key) if admin_key else "",
    }


@router.put("/api/settings/subgent")
async def put_subgent_settings(body: dict):
    """Save Subgent server configuration."""
    admin_url = body.get("admin_url", "").strip()
    admin_key = body.get("admin_key", "").strip()
    org_id = body.get("org_id", "default").strip() or "default"
    if not admin_url:
        return {"error": "admin_url is required"}
    # If no key provided, keep the existing one (user only changed URL/org)
    if not admin_key:
        existing = await store.get_settings()
        admin_key = existing.get("subgent_admin_key", "")
        if not admin_key:
            return {"error": "admin_key is required"}
    # Validate URL before saving (SSRF protection)
    try:
        from coral.messageboard.subgent_client import validate_url
        validate_url(admin_url)
    except ValueError as e:
        return {"error": f"Invalid admin URL: {e}"}
    await store.set_setting("subgent_admin_url", admin_url)
    await store.set_setting("subgent_admin_key", admin_key)
    await store.set_setting("subgent_org_id", org_id)
    return {"ok": True}


@router.delete("/api/settings/subgent")
async def delete_subgent_settings():
    """Clear saved Subgent server configuration."""
    for key in _SUBGENT_KEYS:
        await store.delete_setting(key)
    return {"ok": True}


@router.post("/api/settings/subgent/test")
async def test_subgent_connection(body: dict):
    """Test connectivity to a Subgent server. Proxies the request server-side to avoid CORS."""
    import asyncio
    import httpx

    admin_url = body.get("admin_url", "").strip()
    admin_key = body.get("admin_key", "").strip()
    if not admin_url:
        return {"error": "admin_url is required"}
    # If no key provided, try saved key
    if not admin_key:
        try:
            saved = await store.get_settings()
            admin_key = saved.get("subgent_admin_key", "")
        except Exception:
            pass
    if not admin_key:
        return {"error": "admin_key is required"}
    # Validate URL (SSRF protection)
    try:
        from coral.messageboard.subgent_client import validate_url
        base = validate_url(admin_url)
    except ValueError as e:
        return {"error": f"Invalid URL: {e}"}
    # Make the test request server-side
    try:
        async with httpx.AsyncClient(timeout=10) as client:
            resp = await client.get(
                f"{base}/api/board/projects",
                headers={"Authorization": f"Bearer {admin_key}"},
            )
        if resp.status_code == 401:
            return {"ok": False, "error": "Authentication failed (401)"}
        if resp.status_code == 403:
            return {"ok": False, "error": "Access denied (403)"}
        resp.raise_for_status()
        import shutil
        cli_available = shutil.which("subgent") is not None
        result = {"ok": True, "projects": resp.json(), "cli_available": cli_available}
        if not cli_available:
            result["cli_warning"] = "subgent CLI not found in PATH. Install it so agents can communicate on the board."
        return result
    except httpx.ConnectError:
        return {"ok": False, "error": "Connection refused — is the server running?"}
    except httpx.TimeoutException:
        return {"ok": False, "error": "Connection timed out"}
    except Exception as e:
        return {"ok": False, "error": "Connection failed"}


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
