"""Tmux pane management — discovery, send-keys, capture, kill, and terminal attach."""

from __future__ import annotations

import asyncio
import os
import platform
import shutil
from typing import Any

from corral.tools.utils import run_cmd


async def list_tmux_sessions() -> list[dict[str, str]]:
    """List all tmux panes with their titles, session names, and targets."""
    try:
        rc, stdout, _ = await run_cmd(
            "tmux", "list-panes", "-a",
            "-F", "#{pane_title}|#{session_name}|#S:#I.#P|#{pane_current_path}",
        )
        if rc != 0:
            return []

        results = []
        for line in stdout.splitlines():
            parts = line.split("|", 3)
            if len(parts) == 4:
                results.append({
                    "pane_title": parts[0],
                    "session_name": parts[1],
                    "target": parts[2],
                    "current_path": parts[3],
                })
        return results
    except (OSError, FileNotFoundError):
        return []


async def _find_pane(
    agent_name: str,
    agent_type: str | None = None,
    session_id: str | None = None,
) -> dict[str, str] | None:
    """Find the tmux pane dict for a given agent.

    When *session_id* is provided, matches by tmux session name containing
    that UUID (new format). Falls back to fuzzy agent_name matching.
    """
    sessions = await list_tmux_sessions()

    # Fast path: match by session_id in tmux session name
    if session_id:
        sid_low = session_id.lower()
        for s in sessions:
            if sid_low in s["session_name"].lower():
                return s

    # Fallback: fuzzy match by agent_name
    agent_low = agent_name.lower()
    norm_name = agent_name.replace("_", "-").lower()
    type_low = agent_type.lower() if agent_type else None

    fallback: dict[str, str] | None = None

    for s in sessions:
        title_low = s["pane_title"].lower()
        session_low = s["session_name"].lower()
        path_low = s.get("current_path", "").lower()
        path_base = os.path.basename(path_low.rstrip("/"))

        name_match = (agent_low in title_low or
                      norm_name in title_low or
                      agent_low in session_low or
                      norm_name in session_low or
                      agent_low == path_base or
                      norm_name == path_base)

        if not name_match:
            continue

        if type_low:
            if type_low in title_low or type_low in session_low:
                return s
            if fallback is None:
                fallback = s
        else:
            return s

    return fallback


async def find_pane_target(
    agent_name: str, agent_type: str | None = None, session_id: str | None = None,
) -> str | None:
    """Find the tmux pane target address for a given agent name."""
    pane = await _find_pane(agent_name, agent_type, session_id=session_id)
    return pane["target"] if pane else None


async def get_session_info(
    agent_name: str, agent_type: str | None = None, session_id: str | None = None,
) -> dict[str, Any] | None:
    """Return enriched metadata for a live session (used by the Info modal)."""
    from corral.tools.session_manager import get_agent_log_path

    pane = await _find_pane(agent_name, agent_type, session_id=session_id)
    if not pane:
        return None

    log_path = get_agent_log_path(agent_name, agent_type, session_id=session_id)
    return {
        "agent_name": agent_name,
        "agent_type": agent_type or "claude",
        "session_id": session_id,
        "tmux_session_name": pane["session_name"],
        "tmux_target": pane["target"],
        "tmux_command": f"tmux attach -t {pane['session_name']}",
        "working_directory": pane.get("current_path", ""),
        "log_path": str(log_path) if log_path else None,
        "pane_title": pane.get("pane_title", ""),
    }


async def send_to_tmux(
    agent_name: str, command: str, agent_type: str | None = None, session_id: str | None = None,
) -> str | None:
    """Send a command to the tmux pane for the given agent. Returns error string or None."""
    target = await find_pane_target(agent_name, agent_type, session_id=session_id)
    if not target:
        return f"Pane '{agent_name}' not found in any tmux session"

    try:
        # Send the text content
        rc, _, stderr = await run_cmd(
            "tmux", "send-keys", "-t", target, "-l", command
        )
        if rc != 0:
            return f"send-keys failed (rc={rc}): {stderr}"

        # Pause to let tmux deliver keystrokes to the pane
        await asyncio.sleep(0.3)

        # Send Enter
        rc, _, stderr = await run_cmd(
            "tmux", "send-keys", "-t", target, "Enter"
        )
        if rc != 0:
            return f"send Enter failed (rc={rc}): {stderr}"

        return None
    except Exception as e:
        return str(e)


async def send_raw_keys(
    agent_name: str, keys: list[str], agent_type: str | None = None, session_id: str | None = None,
) -> str | None:
    """Send raw tmux key names (e.g. BTab, Escape) to a pane. Returns error string or None."""
    target = await find_pane_target(agent_name, agent_type, session_id=session_id)
    if not target:
        return f"Pane '{agent_name}' not found in any tmux session"

    try:
        for key in keys:
            rc, _, stderr = await run_cmd(
                "tmux", "send-keys", "-t", target, key
            )
            if rc != 0:
                return f"send-keys '{key}' failed (rc={rc}): {stderr}"
            await asyncio.sleep(0.1)
        return None
    except Exception as e:
        return str(e)


async def capture_pane(
    agent_name: str, lines: int = 200, agent_type: str | None = None, session_id: str | None = None,
) -> str | None:
    """Capture the current content of a tmux pane. Returns text or None on error."""
    target = await find_pane_target(agent_name, agent_type, session_id=session_id)
    if not target:
        return None

    try:
        rc, stdout, _ = await run_cmd(
            "tmux", "capture-pane", "-t", target, "-p", f"-S-{lines}"
        )
        if rc != 0:
            return None
        return stdout
    except (OSError, FileNotFoundError):
        return None


async def kill_session(
    agent_name: str, agent_type: str | None = None, session_id: str | None = None,
) -> str | None:
    """Kill the tmux session for a given agent and remove its log file.

    Returns error string or None.
    """
    from corral.tools.session_manager import get_agent_log_path

    pane = await _find_pane(agent_name, agent_type, session_id=session_id)
    if not pane:
        return f"Pane '{agent_name}' not found in any tmux session"

    session_name = pane["session_name"]
    try:
        rc, _, stderr = await run_cmd(
            "tmux", "kill-session", "-t", session_name
        )
        if rc != 0:
            return f"kill-session failed: {stderr}"

        # Remove the log file so the agent disappears from discover_corral_agents
        log_path = get_agent_log_path(agent_name, agent_type, session_id=session_id)
        if log_path:
            try:
                log_path.unlink()
            except OSError:
                pass

        # Unregister from persistent live sessions
        if session_id:
            try:
                from corral.store import CorralStore
                _store = CorralStore()
                await _store.unregister_live_session(session_id)
            except Exception:
                pass  # Non-critical

        return None
    except Exception as e:
        return str(e)


async def open_terminal_attached(
    agent_name: str, agent_type: str | None = None, session_id: str | None = None,
) -> str | None:
    """Open a local terminal window attached to the agent's tmux session.

    Returns an error string on failure, or None on success.
    Uses osascript on macOS, or falls back to common terminal emulators on Linux.
    """
    pane = await _find_pane(agent_name, agent_type, session_id=session_id)
    if not pane:
        return f"Pane '{agent_name}' not found in any tmux session"

    session_name = pane["session_name"]
    attach_cmd = f"tmux attach -t {session_name}"

    try:
        if platform.system() == "Darwin":
            # macOS: use osascript to open Terminal.app
            script = (
                f'tell application "Terminal"\n'
                f'    activate\n'
                f'    do script "{attach_cmd}"\n'
                f'end tell'
            )
            rc, _, stderr = await run_cmd(
                "osascript", "-e", script
            )
            if rc != 0:
                return f"osascript failed: {stderr}"
        else:
            # Linux: try common terminal emulators
            for term in ("gnome-terminal", "xfce4-terminal", "konsole", "xterm"):
                if shutil.which(term):
                    if term == "gnome-terminal":
                        args = [term, "--", "bash", "-c", attach_cmd]
                    elif term == "konsole":
                        args = [term, "-e", "bash", "-c", attach_cmd]
                    else:
                        args = [term, "-e", attach_cmd]
                    asyncio.create_task(run_cmd(*args))
                    # Don't wait — terminal runs independently
                    return None
            return "No supported terminal emulator found"

        return None
    except Exception as e:
        return str(e)


