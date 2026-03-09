"""FastAPI web server for the Corral Dashboard.

This module handles FastAPI initialization, lifespan (background tasks),
static/template mounting, and router registration. All REST/WebSocket
endpoints live in the ``corral.api`` package.
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

from corral.store import CorralStore
from corral.jsonl_reader import JsonlSessionReader

# Import routers
from corral.api import live_sessions as live_sessions_api
from corral.api import history as history_api
from corral.api import system as system_api

log = logging.getLogger(__name__)
BASE_DIR = Path(__file__).parent


async def _resume_persistent_sessions():
    """Resume live sessions that were running when Corral last stopped.

    Compares the ``live_sessions`` DB table against currently running tmux
    sessions.  Any registered session without a matching tmux session is
    relaunched (with ``--resume`` for Claude agents so they pick up context).
    Sessions whose working directory no longer exists are silently removed.
    """
    from corral.session_manager import discover_corral_agents, launch_claude_session

    try:
        registered = await store.get_all_live_sessions()
        if not registered:
            return

        # Discover what is already alive in tmux
        live_agents = await discover_corral_agents()
        live_session_ids = {a["session_id"] for a in live_agents}

        for rec in registered:
            sid = rec["session_id"]
            if sid in live_session_ids:
                continue  # Already running — nothing to do

            working_dir = rec["working_dir"]
            if not os.path.isdir(working_dir):
                # Working directory gone (worktree removed?) — clean up
                await store.unregister_live_session(sid)
                log.info("Removed stale live session %s (dir missing: %s)", sid[:8], working_dir)
                continue

            agent_type = rec["agent_type"]
            display_name = rec.get("display_name")
            flags = rec.get("flags")  # Already deserialized by get_all_live_sessions

            log.info(
                "Resuming session %s (%s) in %s",
                sid[:8], agent_type, working_dir,
            )

            # Use resume_from_id if available (tracks the original Claude
            # session across multiple Corral restarts), otherwise fall back
            # to the session_id itself (first restart after initial launch).
            resume_id = rec.get("resume_from_id") or sid

            result = await launch_claude_session(
                working_dir, agent_type, display_name=display_name,
                resume_session_id=resume_id,
                flags=flags,
            )

            if result.get("error"):
                log.warning("Failed to resume session %s: %s", sid[:8], result["error"])
                await store.unregister_live_session(sid)
            else:
                # Old session record is replaced by the new launch
                # (launch_claude_session calls register_live_session with new id)
                await store.unregister_live_session(sid)
    except Exception:
        log.exception("Error resuming persistent sessions")


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Start background indexer, batch summarizer, and git poller on server startup."""
    from corral.background_tasks import SessionIndexer, BatchSummarizer, GitPoller
    from corral.agents import get_agent
    from corral.session_manager import discover_corral_agents

    # Resume any persistent live sessions that are no longer running in tmux
    await _resume_persistent_sessions()

    # Install hooks into each live agent's worktree, and register any
    # tmux sessions not yet tracked in the live_sessions table.
    live_agents = await discover_corral_agents()
    tracked = {r["session_id"] for r in await store.get_all_live_sessions()}
    for agent in live_agents:
        wd = agent.get("working_directory") or ""
        if wd:
            agent_impl = get_agent(agent["agent_type"])
            agent_impl.install_hooks(wd)
        sid = agent.get("session_id")
        if sid and sid not in tracked:
            await store.register_live_session(
                sid, agent["agent_type"], agent["agent_name"], wd,
            )

    # Seed _last_known from DB so we don't re-insert events already stored.
    live_sessions_api._last_known.update(await store.get_last_known_status_summary())

    indexer = SessionIndexer(store)
    summarizer = BatchSummarizer(store)
    git_poller = GitPoller(store)

    indexer_task = asyncio.create_task(indexer.run_forever(interval=120))
    summarizer_task = asyncio.create_task(summarizer.run_forever())
    git_task = asyncio.create_task(git_poller.run_forever(interval=120))

    # Store indexer on app state so endpoints can trigger refresh
    app.state.indexer = indexer

    yield

    indexer_task.cancel()
    summarizer_task.cancel()
    git_task.cancel()
    await store.close()


app = FastAPI(title="Corral Dashboard", lifespan=lifespan)
store = CorralStore()
jsonl_reader = JsonlSessionReader()

# Wire up module-level dependencies in router modules
live_sessions_api.store = store
live_sessions_api.jsonl_reader = jsonl_reader
history_api.store = store
history_api._app = app
system_api.store = store

# Register routers
app.include_router(live_sessions_api.router)
app.include_router(history_api.router)
app.include_router(system_api.router)

# Mount static files and templates
app.mount("/static", StaticFiles(directory=str(BASE_DIR / "static")), name="static")
templates = Jinja2Templates(directory=str(BASE_DIR / "templates"))

# Backward-compatible aliases so existing code/tests that reference
# ``corral.web_server._track_status_summary_events`` etc. still work.
_track_status_summary_events = live_sessions_api._track_status_summary_events
_last_known = live_sessions_api._last_known


def _set_store(new_store):
    """Update the store on web_server and all router modules (used by tests)."""
    global store
    store = new_store
    live_sessions_api.store = new_store
    history_api.store = new_store
    system_api.store = new_store


# ── Dashboard SPA ──────────────────────────────────────────────────────────


@app.get("/", response_class=HTMLResponse)
async def index(request: Request):
    """Serve the corral dashboard SPA."""
    return templates.TemplateResponse("index.html", {"request": request, "corral_root": os.getcwd()})


# ── Entry Point ──────────────────────────────────────────────────────────────


def main():
    import threading
    import webbrowser
    import uvicorn

    parser = argparse.ArgumentParser(description="Corral Dashboard")
    parser.add_argument("--host", default="0.0.0.0", help="Host to bind to (default: 0.0.0.0)")
    parser.add_argument("--port", type=int, default=8420, help="Port to bind to (default: 8420)")
    parser.add_argument("--reload", action="store_true", help="Enable auto-reload for development")
    parser.add_argument("--no-browser", action="store_true", help="Don't open the browser on startup")
    args = parser.parse_args()

    if not args.no_browser:
        url = f"http://localhost:{args.port}"
        threading.Timer(1.5, webbrowser.open, args=(url,)).start()

    uvicorn.run(
        "corral.web_server:app",
        host=args.host,
        port=args.port,
        reload=args.reload,
    )


if __name__ == "__main__":
    main()
