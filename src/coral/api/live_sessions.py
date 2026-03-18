"""API routes for live (active) agent sessions."""

from __future__ import annotations

import asyncio
import json
import logging
from pathlib import Path
from typing import TYPE_CHECKING

from fastapi import APIRouter, Query, WebSocket, WebSocketDisconnect

from coral.tools.session_manager import (
    discover_coral_agents,
    get_agent_log_path,
    get_log_status,
    restart_session,
    launch_claude_session,
)
from coral.tools.tmux_manager import (
    get_session_info,
    send_to_tmux,
    send_raw_keys,
    send_terminal_input,
    send_terminal_input_to_target,
    capture_pane,
    capture_pane_raw,
    capture_pane_raw_target,
    kill_session,
    open_terminal_attached,
    resize_pane,
    resize_pane_target,
    find_pane_target,
    _find_pane,
)
from coral.agents import get_agent
from coral.tools.log_streamer import get_log_snapshot
from coral.tools.pulse_detector import scan_log_for_pulse_events
from coral.messageboard.store import MessageBoardStore

if TYPE_CHECKING:
    from coral.store import CoralStore
    from coral.store.schedule import ScheduleStore
    from coral.tools.jsonl_reader import JsonlSessionReader

log = logging.getLogger(__name__)

router = APIRouter()

# Module-level dependencies, set by web_server.py during app setup
store: CoralStore = None  # type: ignore[assignment]
jsonl_reader: JsonlSessionReader = None  # type: ignore[assignment]
schedule_store: ScheduleStore = None  # type: ignore[assignment]
board_store: MessageBoardStore = MessageBoardStore()

# Track last-known status/summary per session_id so we only emit events on change.
_last_known: dict[str, dict[str, str | None]] = {}


async def _resolve_workdir(name: str, agent_type: str | None, session_id: str | None) -> str | None:
    """Resolve working directory from tmux pane, falling back to git snapshot."""
    pane = await _find_pane(name, agent_type, session_id=session_id)
    if pane:
        workdir = pane.get("current_path")
        if workdir:
            return workdir
    git = None
    if session_id:
        git = await store.get_latest_git_state_by_session(session_id)
    if not git:
        git = await store.get_latest_git_state(name)
    if git:
        return git.get("working_directory")
    return None


async def _build_session_list(include_commands: bool = False) -> list[dict]:
    """Build the enriched session list used by both REST and WebSocket endpoints."""
    import time as _time
    _t0 = _time.monotonic()
    agents = await discover_coral_agents()
    git_state = await store.get_all_latest_git_state()
    file_counts = await store.get_all_changed_file_counts()
    session_ids = [a["session_id"] for a in agents if a.get("session_id")]
    display_names = await store.get_display_names(session_ids)
    latest_events = await store.get_latest_event_types(session_ids)
    latest_goals = await store.get_latest_goals(session_ids)

    try:
        board_subs = await board_store.get_all_subscriptions()
    except Exception:
        board_subs = {}

    # Batch fetch all unread counts in one pass (eliminates N+1 queries)
    try:
        all_unread = await board_store.get_all_unread_counts()
    except Exception:
        all_unread = {}

    results = []
    for agent in agents:
        # Self-heal: if the log file is missing but the tmux session is alive,
        # recreate the file and re-establish pipe-pane logging.  This handles
        # the case where a log file was accidentally deleted while the cat
        # pipe-pane process still had an open fd to the removed inode.
        log_path = agent["log_path"]
        if not Path(log_path).exists() and agent.get("tmux_session"):
            try:
                from coral.tools.utils import run_cmd
                tmux_sess = agent["tmux_session"]
                Path(log_path).write_text("")
                # Close existing pipe-pane first (kills stale cat process
                # that may still have an fd to the deleted inode)
                await run_cmd("tmux", "pipe-pane", "-t", tmux_sess)
                # Re-establish pipe-pane to the new file
                await run_cmd(
                    "tmux", "pipe-pane", "-t", tmux_sess,
                    "-o", f"cat >> '{log_path}'"
                )
                log.info("Re-established pipe-pane for %s", agent.get("session_id", "")[:8])
            except Exception:
                pass

        log_info = get_log_status(log_path)
        name = agent["agent_name"]
        sid = agent.get("session_id")

        git = git_state.get(sid) if sid else None
        if not git:
            git = git_state.get(name)
        fc = file_counts.get(sid) if sid else None
        if fc is None:
            fc = file_counts.get(name, 0)
        ev_tuple = latest_events.get(sid) if sid else None
        latest_ev = ev_tuple[0] if ev_tuple else None
        ev_summary = ev_tuple[1] if ev_tuple else None
        needs_input = latest_ev == "notification"
        done = latest_ev == "stop"
        working = latest_ev in ("tool_use", "prompt_submit")
        stuck = working and (log_info["staleness_seconds"] or 999) > 420
        if stuck:
            working = False

        summary = log_info["summary"]
        if not summary and sid:
            summary = latest_goals.get(sid)

        tmux_name = agent.get("tmux_session") or ""
        board_sub = board_subs.get(tmux_name)
        board_unread = all_unread.get(tmux_name, 0) if board_sub else 0

        entry = {
            "name": name,
            "agent_type": agent["agent_type"],
            "session_id": sid,
            "tmux_session": agent.get("tmux_session"),
            "status": log_info["status"],
            "summary": summary,
            "staleness_seconds": log_info["staleness_seconds"],
            "branch": git["branch"] if git else None,
            "display_name": display_names.get(sid) if sid else None,
            "working_directory": agent.get("working_directory", ""),
            "waiting_for_input": needs_input,
            "done": done,
            "working": working,
            "stuck": stuck,
            "waiting_reason": latest_ev if needs_input else None,
            "waiting_summary": ev_summary if needs_input else None,
            "changed_file_count": fc,
            "board_project": board_sub["project"] if board_sub else None,
            "board_job_title": board_sub["job_title"] if board_sub else None,
            "board_unread": board_unread,
        }
        if include_commands:
            entry["commands"] = get_agent(agent["agent_type"]).available_commands
        entry["log_path"] = agent["log_path"]

        results.append(entry)

        # Only write events when status/summary actually changed (dedup is inside but still costs a DB read)
        if log_info["status"] or log_info["summary"]:
            await _track_status_summary_events(name, log_info["status"], log_info["summary"], session_id=sid)
            await scan_log_for_pulse_events(store, name, agent["log_path"], session_id=sid)

    _elapsed = _time.monotonic() - _t0
    if _elapsed > 1.0:
        log.warning("_build_session_list took %.2fs for %d agents", _elapsed, len(agents))

    return results


async def _exclude_job_sessions(results: list[dict]) -> list[dict]:
    """Filter out sessions owned by scheduled job runs."""
    if not schedule_store:
        return results
    try:
        job_sids = await schedule_store.get_all_job_session_ids()
        if job_sids:
            return [s for s in results if s.get("session_id") not in job_sids]
    except Exception:
        pass
    return results


async def _send_auto_accept(tmux_session: str) -> None:
    """Send 'y' + Enter to a tmux session to accept a permission prompt."""
    from coral.tools.utils import run_cmd
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
    """List active coral agents with their current status."""
    results = await _build_session_list(include_commands=True)
    return await _exclude_job_sessions(results)


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


@router.get("/api/sessions/live/{name}/poll")
async def poll_session(
    name: str,
    session_id: str | None = None,
    agent_type: str | None = None,
    events_limit: int = Query(50, ge=1, le=200),
):
    """Batch poll endpoint — returns capture, tasks, and events in one response.

    Combines the data from /capture, /tasks, and /events into a single call
    to reduce the number of polling requests per session.
    """
    async def _empty_list():
        return []

    capture_coro = capture_pane(name, agent_type=agent_type, session_id=session_id)
    tasks_coro = store.list_agent_tasks(name, session_id=session_id) if session_id else _empty_list()
    events_coro = store.list_agent_events(name, events_limit, session_id=session_id)

    capture_text, tasks, events = await asyncio.gather(
        capture_coro, tasks_coro, events_coro
    )

    return {
        "capture": {"name": name, "capture": capture_text} if capture_text is not None else {"error": f"Could not capture pane for '{name}'"},
        "tasks": tasks,
        "events": events,
    }


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
    # Include prompt and board_name from live session record
    if session_id:
        try:
            prompt_info = await store.get_live_session_prompt_info(session_id)
            if prompt_info:
                info["prompt"] = prompt_info["prompt"]
                info["board_name"] = prompt_info["board_name"]
                if prompt_info.get("subgent_key_id"):
                    info["subgent_admin_url"] = prompt_info["subgent_admin_url"]
        except Exception:
            pass
    return info


@router.get("/api/sessions/live/{name}/files")
async def get_live_session_files(name: str, session_id: str | None = None):
    """Return changed files (working tree diff) for a live agent."""
    files = await store.get_changed_files(name, session_id=session_id)
    return {"agent_name": name, "files": files}


@router.post("/api/sessions/live/{name}/files/refresh")
async def refresh_live_session_files(name: str, body: dict | None = None):
    """Run fresh git queries and merge agent Write/Edit events for immediate file visibility."""
    from coral.tools.utils import run_cmd
    import os

    session_id = (body or {}).get("session_id")

    workdir = await _resolve_workdir(name, None, session_id)
    if not workdir:
        return {"error": "Could not determine working directory", "files": []}

    # Run git queries directly
    from coral.background_tasks.git_poller import GitPoller
    poller = GitPoller(store)
    changed_files = await poller._query_changed_files(workdir)

    # Build a map for merging
    file_map = {f["filepath"]: f for f in changed_files}

    # Merge in files from agent events (Write/Edit tool uses) that git may
    # not know about yet (e.g. new files in new directories)
    events = await store.list_agent_events(name, limit=200, session_id=session_id)
    for ev in events:
        if ev.get("tool_name") not in ("Write", "Edit"):
            continue
        detail = ev.get("detail_json")
        if not detail:
            continue
        try:
            d = json.loads(detail) if isinstance(detail, str) else detail
            fp = d.get("file_path", "")
        except (json.JSONDecodeError, TypeError):
            continue
        if not fp:
            continue

        # Convert absolute path to relative path from workdir
        try:
            rel = os.path.relpath(fp, workdir)
        except ValueError:
            continue
        # Skip paths outside the workdir
        if rel.startswith(".."):
            continue

        if rel not in file_map:
            # Count lines for new files
            additions = 0
            if os.path.isfile(fp):
                try:
                    with open(fp, "r", errors="replace") as f:
                        additions = sum(1 for _ in f)
                except Exception:
                    pass
            file_map[rel] = {
                "filepath": rel,
                "additions": additions,
                "deletions": 0,
                "status": "??",
            }

    # Also update the DB cache so the sidebar count stays in sync
    files_list = list(file_map.values())
    await store.replace_changed_files(
        agent_name=name,
        working_directory=workdir,
        files=files_list,
        session_id=session_id,
    )

    return {"agent_name": name, "files": files_list}


@router.get("/api/sessions/live/{name}/diff")
async def get_file_diff(name: str, filepath: str = Query(...), session_id: str | None = None):
    """Return the unified diff for a single file in the agent's working tree."""
    from coral.tools.utils import run_cmd, get_diff_base

    workdir = await _resolve_workdir(name, None, session_id)
    if not workdir:
        return {"error": "Could not determine working directory"}

    # Determine the diff base — on feature branches this is the merge-base
    # with main/master so we show all branch changes, not just uncommitted ones.
    base = await get_diff_base(workdir)

    # Diff from base to working tree for this file — captures committed +
    # staged + unstaged changes in one shot.
    rc, diff_text, _ = await run_cmd(
        "git", "-C", workdir, "diff", base, "--", filepath, timeout=10.0,
    )
    diff_text = diff_text or ""

    # For untracked files, show the file content as a "new file" diff
    if not diff_text:
        import os
        full_path = os.path.realpath(os.path.join(workdir, filepath))
        # Prevent path traversal — resolved path must stay within workdir
        if not full_path.startswith(os.path.realpath(workdir) + os.sep):
            return {"filepath": filepath, "diff": "", "working_directory": workdir}
        if os.path.isfile(full_path):
            try:
                with open(full_path, "r", errors="replace") as f:
                    content = f.read()
                lines = content.split("\n")
                diff_text = (
                    f"diff --git a/{filepath} b/{filepath}\n"
                    f"new file mode 100644\n"
                    f"--- /dev/null\n"
                    f"+++ b/{filepath}\n"
                    f"@@ -0,0 +1,{len(lines)} @@\n"
                )
                diff_text += "\n".join(f"+{line}" for line in lines)
            except Exception:
                diff_text = f"(Unable to read {filepath})"

    return {"filepath": filepath, "diff": diff_text, "working_directory": workdir}


@router.get("/api/sessions/live/{name}/search-files")
async def search_files(name: str, q: str = Query(""), session_id: str | None = None):
    """Search for files in the agent's working directory by name fragment."""
    from coral.tools.utils import run_cmd

    workdir = await _resolve_workdir(name, None, session_id)
    if not workdir:
        return {"files": []}

    # Use git ls-files for tracked files, falling back to find
    query = q.strip().lower()
    rc, stdout, _ = await run_cmd(
        "git", "-C", workdir, "ls-files", "--cached", "--others", "--exclude-standard",
        timeout=10.0,
    )
    if rc != 0 or stdout is None:
        return {"files": []}

    all_files = stdout.strip().split("\n") if stdout.strip() else []

    if not query:
        # Return first 50 files when no query
        return {"files": all_files[:50]}

    # Score matches: basename match is better than path-only match
    import os
    scored = []
    for fp in all_files:
        fp_lower = fp.lower()
        basename = os.path.basename(fp_lower)
        if query in fp_lower:
            # Prioritize: exact basename match > basename contains > path contains
            if basename == query:
                scored.append((0, fp))
            elif query in basename:
                scored.append((1, fp))
            else:
                scored.append((2, fp))
    scored.sort(key=lambda x: (x[0], x[1]))
    return {"files": [s[1] for s in scored[:50]]}


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

    # Revoke subgent key and stop board listener if this was the last agent on the board
    if sid:
        try:
            info = await store.get_live_session_prompt_info(sid)
            if info and info.get("subgent_key_id"):
                # Revoke the agent's API key on the subgent server
                try:
                    from coral.messageboard.subgent_client import revoke_key
                    revoke_key(
                        admin_url=info["subgent_admin_url"],
                        admin_key=info["subgent_admin_key"],
                        key_id=info["subgent_key_id"],
                    )
                except Exception:
                    log.warning("Failed to revoke subgent key for session %s", sid[:8])

                # Check if any other live sessions share this board
                board_name = info.get("board_name")
                board_server = info.get("board_server")
                if board_name and board_server:
                    remaining = await store.get_subgent_live_sessions()
                    others = [r for r in remaining if r["session_id"] != sid and r["board_name"] == board_name and r["board_server"] == board_server]
                    if not others:
                        # Last agent on this board — stop the WS listener
                        try:
                            from coral.web_server import app
                            listener = getattr(app.state, "subgent_listener", None)
                            if listener:
                                ws_url = board_server
                                if ws_url.startswith("https://"):
                                    ws_url = "wss://" + ws_url[len("https://"):]
                                elif ws_url.startswith("http://"):
                                    ws_url = "ws://" + ws_url[len("http://"):]
                                await listener.stop_board(ws_url, board_name)
                        except Exception:
                            log.warning("Failed to stop subgent listener for board %s", board_name)
        except Exception:
            pass  # Non-critical — don't block kill

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
    import asyncio as _asyncio
    from coral.tools.session_manager import setup_board_and_prompt

    working_dir = body.get("working_dir", "").strip()
    agent_type = body.get("agent_type", "claude").strip()
    display_name = body.get("display_name", "").strip() or None
    flags = body.get("flags", [])
    prompt = body.get("prompt", "").strip()
    board_name = body.get("board_name", "").strip()
    board_server = body.get("board_server", "").strip() or None

    if not working_dir:
        return {"error": "working_dir is required"}
    result = await launch_claude_session(
        working_dir, agent_type, display_name=display_name, flags=flags,
        prompt=prompt or None, board_name=board_name or None,
        board_server=board_server,
    )

    if result.get("error"):
        return result

    if board_name or prompt:
        _asyncio.create_task(setup_board_and_prompt(
            result["session_id"], result["session_name"], agent_type,
            prompt=prompt or None, board_name=board_name or None,
            board_server=board_server, display_name=display_name,
        ))

    return result


@router.post("/api/sessions/launch-team")
async def launch_team(body: dict):
    """Launch multiple agents and subscribe them to a shared message board."""
    import asyncio as _asyncio
    from coral.tools.session_manager import setup_board_and_prompt

    board_name = body.get("board_name", "").strip()
    board_server = body.get("board_server", "").strip() or None
    working_dir = body.get("working_dir", "").strip()
    agent_type = body.get("agent_type", "claude").strip()
    flags = body.get("flags", [])
    agents = body.get("agents", [])
    subgent_config = body.get("subgent")  # optional: {admin_url, admin_key, org_id}

    if not board_name:
        return {"error": "board_name is required"}
    if not working_dir:
        return {"error": "working_dir is required"}
    if not agents:
        return {"error": "At least one agent is required"}

    launched = []

    for agent_def in agents:
        agent_name = agent_def.get("name", "").strip()
        agent_prompt = agent_def.get("prompt", "").strip()
        if not agent_name:
            continue

        # Create subgent agent key if subgent config is provided
        subgent_key_id = None
        subgent_api_key = None
        subgent_admin_url = None
        subgent_admin_key = None
        subgent_org_id = None

        if subgent_config:
            subgent_admin_url = subgent_config.get("admin_url", "").strip()
            subgent_admin_key = subgent_config.get("admin_key", "").strip()
            subgent_org_id = subgent_config.get("org_id", "default").strip() or "default"
            if subgent_admin_url and subgent_admin_key:
                try:
                    from coral.messageboard.subgent_client import create_agent_key
                    # Generate a session_id early so we can use it for key creation
                    import uuid
                    pre_session_id = str(uuid.uuid4())
                    key_result = await create_agent_key(
                        admin_url=subgent_admin_url,
                        admin_key=subgent_admin_key,
                        org_id=subgent_org_id,
                        board=board_name,
                        session_id=pre_session_id,
                        job_title=agent_name,
                    )
                    subgent_key_id = str(key_result.get("key_id", ""))
                    subgent_api_key = key_result.get("api_key", "")
                except Exception as e:
                    log.warning("Failed to create subgent key for %s: %s", agent_name, e)
                    launched.append({"name": agent_name, "error": "Subgent key creation failed"})
                    continue

        # Launch the agent session
        result = await launch_claude_session(
            working_dir, agent_type,
            display_name=agent_name,
            flags=flags or None,
            prompt=agent_prompt or None,
            board_name=board_name or None,
            board_server=board_server,
            subgent_key_id=subgent_key_id,
            subgent_api_key=subgent_api_key,
            subgent_admin_url=subgent_admin_url,
            subgent_admin_key=subgent_admin_key,
            subgent_org_id=subgent_org_id,
        )
        if result.get("error"):
            launched.append({"name": agent_name, "error": result["error"]})
            continue

        # Board subscription + prompt send handled by setup_board_and_prompt
        _asyncio.create_task(setup_board_and_prompt(
            result["session_id"], result["session_name"], agent_type,
            prompt=agent_prompt or None, board_name=board_name or None,
            board_server=board_server, display_name=agent_name,
            subgent_api_key=subgent_api_key,
            subgent_admin_url=subgent_admin_url,
        ))

        launched.append({
            "name": agent_name,
            "session_id": result["session_id"],
            "session_name": result["session_name"],
        })

    # Start WebSocket listener for subgent board
    if subgent_config and subgent_api_key and launched:
        try:
            from coral.messageboard.subgent_client import validate_url
            from coral.web_server import app
            # Re-validate URL before WebSocket connection (SSRF protection)
            validate_url(subgent_admin_url)
            listener = getattr(app.state, "subgent_listener", None)
            if listener:
                # Convert http(s) to ws(s) for WebSocket connection
                ws_url = subgent_admin_url
                if ws_url.startswith("https://"):
                    ws_url = "wss://" + ws_url[len("https://"):]
                elif ws_url.startswith("http://"):
                    ws_url = "ws://" + ws_url[len("http://"):]
                # Use the first successfully launched agent's session_id
                first_sid = next((a["session_id"] for a in launched if "session_id" in a), "")
                await listener.start_board(
                    ws_url=ws_url,
                    project=board_name,
                    api_key=subgent_api_key,
                    session_id=first_sid,
                )
        except ValueError as e:
            log.warning("Subgent URL validation failed for board %s: %s", board_name, e)
        except Exception as e:
            log.warning("Failed to start subgent WS listener for board %s: %s", board_name, e)

    return {"ok": True, "board": board_name, "agents": launched}


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
    # Ensure detail_json is a string for SQLite storage
    if detail_json is not None and not isinstance(detail_json, str):
        detail_json = json.dumps(detail_json)
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
        from coral.background_tasks.scheduler import (
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
    """Bidirectional terminal WebSocket.

    Server → Client: polls tmux pane content and pushes ``terminal_update`` messages.
    Client → Server: receives ``terminal_input`` messages and forwards data to tmux.
    """
    await websocket.accept()

    agent_type = websocket.query_params.get("agent_type")
    session_id = websocket.query_params.get("session_id")
    last_content = ""
    closed = False

    # Resolve pane target once to avoid repeated tmux list-panes lookups
    target = await find_pane_target(name, agent_type, session_id=session_id)

    # Event to wake the writer immediately after input (for fast echo)
    input_event = asyncio.Event()

    async def _reader():
        """Read incoming messages from the client (terminal input)."""
        nonlocal closed, target
        try:
            while not closed:
                raw = await websocket.receive_text()
                try:
                    msg = json.loads(raw)
                except (json.JSONDecodeError, TypeError):
                    continue
                msg_type = msg.get("type")
                if msg_type == "terminal_input":
                    data = msg.get("data", "")
                    if data and target:
                        await send_terminal_input_to_target(target, data)
                    # Wake the writer to capture output immediately
                    input_event.set()
                elif msg_type == "terminal_resize":
                    cols = msg.get("cols")
                    if isinstance(cols, int) and cols >= 10 and target:
                        await resize_pane_target(target, cols)
        except WebSocketDisconnect:
            closed = True
        except Exception:
            closed = True

    async def _writer():
        """Poll tmux pane and push content to the client.

        Uses adaptive polling: 50ms after recent input, 300ms when idle.
        """
        nonlocal last_content, closed, target
        idle_interval = 0.30
        active_interval = 0.05
        interval = idle_interval
        try:
            while not closed:
                # Re-resolve target if it was initially unavailable
                if not target:
                    target = await find_pane_target(
                        name, agent_type, session_id=session_id,
                    )
                content = await capture_pane_raw_target(target) if target else None
                if content is not None and content != last_content:
                    await websocket.send_json({
                        "type": "terminal_update",
                        "content": content,
                    })
                    last_content = content

                # Wait for either the interval or an input event
                input_event.clear()
                try:
                    await asyncio.wait_for(input_event.wait(), timeout=interval)
                    # Input happened — use fast poll for the next few cycles
                    interval = active_interval
                except asyncio.TimeoutError:
                    # No input — drift back toward idle rate
                    interval = min(interval + 0.05, idle_interval)
        except WebSocketDisconnect:
            closed = True
        except Exception:
            closed = True

    # Run reader and writer concurrently
    await asyncio.gather(_reader(), _writer())


@router.websocket("/ws/coral")
async def ws_coral(websocket: WebSocket):
    """Stream coral-wide session list updates (polls every 3s).

    First message is a full ``coral_update`` with all sessions.
    Subsequent messages are ``coral_diff`` with only changed/removed sessions
    to reduce bandwidth. Full session objects are sent per changed agent
    (no field-level diffs) as recommended by security review.
    """
    await websocket.accept()

    # Per-session state tracking for diff calculation
    prev_sessions: dict[str, str] = {}  # session key -> json string
    prev_runs_json: str = "[]"
    first_message = True

    try:
        while True:
            results = await _build_session_list()
            results = await _exclude_job_sessions(results)

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

            # Build per-session state map
            curr_sessions: dict[str, str] = {}
            session_by_key: dict[str, dict] = {}
            for s in results:
                key = s.get("session_id") or s["name"]
                curr_sessions[key] = json.dumps(s, sort_keys=True)
                session_by_key[key] = s

            curr_runs_json = json.dumps(active_runs, sort_keys=True)

            if first_message:
                # Always send full state on first message
                await websocket.send_json({
                    "type": "coral_update",
                    "sessions": results,
                    "active_runs": active_runs,
                })
                prev_sessions = curr_sessions
                prev_runs_json = curr_runs_json
                first_message = False
            else:
                # Calculate diff: changed + removed sessions
                changed = []
                for key, s_json in curr_sessions.items():
                    if prev_sessions.get(key) != s_json:
                        changed.append(session_by_key[key])

                removed = [k for k in prev_sessions if k not in curr_sessions]
                runs_changed = curr_runs_json != prev_runs_json

                if changed or removed or runs_changed:
                    payload: dict = {"type": "coral_diff"}
                    if changed:
                        payload["changed"] = changed
                    if removed:
                        payload["removed"] = removed
                    if runs_changed:
                        payload["active_runs"] = active_runs
                    await websocket.send_json(payload)
                    prev_sessions = curr_sessions
                    prev_runs_json = curr_runs_json

            from coral.config import WS_POLL_INTERVAL_S
            await asyncio.sleep(WS_POLL_INTERVAL_S)
    except WebSocketDisconnect:
        pass
    except Exception:
        pass
