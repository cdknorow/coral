"""Tests that detail_json is properly serialized when creating agent events.

Validates the fix for the bug where passing a dict (not a string) as
detail_json caused a SQLite InterfaceError.
"""

import json
import pytest
import pytest_asyncio
from httpx import ASGITransport, AsyncClient

from corral.web_server import app
from corral.store import CorralStore as SessionStore


@pytest_asyncio.fixture
async def tmp_store(tmp_path):
    db_path = tmp_path / "test.db"
    s = SessionStore(db_path=db_path)
    await s._get_conn()
    yield s
    await s.close()


@pytest_asyncio.fixture
async def client(tmp_store, monkeypatch):
    import corral.web_server as ws
    ws._set_store(tmp_store)
    monkeypatch.setattr(ws, "store", tmp_store)
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as c:
        yield c


@pytest.mark.asyncio
async def test_detail_json_as_dict_is_serialized(client):
    """Passing detail_json as a dict should be auto-serialized to a JSON string."""
    resp = await client.post("/api/sessions/live/agent-1/events", json={
        "event_type": "tool_use",
        "summary": "Edited file",
        "tool_name": "Edit",
        "session_id": "test-session",
        "detail_json": {"file_path": "/tmp/test.py", "old_string": "foo", "new_string": "bar"},
    })
    assert resp.status_code == 200
    data = resp.json()
    assert "error" not in data
    assert data["event_type"] == "tool_use"
    # detail_json should be stored as a string
    assert isinstance(data["detail_json"], str)
    parsed = json.loads(data["detail_json"])
    assert parsed["file_path"] == "/tmp/test.py"


@pytest.mark.asyncio
async def test_detail_json_as_string_passes_through(client):
    """Passing detail_json as a string should work as before."""
    detail = json.dumps({"file_path": "/tmp/test.py"})
    resp = await client.post("/api/sessions/live/agent-1/events", json={
        "event_type": "tool_use",
        "summary": "Read file",
        "tool_name": "Read",
        "session_id": "test-session",
        "detail_json": detail,
    })
    assert resp.status_code == 200
    data = resp.json()
    assert "error" not in data
    assert data["detail_json"] == detail


@pytest.mark.asyncio
async def test_detail_json_as_none_works(client):
    """Passing no detail_json should work."""
    resp = await client.post("/api/sessions/live/agent-1/events", json={
        "event_type": "status",
        "summary": "Working on task",
        "session_id": "test-session",
    })
    assert resp.status_code == 200
    data = resp.json()
    assert "error" not in data
    assert data["detail_json"] is None


@pytest.mark.asyncio
async def test_detail_json_as_nested_dict(client):
    """Deeply nested dict should serialize correctly."""
    nested = {
        "tool": "Bash",
        "args": {"command": "ls -la", "timeout": 5000},
        "result": {"stdout": "file1\nfile2", "exit_code": 0},
    }
    resp = await client.post("/api/sessions/live/agent-1/events", json={
        "event_type": "tool_use",
        "summary": "Ran bash command",
        "tool_name": "Bash",
        "session_id": "test-session",
        "detail_json": nested,
    })
    assert resp.status_code == 200
    data = resp.json()
    assert "error" not in data
    parsed = json.loads(data["detail_json"])
    assert parsed["args"]["command"] == "ls -la"


@pytest.mark.asyncio
async def test_detail_json_list_is_serialized(client):
    """Passing detail_json as a list should also serialize."""
    resp = await client.post("/api/sessions/live/agent-1/events", json={
        "event_type": "tool_use",
        "summary": "Multiple files",
        "session_id": "test-session",
        "detail_json": ["/tmp/a.py", "/tmp/b.py"],
    })
    assert resp.status_code == 200
    data = resp.json()
    assert "error" not in data
    parsed = json.loads(data["detail_json"])
    assert parsed == ["/tmp/a.py", "/tmp/b.py"]
