"""Session manager — log parsing, agent discovery, session launch/restart, and history loading."""

from __future__ import annotations

import asyncio
import logging
import os
import re
import time
import uuid as _uuid
from pathlib import Path
from typing import Any

import json as _json_mod

from coral.tools.utils import run_cmd, LOG_DIR, LOG_PATTERN, get_package_dir

ANSI_RE = re.compile(r"\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])")
_CONTROL_CHAR_RE = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")
STATUS_RE = re.compile(r"^[\s●⏺]*\|\|PULSE:STATUS (.*?)\|\|", re.MULTILINE)
SUMMARY_RE = re.compile(r"^[\s●⏺]*\|\|PULSE:SUMMARY (.*?)\|\|", re.MULTILINE)

# Regex to parse new-format tmux session names: {agent_type}-{uuid}
_UUID_RE = re.compile(
    r"^(\w+)-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$",
    re.IGNORECASE,
)


def _write_board_state(tmux_session_name: str, project: str, job_title: str,
                       server_url: str | None = None) -> None:
    """Write the coral-board state file for an agent's tmux session.

    This pre-configures the agent's coral-board CLI so it auto-routes
    to the correct server without needing --server on every command.
    """
    state_dir = Path.home() / ".coral"
    state_dir.mkdir(parents=True, exist_ok=True)
    safe_name = tmux_session_name.replace("/", "_").replace("\\", "_")
    state_file = state_dir / f"board_state_{safe_name}.json"
    data = {
        "project": project,
        "job_title": job_title,
        "session_id": tmux_session_name,
    }
    if server_url:
        data["server_url"] = server_url.rstrip("/")
    state_file.write_text(_json_mod.dumps(data))


def _build_board_prompt(prompt: str, board_name: str | None, role: str) -> str:
    """Build a prompt string with board instructions appended if applicable."""
    if not board_name:
        return prompt
    return prompt + (
        f"\n\nYou are subscribed to message board \"{board_name}\". "
        f"Your role is: {role}. "
        f"Use the coral-board CLI to communicate with your teammates:\n"
        f"  coral-board read          — read new messages from teammates\n"
        f"  coral-board post \"msg\"    — post a message to the board\n"
        f"  coral-board read --last 5 — see the 5 most recent messages\n"
        f"  coral-board subscribers   — see who is on the board\n"
        f"Check the board periodically for updates from your teammates."
    )


async def setup_board_and_prompt(
    session_id: str,
    session_name: str,
    agent_type: str,
    prompt: str | None = None,
    board_name: str | None = None,
    board_server: str | None = None,
    display_name: str | None = None,
) -> None:
    """Subscribe to a message board and send the initial prompt to an agent.

    This is the single entry point for the post-launch setup that is shared
    across initial launch, restart, and resume-persistent-sessions paths.
    The prompt is sent after a short delay to give the agent time to initialize.
    """
    log = logging.getLogger(__name__)
    role = display_name or agent_type

    # Board subscription (immediate — no delay needed)
    if board_name:
        try:
            from coral.messageboard.store import MessageBoardStore
            board_store = MessageBoardStore()
            await board_store.subscribe(board_name, session_id, role)
        except Exception:
            log.warning("Failed to subscribe session %s to board %s", session_id[:8], board_name)
        try:
            _write_board_state(session_name, board_name, role, server_url=board_server)
        except Exception:
            log.warning("Failed to write board state for session %s", session_id[:8])

    # Send prompt after agent initializes
    if prompt:
        await asyncio.sleep(3)
        full = _build_board_prompt(prompt, board_name, role)
        try:
            from coral.tools.tmux_manager import send_to_tmux
            err = await send_to_tmux(agent_type, full, session_id=session_id)
            if err:
                await run_cmd("tmux", "send-keys", "-t", session_name, "-l", full)
            await run_cmd("tmux", "send-keys", "-t", session_name, "Enter")
        except Exception:
            log.warning("Failed to send prompt to session %s", session_id[:8])


def strip_ansi(text: str) -> str:
    """Remove ANSI escape sequences, replacing each with a space."""
    text = ANSI_RE.sub(" ", text)
    # Remove stray control characters (BEL \x07, etc.) left after partial sequences
    text = _CONTROL_CHAR_RE.sub("", text)
    return text


def clean_match(text: str) -> str:
    """Collapse whitespace runs into a single space and strip."""
    return " ".join(text.split())


def _rejoin_pulse_lines(lines: list[str]) -> list[str]:
    """Rejoin lines where ``||PULSE:...||`` tags were split by terminal wrapping.

    ``tmux pipe-pane`` captures output with hard wraps at the terminal width,
    which can split a single PULSE tag across multiple log lines, e.g.::

        ||PULSE:SUMMARY Moving Settings button to top gear icon and creating
        persistent settings store in database||

    This function detects an opening ``||PULSE:`` without a closing ``||`` on
    the same line and merges subsequent lines until the closing ``||`` is found
    (up to *MAX_JOIN* continuation lines as a safety limit).
    """
    MAX_JOIN = 5
    result: list[str] = []
    pending: str | None = None
    depth = 0

    for line in lines:
        if pending is not None:
            # Accumulating continuation of a split PULSE tag
            pending = pending + " " + line.strip()
            depth += 1
            if "||" in line or depth >= MAX_JOIN:
                result.append(pending)
                pending = None
                depth = 0
        elif "||PULSE:" in line:
            # Check whether the tag is complete on this line
            idx = line.rfind("||PULSE:")
            rest = line[idx + len("||PULSE:"):]
            if "||" in rest:
                # Complete tag — emit as-is
                result.append(line)
            else:
                # Incomplete tag — start accumulating
                pending = line
                depth = 0
        else:
            result.append(line)

    # Flush any incomplete tag at end of chunk
    if pending is not None:
        result.append(pending)

    return result


async def discover_coral_agents() -> list[dict[str, Any]]:
    """Discover live coral agents from tmux sessions.

    Parses tmux session names to extract session_id (new UUID format)
    or falls back to old agent-N naming. Derives agent_name from the
    pane's working directory.
    """
    from glob import glob
    from coral.tools.tmux_manager import list_tmux_sessions

    panes = await list_tmux_sessions()
    results = []
    seen_session_ids: set[str] = set()

    for pane in panes:
        session_name = pane["session_name"]
        current_path = pane.get("current_path", "")
        agent_name = os.path.basename(current_path.rstrip("/")) if current_path else ""

        # Match new format: {agent_type}-{uuid}
        m = _UUID_RE.match(session_name)
        if not m:
            continue  # not a coral session

        agent_type = m.group(1)
        session_id = m.group(2).lower()
        if session_id in seen_session_ids:
            continue  # skip duplicate panes of same session
        seen_session_ids.add(session_id)

        log_path = os.path.join(LOG_DIR, f"{agent_type}_coral_{session_id}.log")
        results.append({
            "agent_type": agent_type,
            "agent_name": agent_name or session_id[:8],
            "session_id": session_id,
            "tmux_session": session_name,
            "log_path": log_path,
            "working_directory": current_path,
        })

    # Clean up stale log files that don't match any live session
    live_log_paths = {r["log_path"] for r in results}
    for log_path in glob(LOG_PATTERN):
        if log_path not in live_log_paths:
            try:
                Path(log_path).unlink()
            except OSError:
                pass

    return sorted(results, key=lambda r: r["agent_name"])


def get_agent_log_path(
    agent_name: str, agent_type: str | None = None, session_id: str | None = None,
) -> Path | None:
    """Find the log file for a given agent name or session_id.

    When *session_id* is provided, looks for ``{type}_coral_{session_id}.log``
    first. Falls back to matching by agent_name.
    """
    from glob import glob

    # Fast path: session_id-based log file
    if session_id:
        if agent_type:
            p = Path(LOG_DIR) / f"{agent_type}_coral_{session_id}.log"
            if p.exists():
                return p
        # Try any type prefix
        for log_path in glob(os.path.join(LOG_DIR, f"*_coral_{session_id}.log")):
            return Path(log_path)

    # Fallback: match by agent_name
    best: Path | None = None
    for log_path in glob(LOG_PATTERN):
        p = Path(log_path)
        match = re.search(r"([^_]+)_coral_(.+)", p.stem)
        if match and match.group(2) == agent_name:
            if agent_type and match.group(1).lower() == agent_type.lower():
                return p
            if best is None:
                best = p
    return best


def get_log_status(log_path: str | Path) -> dict[str, Any]:
    """Read a log file and return current status, summary, staleness, and recent lines."""
    log_path = Path(log_path)
    result: dict[str, Any] = {
        "status": None,
        "summary": None,
        "staleness_seconds": None,
        "recent_lines": [],
    }
    try:
        result["staleness_seconds"] = time.time() - log_path.stat().st_mtime

        with open(log_path, "rb") as f:
            f.seek(0, 2)
            file_size = f.tell()
            pos = file_size
            lines = []
            leftover = b""
            chunk_size = 4096

            max_chunks = 1000  # Up to ~4MB backwards
            chunks_read = 0

            while pos > 0 and (len(lines) < 20 or result["status"] is None or result["summary"] is None):
                if chunks_read >= max_chunks:
                    break

                read_size = min(chunk_size, pos)
                pos -= read_size
                f.seek(pos)
                chunk = f.read(read_size) + leftover

                parts = chunk.split(b"\n")
                if pos > 0:
                    leftover = parts.pop(0)

                # Decode, strip ANSI, and rejoin split PULSE tags
                clean_parts = []
                for p in parts:
                    try:
                        clean_parts.append(strip_ansi(p.decode("utf-8", errors="replace")))
                    except Exception:
                        clean_parts.append("")
                clean_parts = _rejoin_pulse_lines(clean_parts)

                for clean_line in reversed(clean_parts):
                    need_status = result["status"] is None
                    need_summary = result["summary"] is None
                    need_lines = len(lines) < 20

                    if not (need_status or need_summary or need_lines):
                        break

                    if need_status:
                        status_matches = STATUS_RE.findall(clean_line)
                        if status_matches:
                            result["status"] = clean_match(status_matches[-1])

                    if need_summary:
                        summary_matches = SUMMARY_RE.findall(clean_line)
                        if summary_matches:
                            result["summary"] = clean_match(summary_matches[-1])

                    if need_lines:
                        lines.insert(0, clean_line)

                chunks_read += 1

            # Fallback for summary: if not found in the tail, it might be at the very top
            if result["summary"] is None:
                f.seek(0)
                head_chunk = f.read(16384).decode("utf-8", errors="replace")
                head_matches = SUMMARY_RE.findall(strip_ansi(head_chunk))
                if head_matches:
                    result["summary"] = clean_match(head_matches[-1])

            result["recent_lines"] = lines
    except OSError:
        pass
    return result


def load_history_sessions() -> list[dict[str, Any]]:
    """Load session history from all registered agents.

    Returns list of session summaries sorted by last timestamp descending.
    """
    from coral.agents import get_all_agents
    result = []
    for agent in get_all_agents():
        result.extend(agent.load_history_sessions())
    result.sort(key=lambda x: x.get("last_timestamp") or "", reverse=True)
    return result


def load_history_session_messages(session_id: str) -> list[dict[str, Any]]:
    """Load all messages for a specific historical session (tries each agent)."""
    from coral.agents import get_all_agents
    for agent in get_all_agents():
        messages = agent.load_session_messages(session_id)
        if messages:
            messages.sort(key=lambda x: x.get("timestamp") or "")
            return messages
    return []


async def resume_persistent_sessions(store, schedule_store=None) -> None:
    """Resume live sessions that were running when Coral last stopped.

    Compares the ``live_sessions`` DB table against currently running tmux
    sessions.  Any registered session without a matching tmux session is
    relaunched (with ``--resume`` for Claude agents so they pick up context).
    Sessions whose working directory no longer exists are silently removed.

    Sessions that belong to scheduled/oneshot job runs are skipped and
    unregistered (they should not be auto-resumed).

    *store* is a :class:`~coral.store.CoralStore` instance.
    """
    log = logging.getLogger(__name__)

    try:
        registered = await store.get_all_live_sessions()
        if not registered:
            return

        # Discover what is already alive in tmux
        live_agents = await discover_coral_agents()
        live_session_ids = {a["session_id"] for a in live_agents}

        # Get session IDs belonging to job runs so we skip them
        job_session_ids: set[str] = set()
        if schedule_store:
            try:
                job_session_ids = await schedule_store.get_all_job_session_ids()
            except Exception:
                pass

        for rec in registered:
            sid = rec["session_id"]
            if sid in live_session_ids:
                continue  # Already running — nothing to do

            if rec.get("is_job") or sid in job_session_ids:
                # Job run session — don't resume, just clean up the record
                await store.unregister_live_session(sid)
                log.info("Skipping job session %s (not resumable)", sid[:8])
                continue

            working_dir = rec["working_dir"]
            if not os.path.isdir(working_dir):
                # Working directory gone (worktree removed?) — clean up
                await store.unregister_live_session(sid)
                log.info("Removed stale live session %s (dir missing: %s)", sid[:8], working_dir)
                continue

            agent_type = rec["agent_type"]
            display_name = rec.get("display_name")
            flags = rec.get("flags")  # Already deserialized by get_all_live_sessions
            prompt = rec.get("prompt")
            board_name = rec.get("board_name")
            board_server = rec.get("board_server")

            log.info(
                "Resuming session %s (%s) in %s",
                sid[:8], agent_type, working_dir,
            )

            # Use resume_from_id if available (tracks the original Claude
            # session across multiple Coral restarts), otherwise fall back
            # to the session_id itself (first restart after initial launch).
            resume_id = rec.get("resume_from_id") or sid

            result = await launch_claude_session(
                working_dir, agent_type, display_name=display_name,
                resume_session_id=resume_id,
                flags=flags,
                prompt=prompt,
                board_name=board_name,
                board_server=board_server,
            )

            if result.get("error"):
                log.warning("Failed to resume session %s: %s", sid[:8], result["error"])
                await store.unregister_live_session(sid)
            else:
                new_session_id = result["session_id"]
                new_session_name = result["session_name"]
                # Old session record is replaced by the new launch
                # (launch_claude_session calls register_live_session with new id)
                await store.unregister_live_session(sid)

                # Re-subscribe to message board and re-send prompt
                if board_name or prompt:
                    asyncio.create_task(setup_board_and_prompt(
                        new_session_id, new_session_name, agent_type,
                        prompt=prompt, board_name=board_name,
                        board_server=board_server, display_name=display_name,
                    ))
    except Exception:
        log.exception("Error resuming persistent sessions")


async def restart_session(
    agent_name: str,
    agent_type: str | None = None,
    resume_session_id: str | None = None,
    extra_flags: str | None = None,
    session_id: str | None = None,
) -> dict[str, Any]:
    """Restart the Claude/Gemini session in the same tmux pane.

    Uses ``tmux respawn-pane -k`` to forcefully kill the running process
    and spawn a fresh shell, then re-launches the agent with the same
    system prompt (PROTOCOL.md).

    If *resume_session_id* is provided, Claude is launched with
    ``--resume <session_id>`` to continue a previous conversation.
    Only supported for Claude agents (returns error for Gemini).

    Returns a dict with result info or an ``error`` key.
    """
    from coral.tools.tmux_manager import _find_pane

    pane = await _find_pane(agent_name, agent_type, session_id=session_id)
    if not pane:
        return {"error": f"Pane '{agent_name}' not found in any tmux session"}

    target = pane["target"]
    session_name = pane["session_name"]
    working_dir = pane.get("current_path", "")
    effective_type = (agent_type or "claude").lower()

    from coral.agents import get_agent
    agent_impl = get_agent(effective_type)

    if resume_session_id and not agent_impl.supports_resume:
        return {"error": f"Resume is not supported for {effective_type} agents"}

    # If resuming, let the agent prepare (e.g. copy session files)
    if resume_session_id and working_dir:
        agent_impl.prepare_resume(resume_session_id, working_dir)

    try:
        # Install agent-specific hooks before launching
        if working_dir:
            agent_impl.install_hooks(working_dir)

        # 0. Generate a new UUID for the restarted session.  This UUID is
        #    used for *both* the tmux session name and the Claude --session-id
        #    so that discover_coral_agents, the log file, and Claude all
        #    agree on the same identifier.
        new_session_id = str(_uuid.uuid4())
        new_session_name = f"{effective_type}-{new_session_id}"
        new_log_path = Path(LOG_DIR) / f"{effective_type}_coral_{new_session_id}.log"
        new_log_file = str(new_log_path)

        # 0b. Explicitly close any existing pipe-pane so respawn-pane doesn't
        #     leave a stale pipe that swallows output.
        await run_cmd("tmux", "pipe-pane", "-t", target)

        # 1. Kill the running process and respawn a fresh shell in the same pane.
        respawn_args = ["tmux", "respawn-pane", "-k", "-t", target]
        if working_dir:
            respawn_args.extend(["-c", working_dir])
        rc, _, stderr = await run_cmd(*respawn_args)
        if rc != 0:
            return {"error": f"respawn-pane failed: {stderr}"}

        # 2. Rename the tmux session so discover_coral_agents picks up
        #    the new UUID from the session name.
        rc, _, stderr = await run_cmd(
            "tmux", "rename-session", "-t", session_name, new_session_name
        )
        if rc != 0:
            return {"error": f"rename-session failed: {stderr}"}

        # The target has changed after rename — update for subsequent commands.
        # Use the new session name with pane 0.
        target = f"{new_session_name}:0.0"

        # Wait for the shell to initialise
        await asyncio.sleep(1)

        # 2b. Clear the tmux pane scrollback so capture_pane returns fresh content
        await run_cmd(
            "tmux", "clear-history", "-t", target
        )

        # 3. Remove the old log file and create a fresh one for the new session
        old_log_path = get_agent_log_path(agent_name, agent_type, session_id=session_id)
        if old_log_path and old_log_path.exists():
            try:
                old_log_path.unlink()
            except OSError:
                pass
        try:
            new_log_path.write_text("")
        except OSError:
            pass

        # 4. Establish pipe-pane logging for the new log file
        await run_cmd(
            "tmux", "pipe-pane", "-t", target, "-o", f"cat >> '{new_log_file}'"
        )

        # 5. Restore the pane title
        folder_name = os.path.basename(working_dir.rstrip("/")) if working_dir else agent_name
        await run_cmd(
            "tmux", "send-keys", "-t", target,
            f"printf '\\033]2;{folder_name} \\xe2\\x80\\x94 {effective_type}\\033\\\\'", "Enter",
        )
        await asyncio.sleep(0.3)

        # 6. Load persisted flags, prompt, board_name, and board_server from the live session record
        stored_flags = []
        stored_prompt = None
        stored_board = None
        stored_board_server = None
        if session_id:
            try:
                import json as _json
                from coral.store import CoralStore
                _flag_store = CoralStore()
                _flag_conn = await _flag_store._get_conn()
                _flag_row = await (await _flag_conn.execute(
                    "SELECT flags, prompt, board_name, board_server FROM live_sessions WHERE session_id = ?", (session_id,)
                )).fetchone()
                if _flag_row:
                    if _flag_row["flags"]:
                        stored_flags = _json.loads(_flag_row["flags"])
                    stored_prompt = _flag_row["prompt"]
                    stored_board = _flag_row["board_name"]
                    stored_board_server = _flag_row["board_server"]
            except Exception:
                pass

        # 7. Re-launch the agent with the same system prompt
        protocol_path = get_package_dir() / "PROTOCOL.md"

        all_flags = list(stored_flags)
        if extra_flags:
            all_flags.append(extra_flags)

        cmd = agent_impl.build_launch_command(
            new_session_id, protocol_path,
            resume_session_id=resume_session_id,
            flags=all_flags or None,
        )

        rc, _, stderr = await run_cmd(
            "tmux", "send-keys", "-t", target, "-l", cmd
        )
        if rc != 0:
            return {"error": f"re-launch failed: {stderr}"}

        await asyncio.sleep(0.3)

        await run_cmd(
            "tmux", "send-keys", "-t", target, "Enter"
        )

        # Migrate display_name and update live session record
        if session_id:
            try:
                from coral.store import CoralStore
                _store = CoralStore()
                old_display_name = await _store.get_display_name(session_id)
                await _store.migrate_display_name(session_id, new_session_id)
                await _store.replace_live_session(
                    session_id, new_session_id, effective_type, agent_name, working_dir,
                    display_name=old_display_name,
                    resume_from_id=resume_session_id,
                )
            except Exception:
                pass  # Non-critical
        else:
            # No old session_id — just register the new one
            try:
                from coral.store import CoralStore
                _store = CoralStore()
                await _store.register_live_session(
                    new_session_id, effective_type, agent_name, working_dir,
                )
            except Exception:
                pass

        # Re-subscribe to message board and re-send prompt on restart
        if stored_board or stored_prompt:
            old_display_name_for_prompt = None
            try:
                from coral.store import CoralStore
                _dn_store = CoralStore()
                old_display_name_for_prompt = await _dn_store.get_display_name(new_session_id)
            except Exception:
                pass
            asyncio.create_task(setup_board_and_prompt(
                new_session_id, new_session_name, effective_type,
                prompt=stored_prompt, board_name=stored_board,
                board_server=stored_board_server,
                display_name=old_display_name_for_prompt,
            ))

        return {
            "ok": True,
            "agent_name": agent_name,
            "agent_type": effective_type,
            "working_dir": working_dir,
            "session_id": new_session_id,
        }
    except Exception as e:
        return {"error": str(e)}


async def launch_claude_session(working_dir: str, agent_type: str = "claude", display_name: str | None = None, resume_session_id: str | None = None, flags: list[str] | None = None, is_job: bool = False, prompt: str | None = None, board_name: str | None = None, board_server: str | None = None) -> dict[str, str]:
    """Launch a new tmux session with a Claude/Gemini agent.

    Returns dict with session_name, session_id, log_file, and any error.
    """
    working_dir = os.path.abspath(working_dir)
    if not os.path.isdir(working_dir):
        return {"error": f"Directory not found: {working_dir}"}

    folder_name = os.path.basename(working_dir)
    log_dir = os.environ.get("TMPDIR", "/tmp").rstrip("/")

    session_id = str(_uuid.uuid4())
    session_name = f"{agent_type}-{session_id}"
    log_file = f"{log_dir}/{agent_type}_coral_{session_id}.log"

    is_terminal = agent_type == "terminal"

    if not is_terminal:
        from coral.agents import get_agent
        agent_impl = get_agent(agent_type)

        # If resuming, let the agent prepare (e.g. copy session files)
        if resume_session_id:
            agent_impl.prepare_resume(resume_session_id, working_dir)

    try:
        # Install agent-specific hooks before launching
        if not is_terminal:
            agent_impl.install_hooks(working_dir)

        # Clear old log
        Path(log_file).write_text("")

        # Create detached session
        rc, _, stderr = await run_cmd(
            "tmux", "new-session", "-d", "-s", session_name, "-c", working_dir
        )
        if rc != 0:
            return {"error": f"tmux new-session failed: {stderr}"}

        # Set up pipe-pane logging
        await run_cmd(
            "tmux", "pipe-pane", "-t", session_name, "-o", f"cat >> '{log_file}'"
        )

        # Set pane title
        await run_cmd(
            "tmux", "send-keys", "-t", f"{session_name}.0",
            f"printf '\\033]2;{folder_name} \\xe2\\x80\\x94 {agent_type}\\033\\\\'", "Enter"
        )

        await asyncio.sleep(0.3)

        if not is_terminal:
            # Launch the agent
            script_dir = Path(__file__).parent.parent
            protocol_path = script_dir / "PROTOCOL.md"

            cmd = agent_impl.build_launch_command(
                session_id, protocol_path,
                resume_session_id=resume_session_id,
                flags=flags,
            )

            await asyncio.create_subprocess_exec(
                "tmux", "send-keys", "-t", f"{session_name}.0", cmd, "Enter"
            )

        # Store display_name and register live session
        try:
            from coral.store import CoralStore
            _store = CoralStore()
            if display_name:
                await _store.set_display_name(session_id, display_name)
            await _store.register_live_session(
                session_id, agent_type, folder_name, working_dir, display_name,
                resume_from_id=resume_session_id,
                flags=flags or None,
                is_job=is_job,
                prompt=prompt,
                board_name=board_name,
                board_server=board_server,
            )
        except Exception:
            pass  # Non-critical

        return {
            "session_name": session_name,
            "session_id": session_id,
            "log_file": log_file,
            "working_dir": working_dir,
            "agent_type": agent_type,
        }
    except Exception as e:
        return {"error": str(e)}
