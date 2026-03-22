"""Tests for the message board API layer."""

import pytest
import pytest_asyncio
from httpx import AsyncClient, ASGITransport

from coral.messageboard.app import create_app


@pytest_asyncio.fixture
async def client(tmp_path):
    from coral.messageboard import api as board_api
    app = create_app(db_path=tmp_path / "test_board.db")
    async with AsyncClient(
        transport=ASGITransport(app=app), base_url="http://test"
    ) as c:
        yield c
    # Close the store's aiosqlite connection to prevent worker thread leaks
    if board_api.store is not None:
        await board_api.store.close()


@pytest.mark.asyncio
async def test_subscribe(client):
    resp = await client.post(
        "/proj1/subscribe",
        json={"session_id": "a1", "job_title": "Backend Dev"},
    )
    assert resp.status_code == 200
    data = resp.json()
    assert data["session_id"] == "a1"
    assert data["job_title"] == "Backend Dev"


@pytest.mark.asyncio
async def test_post_and_read_messages(client):
    # Subscribe two agents
    await client.post("/proj1/subscribe", json={"session_id": "a1", "job_title": "Backend"})
    await client.post("/proj1/subscribe", json={"session_id": "a2", "job_title": "Frontend"})

    # a1 posts
    resp = await client.post(
        "/proj1/messages",
        json={"session_id": "a1", "content": "Found 401 errors"},
    )
    assert resp.status_code == 200
    msg = resp.json()
    assert msg["id"] is not None

    # a2 reads — sees a1's message
    resp = await client.get("/proj1/messages", params={"session_id": "a2"})
    assert resp.status_code == 200
    msgs = resp.json()
    assert len(msgs) == 1
    assert msgs[0]["content"] == "Found 401 errors"
    assert msgs[0]["job_title"] == "Backend"

    # a2 reads again — empty
    resp = await client.get("/proj1/messages", params={"session_id": "a2"})
    assert resp.status_code == 200
    assert resp.json() == []


@pytest.mark.asyncio
async def test_list_subscribers(client):
    await client.post("/proj1/subscribe", json={"session_id": "a1", "job_title": "Backend"})
    await client.post("/proj1/subscribe", json={"session_id": "a2", "job_title": "Frontend"})

    resp = await client.get("/proj1/subscribers")
    assert resp.status_code == 200
    subs = resp.json()
    assert len(subs) == 2


@pytest.mark.asyncio
async def test_list_projects(client):
    await client.post("/proj1/subscribe", json={"session_id": "a1", "job_title": "Dev"})
    await client.post("/proj2/subscribe", json={"session_id": "a2", "job_title": "Dev"})

    resp = await client.get("/projects")
    assert resp.status_code == 200
    projects = resp.json()
    assert len(projects) == 2


@pytest.mark.asyncio
async def test_unsubscribe(client):
    await client.post("/proj1/subscribe", json={"session_id": "a1", "job_title": "Dev"})

    resp = await client.request(
        "DELETE", "/proj1/subscribe", json={"session_id": "a1"}
    )
    assert resp.status_code == 200

    # Verify removed
    resp = await client.get("/proj1/subscribers")
    assert resp.json() == []


@pytest.mark.asyncio
async def test_unsubscribe_not_found(client):
    resp = await client.request(
        "DELETE", "/proj1/subscribe", json={"session_id": "nonexistent"}
    )
    assert resp.status_code == 404


@pytest.mark.asyncio
async def test_delete_project(client):
    await client.post("/proj1/subscribe", json={"session_id": "a1", "job_title": "Dev"})
    await client.post("/proj1/messages", json={"session_id": "a1", "content": "hi"})

    resp = await client.delete("/proj1")
    assert resp.status_code == 200

    resp = await client.get("/projects")
    assert resp.json() == []


@pytest.mark.asyncio
async def test_check_unread(client):
    # Subscribe two agents
    await client.post("/proj1/subscribe", json={"session_id": "a1", "job_title": "Backend"})
    await client.post("/proj1/subscribe", json={"session_id": "a2", "job_title": "Frontend"})

    # No messages — unread is 0
    resp = await client.get("/proj1/messages/check", params={"session_id": "a1"})
    assert resp.status_code == 200
    assert resp.json() == {"unread": 0}

    # a2 posts a message mentioning a1
    await client.post("/proj1/messages", json={"session_id": "a2", "content": "@a1 hello"})

    # a1 checks — 1 unread (mentioned by session_id)
    resp = await client.get("/proj1/messages/check", params={"session_id": "a1"})
    assert resp.json() == {"unread": 1}

    # Check again — still 1 (cursor not advanced)
    resp = await client.get("/proj1/messages/check", params={"session_id": "a1"})
    assert resp.json() == {"unread": 1}

    # a1 reads messages (advances cursor)
    await client.get("/proj1/messages", params={"session_id": "a1"})

    # Now check returns 0
    resp = await client.get("/proj1/messages/check", params={"session_id": "a1"})
    assert resp.json() == {"unread": 0}


@pytest.mark.asyncio
async def test_webhook_dispatch(client, tmp_path):
    """Test that webhook is dispatched on message post (best-effort)."""
    # We test by subscribing with a webhook URL that won't connect —
    # the post should still succeed (fire-and-forget).
    await client.post(
        "/proj1/subscribe",
        json={"session_id": "a1", "job_title": "Dev", "webhook_url": "http://127.0.0.1:19999/hook"},
    )
    await client.post(
        "/proj1/subscribe",
        json={"session_id": "a2", "job_title": "Frontend"},
    )

    # a2 posts — should trigger webhook to a1 (which will fail, but post succeeds)
    resp = await client.post(
        "/proj1/messages",
        json={"session_id": "a2", "content": "hello"},
    )
    assert resp.status_code == 200
    assert resp.json()["content"] == "hello"


@pytest.mark.asyncio
async def test_list_all_messages(client):
    """GET /{project}/messages/all returns all messages including sender's own."""
    await client.post("/proj1/subscribe", json={"session_id": "a1", "job_title": "Backend"})
    await client.post("/proj1/subscribe", json={"session_id": "a2", "job_title": "Frontend"})

    await client.post("/proj1/messages", json={"session_id": "a1", "content": "msg 1"})
    await client.post("/proj1/messages", json={"session_id": "a2", "content": "msg 2"})
    await client.post("/proj1/messages", json={"session_id": "a1", "content": "msg 3"})

    # Default format returns bare array
    resp = await client.get("/proj1/messages/all")
    assert resp.status_code == 200
    msgs = resp.json()
    assert isinstance(msgs, list)
    assert len(msgs) == 3
    assert msgs[0]["content"] == "msg 1"
    assert msgs[0]["job_title"] == "Backend"
    assert msgs[2]["content"] == "msg 3"

    # format=dashboard returns wrapped object
    resp = await client.get("/proj1/messages/all", params={"format": "dashboard"})
    data = resp.json()
    assert len(data["messages"]) == 3
    assert data["total"] == 3


@pytest.mark.asyncio
async def test_list_all_messages_with_limit(client):
    """GET /{project}/messages/all respects the limit param."""
    await client.post("/proj1/subscribe", json={"session_id": "a1", "job_title": "Dev"})
    for i in range(5):
        await client.post("/proj1/messages", json={"session_id": "a1", "content": f"msg {i}"})

    # Bare array default
    resp = await client.get("/proj1/messages/all", params={"limit": 2})
    assert resp.status_code == 200
    msgs = resp.json()
    assert isinstance(msgs, list)
    assert len(msgs) == 2

    # Dashboard format
    resp = await client.get("/proj1/messages/all", params={"limit": 2, "format": "dashboard"})
    data = resp.json()
    assert len(data["messages"]) == 2
    assert data["total"] == 5


@pytest.mark.asyncio
async def test_list_all_messages_does_not_advance_cursor(client):
    """Calling /messages/all should NOT advance read cursors."""
    await client.post("/proj1/subscribe", json={"session_id": "a1", "job_title": "Backend"})
    await client.post("/proj1/subscribe", json={"session_id": "a2", "job_title": "Frontend"})

    await client.post("/proj1/messages", json={"session_id": "a2", "content": "hello"})

    # Call /messages/all (bare array default)
    resp = await client.get("/proj1/messages/all")
    assert len(resp.json()) == 1

    # a1 should still see the message via cursor-based read
    resp = await client.get("/proj1/messages", params={"session_id": "a1"})
    assert len(resp.json()) == 1
    assert resp.json()[0]["content"] == "hello"
