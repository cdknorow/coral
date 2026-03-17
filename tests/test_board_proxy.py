"""Tests for proxy endpoints and CRUD in board_remotes.py."""

from unittest.mock import AsyncMock, patch

import pytest
import pytest_asyncio
from fastapi import HTTPException
from httpx import AsyncClient, ASGITransport

from coral.store.remote_boards import RemoteBoardStore


@pytest_asyncio.fixture
async def remote_store(tmp_path):
    store = RemoteBoardStore(db_path=tmp_path / "test.db")
    yield store
    await store.close()


@pytest_asyncio.fixture
async def client(remote_store):
    """Create a test client with the board_remotes router wired up."""
    from fastapi import FastAPI
    from coral.api import board_remotes

    app = FastAPI()
    board_remotes.store = remote_store
    app.include_router(board_remotes.router)

    async with AsyncClient(
        transport=ASGITransport(app=app), base_url="http://test"
    ) as c:
        yield c


# ── Proxy route tests (mock _proxy_get to test route wiring + error mapping) ─


@pytest.mark.asyncio
async def test_proxy_projects_route(client):
    """GET /proxy/{server}/projects returns proxied data."""
    with patch("coral.api.board_remotes._proxy_get", new_callable=AsyncMock,
               return_value=[{"project": "p1", "subscriber_count": 2}]):
        resp = await client.get("/api/board/remotes/proxy/http://remote:8420/projects")
    assert resp.status_code == 200
    assert resp.json()[0]["project"] == "p1"


@pytest.mark.asyncio
async def test_proxy_messages_route(client):
    """GET /proxy/{server}/{project}/messages/all returns proxied data."""
    with patch("coral.api.board_remotes._proxy_get", new_callable=AsyncMock,
               return_value=[{"id": 1, "content": "hi"}]):
        resp = await client.get("/api/board/remotes/proxy/http://remote:8420/proj1/messages/all")
    assert resp.status_code == 200
    assert resp.json()[0]["content"] == "hi"


@pytest.mark.asyncio
async def test_proxy_subscribers_route(client):
    """GET /proxy/{server}/{project}/subscribers returns proxied data."""
    with patch("coral.api.board_remotes._proxy_get", new_callable=AsyncMock,
               return_value=[{"session_id": "a1", "job_title": "Dev"}]):
        resp = await client.get("/api/board/remotes/proxy/http://remote:8420/proj1/subscribers")
    assert resp.status_code == 200
    assert resp.json()[0]["session_id"] == "a1"


@pytest.mark.asyncio
async def test_proxy_check_route(client):
    """GET /proxy/{server}/{project}/messages/check returns proxied data."""
    with patch("coral.api.board_remotes._proxy_get", new_callable=AsyncMock,
               return_value={"unread": 5}):
        resp = await client.get(
            "/api/board/remotes/proxy/http://remote:8420/proj1/messages/check",
            params={"session_id": "a1"},
        )
    assert resp.status_code == 200
    assert resp.json()["unread"] == 5


@pytest.mark.asyncio
async def test_proxy_route_returns_504_on_timeout(client):
    """Proxy route returns 504 when remote server times out."""
    with patch("coral.api.board_remotes._proxy_get", new_callable=AsyncMock,
               side_effect=HTTPException(504, "Remote server timed out")):
        resp = await client.get("/api/board/remotes/proxy/http://slow:8420/projects")
    assert resp.status_code == 504
    assert "timed out" in resp.json()["detail"].lower()


@pytest.mark.asyncio
async def test_proxy_route_returns_502_on_unreachable(client):
    """Proxy route returns 502 when remote server is unreachable."""
    with patch("coral.api.board_remotes._proxy_get", new_callable=AsyncMock,
               side_effect=HTTPException(502, "Cannot reach remote server")):
        resp = await client.get("/api/board/remotes/proxy/http://dead:8420/projects")
    assert resp.status_code == 502


@pytest.mark.asyncio
async def test_proxy_route_forwards_remote_404(client):
    """Proxy route forwards HTTP 404 from remote server."""
    with patch("coral.api.board_remotes._proxy_get", new_callable=AsyncMock,
               side_effect=HTTPException(404, "Remote server error: Not Found")):
        resp = await client.get("/api/board/remotes/proxy/http://remote:8420/proj1/subscribers")
    assert resp.status_code == 404


@pytest.mark.asyncio
async def test_proxy_route_forwards_remote_500(client):
    """Proxy route forwards HTTP 500 from remote server."""
    with patch("coral.api.board_remotes._proxy_get", new_callable=AsyncMock,
               side_effect=HTTPException(500, "Remote server error: Internal Server Error")):
        resp = await client.get("/api/board/remotes/proxy/http://remote:8420/projects")
    assert resp.status_code == 500


# ── CRUD endpoint tests ──────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_add_remote_subscription_endpoint(client):
    """POST /api/board/remotes adds a subscription."""
    with patch("coral.api.board_remotes._resolve_and_validate_url", return_value="203.0.113.1"):
        resp = await client.post("/api/board/remotes", json={
            "session_id": "agent-1",
            "remote_server": "http://remote:8420",
            "project": "proj1",
            "job_title": "Dev",
        })
    assert resp.status_code == 200
    data = resp.json()
    assert data["session_id"] == "agent-1"
    assert data["remote_server"] == "http://remote:8420"


@pytest.mark.asyncio
async def test_list_remote_subscriptions_endpoint(client):
    """GET /api/board/remotes lists all subscriptions."""
    with patch("coral.api.board_remotes._resolve_and_validate_url", return_value="203.0.113.1"):
        await client.post("/api/board/remotes", json={
            "session_id": "agent-1",
            "remote_server": "http://remote:8420",
            "project": "proj1",
            "job_title": "Dev",
        })
    resp = await client.get("/api/board/remotes")
    assert resp.status_code == 200
    assert len(resp.json()) == 1


@pytest.mark.asyncio
async def test_delete_remote_subscription_endpoint(client):
    """DELETE /api/board/remotes removes a subscription."""
    with patch("coral.api.board_remotes._resolve_and_validate_url", return_value="203.0.113.1"):
        await client.post("/api/board/remotes", json={
            "session_id": "agent-1",
            "remote_server": "http://remote:8420",
            "project": "proj1",
            "job_title": "Dev",
        })
    resp = await client.request("DELETE", "/api/board/remotes", json={
        "session_id": "agent-1",
    })
    assert resp.status_code == 200
    assert resp.json()["removed"] is True

    resp = await client.get("/api/board/remotes")
    assert resp.json() == []
