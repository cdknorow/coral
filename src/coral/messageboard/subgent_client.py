"""Subgent admin API client for Coral integration.

Provides functions to manage agent-scoped API keys on a Subgent server:
create, revoke, and list keys. Used by Coral to provision agent identities
when launching agent teams on a Subgent board.
"""

from __future__ import annotations

import ipaddress
import logging
import socket
from urllib.parse import quote as url_quote, urlparse

import httpx

log = logging.getLogger(__name__)

_REQUEST_TIMEOUT = 10.0


# ── SSRF Validation ──────────────────────────────────────────────────────────
# Reuses the pattern from coral.api.board_remotes to prevent requests
# to private/reserved IP ranges.


def _is_ip_blocked(ip: ipaddress.IPv4Address | ipaddress.IPv6Address) -> bool:
    """Check if an IP address is private, reserved, or otherwise unsafe.

    Loopback (127.0.0.0/8, ::1) is allowed since Coral is a local dev tool
    and localhost is the most common Subgent deployment target.
    """
    # Handle IPv6-mapped IPv4 addresses (e.g. ::ffff:127.0.0.1)
    if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped:
        ip = ip.ipv4_mapped
    # Allow loopback — localhost is a legitimate target for local dev
    if ip.is_loopback:
        return False
    if ip.is_private or ip.is_link_local or ip.is_reserved:
        return True
    if isinstance(ip, ipaddress.IPv4Address):
        if ip in ipaddress.IPv4Network("100.64.0.0/10"):
            return True
    return False


def validate_url(url: str) -> str:
    """Validate a Subgent admin URL is safe (not targeting internal networks).

    Returns the validated URL (stripped of trailing slash).
    Raises ValueError if the URL is invalid or resolves to a blocked IP.
    """
    try:
        parsed = urlparse(url)
    except Exception as exc:
        raise ValueError(f"Invalid URL: {url}") from exc

    if parsed.scheme not in ("http", "https"):
        raise ValueError(f"URL scheme must be http or https, got: {parsed.scheme}")
    if not parsed.hostname:
        raise ValueError(f"URL has no hostname: {url}")

    try:
        addr_infos = socket.getaddrinfo(
            parsed.hostname, parsed.port or 80, proto=socket.IPPROTO_TCP
        )
    except socket.gaierror as exc:
        raise ValueError(f"Cannot resolve hostname: {parsed.hostname}") from exc

    for _family, _, _, _, sockaddr in addr_infos:
        ip = ipaddress.ip_address(sockaddr[0])
        if _is_ip_blocked(ip):
            raise ValueError(
                f"URL resolves to blocked IP ({ip}): {url}"
            )

    # NOTE: DNS rebinding TOCTOU — we resolve DNS here for validation, but
    # httpx resolves again when making the request. An attacker-controlled DNS
    # server could return a different IP between checks. Low risk because
    # admin_url is operator-configured, not user input. If admin_url ever
    # becomes user-configurable, pin the resolved IP and use a Host header
    # override (see board_remotes.py _proxy_get for the pattern).
    return url.rstrip("/")


# ── API Client Functions ─────────────────────────────────────────────────────


def ensure_project(admin_url: str, admin_key: str, project_name: str) -> str:
    """Create project if needed, return project UUID."""
    base = validate_url(admin_url)
    headers = {"Authorization": f"Bearer {admin_key}"}
    with httpx.Client(timeout=_REQUEST_TIMEOUT) as client:
        resp = client.get(f"{base}/api/board/projects", headers=headers)
        resp.raise_for_status()
        for proj in resp.json():
            if proj.get("name") == project_name:
                return proj["id"]
        resp = client.post(
            f"{base}/api/board/projects",
            json={"name": project_name},
            headers=headers,
        )
        resp.raise_for_status()
        return resp.json()["id"]


def ensure_board(admin_url: str, admin_key: str, project_id: str, board_name: str) -> str:
    """Create board if needed, return board UUID."""
    base = validate_url(admin_url)
    headers = {"Authorization": f"Bearer {admin_key}"}
    with httpx.Client(timeout=_REQUEST_TIMEOUT) as client:
        resp = client.get(
            f"{base}/api/board/projects/{project_id}/boards", headers=headers,
        )
        resp.raise_for_status()
        for board in resp.json():
            if board.get("board") == board_name or board.get("name") == board_name:
                return board.get("board_id") or board["id"]
        resp = client.post(
            f"{base}/api/board/projects/{project_id}/boards",
            json={"name": board_name},
            headers=headers,
        )
        resp.raise_for_status()
        return resp.json()["id"]


def create_agent_key(
    admin_url: str,
    admin_key: str,
    org_id: str,
    board: str,
    session_id: str,
    job_title: str,
    *,
    label: str = "",
    scopes: str = "read,write",
    webhook_url: str = "",
    check_mode: str = "all",
) -> dict:
    """Create an agent-scoped API key on a Subgent server.

    Args:
        admin_url: Base URL of the Subgent server (e.g. https://subgent.io).
        admin_key: Admin API key (cb_live_ prefix with admin scope).
        org_id: Organization ID for the key.
        board: Board UUID the agent key is scoped to.
        session_id: Session identity baked into the key.
        job_title: Role/job title baked into the key.
        label: Optional human-readable label for the key.
        scopes: Comma-separated scopes (default: read,write).
        webhook_url: Optional webhook URL for push notifications.
        check_mode: Message check mode (all, mentions, group, none).

    Returns:
        Dict with 'key' (plaintext, shown once) and key metadata.

    Raises:
        ValueError: If admin_url fails SSRF validation.
        httpx.HTTPStatusError: If the API returns an error status.
    """
    base = validate_url(admin_url)
    payload = {
        "org_id": org_id,
        "board": board,
        "session_id": session_id,
        "job_title": job_title,
        "label": label or f"coral-{session_id}",
        "scopes": scopes,
        "check_mode": check_mode,
    }
    if webhook_url:
        payload["webhook_url"] = webhook_url

    with httpx.Client(timeout=_REQUEST_TIMEOUT) as client:
        resp = client.post(
            f"{base}/api/board/admin/agent-keys",
            json=payload,
            headers={"Authorization": f"Bearer {admin_key}"},
        )
        resp.raise_for_status()
        return resp.json()


def revoke_key(admin_url: str, admin_key: str, key_id: str) -> dict:
    """Revoke an API key on a Subgent server.

    Args:
        admin_url: Base URL of the Subgent server.
        admin_key: Admin API key.
        key_id: ID of the key to revoke.

    Returns:
        Response dict from the server.

    Raises:
        ValueError: If admin_url fails SSRF validation.
        httpx.HTTPStatusError: If the API returns an error status.
    """
    base = validate_url(admin_url)

    with httpx.Client(timeout=_REQUEST_TIMEOUT) as client:
        resp = client.delete(
            f"{base}/api/board/admin/keys/{url_quote(str(key_id), safe='')}",
            headers={"Authorization": f"Bearer {admin_key}"},
        )
        resp.raise_for_status()
        return resp.json()


def list_keys(admin_url: str, admin_key: str, org_id: str) -> list[dict]:
    """List all API keys for an organization on a Subgent server.

    Args:
        admin_url: Base URL of the Subgent server.
        admin_key: Admin API key.
        org_id: Organization ID to list keys for.

    Returns:
        List of key metadata dicts.

    Raises:
        ValueError: If admin_url fails SSRF validation.
        httpx.HTTPStatusError: If the API returns an error status.
    """
    base = validate_url(admin_url)

    with httpx.Client(timeout=_REQUEST_TIMEOUT) as client:
        resp = client.get(
            f"{base}/api/board/admin/keys",
            params={"org_id": org_id},
            headers={"Authorization": f"Bearer {admin_key}"},
        )
        resp.raise_for_status()
        return resp.json()
