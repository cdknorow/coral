"""FastAPI web server for the Corral Dashboard."""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Optional

from fastapi import FastAPI, Query, WebSocket, WebSocketDisconnect
from fastapi.responses import HTMLResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates
from starlette.requests import Request

from corral.session_manager import (
    discover_corral_agents,
    get_agent_log_path,
    get_log_status,
    get_session_info,
    send_to_tmux,
    send_raw_keys,
    capture_pane,
    kill_session,
    restart_session,
    open_terminal_attached,
    load_history_sessions,
    load_history_session_messages,
    launch_claude_session,
)
from corral.log_streamer import get_log_snapshot
from corral.session_store import SessionStore
from corral.task_detector import scan_log_for_pulse_events
from corral.jsonl_reader import JsonlSessionReader

log = logging.getLogger(__name__)
BASE_DIR = Path(__file__).parent

# Track last-known status/summary per session_id so we only emit events on change.
_last_known: dict[str, dict[str, str | None]] = {}


async def _track_status_summary_events(
    agent_name: str, status: str | None, summary: str | None, session_id: str | None = None,
):
    """Insert agent_events when status or summary changes for a live agent."""
    # Resolve session_id from store if not provided
    if session_id is None:
        session_id = await store.get_agent_session_id(agent_name)
    # Dedup key: use session_id if available, fall back to agent_name
    dedup_key = session_id or agent_name
    prev = _last_known.get(dedup_key, {"status": None, "summary": None})

    if status and status != prev.get("status"):
        await store.insert_agent_event(
            agent_name, "status", status, session_id=session_id,
        )
    if summary and summary != prev.get("summary"):
        await store.insert_agent_event(
            agent_name, "goal", summary, session_id=session_id,
        )

    _last_known[dedup_key] = {"status": status, "summary": summary}


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
    from corral.session_indexer import SessionIndexer, BatchSummarizer
    from corral.git_poller import GitPoller
    from corral.utils import install_hooks
    from corral.session_manager import discover_corral_agents, launch_claude_session

    # Resume any persistent live sessions that are no longer running in tmux
    await _resume_persistent_sessions()

    # Install hooks into each live agent's worktree, and register any
    # tmux sessions not yet tracked in the live_sessions table.
    live_agents = await discover_corral_agents()
    tracked = {r["session_id"] for r in await store.get_all_live_sessions()}
    for agent in live_agents:
        wd = agent.get("working_directory") or ""
        if wd:
            install_hooks(wd)
        sid = agent.get("session_id")
        if sid and sid not in tracked:
            await store.register_live_session(
                sid, agent["agent_type"], agent["agent_name"], wd,
            )

    # Seed _last_known from DB so we don't re-insert events already stored.
    _last_known.update(await store.get_last_known_status_summary())

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
store = SessionStore()
jsonl_reader = JsonlSessionReader()

# Mount static files and templates
app.mount("/static", StaticFiles(directory=str(BASE_DIR / "static")), name="static")
templates = Jinja2Templates(directory=str(BASE_DIR / "templates"))


# ── REST Endpoints ──────────────────────────────────────────────────────────


@app.get("/", response_class=HTMLResponse)
async def index(request: Request):
    """Serve the corral dashboard SPA."""
    import os
    return templates.TemplateResponse("index.html", {"request": request, "corral_root": os.getcwd()})


COMMAND_MAP = {
    "claude": {"compress": "/compact", "clear": "/clear"},
    "gemini": {"compress": "/compact", "clear": "/clear"},
}


@app.get("/api/sessions/live")
async def get_live_sessions():
    """List active corral agents with their current status."""
    agents = await discover_corral_agents()
    git_state = await store.get_all_latest_git_state()
    # Bulk-fetch display names for all live sessions
    session_ids = [a["session_id"] for a in agents if a.get("session_id")]
    display_names = await store.get_display_names(session_ids)
    latest_events = await store.get_latest_event_types(session_ids)
    results = []
    for agent in agents:
        log_info = get_log_status(agent["log_path"])
        git = git_state.get(agent["agent_name"])
        name = agent["agent_name"]
        sid = agent.get("session_id")
        waiting = latest_events.get(sid) == "stop" if sid else False
        entry = {
            "name": name,
            "agent_type": agent["agent_type"],
            "session_id": sid,
            "tmux_session": agent.get("tmux_session"),
            "log_path": agent["log_path"],
            "status": log_info["status"],
            "summary": log_info["summary"],
            "staleness_seconds": log_info["staleness_seconds"],
            "commands": COMMAND_MAP.get(agent["agent_type"].lower(), COMMAND_MAP["claude"]),
            "branch": git["branch"] if git else None,
            "display_name": display_names.get(sid) if sid else None,
            "working_directory": agent.get("working_directory", ""),
            "waiting_for_input": waiting,
        }
        results.append(entry)
        await _track_status_summary_events(name, log_info["status"], log_info["summary"], session_id=sid)
        # Scan log for all protocol events (TASK, TASK_DONE, custom events)
        await scan_log_for_pulse_events(store, name, agent["log_path"], session_id=sid)
    return results


@app.get("/api/sessions/live/{name}")
async def get_live_session_detail(
    name: str, agent_type: str | None = None, session_id: str | None = None,
):
    """Get detailed info for a specific live session."""
    log_path = get_agent_log_path(name, agent_type, session_id=session_id)
    if not log_path:
        return {"error": f"Agent '{name}' not found"}

    snapshot = get_log_snapshot(str(log_path))
    pane_text = await capture_pane(name, agent_type=agent_type, session_id=session_id)

    return {
        "name": name,
        "session_id": session_id,
        "status": snapshot["status"],
        "summary": snapshot["summary"],
        "recent_lines": snapshot["recent_lines"],
        "staleness_seconds": snapshot["staleness_seconds"],
        "pane_capture": pane_text,
    }


@app.get("/api/sessions/live/{name}/capture")
async def get_pane_capture(name: str, agent_type: str | None = None, session_id: str | None = None):
    """Capture current tmux pane content."""
    text = await capture_pane(name, agent_type=agent_type, session_id=session_id)
    if text is None:
        return {"error": f"Could not capture pane for '{name}'"}
    return {"name": name, "capture": text}


@app.get("/api/sessions/live/{name}/chat")
async def get_live_chat(
    name: str,
    session_id: str | None = None,
    working_directory: str | None = None,
    after: int = Query(0, ge=0),
):
    """Get chat messages from the JSONL transcript for a live session."""
    if not session_id:
        return {"messages": [], "total": 0}
    new_msgs, total = await asyncio.to_thread(
        jsonl_reader.read_new_messages, session_id, working_directory or ""
    )
    # Return messages after the caller's cursor
    return {"messages": jsonl_reader._cache[session_id].messages[after:], "total": total}


@app.get("/api/sessions/live/{name}/info")
async def get_live_session_info(name: str, agent_type: str | None = None, session_id: str | None = None):
    """Return enriched metadata for a live session (Info modal)."""
    info = await get_session_info(name, agent_type, session_id=session_id)
    if not info:
        return {"error": f"Agent '{name}' not found"}
    git = await store.get_latest_git_state(name)
    if git:
        info["git_branch"] = git["branch"]
        info["git_commit_hash"] = git["commit_hash"]
        info["git_commit_subject"] = git["commit_subject"]
    return info


@app.get("/api/sessions/live/{name}/git")
async def get_live_session_git(name: str, limit: int = Query(20, ge=1, le=100)):
    """Return recent git snapshots (commit history) for a live agent."""
    snapshots = await asyncio.to_thread(store.get_git_snapshots, name, limit)
    return {"agent_name": name, "snapshots": snapshots}


@app.get("/api/sessions/history")
async def get_history_sessions(
    page: int = Query(1, ge=1),
    page_size: int = Query(50, ge=1, le=200),
    q: Optional[str] = Query(None),
    tag_id: Optional[int] = Query(None),
    source_type: Optional[str] = Query(None),
):
    """Paginated history sessions from the index, with search/tag/source filters.

    Falls back to scanning files if the index is empty (cold start).
    """
    result = await store.list_sessions_paged(page, page_size, q, tag_id, source_type)

    if result["total"] == 0 and not q and not tag_id and not source_type:
        # Cold start — index hasn't run yet; trigger immediate index and fall back
        indexer = getattr(app.state, "indexer", None)
        if indexer:
            try:
                await indexer.run_once()
                result = await store.list_sessions_paged(
                    page, page_size, q, tag_id, source_type
                )
            except Exception:
                pass

        # If still empty, fall back to old file-scan method
        if result["total"] == 0:
            sessions = load_history_sessions()
            metadata = await store.get_all_session_metadata()
            for s in sessions:
                meta = metadata.get(s["session_id"])
                if meta:
                    s["tags"] = meta["tags"]
                    s["has_notes"] = meta["has_notes"]
                else:
                    s["tags"] = []
                    s["has_notes"] = False
            return {"sessions": sessions, "total": len(sessions), "page": 1, "page_size": len(sessions)}

    return result


@app.post("/api/indexer/refresh")
async def trigger_indexer_refresh():
    """Trigger an immediate re-index."""
    indexer = getattr(app.state, "indexer", None)
    if not indexer:
        return {"error": "Indexer not available"}
    result = await indexer.run_once()
    return result


@app.get("/api/sessions/history/{session_id}/git")
async def get_history_session_git(session_id: str):
    """Return git commits that occurred during a historical session's time range."""
    snapshots = await store.get_git_snapshots_for_session(session_id)
    return {"session_id": session_id, "commits": snapshots}


@app.get("/api/sessions/history/{session_id}/tasks")
async def get_history_session_tasks(session_id: str):
    """Get tasks for a historical session (read-only)."""
    return await store.list_tasks_by_session(session_id)


@app.get("/api/sessions/history/{session_id}/agent-notes")
async def get_history_session_agent_notes(session_id: str):
    """Get agent notes for a historical session (read-only)."""
    return await store.list_notes_by_session(session_id)


@app.get("/api/sessions/history/{session_id}/events")
async def get_history_session_events(session_id: str, limit: int = Query(200, ge=1, le=500)):
    """Get activity events for a historical session (read-only)."""
    return await store.list_events_by_session(session_id, limit)


@app.get("/api/sessions/history/{session_id}")
async def get_history_session_detail(session_id: str):
    """Get all messages for a historical session."""
    messages = load_history_session_messages(session_id)
    if not messages:
        return {"error": f"Session '{session_id}' not found"}
    return {"session_id": session_id, "messages": messages}


@app.post("/api/sessions/live/{name}/send")
async def send_command(name: str, body: dict):
    """Send a command to a live tmux session."""
    command = body.get("command", "").strip()
    if not command:
        return {"error": "No command provided"}

    agent_type = body.get("agent_type") or None
    sid = body.get("session_id") or None
    error = await send_to_tmux(name, command, agent_type=agent_type, session_id=sid)
    if error:
        return {"error": error}
    return {"ok": True, "command": command}


@app.post("/api/sessions/live/{name}/keys")
async def send_keys(name: str, body: dict):
    """Send raw tmux key names (e.g. BTab, Escape) to a live session."""
    keys = body.get("keys", [])
    if not keys or not isinstance(keys, list):
        return {"error": "keys must be a non-empty list of tmux key names"}

    agent_type = body.get("agent_type") or None
    sid = body.get("session_id") or None
    error = await send_raw_keys(name, keys, agent_type=agent_type, session_id=sid)
    if error:
        return {"error": error}
    return {"ok": True, "keys": keys}


@app.post("/api/sessions/live/{name}/kill")
async def kill_live_session(name: str, body: dict | None = None):
    """Kill the tmux session for a live agent."""
    agent_type = (body or {}).get("agent_type") or None
    sid = (body or {}).get("session_id") or None
    error = await kill_session(name, agent_type=agent_type, session_id=sid)
    if error:
        return {"error": error}
    return {"ok": True}


@app.post("/api/sessions/live/{name}/restart")
async def restart_live_session(name: str, body: dict | None = None):
    """Restart the agent session: exit the current session and launch a fresh one in the same pane."""
    agent_type = (body or {}).get("agent_type") or None
    extra_flags = (body or {}).get("extra_flags") or None
    sid = (body or {}).get("session_id") or None
    result = await restart_session(name, agent_type=agent_type, extra_flags=extra_flags, session_id=sid)
    return result


@app.post("/api/sessions/live/{name}/resume")
async def resume_live_session(name: str, body: dict):
    """Restart the agent with --resume to continue a historical session."""
    resume_sid = body.get("session_id")
    agent_type = body.get("agent_type") or None
    current_sid = body.get("current_session_id") or None
    if not resume_sid:
        return {"error": "session_id is required"}
    result = await restart_session(
        name, agent_type=agent_type, resume_session_id=resume_sid, session_id=current_sid,
    )
    return result


@app.post("/api/sessions/live/{name}/attach")
async def attach_terminal(name: str, body: dict | None = None):
    """Open a local terminal window attached to the agent's tmux session."""
    agent_type = (body or {}).get("agent_type") or None
    sid = (body or {}).get("session_id") or None
    error = await open_terminal_attached(name, agent_type=agent_type, session_id=sid)
    if error:
        return {"error": error}
    return {"ok": True}


@app.put("/api/sessions/live/{name}/display-name")
async def set_display_name(name: str, body: dict):
    """Set or update the display name for a live session."""
    display_name = body.get("display_name", "").strip()
    session_id = body.get("session_id")
    if not session_id:
        return {"error": "session_id is required"}
    if not display_name:
        return {"error": "display_name is required"}
    await store.set_display_name(session_id, display_name)
    await store.update_live_session_display_name(session_id, display_name)
    return {"ok": True, "display_name": display_name}


# ── User Settings Endpoints ─────────────────────────────────────────────────


@app.get("/api/settings")
async def get_settings():
    """Return all global user settings."""
    settings = await store.get_settings()
    return {"settings": settings}


@app.put("/api/settings")
async def put_settings(body: dict):
    """Upsert one or more global user settings."""
    for key, value in body.items():
        await store.set_setting(str(key), str(value))
    return {"ok": True}


@app.get("/api/filesystem/list")
async def list_filesystem(path: str = "~"):
    """List directories at a given path for the directory browser."""
    import os

    expanded = os.path.expanduser(path)
    if not os.path.isdir(expanded):
        return {"error": f"Not a directory: {path}", "entries": []}

    entries = []
    try:
        for name in sorted(os.listdir(expanded), key=str.lower):
            full = os.path.join(expanded, name)
            if os.path.isdir(full) and not name.startswith("."):
                entries.append(name)
    except PermissionError:
        return {"error": "Permission denied", "entries": []}

    return {"path": expanded, "entries": entries}


@app.post("/api/sessions/launch")
async def launch_session(body: dict):
    """Launch a new Claude/Gemini session."""
    working_dir = body.get("working_dir", "").strip()
    agent_type = body.get("agent_type", "claude").strip()
    display_name = body.get("display_name", "").strip() or None

    if not working_dir:
        return {"error": "working_dir is required"}

    result = await launch_claude_session(working_dir, agent_type, display_name=display_name)
    return result


# ── Session Notes Endpoints ─────────────────────────────────────────────────


@app.get("/api/sessions/history/{session_id}/notes")
async def get_session_notes(session_id: str):
    """Get notes and auto-summary for a session. Triggers auto-summarization if empty."""
    notes = await store.get_session_notes(session_id)

    # If no notes and no auto-summary, trigger summarization in background
    if not notes["notes_md"] and not notes["auto_summary"]:
        try:
            from corral.auto_summarizer import AutoSummarizer

            summarizer = AutoSummarizer(store)
            asyncio.create_task(summarizer.summarize_session(session_id))
            notes["summarizing"] = True
        except ImportError:
            notes["summarizing"] = False

    return notes


@app.put("/api/sessions/history/{session_id}/notes")
async def save_session_notes(session_id: str, body: dict):
    """Save user-edited markdown notes for a session."""
    notes_md = body.get("notes_md", "")
    await store.save_session_notes(session_id, notes_md)
    return {"ok": True}


@app.post("/api/sessions/history/{session_id}/resummarize")
async def resummarize_session(session_id: str):
    """Force re-generate auto-summary for a session."""
    try:
        from corral.auto_summarizer import AutoSummarizer

        summarizer = AutoSummarizer(store)
        summary = await summarizer.summarize_session(session_id)
        return {"ok": True, "auto_summary": summary}
    except ImportError:
        return {"error": "claude-agent-sdk not installed"}
    except Exception as e:
        return {"error": str(e)}


# ── Tags Endpoints ─────────────────────────────────────────────────────────


@app.get("/api/tags")
async def list_tags():
    """List all tags."""
    return await store.list_tags()


@app.post("/api/tags")
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


@app.delete("/api/tags/{tag_id}")
async def delete_tag(tag_id: int):
    """Delete a tag."""
    await store.delete_tag(tag_id)
    return {"ok": True}


@app.get("/api/sessions/history/{session_id}/tags")
async def get_session_tags(session_id: str):
    """Get tags for a session."""
    return await store.get_session_tags(session_id)


@app.post("/api/sessions/history/{session_id}/tags")
async def add_session_tag(session_id: str, body: dict):
    """Add a tag to a session."""
    tag_id = body.get("tag_id")
    if tag_id is None:
        return {"error": "tag_id is required"}
    await store.add_session_tag(session_id, int(tag_id))
    return {"ok": True}


@app.delete("/api/sessions/history/{session_id}/tags/{tag_id}")
async def remove_session_tag(session_id: str, tag_id: int):
    """Remove a tag from a session."""
    await store.remove_session_tag(session_id, tag_id)
    return {"ok": True}


# ── Agent Tasks Endpoints ──────────────────────────────────────────────────


@app.get("/api/sessions/live/{name}/tasks")
async def list_agent_tasks(name: str, session_id: str | None = None):
    """List tasks for a live agent, scoped by session_id."""
    if session_id is None:
        return []
    return await store.list_agent_tasks(name, session_id=session_id)


@app.post("/api/sessions/live/{name}/tasks")
async def create_agent_task(name: str, body: dict):
    """Create a task for a live agent, scoped to the current session."""
    title = body.get("title", "").strip()
    if not title:
        return {"error": "title is required"}
    session_id = body.get("session_id")
    task = await store.create_agent_task(name, title, session_id=session_id)
    return task


@app.patch("/api/sessions/live/{name}/tasks/{task_id}")
async def update_agent_task(name: str, task_id: int, body: dict):
    """Update a task (toggle complete, edit title, reorder)."""
    title = body.get("title")
    completed = body.get("completed")
    sort_order = body.get("sort_order")
    await store.update_agent_task(task_id, title=title, completed=completed, sort_order=sort_order)
    return {"ok": True}


@app.delete("/api/sessions/live/{name}/tasks/{task_id}")
async def delete_agent_task(name: str, task_id: int):
    """Delete a task."""
    await store.delete_agent_task(task_id)
    return {"ok": True}


@app.post("/api/sessions/live/{name}/tasks/reorder")
async def reorder_agent_tasks(name: str, body: dict):
    """Reorder tasks by providing an ordered list of task IDs."""
    task_ids = body.get("task_ids", [])
    if not task_ids:
        return {"error": "task_ids is required"}
    await store.reorder_agent_tasks(name, task_ids)
    return {"ok": True}


# ── Agent Notes Endpoints ──────────────────────────────────────────────────


@app.get("/api/sessions/live/{name}/notes")
async def list_agent_notes(name: str, session_id: str | None = None):
    """List notes for a live agent, scoped by session_id."""
    if session_id is None:
        return []
    return await store.list_agent_notes(name, session_id=session_id)


@app.post("/api/sessions/live/{name}/notes")
async def create_agent_note(name: str, body: dict):
    """Create a note for a live agent, scoped to the current session."""
    content = body.get("content", "").strip()
    if not content:
        return {"error": "content is required"}
    session_id = body.get("session_id")
    note = await store.create_agent_note(name, content, session_id=session_id)
    return note


@app.patch("/api/sessions/live/{name}/notes/{note_id}")
async def update_agent_note(name: str, note_id: int, body: dict):
    """Update a note's content."""
    content = body.get("content")
    if content is None:
        return {"error": "content is required"}
    await store.update_agent_note(note_id, content)
    return {"ok": True}


@app.delete("/api/sessions/live/{name}/notes/{note_id}")
async def delete_agent_note(name: str, note_id: int):
    """Delete a note."""
    await store.delete_agent_note(note_id)
    return {"ok": True}


# ── Agent Events Endpoints ─────────────────────────────────────────────────


@app.get("/api/sessions/live/{name}/events")
async def list_agent_events(
    name: str, limit: int = Query(50, ge=1, le=200), session_id: str | None = None,
):
    """List recent events for a live agent, scoped by session_id."""
    events = await store.list_agent_events(name, limit, session_id=session_id)
    return events


@app.post("/api/sessions/live/{name}/events")
async def create_agent_event(name: str, body: dict):
    """Create an event for a live agent (called by hook)."""
    event_type = body.get("event_type", "").strip()
    summary = body.get("summary", "").strip()
    if not event_type or not summary:
        return {"error": "event_type and summary are required"}
    tool_name = body.get("tool_name")
    session_id = body.get("session_id")
    detail_json = body.get("detail_json")

    event = await store.insert_agent_event(
        name, event_type, summary,
        tool_name=tool_name, session_id=session_id, detail_json=detail_json,
    )
    return event


@app.get("/api/sessions/live/{name}/events/counts")
async def get_agent_event_counts(name: str, session_id: str | None = None):
    """Get event counts grouped by tool name for a session."""
    counts = await store.get_agent_event_counts(name, session_id=session_id)
    return counts


@app.delete("/api/sessions/live/{name}/events")
async def clear_agent_events(name: str, session_id: str | None = None):
    """Clear events for a live agent session."""
    await store.clear_agent_events(name, session_id=session_id)
    return {"ok": True}


# ── WebSocket Endpoints ─────────────────────────────────────────────────────


@app.websocket("/ws/corral")
async def ws_corral(websocket: WebSocket):
    """Stream corral-wide session list updates (polls every 3s)."""
    await websocket.accept()

    last_state = None
    try:
        while True:
            agents = await discover_corral_agents()
            git_state = await store.get_all_latest_git_state()
            # Bulk-fetch display names
            ws_session_ids = [a["session_id"] for a in agents if a.get("session_id")]
            ws_display_names = await store.get_display_names(ws_session_ids)
            # Check which agents are waiting for input (latest event is "stop")
            ws_latest_events = await store.get_latest_event_types(ws_session_ids)
            results = []
            for agent in agents:
                log_info = get_log_status(agent["log_path"])
                git = git_state.get(agent["agent_name"])
                name = agent["agent_name"]
                sid = agent.get("session_id")
                waiting = ws_latest_events.get(sid) == "stop" if sid else False
                results.append({
                    "name": name,
                    "agent_type": agent["agent_type"],
                    "session_id": sid,
                    "tmux_session": agent.get("tmux_session"),
                    "status": log_info["status"],
                    "summary": log_info["summary"],
                    "staleness_seconds": log_info["staleness_seconds"],
                    "branch": git["branch"] if git else None,
                    "display_name": ws_display_names.get(sid) if sid else None,
                    "working_directory": agent.get("working_directory", ""),
                    "waiting_for_input": waiting,
                })
                await _track_status_summary_events(name, log_info["status"], log_info["summary"], session_id=sid)
                await scan_log_for_pulse_events(store, name, agent["log_path"], session_id=sid)

            current_state = json.dumps(results, sort_keys=True)
            if current_state != last_state:
                await websocket.send_json({"type": "corral_update", "sessions": results})
                last_state = current_state

            await asyncio.sleep(3)
    except WebSocketDisconnect:
        pass
    except Exception:
        pass


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
