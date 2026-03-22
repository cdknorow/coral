"""API routes for live (active) agent sessions."""

from __future__ import annotations

import asyncio
import json
import logging
import time
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
board_store: MessageBoardStore = None  # type: ignore[assignment]  # set by web_server.py

# Track last-known status/summary per session_id so we only emit events on change.
_last_known: dict[str, dict[str, str | None]] = {}

# TTL cache for git ls-files results, keyed by workdir path.
_FILE_LIST_TTL_S = 60
_file_list_cache: dict[str, tuple[float, list[str]]] = {}


def _file_list_cache_get(workdir: str) -> list[str] | None:
    entry = _file_list_cache.get(workdir)
    if entry and (time.monotonic() - entry[0]) < _FILE_LIST_TTL_S:
        return entry[1]
    return None


def _file_list_cache_set(workdir: str, files: list[str]) -> None:
    _file_list_cache[workdir] = (time.monotonic(), files)


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
    icons = await store.get_icons(session_ids)
    latest_events = await store.get_latest_event_types(session_ids)
    latest_goals = await store.get_latest_goals(session_ids)

    try:
        board_subs = await board_store.get_all_subscriptions()
    except Exception:
        board_subs = {}

    # Fallback: fetch board_name from live_sessions DB for agents not yet
    # subscribed (race condition during team launch — the async
    # setup_board_and_prompt task may not have completed yet).
    live_board_names: dict[str, tuple[str, str]] = {}
    live_sleeping: dict[str, bool] = {}
    try:
        conn = await store._get_conn()
        rows = await (await conn.execute(
            "SELECT session_id, board_name, display_name, is_sleeping FROM live_sessions WHERE board_name IS NOT NULL"
        )).fetchall()
        for row in rows:
            live_board_names[row["session_id"]] = (row["board_name"], row["display_name"] or "")
            live_sleeping[row["session_id"]] = bool(row["is_sleeping"])
    except Exception:
        pass


    # Batch fetch all unread counts in one pass (eliminates N+1 queries)
    try:
        all_unread = await board_store.get_all_unread_counts()
    except Exception:
        all_unread = {}

    # Fetch board_name + subgent_board_id from live_sessions DB for sessions
    # without local board subscriptions (e.g. subgent-backed sessions)
    live_board_names: dict[str, dict] = {}
    try:
        live_board_names = await store.get_live_session_board_names(session_ids)
    except Exception:
        live_board_names = {}

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
        # Detect sleep/polling loops — not real work
        if working and ev_summary and ev_summary.startswith("Ran: sleep"):
            working = False
        # If the last event was a tool_use but it was a long time ago,
        # the agent is idle, not "working" or "stuck".
        if working and (log_info["staleness_seconds"] or 999) > 420:
            working = False
        stuck = False

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
            "icon": icons.get(sid) if sid else None,
            "working_directory": agent.get("working_directory", ""),
            "waiting_for_input": needs_input,
            "done": done,
            "working": working,
            "stuck": stuck,
            "waiting_reason": latest_ev if needs_input else None,
            "waiting_summary": ev_summary if needs_input else None,
            "changed_file_count": fc,
            "board_project": board_sub["project"] if board_sub else (live_board_names.get(sid, {}).get("board_name") if sid else None),
            "board_job_title": board_sub["job_title"] if board_sub else None,
            "board_unread": board_unread,
            "sleeping": live_sleeping.get(sid, False) if sid else False,
            "subgent_board_id": live_board_names.get(sid, {}).get("subgent_board_id") if sid else None,
        }
        if include_commands:
            wd = agent.get("working_directory", "")
            entry["commands"] = get_agent(agent["agent_type"]).available_commands(wd or None)
        entry["log_path"] = agent["log_path"]

        results.append(entry)

        # Only write events when status/summary actually changed (dedup is inside but still costs a DB read)
        if log_info["status"] or log_info["summary"]:
            try:
                await _track_status_summary_events(name, log_info["status"], log_info["summary"], session_id=sid)
                await scan_log_for_pulse_events(store, name, agent["log_path"], session_id=sid)
            except Exception:
                log.debug("Failed to track events for %s (DB busy?)", name)

    # Include sleeping sessions from DB that have no tmux session
    # (their tmux was killed when the team was put to sleep).
    live_sids = {r["session_id"] for r in results if r.get("session_id")}
    try:
        all_live = await store.get_all_live_sessions()
        for sess in all_live:
            if not sess.get("is_sleeping"):
                continue
            sid = sess["session_id"]
            if sid in live_sids:
                continue  # Already in results from tmux discovery
            board_name = sess.get("board_name")
            results.append({
                "name": sess.get("agent_name", ""),
                "agent_type": sess.get("agent_type", "claude"),
                "session_id": sid,
                "tmux_session": None,
                "status": "Sleeping",
                "summary": None,
                "staleness_seconds": None,
                "branch": None,
                "display_name": sess.get("display_name"),
                "icon": sess.get("icon"),
                "working_directory": sess.get("working_dir", ""),
                "waiting_for_input": False,
                "done": False,
                "working": False,
                "stuck": False,
                "waiting_reason": None,
                "waiting_summary": None,
                "changed_file_count": 0,
                "board_project": board_name,
                "board_job_title": sess.get("display_name"),
                "board_unread": 0,
                "sleeping": True,
                "log_path": "",
            })
    except Exception:
        log.debug("Failed to append sleeping sessions to session list")

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
    # Resumed sessions write transcripts under the original session ID
    transcript_id = await store.get_transcript_session_id(session_id)
    new_msgs, total = await asyncio.to_thread(
        jsonl_reader.read_new_messages, transcript_id, working_directory or "", agent_type
    )
    return {"messages": jsonl_reader._cache[transcript_id].messages[after:], "total": total}


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
                    info["subgent_board_id"] = prompt_info.get("subgent_board_id")
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

    # Invalidate file list cache so next search picks up new files
    _file_list_cache.pop(workdir, None)

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


@router.get("/api/sessions/live/{name}/file-content")
async def get_file_content(name: str, filepath: str = Query(...), session_id: str | None = None):
    """Return the raw content of a file in the agent's working tree."""
    import os

    workdir = await _resolve_workdir(name, None, session_id)
    if not workdir:
        return {"error": "Could not determine working directory"}

    full_path = os.path.realpath(os.path.join(workdir, filepath))
    if not full_path.startswith(os.path.realpath(workdir) + os.sep):
        return {"error": "Path traversal not allowed"}

    if not os.path.isfile(full_path):
        return {"error": "File not found"}

    try:
        with open(full_path, "r", errors="replace") as f:
            content = f.read()
        return {"filepath": filepath, "content": content, "working_directory": workdir}
    except Exception as e:
        return {"error": str(e)}


@router.put("/api/sessions/live/{name}/file-content")
async def save_file_content(name: str, body: dict, filepath: str = Query(...), session_id: str | None = None):
    """Write content to a file in the agent's working tree."""
    import os

    workdir = await _resolve_workdir(name, None, session_id)
    if not workdir:
        return {"error": "Could not determine working directory"}

    content = body.get("content")
    if content is None:
        return {"error": "content is required"}

    full_path = os.path.realpath(os.path.join(workdir, filepath))
    if not full_path.startswith(os.path.realpath(workdir) + os.sep):
        return {"error": "Path traversal not allowed"}

    try:
        with open(full_path, "w") as f:
            f.write(content)
        return {"ok": True, "filepath": filepath}
    except Exception as e:
        return {"error": str(e)}


@router.get("/api/sessions/live/{name}/search-files")
async def search_files(name: str, session_id: str | None = None):
    """Return the full file list for the agent's working directory.

    Fuzzy matching is handled client-side in file_mention.js.
    The server caches the git ls-files output with a TTL to avoid
    repeated subprocess calls.
    """
    from coral.tools.utils import run_cmd

    workdir = await _resolve_workdir(name, None, session_id)
    if not workdir:
        return {"files": []}

    # Check TTL cache before calling git ls-files
    files = _file_list_cache_get(workdir)
    if files is None:
        rc, stdout, _ = await run_cmd(
            "git", "-C", workdir, "ls-files",
            "--cached", "--others", "--exclude-standard",
            timeout=10.0,
        )
        if rc != 0 or not stdout or not stdout.strip():
            return {"files": []}
        files = stdout.strip().split("\n")
        _file_list_cache_set(workdir, files)

    return {"files": files}


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
    """Kill the tmux session for a live agent.

    Sleeping agents have no tmux session, so we handle them specially:
    clean up DB record, log file, and board subscription without touching tmux.
    """
    agent_type = (body or {}).get("agent_type") or None
    sid = (body or {}).get("session_id") or None

    # Check if this is a sleeping agent (no tmux session to kill)
    if sid:
        session_info = await store.get_live_session(sid)
        if session_info and session_info.get("is_sleeping"):
            return await _kill_sleeping_agent(sid, name, agent_type, session_info)

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
                board_id = info.get("subgent_board_id")
                board_server = info.get("board_server")
                if board_id and board_server:
                    remaining = await store.get_subgent_live_sessions()
                    others = [r for r in remaining if r["session_id"] != sid and r.get("subgent_board_id") == board_id]
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
                                await listener.stop_board(ws_url, board_id)
                        except Exception:
                            log.warning("Failed to stop subgent listener for board %s", board_id)
        except Exception:
            pass  # Non-critical — don't block kill

    error = await kill_session(name, agent_type=agent_type, session_id=sid)
    if error:
        return {"error": error}
    return {"ok": True}


async def _kill_sleeping_agent(
    session_id: str, agent_name: str, agent_type: str | None, session_info: dict,
) -> dict:
    """Remove a sleeping agent: clean up DB, log file, and board subscription."""
    from coral.tools.session_manager import get_agent_log_path

    # Remove log file so agent disappears from discover_coral_agents
    log_path = get_agent_log_path(agent_name, agent_type, session_id=session_id)
    if log_path:
        try:
            log_path.unlink(missing_ok=True)
        except OSError:
            pass

    # Clean up settings temp file
    settings_file = Path(f"/tmp/coral_settings_{session_id}.json")
    try:
        settings_file.unlink(missing_ok=True)
    except OSError:
        pass

    # Unsubscribe from message board if on one
    board_name = session_info.get("board_name")
    if board_name and board_store:
        try:
            await board_store.unsubscribe(board_name, session_id)
        except Exception:
            pass  # Non-critical

    # Unregister from persistent live sessions DB
    await store.unregister_live_session(session_id)

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


@router.put("/api/sessions/live/{name}/icon")
async def set_session_icon(name: str, body: dict):
    """Set or clear the emoji icon for a live session."""
    session_id = body.get("session_id")
    if not session_id:
        return {"error": "session_id is required"}
    icon = body.get("icon", "").strip() or None
    await store.set_icon(session_id, icon)
    return {"ok": True, "icon": icon}


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

    if board_name:
        _asyncio.create_task(setup_board_and_prompt(
            result["session_id"], result["session_name"], agent_type,
            board_name=board_name or None,
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

    # Extract subgent config: use request values if provided, else fall back to saved settings
    subgent_admin_url = None
    subgent_admin_key = None
    subgent_org_id = None
    if subgent_config:
        subgent_admin_url = subgent_config.get("admin_url", "").strip()
        subgent_admin_key = subgent_config.get("admin_key", "").strip()
        subgent_org_id = subgent_config.get("org_id", "default").strip() or "default"
    else:
        # Fall back to saved subgent settings from DB
        try:
            saved = await store.get_settings()
            if saved.get("subgent_admin_url") and saved.get("subgent_admin_key"):
                subgent_admin_url = saved["subgent_admin_url"]
                subgent_admin_key = saved["subgent_admin_key"]
                subgent_org_id = saved.get("subgent_org_id", "default") or "default"
        except Exception:
            pass

    # When using subgent, set board_server to the admin URL if not explicitly provided
    if subgent_admin_url and not board_server:
        board_server = subgent_admin_url

    # Resolve project + board UUIDs before creating agent keys
    subgent_board_id = None
    if subgent_admin_url and subgent_admin_key:
        try:
            from coral.messageboard.subgent_client import ensure_project, ensure_board
            project_id = await _asyncio.to_thread(
                ensure_project, subgent_admin_url, subgent_admin_key, board_name,
            )
            subgent_board_id = await _asyncio.to_thread(
                ensure_board, subgent_admin_url, subgent_admin_key, project_id, board_name,
            )
        except Exception as e:
            log.warning("Failed to resolve subgent project/board UUIDs: %s", e)
            return {"error": f"Subgent setup failed: {e}"}

    for agent_def in agents:
        agent_name = agent_def.get("name", "").strip()
        agent_prompt = agent_def.get("prompt", "").strip()
        agent_icon = agent_def.get("icon", "").strip() or None
        if not agent_name:
            continue

        # Launch the agent session first (without subgent fields —
        # we need the real session_name before creating the key)
        result = await launch_claude_session(
            working_dir, agent_type,
            display_name=agent_name,
            flags=flags or None,
            prompt=agent_prompt or None,
            board_name=board_name or None,
            board_server=board_server,
            icon=agent_icon,
        )
        if result.get("error"):
            launched.append({"name": agent_name, "error": result["error"]})
            continue

        real_session_name = result["session_name"]
        real_session_id = result["session_id"]

        # Create subgent agent key AFTER launch so we use the real session_name
        subgent_key_id = None
        subgent_api_key = None

        if subgent_admin_url and subgent_admin_key:
            try:
                from coral.messageboard.subgent_client import create_agent_key
                key_result = await _asyncio.to_thread(
                    create_agent_key,
                    admin_url=subgent_admin_url,
                    admin_key=subgent_admin_key,
                    org_id=subgent_org_id,
                    board=subgent_board_id,
                    session_id=real_session_name,
                    job_title=agent_name,
                    check_mode="all",
                )
                subgent_key_id = str(key_result.get("id", ""))
                subgent_api_key = key_result.get("key", "")

                # Update the DB record with subgent credentials
                await store.update_live_session_subgent(
                    real_session_id,
                    subgent_key_id=subgent_key_id,
                    subgent_api_key=subgent_api_key,
                    subgent_admin_url=subgent_admin_url,
                    subgent_admin_key=subgent_admin_key,
                    subgent_org_id=subgent_org_id,
                    subgent_board_id=subgent_board_id,
                )
            except Exception as e:
                log.warning("Failed to create subgent key for %s: %s", agent_name, e)
                # Agent is already launched — continue without subgent integration
                launched.append({
                    "name": agent_name,
                    "session_id": real_session_id,
                    "session_name": real_session_name,
                    "warning": "Subgent key creation failed",
                })
                continue

        # Board subscription handled by setup_board_and_prompt
        if board_name:
            _asyncio.create_task(setup_board_and_prompt(
                real_session_id, real_session_name, agent_type,
                board_name=board_name or None,
                board_server=board_server, display_name=agent_name,
                subgent_api_key=subgent_api_key,
                subgent_admin_url=subgent_admin_url,
                subgent_board_id=subgent_board_id,
            ))

        launched.append({
            "name": agent_name,
            "session_id": real_session_id,
            "session_name": real_session_name,
        })

    # Start WebSocket listener for subgent board (if any agents got keys)
    if subgent_admin_url and subgent_admin_key and launched:
        # Find a successfully launched agent with subgent credentials for the WS listener
        first_subgent = None
        for a in launched:
            if "session_id" in a and "warning" not in a:
                # Look up subgent credentials from DB
                try:
                    info = await store.get_live_session_prompt_info(a["session_id"])
                    if info and info.get("subgent_api_key"):
                        first_subgent = info
                        break
                except Exception:
                    pass
        if first_subgent:
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
                    first_sid = next((a["session_name"] for a in launched if "session_id" in a), "")
                    await listener.start_board(
                        ws_url=ws_url,
                        board_id=subgent_board_id,
                        api_key=first_subgent["subgent_api_key"],
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


# ── Team Sleep/Wake ────────────────────────────────────────────────────────


@router.get("/api/sessions/live/team/{board_name}/sleep-status")
async def get_sleep_status(board_name: str):
    """Return the sleep state for a team board."""
    sleeping_boards = await store.get_sleeping_board_names()
    return {"sleeping": board_name in sleeping_boards}


@router.post("/api/sessions/live/team/{board_name}/sleep")
async def sleep_team(board_name: str):
    """Put all agents on a board to sleep.

    Sets is_sleeping=1 for all sessions on the board, pauses the board,
    and kills the tmux sessions to free resources.  DB records are kept
    so wake can relaunch them.
    """
    all_sessions = await store.get_all_live_sessions()
    board_sessions = [s for s in all_sessions if s.get("board_name") == board_name]
    if not board_sessions:
        return {"ok": False, "error": "No sessions found on that board"}

    count = await store.set_board_sleeping(board_name, sleeping=True)

    # Pause the board
    from coral.messageboard.api import _paused_projects
    _paused_projects.add(board_name)

    # Kill tmux sessions to free resources — use direct tmux kill
    # instead of kill_session() which would unregister the DB record.
    from coral.tools.utils import run_cmd
    from coral.tools.tmux_manager import _find_pane
    killed = 0
    for sess in board_sessions:
        try:
            pane = await _find_pane(
                sess["agent_name"],
                sess.get("agent_type"),
                session_id=sess["session_id"],
            )
            if pane:
                await run_cmd("tmux", "kill-session", "-t", pane["session_name"])
                killed += 1
        except Exception:
            log.debug("Failed to kill session %s during sleep", sess["session_id"][:8])

    return {"ok": True, "sleeping": True, "sessions_affected": count, "sessions_killed": killed, "board_paused": True}


@router.post("/api/sessions/live/team/{board_name}/wake")
async def wake_team(board_name: str):
    """Wake all agents on a board.

    Relaunches sleeping sessions, sets is_sleeping=0, and unpauses the board.
    """
    from coral.tools.session_manager import _resume_single_session

    all_sessions = await store.get_all_live_sessions()
    sleeping_sessions = [
        s for s in all_sessions
        if s.get("board_name") == board_name and s.get("is_sleeping")
    ]

    # Relaunch sleeping sessions — only clear is_sleeping on success
    relaunched = 0
    from coral.messageboard.api import _paused_projects
    for sess in sleeping_sessions:
        sid = sess["session_id"]
        try:
            wake_rec = {**sess, "is_sleeping": False}
            await _resume_single_session(store, wake_rec, log)
            relaunched += 1
            await store.set_session_sleeping(sid, sleeping=False)
        except Exception:
            log.exception("Failed to wake session %s — keeping asleep", sid[:8])

    # Unpause board if at least one agent was woken
    if relaunched > 0:
        _paused_projects.discard(board_name)

    return {"ok": True, "sleeping": False, "sessions_relaunched": relaunched, "board_paused": False}


@router.post("/api/sessions/live/sleep-all")
async def sleep_all():
    """Put all agents to sleep."""
    all_sessions = await store.get_all_live_sessions()
    active = [s for s in all_sessions if not s.get("is_sleeping")]
    if not active:
        return {"ok": True, "sessions_affected": 0}

    # Group by board for board-level pause
    boards = set()
    from coral.tools.utils import run_cmd
    from coral.tools.tmux_manager import _find_pane
    from coral.messageboard.api import _paused_projects

    killed = 0
    for sess in active:
        sid = sess["session_id"]
        await store.set_session_sleeping(sid, sleeping=True)
        board = sess.get("board_name")
        if board:
            boards.add(board)
        try:
            pane = await _find_pane(
                sess["agent_name"], sess.get("agent_type"), session_id=sid,
            )
            if pane:
                await run_cmd("tmux", "kill-session", "-t", pane["session_name"])
                killed += 1
        except Exception:
            log.debug("Failed to kill tmux for session %s during sleep-all", sid[:8])

    for board in boards:
        _paused_projects.add(board)

    return {"ok": True, "sessions_affected": len(active), "sessions_killed": killed}


@router.post("/api/sessions/live/wake-all")
async def wake_all():
    """Wake all sleeping agents."""
    from coral.tools.session_manager import _resume_single_session
    from coral.messageboard.api import _paused_projects

    all_sessions = await store.get_all_live_sessions()
    sleeping = [s for s in all_sessions if s.get("is_sleeping")]
    if not sleeping:
        return {"ok": True, "sessions_relaunched": 0}

    boards = set()
    relaunched = 0
    for sess in sleeping:
        sid = sess["session_id"]
        try:
            wake_rec = {**sess, "is_sleeping": False}
            await _resume_single_session(store, wake_rec, log)
            relaunched += 1
            # Only clear sleeping state on successful relaunch
            await store.set_session_sleeping(sid, sleeping=False)
            board = sess.get("board_name")
            if board:
                boards.add(board)
        except Exception:
            log.exception("Failed to wake session %s — keeping asleep", sid[:8])

    for board in boards:
        _paused_projects.discard(board)

    return {"ok": True, "sessions_relaunched": relaunched}


@router.post("/api/sessions/live/{session_id}/sleep")
async def sleep_session(session_id: str):
    """Put a single agent to sleep."""
    all_sessions = await store.get_all_live_sessions()
    sess = next((s for s in all_sessions if s["session_id"] == session_id), None)
    if not sess:
        return {"ok": False, "error": "Session not found"}

    await store.set_session_sleeping(session_id, sleeping=True)

    # Kill tmux session to free resources
    from coral.tools.utils import run_cmd
    from coral.tools.tmux_manager import _find_pane
    try:
        pane = await _find_pane(
            sess["agent_name"], sess.get("agent_type"), session_id=session_id,
        )
        if pane:
            await run_cmd("tmux", "kill-session", "-t", pane["session_name"])
    except Exception:
        log.debug("Failed to kill tmux for session %s during sleep", session_id[:8])

    return {"ok": True, "sleeping": True}


@router.post("/api/sessions/live/{session_id}/wake")
async def wake_session(session_id: str):
    """Wake a single sleeping agent."""
    from coral.tools.session_manager import _resume_single_session

    all_sessions = await store.get_all_live_sessions()
    sess = next((s for s in all_sessions if s["session_id"] == session_id), None)
    if not sess:
        return {"ok": False, "error": "Session not found"}
    if not sess.get("is_sleeping"):
        return {"ok": False, "error": "Session is not sleeping"}

    try:
        wake_rec = {**sess, "is_sleeping": False}
        await _resume_single_session(store, wake_rec, log)
    except Exception:
        log.exception("Failed to wake session %s", session_id[:8])
        return {"ok": False, "error": "Failed to relaunch session"}

    await store.set_session_sleeping(session_id, sleeping=False)

    # Unpause the board if this agent was on one
    board = sess.get("board_name")
    if board:
        from coral.messageboard.api import _paused_projects
        _paused_projects.discard(board)

    return {"ok": True, "sleeping": False}


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
        """Push tmux pane content to the client, triggered by log file changes.

        Instead of fixed-interval polling, watches the agent's log file
        (written by tmux pipe-pane) for modifications. When the file changes,
        immediately captures the pane and pushes the update. This gives
        near-real-time latency with zero cost when idle.

        Fallback: 10ms stat polling on the log file (kernel syscall, ~0.01ms)
        is vastly cheaper than capture-pane subprocess spawns (~2ms each).

        Three triggers cause a capture:
        - Log file mtime changed (new output from agent)
        - User input event (keystroke echo)
        - Heartbeat every 2s (detect pane disappearance)
        """
        nonlocal last_content, closed, target
        pane_gone_notified = False

        # Resolve the log file path for this session
        log_path = get_agent_log_path(name, agent_type, session_id=session_id)
        last_mtime = 0.0
        if log_path and log_path.exists():
            try:
                last_mtime = log_path.stat().st_mtime
            except OSError:
                pass

        # Rate limit: minimum 15ms between capture-pane calls
        import time as _time
        last_capture_time = 0.0
        min_capture_interval = 0.015

        last_cursor = (None, None)

        async def _do_capture():
            """Capture pane and send update if content or cursor changed."""
            nonlocal last_content, last_cursor, pane_gone_notified, target, last_capture_time

            now = _time.monotonic()
            if now - last_capture_time < min_capture_interval:
                return
            last_capture_time = now

            if not target:
                target = await find_pane_target(
                    name, agent_type, session_id=session_id,
                )

            content = None
            cursor_x = cursor_y = None
            if target:
                try:
                    # Query cursor position and alternate screen mode in one call
                    from coral.tools.utils import run_cmd as _run_cmd
                    rc, info_out, _ = await _run_cmd(
                        "tmux", "display-message", "-t", target,
                        "-p", "#{cursor_x},#{cursor_y},#{alternate_on}",
                    )
                    alt_screen = False
                    if rc == 0 and "," in info_out:
                        parts = info_out.strip().split(",")
                        if len(parts) >= 3:
                            cursor_x, cursor_y = int(parts[0]), int(parts[1])
                            alt_screen = parts[2] == "1"
                    # Use visible-only capture when a TUI app (vim, nano, htop)
                    # is using the alternate screen buffer; otherwise include scrollback
                    content = await capture_pane_raw_target(target, visible_only=alt_screen)
                except Exception:
                    target = None

            if content is not None:
                pane_gone_notified = False
                new_cursor = (cursor_x, cursor_y)
                if content != last_content or new_cursor != last_cursor:
                    msg = {
                        "type": "terminal_update",
                        "content": content,
                    }
                    if cursor_x is not None:
                        msg["cursor_x"] = cursor_x
                        msg["cursor_y"] = cursor_y
                    await websocket.send_json(msg)
                    last_content = content
                    last_cursor = new_cursor
            elif not pane_gone_notified:
                await websocket.send_json({"type": "terminal_closed"})
                pane_gone_notified = True

        try:
            # Send initial snapshot immediately on connect
            await _do_capture()

            while not closed:
                if pane_gone_notified:
                    # Pane is gone — slow heartbeat to detect if it comes back
                    input_event.clear()
                    try:
                        await asyncio.wait_for(input_event.wait(), timeout=3.0)
                    except asyncio.TimeoutError:
                        pass
                    # Try to re-resolve and capture
                    target = None
                    await _do_capture()
                    continue

                # Check if log file changed (cheap stat syscall)
                file_changed = False
                if log_path:
                    try:
                        mtime = log_path.stat().st_mtime
                        if mtime != last_mtime:
                            last_mtime = mtime
                            file_changed = True
                    except OSError:
                        pass

                if file_changed:
                    await _do_capture()

                # Wait for input event or next stat check (10ms)
                input_event.clear()
                try:
                    await asyncio.wait_for(input_event.wait(), timeout=0.010)
                    # User typed something — capture immediately for echo
                    await _do_capture()
                except asyncio.TimeoutError:
                    pass

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
