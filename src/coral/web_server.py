"""FastAPI web server for the Coral Dashboard.

This module handles FastAPI initialization, lifespan (background tasks),
static/template mounting, and router registration. All REST/WebSocket
endpoints live in the ``coral.api`` package.
"""

from __future__ import annotations

import argparse
import asyncio
import logging
import os
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI
from fastapi.responses import HTMLResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates
from starlette.requests import Request

from fastapi.middleware.cors import CORSMiddleware

from coral.store import CoralStore
from coral.tools.jsonl_reader import JsonlSessionReader

# Import routers
from coral.api import live_sessions as live_sessions_api
from coral.api import history as history_api
from coral.api import system as system_api
from coral.api import schedule as schedule_api
from coral.api import webhooks as webhooks_api
from coral.api import tasks as tasks_api
from coral.api import uploads as uploads_api
from coral.api import themes as themes_api
from coral.api import board_remotes as board_remotes_api
from coral.api import templates as templates_api

from coral.tools.utils import get_package_dir

log = logging.getLogger(__name__)
BASE_DIR = get_package_dir()

# WAL size threshold (1 MB) — checkpoint and vacuum when exceeded
_WAL_COMPACT_THRESHOLD = 1_000_000


async def _compact_databases() -> None:
    """Checkpoint and vacuum SQLite databases if their WAL files are too large."""
    import aiosqlite
    from coral.store.connection import get_db_path
    from coral.messageboard.store import get_db_path as get_board_db_path

    for db_path in [get_db_path(), get_board_db_path()]:
        wal_path = Path(f"{db_path}-wal")
        if not wal_path.exists():
            continue
        wal_size = wal_path.stat().st_size
        if wal_size < _WAL_COMPACT_THRESHOLD:
            continue
        try:
            conn = await aiosqlite.connect(str(db_path))
            await conn.execute("PRAGMA wal_checkpoint(TRUNCATE)")
            try:
                await conn.execute("VACUUM")
            except Exception:
                pass  # VACUUM needs exclusive access; checkpoint alone is fine
            await conn.close()
            log.info("Compacted %s (WAL was %.1f MB)", db_path.name, wal_size / 1_048_576)
        except Exception as e:
            log.warning("Failed to compact %s: %s", db_path.name, e)


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Start background indexer, batch summarizer, git poller, and webhook dispatcher on server startup."""
    from coral.background_tasks import SessionIndexer, BatchSummarizer, GitPoller, WebhookDispatcher, IdleDetector, MessageBoardNotifier
    from coral.background_tasks.remote_board_poller import RemoteBoardPoller
    from coral.store.remote_boards import RemoteBoardStore
    from coral.tools.session_manager import discover_coral_agents, resume_persistent_sessions
    from coral.api.themes import seed_bundled_themes

    # Seed bundled themes (e.g. GhostV3) into ~/.coral/themes/ on first run
    seed_bundled_themes()

    # Defer heavy startup work so the server accepts requests immediately
    app.state.startup_complete = False

    async def _deferred_startup():
        import time as _time
        from coral.config import DEFERRED_STARTUP_DELAY_S
        await asyncio.sleep(DEFERRED_STARTUP_DELAY_S)

        log.info("Deferred startup: resuming persistent sessions...")
        _t0 = _time.monotonic()
        await resume_persistent_sessions(store, schedule_store)
        log.info("Deferred startup: resume_persistent_sessions took %.2fs", _time.monotonic() - _t0)

        # Restore board pause state for sleeping teams
        try:
            sleeping_boards = await store.get_sleeping_board_names()
            if sleeping_boards:
                from coral.messageboard.api import _paused_projects
                _paused_projects.update(sleeping_boards)
                log.info("Restored pause state for %d sleeping board(s)", len(sleeping_boards))
        except Exception:
            log.exception("Failed to restore sleeping board pause state")

        # Register any tmux sessions not yet tracked in the live_sessions table.
        log.info("Deferred startup: discovering agents...")
        _t1 = _time.monotonic()
        live_agents = await discover_coral_agents()
        tracked = {r["session_id"] for r in await store.get_all_live_sessions()}
        for agent in live_agents:
            wd = agent.get("working_directory") or ""
            sid = agent.get("session_id")
            if sid and sid not in tracked:
                await store.register_live_session(
                    sid, agent["agent_type"], agent["agent_name"], wd,
                )
        log.info("Deferred startup: agent discovery took %.2fs (%d agents)", _time.monotonic() - _t1, len(live_agents))

        # Seed _last_known from DB so we don't re-insert events already stored.
        live_sessions_api._last_known.update(await store.get_last_known_status_summary())
        app.state.startup_complete = True
        log.info("Deferred startup complete (total %.2fs)", _time.monotonic() - _t0)

    startup_task = asyncio.create_task(_deferred_startup())

    indexer = SessionIndexer(store)
    summarizer = BatchSummarizer(store)
    git_poller = GitPoller(store)
    dispatcher = WebhookDispatcher(store)
    idle_detector = IdleDetector(store)

    from coral.store.registry import get_board_store, set_store, set_board_store
    set_store(store)
    board_store = get_board_store()
    set_board_store(board_store)
    live_sessions_api.board_store = board_store
    board_notifier = MessageBoardNotifier(board_store)

    from coral.config import (
        INDEXER_INTERVAL_S, INDEXER_STARTUP_DELAY_S,
        GIT_POLLER_INTERVAL_S, WEBHOOK_DISPATCHER_INTERVAL_S,
        IDLE_DETECTOR_INTERVAL_S, BOARD_NOTIFIER_INTERVAL_S,
        REMOTE_POLLER_INTERVAL_S,
    )

    indexer_task = asyncio.create_task(indexer.run_forever(interval=INDEXER_INTERVAL_S, startup_delay=INDEXER_STARTUP_DELAY_S))
    summarizer_task = asyncio.create_task(summarizer.run_forever())
    git_task = asyncio.create_task(git_poller.run_forever(interval=GIT_POLLER_INTERVAL_S))
    webhook_task = asyncio.create_task(dispatcher.run_forever(interval=WEBHOOK_DISPATCHER_INTERVAL_S))
    idle_task = asyncio.create_task(idle_detector.run_forever(interval=IDLE_DETECTOR_INTERVAL_S))
    board_notifier_task = asyncio.create_task(board_notifier.run_forever(interval=BOARD_NOTIFIER_INTERVAL_S))

    remote_board_store = RemoteBoardStore()
    board_remotes_api.store = remote_board_store
    remote_poller = RemoteBoardPoller(remote_board_store)
    remote_poller_task = asyncio.create_task(remote_poller.run_forever(interval=REMOTE_POLLER_INTERVAL_S))

    # Start job scheduler
    from coral.background_tasks.scheduler import JobScheduler
    scheduler = JobScheduler(schedule_store)
    scheduler_task = asyncio.create_task(scheduler.run_forever())
    app.state.scheduler = scheduler
    tasks_api.scheduler = scheduler

    # Periodic update check (PyPI + GitHub releases)
    from coral.tools.update_checker import UpdateInfo, check_for_update
    app.state.update_info = UpdateInfo()

    async def _periodic_update_check():
        while True:
            await check_for_update(app.state.update_info)
            await asyncio.sleep(6 * 3600)

    update_task = asyncio.create_task(_periodic_update_check())

    # Periodic WAL checkpoint to keep the WAL file small during runtime
    from coral.config import WAL_CHECKPOINT_INTERVAL_S

    async def _periodic_wal_checkpoint():
        while True:
            await asyncio.sleep(WAL_CHECKPOINT_INTERVAL_S)
            try:
                conn = await store._get_conn()
                await conn.execute("PRAGMA wal_checkpoint(PASSIVE)")
            except Exception:
                log.debug("WAL checkpoint skipped (connection busy)")

    wal_task = asyncio.create_task(_periodic_wal_checkpoint())

    # Store indexer and git poller on app state so endpoints can trigger refresh
    app.state.indexer = indexer
    app.state.git_poller = git_poller

    app.state.webhook_dispatcher = dispatcher

    yield

    startup_task.cancel()
    update_task.cancel()
    wal_task.cancel()
    indexer_task.cancel()
    summarizer_task.cancel()
    git_task.cancel()
    scheduler_task.cancel()
    webhook_task.cancel()
    idle_task.cancel()
    board_notifier_task.cancel()
    remote_poller_task.cancel()
    try:
        await asyncio.wait_for(remote_poller.close(), timeout=5)
    except asyncio.TimeoutError:
        log.warning("Remote board poller close timed out")
    try:
        await asyncio.wait_for(dispatcher.close(), timeout=5)
    except asyncio.TimeoutError:
        log.warning("Webhook dispatcher close timed out")
    # Flush any pending events before closing the store connection
    from coral.store.tasks import _flush_events
    try:
        await _flush_events(store)
    except Exception:
        log.debug("Failed to flush pending events on shutdown")
    await board_store.close()
    await remote_board_store.close()
    await store.close()


app = FastAPI(title="Coral Dashboard", lifespan=lifespan)

# CORS: allow same-origin by default (browser enforces this).
# Explicitly allow localhost origins for cross-tab/port scenarios.
# This blocks cross-site attacks from arbitrary external origins.
app.add_middleware(
    CORSMiddleware,
    allow_origin_regex=r"^https?://(localhost|127\.0\.0\.1)(:\d+)?$",
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

store = CoralStore()
jsonl_reader = JsonlSessionReader()

# Schedule store shares the same DB but is a separate instance for the scheduler
from coral.store.schedule import ScheduleStore
schedule_store = ScheduleStore()

# Wire up module-level dependencies in router modules
live_sessions_api.store = store
live_sessions_api.jsonl_reader = jsonl_reader
live_sessions_api.schedule_store = schedule_store
history_api.store = store
history_api._app = app
system_api.store = store
schedule_api.store = schedule_store
webhooks_api.store = store
webhooks_api._app = app
tasks_api.store = schedule_store

# Register routers
app.include_router(live_sessions_api.router)
app.include_router(history_api.router)
app.include_router(system_api.router)
app.include_router(schedule_api.router)
app.include_router(webhooks_api.router)
app.include_router(tasks_api.router)
app.include_router(uploads_api.router)
app.include_router(themes_api.router)
app.include_router(board_remotes_api.router)
app.include_router(templates_api.router)

# Mount self-contained message board sub-app
from coral.messageboard.app import create_app as create_board_app
app.mount("/api/board", create_board_app())

# Mount static files and templates
app.mount("/static", StaticFiles(directory=str(BASE_DIR / "static")), name="static")
templates = Jinja2Templates(directory=str(BASE_DIR / "templates"))

# Backward-compatible aliases so existing code/tests that reference
# ``coral.web_server._track_status_summary_events`` etc. still work.
_track_status_summary_events = live_sessions_api._track_status_summary_events
_last_known = live_sessions_api._last_known


def _set_store(new_store):
    """Update the store on web_server and all router modules (used by tests)."""
    global store, schedule_store
    store = new_store
    live_sessions_api.store = new_store
    history_api.store = new_store
    system_api.store = new_store
    webhooks_api.store = new_store


def _set_schedule_store(new_store):
    """Update the schedule store (used by tests)."""
    global schedule_store
    schedule_store = new_store
    schedule_api.store = new_store
    tasks_api.store = new_store
    live_sessions_api.schedule_store = new_store


# ── Dashboard SPA ──────────────────────────────────────────────────────────


@app.get("/", response_class=HTMLResponse)
async def index(request: Request):
    """Serve the coral dashboard SPA."""
    try:
        from importlib.metadata import version as _pkg_version
        coral_version = _pkg_version("agent-coral")
    except Exception:
        coral_version = "dev"
    return templates.TemplateResponse(request=request, name="index.html", context={"coral_root": os.getcwd(), "coral_version": coral_version})


@app.get("/diff", response_class=HTMLResponse)
async def diff_view(request: Request):
    """Serve the standalone diff viewer page."""
    return templates.TemplateResponse(request=request, name="diff.html")


@app.get("/preview", response_class=HTMLResponse)
async def preview_view(request: Request):
    """Serve the standalone file preview page."""
    return templates.TemplateResponse(request=request, name="preview.html")


# ── Entry Point ──────────────────────────────────────────────────────────────


def main():
    import shutil
    import threading
    import webbrowser
    import uvicorn

    if not shutil.which("tmux"):
        print("Error: tmux is not installed. Coral requires tmux for agent management.", file=sys.stderr)
        print("", file=sys.stderr)
        print("Install tmux:", file=sys.stderr)
        print("  macOS:  brew install tmux", file=sys.stderr)
        print("  Ubuntu: sudo apt install tmux", file=sys.stderr)
        print("  Fedora: sudo dnf install tmux", file=sys.stderr)
        sys.exit(1)

    parser = argparse.ArgumentParser(description="Coral Dashboard")
    parser.add_argument("--host", default=os.environ.get("CORAL_HOST", "127.0.0.1"), help="Host to bind to (default: 127.0.0.1, env: CORAL_HOST)")
    parser.add_argument("--port", type=int, default=8420, help="Port to bind to (default: 8420)")
    parser.add_argument("--reload", action="store_true", help="Enable auto-reload for development")
    parser.add_argument("--no-browser", action="store_true", help="Don't open the browser on startup")
    parser.add_argument("--data-dir", type=str, default=None,
        help="Directory for Coral data (databases, uploads, themes). Default: ~/.coral. Env: CORAL_DATA_DIR")
    args = parser.parse_args()

    # Set data dir env var before any store/DB initialization
    if args.data_dir:
        os.environ["CORAL_DATA_DIR"] = str(Path(args.data_dir).expanduser().resolve())

    if not args.no_browser:
        url = f"http://localhost:{args.port}"

        def _open_browser():
            try:
                webbrowser.open(url)
            except Exception:
                pass

        t = threading.Timer(1.5, _open_browser)
        t.daemon = True
        t.start()

    uvicorn.run(
        "coral.web_server:app",
        host=args.host,
        port=args.port,
        reload=args.reload,
    )


if __name__ == "__main__":
    main()
