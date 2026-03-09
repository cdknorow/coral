"""Tests that validate the hook JSON → API → database flow for agent tasks.

Sends the same JSON payloads that Claude Code's PostToolUse hook produces,
routes them through the real FastAPI endpoints, and asserts the SQLite
database reflects the expected state.
"""

import pytest
import pytest_asyncio
from httpx import ASGITransport, AsyncClient
from pathlib import Path
from tempfile import TemporaryDirectory

from corral.web_server import app, store as _default_store
from corral.store import CorralStore as SessionStore


# ── Fixtures ──────────────────────────────────────────────────────────────


@pytest_asyncio.fixture
async def tmp_store(tmp_path):
    """Create a SessionStore backed by a temp SQLite database."""
    db_path = tmp_path / "test.db"
    s = SessionStore(db_path=db_path)
    await s._get_conn()  # Force schema creation
    yield s
    await s.close()


@pytest_asyncio.fixture
async def client(tmp_store, monkeypatch):
    """AsyncClient wired to the real FastAPI app with a temp database."""
    import corral.web_server as ws
    ws._set_store(tmp_store)
    monkeypatch.setattr(ws, "store", tmp_store)
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as c:
        yield c


# ── Hook JSON payloads (matching real Claude Code PostToolUse format) ─────


TASK_CREATE_PAYLOAD = {
    "session_id": "test-session-001",
    "cwd": "/home/user/projects/my_agent",
    "hook_event_name": "PostToolUse",
    "tool_name": "TaskCreate",
    "tool_input": {
        "subject": "Fix authentication bug",
        "description": "The login endpoint returns 500 on invalid tokens",
        "activeForm": "Fixing authentication bug",
    },
    "tool_response": {
        "task": {
            "id": "42",
            "subject": "Fix authentication bug",
            "description": "The login endpoint returns 500 on invalid tokens",
            "status": "pending",
        }
    },
}

TASK_UPDATE_COMPLETE_PAYLOAD = {
    "session_id": "test-session-001",
    "cwd": "/home/user/projects/my_agent",
    "hook_event_name": "PostToolUse",
    "tool_name": "TaskUpdate",
    "tool_input": {
        "taskId": "42",
        "status": "completed",
    },
    "tool_response": {
        "success": True,
        "taskId": "42",
        "updatedFields": ["status"],
        "statusChange": {"from": "pending", "to": "completed"},
    },
}


# ── Tests ─────────────────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_create_task_via_api(client, tmp_store):
    """POST /api/sessions/live/{name}/tasks creates a row in agent_tasks."""
    resp = await client.post(
        "/api/sessions/live/my_agent/tasks",
        json={"title": "Fix authentication bug"},
    )
    assert resp.status_code == 200
    data = resp.json()
    assert data["title"] == "Fix authentication bug"
    assert data["agent_name"] == "my_agent"
    assert data["completed"] == 0
    assert "id" in data

    # Verify it's in the database
    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert len(tasks) == 1
    assert tasks[0]["title"] == "Fix authentication bug"
    assert tasks[0]["completed"] == 0


@pytest.mark.asyncio
async def test_complete_task_via_api(client, tmp_store):
    """PATCH /api/sessions/live/{name}/tasks/{id} with completed=1 updates the row."""
    # Create
    create_resp = await client.post(
        "/api/sessions/live/my_agent/tasks",
        json={"title": "Write unit tests"},
    )
    task_id = create_resp.json()["id"]

    # Complete
    resp = await client.patch(
        f"/api/sessions/live/my_agent/tasks/{task_id}",
        json={"completed": 1},
    )
    assert resp.status_code == 200
    assert resp.json()["ok"] is True

    # Verify in database
    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert len(tasks) == 1
    assert tasks[0]["completed"] == 1


@pytest.mark.asyncio
async def test_full_hook_create_then_complete(client, tmp_store):
    """Simulate the full hook flow: TaskCreate JSON → API → TaskUpdate JSON → API.

    This mirrors what happens when Claude Code's PostToolUse hook fires
    corral-hook-task-sync for a TaskCreate followed by a TaskUpdate(completed).
    """
    agent_name = "my_agent"

    # Step 1: Hook fires for TaskCreate — extract title and POST to API
    create_input = TASK_CREATE_PAYLOAD["tool_input"]
    subject = create_input["subject"]
    session_id = TASK_CREATE_PAYLOAD["session_id"]

    resp = await client.post(
        f"/api/sessions/live/{agent_name}/tasks",
        json={"title": subject, "session_id": session_id},
    )
    assert resp.status_code == 200
    dashboard_task = resp.json()
    assert dashboard_task["title"] == "Fix authentication bug"

    # Verify task exists and is not completed
    tasks = await tmp_store.list_agent_tasks(agent_name)
    assert len(tasks) == 1
    assert tasks[0]["title"] == "Fix authentication bug"
    assert tasks[0]["completed"] == 0

    # Step 2: Hook fires for TaskUpdate(completed) — find task by title, PATCH it
    # The hook resolves the title from its cache (or response), then:
    list_resp = await client.get(f"/api/sessions/live/{agent_name}/tasks?session_id={session_id}")
    assert list_resp.status_code == 200
    task_list = list_resp.json()

    # Find the matching uncompleted task
    match = next(
        (t for t in task_list if t["title"] == subject and not t["completed"]),
        None,
    )
    assert match is not None

    patch_resp = await client.patch(
        f"/api/sessions/live/{agent_name}/tasks/{match['id']}",
        json={"completed": 1},
    )
    assert patch_resp.status_code == 200

    # Verify task is now completed in the database
    tasks = await tmp_store.list_agent_tasks(agent_name)
    assert len(tasks) == 1
    assert tasks[0]["completed"] == 1


@pytest.mark.asyncio
async def test_delete_task_via_api(client, tmp_store):
    """DELETE /api/sessions/live/{name}/tasks/{id} removes the row."""
    create_resp = await client.post(
        "/api/sessions/live/my_agent/tasks",
        json={"title": "Temporary task"},
    )
    task_id = create_resp.json()["id"]

    resp = await client.delete(f"/api/sessions/live/my_agent/tasks/{task_id}")
    assert resp.status_code == 200

    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert len(tasks) == 0


@pytest.mark.asyncio
async def test_tasks_are_per_agent(client, tmp_store):
    """Tasks for different agents are isolated."""
    await client.post("/api/sessions/live/agent_a/tasks", json={"title": "Task A"})
    await client.post("/api/sessions/live/agent_b/tasks", json={"title": "Task B"})

    tasks_a = await tmp_store.list_agent_tasks("agent_a")
    tasks_b = await tmp_store.list_agent_tasks("agent_b")
    assert len(tasks_a) == 1
    assert tasks_a[0]["title"] == "Task A"
    assert len(tasks_b) == 1
    assert tasks_b[0]["title"] == "Task B"


@pytest.mark.asyncio
async def test_reorder_tasks(client, tmp_store):
    """POST /api/sessions/live/{name}/tasks/reorder updates sort_order."""
    r1 = await client.post("/api/sessions/live/my_agent/tasks", json={"title": "First"})
    r2 = await client.post("/api/sessions/live/my_agent/tasks", json={"title": "Second"})
    r3 = await client.post("/api/sessions/live/my_agent/tasks", json={"title": "Third"})
    id1, id2, id3 = r1.json()["id"], r2.json()["id"], r3.json()["id"]

    # Reverse the order
    resp = await client.post(
        "/api/sessions/live/my_agent/tasks/reorder",
        json={"task_ids": [id3, id2, id1]},
    )
    assert resp.status_code == 200

    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert [t["title"] for t in tasks] == ["Third", "Second", "First"]


@pytest.mark.asyncio
async def test_edit_task_title(client, tmp_store):
    """PATCH with a new title updates the row."""
    create_resp = await client.post(
        "/api/sessions/live/my_agent/tasks",
        json={"title": "Old title"},
    )
    task_id = create_resp.json()["id"]

    resp = await client.patch(
        f"/api/sessions/live/my_agent/tasks/{task_id}",
        json={"title": "New title"},
    )
    assert resp.status_code == 200

    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert tasks[0]["title"] == "New title"


@pytest.mark.asyncio
async def test_create_task_empty_title_rejected(client):
    """POST with empty title returns an error."""
    resp = await client.post(
        "/api/sessions/live/my_agent/tasks",
        json={"title": ""},
    )
    assert resp.status_code == 200
    assert "error" in resp.json()


@pytest.mark.asyncio
async def test_idempotent_create(tmp_store):
    """create_agent_task_if_not_exists does not duplicate tasks."""
    await tmp_store.create_agent_task_if_not_exists("my_agent", "Unique task")
    await tmp_store.create_agent_task_if_not_exists("my_agent", "Unique task")

    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert len(tasks) == 1


@pytest.mark.asyncio
async def test_complete_by_title(tmp_store):
    """complete_agent_task_by_title marks the right task done."""
    await tmp_store.create_agent_task("my_agent", "Task A")
    await tmp_store.create_agent_task("my_agent", "Task B")

    await tmp_store.complete_agent_task_by_title("my_agent", "Task A")

    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert tasks[0]["title"] == "Task A"
    assert tasks[0]["completed"] == 1
    assert tasks[1]["title"] == "Task B"
    assert tasks[1]["completed"] == 0


@pytest.mark.asyncio
async def test_in_progress_sets_completed_to_2(client, tmp_store):
    """PATCH with completed=2 marks a task as in-progress."""
    create_resp = await client.post(
        "/api/sessions/live/my_agent/tasks",
        json={"title": "Working on it"},
    )
    task_id = create_resp.json()["id"]

    resp = await client.patch(
        f"/api/sessions/live/my_agent/tasks/{task_id}",
        json={"completed": 2},
    )
    assert resp.status_code == 200

    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert tasks[0]["completed"] == 2


@pytest.mark.asyncio
async def test_full_lifecycle_pending_inprogress_completed(client, tmp_store):
    """Task goes through pending(0) → in_progress(2) → completed(1)."""
    create_resp = await client.post(
        "/api/sessions/live/my_agent/tasks",
        json={"title": "Lifecycle task"},
    )
    task_id = create_resp.json()["id"]

    # Starts as pending
    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert tasks[0]["completed"] == 0

    # Mark in-progress
    await client.patch(
        f"/api/sessions/live/my_agent/tasks/{task_id}",
        json={"completed": 2},
    )
    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert tasks[0]["completed"] == 2

    # Mark completed
    await client.patch(
        f"/api/sessions/live/my_agent/tasks/{task_id}",
        json={"completed": 1},
    )
    tasks = await tmp_store.list_agent_tasks("my_agent")
    assert tasks[0]["completed"] == 1


# ── hook_task_sync unit tests ─────────────────────────────────────────────


class TestParseResponse:
    """Test _parse_response with various tool_response formats."""

    def test_structured_dict(self):
        from corral.hook_task_sync import _parse_response

        resp = {"task": {"id": "10", "subject": "Fix bug"}}
        parsed = _parse_response(resp)
        assert parsed["task_id"] == "10"
        assert parsed["subject"] == "Fix bug"

    def test_string_with_task_number(self):
        from corral.hook_task_sync import _parse_response

        parsed = _parse_response("Task #7 created successfully: Do stuff")
        assert parsed["task_id"] == "7"
        assert parsed["subject"] == ""

    def test_flat_dict_with_taskId(self):
        from corral.hook_task_sync import _parse_response

        resp = {"success": True, "taskId": "15", "updatedFields": ["status"]}
        parsed = _parse_response(resp)
        assert parsed["task_id"] == "15"

    def test_empty_string(self):
        from corral.hook_task_sync import _parse_response

        parsed = _parse_response("")
        assert parsed["task_id"] == ""
        assert parsed["subject"] == ""

    def test_empty_dict(self):
        from corral.hook_task_sync import _parse_response

        parsed = _parse_response({})
        assert parsed["task_id"] == ""
        assert parsed["subject"] == ""
