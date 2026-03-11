"""API routes for live (active) agent sessions."""

from __future__ import annotations

import asyncio
import json
import logging
from typing import TYPE_CHECKING

from fastapi import APIRouter, Query, WebSocket, WebSocketDisconnect

from corral.tools.session_manager import (
    discover_corral_agents,
    get_agent_log_path,
    get_log_status,
    restart_session,
    launch_claude_session,
)
from corral.tools.tmux_manager import (
    get_session_info,
    send_to_tmux,
    send_raw_keys,
    capture_pane,
    capture_pane_raw,
    kill_session,
    open_terminal_attached,
    resize_pane,
)
from corral.agents import get_agent
from corral.tools.log_streamer import get_log_snapshot
from corral.tools.pulse_detector import scan_log_for_pulse_events

if TYPE_CHECKING:
    from corral.store import CorralStore
    from corral.store.schedule import ScheduleStore
    from corral.tools.jsonl_reader import JsonlSessionReader

log = logging.getLogger(__name__)

router = APIRouter()

# Module-level dependencies, set by web_server.py during app setup
store: CorralStore = None  # type: ignore[assignment]
jsonl_reader: JsonlSessionReader = None  # type: ignore[assignment]
schedule_store: ScheduleStore = None  # type: ignore[assignment]

# Track last-known status/summary per session_id so we only emit events on change.
_last_known: dict[str, dict[str, str | None]] = {}


async def _send_auto_accept(tmux_session: str) -> None:
    """Send 'y' + Enter to a tmux session to accept a permission prompt."""
    from corral.tools.utils import run_cmd
    try:
        await asyncio.sleep(0.5)  # Brief delay to let the prompt render
        await run_cmd("tmux", "send-keys", "-t", tmux_session, "y", timeout=5.0)
        await run_cmd("tmux", "send-keys", "-t", tmux_session, "Enter", timeout=5.0)
    except Exception:
        log.warning("Failed to auto-accept in session %s", tmux_session)


async def _track_status_summary_events(
    agent_name: str, status: str | None, summary: str | None, session_id: str | None = None,
):
    """Insert agent_events when status or summary changes for a live agent."""
    if session_id is None:
        session_id = await store.get_agent_session_id(agent_name)
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


@router.get("/api/sessions/live")
async def get_live_sessions():
    """List active corral agents with their current status."""
    agents = await discover_corral_agents()
    git_state = await store.get_all_latest_git_state()
    session_ids = [a["session_id"] for a in agents if a.get("session_id")]
    display_names = await store.get_display_names(session_ids)
    latest_events = await store.get_latest_event_types(session_ids)
    results = []
    for agent in agents:
        log_info = get_log_status(agent["log_path"])
        name = agent["agent_name"]
        sid = agent.get("session_id")
        # Look up git state by session_id first, then fall back to agent_name
        git = git_state.get(sid) if sid else None
        if not git:
            git = git_state.get(name)
        latest_ev = latest_events.get(sid) if sid else None
        waiting = latest_ev in ("stop", "notification")
        working = latest_ev == "tool_use" and (log_info["staleness_seconds"] or 999) < 120
        entry = {
            "name": name,
            "agent_type": agent["agent_type"],
            "session_id": sid,
            "tmux_session": agent.get("tmux_session"),
            "log_path": agent["log_path"],
            "status": log_info["status"],
            "summary": log_info["summary"],
            "staleness_seconds": log_info["staleness_seconds"],
            "commands": get_agent(agent["agent_type"]).available_commands,
            "branch": git["branch"] if git else None,
            "display_name": display_names.get(sid) if sid else None,
            "working_directory": agent.get("working_directory", ""),
            "waiting_for_input": waiting,
            "waiting_reason": latest_ev if waiting else None,
            "working": working,
        }
        results.append(entry)
        await _track_status_summary_events(name, log_info["status"], log_info["summary"], session_id=sid)
        await scan_log_for_pulse_events(store, name, agent["log_path"], session_id=sid)

    # Exclude sessions owned by job runs (any status)
    if schedule_store:
        try:
            job_sids = await schedule_store.get_all_job_session_ids()
            if job_sids:
                results = [s for s in results if s.get("session_id") not in job_sids]
        except Exception:
            pass

    return results


@router.get("/api/sessions/live/{name}")
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


@router.get("/api/sessions/live/{name}/capture")
async def get_pane_capture(name: str, agent_type: str | None = None, session_id: str | None = None):
    text = await capture_pane(name, agent_type=agent_type, session_id=session_id)
    if text is None:
        return {"error": f"Could not capture pane for '{name}'"}
    return {"name": name, "capture": text}


@router.get("/api/sessions/live/{name}/chat")
async def get_live_chat(
    name: str,
    session_id: str | None = None,
    working_directory: str | None = None,
    after: int = Query(0, ge=0),
):
    """Get chat messages from the JSONL transcript for a live session."""
    if not session_id:
        return {"messages": [], "total": 0}
    agent_type = await store.get_agent_type_for_session(session_id)
    new_msgs, total = await asyncio.to_thread(
        jsonl_reader.read_new_messages, session_id, working_directory or "", agent_type
    )
    return {"messages": jsonl_reader._cache[session_id].messages[after:], "total": total}


@router.get("/api/sessions/live/{name}/info")
async def get_live_session_info(name: str, agent_type: str | None = None, session_id: str | None = None):
    """Return enriched metadata for a live session (Info modal)."""
    info = await get_session_info(name, agent_type, session_id=session_id)
    if not info:
        return {"error": f"Agent '{name}' not found"}
    # Look up git state by session_id first for accurate per-session results
    git = None
    if session_id:
        git = await store.get_latest_git_state_by_session(session_id)
    if not git:
        git = await store.get_latest_git_state(name)
    if git:
        info["git_branch"] = git["branch"]
        info["git_commit_hash"] = git["commit_hash"]
        info["git_commit_subject"] = git["commit_subject"]
    return info


@router.get("/api/sessions/live/{name}/git")
async def get_live_session_git(name: str, limit: int = Query(20, ge=1, le=100), session_id: str | None = None):
    """Return recent git snapshots (commit history) for a live agent."""
    if session_id:
        snapshots = await store.get_git_snapshots_for_session(session_id, limit)
    else:
        snapshots = await asyncio.to_thread(store.get_git_snapshots, name, limit)
    return {"agent_name": name, "snapshots": snapshots}


@router.post("/api/sessions/live/{name}/send")
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


@router.post("/api/sessions/live/{name}/keys")
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


@router.post("/api/sessions/live/{name}/resize")
async def resize_pane_width(name: str, body: dict):
    """Resize the tmux pane width to match the browser display columns."""
    columns = body.get("columns")
    if not isinstance(columns, int) or columns < 10:
        return {"error": "columns must be an integer >= 10"}
    agent_type = body.get("agent_type") or None
    sid = body.get("session_id") or None
    error = await resize_pane(name, columns, agent_type=agent_type, session_id=sid)
    if error:
        return {"error": error}
    return {"ok": True, "columns": columns}


@router.post("/api/sessions/live/{name}/kill")
async def kill_live_session(name: str, body: dict | None = None):
    """Kill the tmux session for a live agent."""
    agent_type = (body or {}).get("agent_type") or None
    sid = (body or {}).get("session_id") or None
    error = await kill_session(name, agent_type=agent_type, session_id=sid)
    if error:
        return {"error": error}
    return {"ok": True}


@router.post("/api/sessions/live/{name}/restart")
async def restart_live_session(name: str, body: dict | None = None):
    """Restart the agent session."""
    agent_type = (body or {}).get("agent_type") or None
    extra_flags = (body or {}).get("extra_flags") or None
    sid = (body or {}).get("session_id") or None
    result = await restart_session(name, agent_type=agent_type, extra_flags=extra_flags, session_id=sid)
    return result


@router.post("/api/sessions/live/{name}/resume")
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


@router.post("/api/sessions/live/{name}/attach")
async def attach_terminal(name: str, body: dict | None = None):
    """Open a local terminal window attached to the agent's tmux session."""
    agent_type = (body or {}).get("agent_type") or None
    sid = (body or {}).get("session_id") or None
    error = await open_terminal_attached(name, agent_type=agent_type, session_id=sid)
    if error:
        return {"error": error}
    return {"ok": True}


@router.put("/api/sessions/live/{name}/display-name")
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


@router.post("/api/sessions/launch")
async def launch_session(body: dict):
    """Launch a new Claude/Gemini session."""
    working_dir = body.get("working_dir", "").strip()
    agent_type = body.get("agent_type", "claude").strip()
    display_name = body.get("display_name", "").strip() or None
    flags = body.get("flags", [])
    if not working_dir:
        return {"error": "working_dir is required"}
    result = await launch_claude_session(working_dir, agent_type, display_name=display_name, flags=flags)
    return result


# ── Agent Tasks Endpoints ──────────────────────────────────────────────────


@router.get("/api/sessions/live/{name}/tasks")
async def list_agent_tasks(name: str, session_id: str | None = None):
    if session_id is None:
        return []
    return await store.list_agent_tasks(name, session_id=session_id)


@router.post("/api/sessions/live/{name}/tasks")
async def create_agent_task(name: str, body: dict):
    title = body.get("title", "").strip()
    if not title:
        return {"error": "title is required"}
    session_id = body.get("session_id")
    task = await store.create_agent_task(name, title, session_id=session_id)
    return task


@router.patch("/api/sessions/live/{name}/tasks/{task_id}")
async def update_agent_task(name: str, task_id: int, body: dict):
    title = body.get("title")
    completed = body.get("completed")
    sort_order = body.get("sort_order")
    await store.update_agent_task(task_id, title=title, completed=completed, sort_order=sort_order)
    return {"ok": True}


@router.delete("/api/sessions/live/{name}/tasks/{task_id}")
async def delete_agent_task(name: str, task_id: int):
    await store.delete_agent_task(task_id)
    return {"ok": True}


@router.post("/api/sessions/live/{name}/tasks/reorder")
async def reorder_agent_tasks(name: str, body: dict):
    task_ids = body.get("task_ids", [])
    if not task_ids:
        return {"error": "task_ids is required"}
    await store.reorder_agent_tasks(name, task_ids)
    return {"ok": True}


# ── Agent Notes Endpoints ──────────────────────────────────────────────────


@router.get("/api/sessions/live/{name}/notes")
async def list_agent_notes(name: str, session_id: str | None = None):
    if session_id is None:
        return []
    return await store.list_agent_notes(name, session_id=session_id)


@router.post("/api/sessions/live/{name}/notes")
async def create_agent_note(name: str, body: dict):
    content = body.get("content", "").strip()
    if not content:
        return {"error": "content is required"}
    session_id = body.get("session_id")
    note = await store.create_agent_note(name, content, session_id=session_id)
    return note


@router.patch("/api/sessions/live/{name}/notes/{note_id}")
async def update_agent_note(name: str, note_id: int, body: dict):
    content = body.get("content")
    if content is None:
        return {"error": "content is required"}
    await store.update_agent_note(note_id, content)
    return {"ok": True}


@router.delete("/api/sessions/live/{name}/notes/{note_id}")
async def delete_agent_note(name: str, note_id: int):
    await store.delete_agent_note(note_id)
    return {"ok": True}


# ── Agent Events Endpoints ─────────────────────────────────────────────────


@router.get("/api/sessions/live/{name}/events")
async def list_agent_events(
    name: str, limit: int = Query(50, ge=1, le=200), session_id: str | None = None,
):
    events = await store.list_agent_events(name, limit, session_id=session_id)
    return events


@router.post("/api/sessions/live/{name}/events")
async def create_agent_event(name: str, body: dict):
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

    # Auto-accept: if this session has auto_accept enabled and the event
    # is a notification (permission prompt), send "y" + Enter.
    # We intentionally skip "stop" events — those fire on end_turn (agent
    # finished) as well as permission prompts, and --dangerously-skip-permissions
    # already handles the permission case.  Sending blind "y" on end_turn
    # would type into the idle prompt.
    if session_id and event_type == "notification":
        from corral.background_tasks.scheduler import (
            auto_accept_sessions, auto_accept_counts, auto_accept_limits,
            DEFAULT_MAX_AUTO_ACCEPTS,
        )
        tmux_session = auto_accept_sessions.get(session_id)
        if tmux_session:
            count = auto_accept_counts.get(session_id, 0)
            limit = auto_accept_limits.get(session_id, DEFAULT_MAX_AUTO_ACCEPTS)
            if count >= limit:
                log.warning(
                    "Auto-accept limit reached for session %s (%d/%d), disabling",
                    session_id, count, limit,
                )
                auto_accept_sessions.pop(session_id, None)
            else:
                auto_accept_counts[session_id] = count + 1
                log.info(
                    "Auto-accepting permission for session %s (%d/%d)",
                    session_id, count + 1, limit,
                )
                asyncio.ensure_future(_send_auto_accept(tmux_session))

    return event


@router.get("/api/sessions/live/{name}/events/counts")
async def get_agent_event_counts(name: str, session_id: str | None = None):
    counts = await store.get_agent_event_counts(name, session_id=session_id)
    return counts


@router.delete("/api/sessions/live/{name}/events")
async def clear_agent_events(name: str, session_id: str | None = None):
    await store.clear_agent_events(name, session_id=session_id)
    return {"ok": True}


# ── WebSocket Endpoints ─────────────────────────────────────────────────────


@router.websocket("/ws/terminal/{name}")
async def ws_terminal(websocket: WebSocket, name: str):
    """Stream raw terminal content (with ANSI escapes) for a single agent."""
    await websocket.accept()

    agent_type = websocket.query_params.get("agent_type")
    session_id = websocket.query_params.get("session_id")
    last_content = ""

    try:
        while True:
            content = await capture_pane_raw(
                name, agent_type=agent_type, session_id=session_id
            )
            if content is not None and content != last_content:
                await websocket.send_json({
                    "type": "terminal_update",
                    "content": content,
                })
                last_content = content
            await asyncio.sleep(0.5)
    except WebSocketDisconnect:
        pass
    except Exception:
        pass


@router.websocket("/ws/corral")
async def ws_corral(websocket: WebSocket):
    """Stream corral-wide session list updates (polls every 3s)."""
    await websocket.accept()

    last_state = None
    try:
        while True:
            agents = await discover_corral_agents()
            git_state = await store.get_all_latest_git_state()
            ws_session_ids = [a["session_id"] for a in agents if a.get("session_id")]
            ws_display_names = await store.get_display_names(ws_session_ids)
            ws_latest_events = await store.get_latest_event_types(ws_session_ids)
            results = []
            for agent in agents:
                log_info = get_log_status(agent["log_path"])
                name = agent["agent_name"]
                sid = agent.get("session_id")
                # Look up git state by session_id first, then fall back to agent_name
                git = git_state.get(sid) if sid else None
                if not git:
                    git = git_state.get(name)
                latest_ev = ws_latest_events.get(sid) if sid else None
                waiting = latest_ev in ("stop", "notification")
                working = latest_ev == "tool_use" and (log_info["staleness_seconds"] or 999) < 120
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
                    "waiting_reason": latest_ev if waiting else None,
                    "working": working,
                })
                await _track_status_summary_events(name, log_info["status"], log_info["summary"], session_id=sid)
                await scan_log_for_pulse_events(store, name, agent["log_path"], session_id=sid)

            # Fetch active job runs for Jobs sidebar
            active_runs = []
            if schedule_store:
                try:
                    active_runs = await schedule_store.list_active_runs()
                    for r in active_runs:
                        if r.get("job_name") == "__oneshot__":
                            r["job_name"] = None
                except Exception:
                    pass

                # Exclude sessions owned by job runs (any status) from live sessions
                try:
                    job_session_ids = await schedule_store.get_all_job_session_ids()
                    if job_session_ids:
                        results = [s for s in results if s.get("session_id") not in job_session_ids]
                except Exception:
                    pass

            payload = {"type": "corral_update", "sessions": results, "active_runs": active_runs}
            current_state = json.dumps(payload, sort_keys=True)
            if current_state != last_state:
                await websocket.send_json(payload)
                last_state = current_state

            await asyncio.sleep(3)
    except WebSocketDisconnect:
        pass
    except Exception:
        pass
