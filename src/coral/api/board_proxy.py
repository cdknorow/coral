"""Proxy endpoints for Subgent board API calls.

Forwards board requests to a remote Subgent server with authentication,
so the dashboard can display board data without CORS issues.
"""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING

import httpx
from fastapi import APIRouter, HTTPException

if TYPE_CHECKING:
    from coral.store import CoralStore

log = logging.getLogger(__name__)

router = APIRouter(prefix="/api/board-proxy", tags=["board-proxy"])

# Injected by web_server.py at startup
store: CoralStore = None  # type: ignore[assignment]


async def _get_subgent_credentials() -> tuple[str, str]:
    """Read subgent admin_url and admin_key from saved settings.

    Returns (base_url, admin_key).
    Raises HTTPException if not configured.
    """
    settings = await store.get_settings()
    admin_url = settings.get("subgent_admin_url", "").strip()
    admin_key = settings.get("subgent_admin_key", "").strip()
    if not admin_url or not admin_key:
        raise HTTPException(400, "Subgent server not configured")

    from coral.messageboard.subgent_client import validate_url

    try:
        base = validate_url(admin_url)
    except ValueError as e:
        raise HTTPException(400, f"Invalid subgent URL: {e}")
    return base, admin_key


async def _proxy_get(path: str, timeout: float = 10.0):
    """Forward a GET request to the configured Subgent server."""
    base, key = await _get_subgent_credentials()
    try:
        async with httpx.AsyncClient(timeout=timeout) as client:
            resp = await client.get(
                f"{base}{path}",
                headers={"Authorization": f"Bearer {key}"},
            )
        resp.raise_for_status()
        return resp.json()
    except httpx.ConnectError:
        raise HTTPException(502, "Cannot reach Subgent server")
    except httpx.TimeoutException:
        raise HTTPException(504, "Subgent server timed out")
    except httpx.HTTPStatusError as e:
        raise HTTPException(e.response.status_code, f"Subgent error: {e.response.text[:200]}")


async def _proxy_post(path: str, body: dict, timeout: float = 10.0):
    """Forward a POST request to the configured Subgent server."""
    base, key = await _get_subgent_credentials()
    try:
        async with httpx.AsyncClient(timeout=timeout) as client:
            resp = await client.post(
                f"{base}{path}",
                json=body,
                headers={"Authorization": f"Bearer {key}"},
            )
        resp.raise_for_status()
        return resp.json()
    except httpx.ConnectError:
        raise HTTPException(502, "Cannot reach Subgent server")
    except httpx.TimeoutException:
        raise HTTPException(504, "Subgent server timed out")
    except httpx.HTTPStatusError as e:
        raise HTTPException(e.response.status_code, f"Subgent error: {e.response.text[:200]}")


@router.get("/{board_id}/messages/all")
async def proxy_messages(board_id: str, limit: int = 200):
    """Proxy: list all messages on a Subgent board."""
    return await _proxy_get(f"/api/board/{board_id}/messages/all?limit={limit}")


@router.post("/{board_id}/messages")
async def proxy_post_message(board_id: str, body: dict):
    """Proxy: post a message to a Subgent board."""
    return await _proxy_post(f"/api/board/{board_id}/messages", body)


@router.get("/{board_id}/subscribers")
async def proxy_subscribers(board_id: str):
    """Proxy: list subscribers on a Subgent board."""
    return await _proxy_get(f"/api/board/{board_id}/subscribers")
