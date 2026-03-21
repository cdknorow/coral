"""Tests for the /api/sessions/live/{name}/search-files endpoint."""

import pytest
import pytest_asyncio
from unittest.mock import AsyncMock, patch
from httpx import ASGITransport, AsyncClient

from coral.web_server import app
from coral.store import CoralStore as SessionStore
from coral.api.live_sessions import _file_list_cache


# ── Fixtures ──────────────────────────────────────────────────────────────


@pytest_asyncio.fixture
async def tmp_store(tmp_path):
    db_path = tmp_path / "test.db"
    s = SessionStore(db_path=db_path)
    await s._get_conn()
    yield s
    await s.close()


@pytest_asyncio.fixture
async def client(tmp_store, monkeypatch):
    import coral.web_server as ws
    ws._set_store(tmp_store)
    monkeypatch.setattr(ws, "store", tmp_store)
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as c:
        yield c


SAMPLE_GIT_LS_FILES = "\n".join([
    "src/main.py",
    "src/utils.py",
    "src/api/routes.py",
    "src/api/models.py",
    "tests/test_main.py",
    "tests/test_utils.py",
    "tests/test_api.py",
    "README.md",
    "setup.py",
    "docs/guide.md",
])


@pytest.fixture(autouse=True)
def clear_file_list_cache():
    """Clear the file list cache before and after each test."""
    _file_list_cache.clear()
    yield
    _file_list_cache.clear()


@pytest.fixture
def mock_workdir_and_git():
    """Patch _find_pane + run_cmd (for git ls-files) to return sample files."""
    find_pane = AsyncMock(return_value={"current_path": "/fake/workdir"})

    async def fake_run_cmd(*args, **kwargs):
        if "ls-files" in args:
            return (0, SAMPLE_GIT_LS_FILES, "")
        return (0, "", "")

    with patch("coral.api.live_sessions._find_pane", find_pane), \
         patch("coral.tools.utils.run_cmd", fake_run_cmd):
        yield


# ── Endpoint tests ───────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_list_files_returns_all(client, mock_workdir_and_git):
    """With no query param, returns the full file list."""
    resp = await client.get("/api/sessions/live/agent-1/search-files")
    assert resp.status_code == 200
    files = resp.json()["files"]
    assert len(files) == 10


@pytest.mark.asyncio
async def test_search_git_failure_returns_empty(client):
    """If git ls-files fails, returns empty list."""
    find_pane = AsyncMock(return_value={"current_path": "/fake/workdir"})

    async def failing_git(*args, **kwargs):
        return (1, "", "fatal: not a git repo")

    with patch("coral.api.live_sessions._find_pane", find_pane), \
         patch("coral.tools.utils.run_cmd", failing_git):
        resp = await client.get("/api/sessions/live/agent-1/search-files")
    assert resp.status_code == 200
    assert resp.json()["files"] == []


@pytest.mark.asyncio
async def test_search_no_workdir_returns_empty(client):
    """If working directory can't be resolved, returns empty."""
    with patch("coral.api.live_sessions._find_pane", AsyncMock(return_value=None)):
        resp = await client.get("/api/sessions/live/agent-1/search-files")
    assert resp.status_code == 200
    assert resp.json()["files"] == []


@pytest.mark.asyncio
async def test_search_cache_hit_avoids_second_git_call(client):
    """Second search with same workdir should use cached file list (git called once)."""
    find_pane = AsyncMock(return_value={"current_path": "/fake/workdir"})
    call_count = 0

    async def counting_run_cmd(*args, **kwargs):
        nonlocal call_count
        if "ls-files" in args:
            call_count += 1
            return (0, SAMPLE_GIT_LS_FILES, "")
        return (0, "", "")

    with patch("coral.api.live_sessions._find_pane", find_pane), \
         patch("coral.tools.utils.run_cmd", counting_run_cmd):
        resp1 = await client.get("/api/sessions/live/agent-1/search-files")
        assert resp1.status_code == 200
        assert len(resp1.json()["files"]) == 10

        resp2 = await client.get("/api/sessions/live/agent-1/search-files")
        assert resp2.status_code == 200
        assert len(resp2.json()["files"]) == 10

    assert call_count == 1  # git ls-files called only once


@pytest.mark.asyncio
async def test_search_cache_expiry(client):
    """After TTL expires, git ls-files should be called again."""
    import coral.api.live_sessions as mod

    find_pane = AsyncMock(return_value={"current_path": "/fake/workdir"})
    call_count = 0

    async def counting_run_cmd(*args, **kwargs):
        nonlocal call_count
        if "ls-files" in args:
            call_count += 1
            return (0, SAMPLE_GIT_LS_FILES, "")
        return (0, "", "")

    with patch("coral.api.live_sessions._find_pane", find_pane), \
         patch("coral.tools.utils.run_cmd", counting_run_cmd):
        # First call populates cache
        resp1 = await client.get("/api/sessions/live/agent-1/search-files")
        assert resp1.status_code == 200
        assert call_count == 1

        # Expire the cache by backdating the timestamp
        for key in mod._file_list_cache:
            ts, files = mod._file_list_cache[key]
            mod._file_list_cache[key] = (ts - mod._FILE_LIST_TTL_S - 1, files)

        # Second call should miss cache and call git again
        resp2 = await client.get("/api/sessions/live/agent-1/search-files")
        assert resp2.status_code == 200
        assert call_count == 2
