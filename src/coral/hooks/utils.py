"""Shared utilities for Coral hooks (lightweight, no heavy imports)."""

import json
import os
import re
import subprocess
import urllib.request

_TMUX_UUID_RE = re.compile(
    r"^[a-z]+-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$", re.I
)


def resolve_session_id(payload_session_id: str | None) -> str | None:
    """Get the session_id from the tmux session name, falling back to the payload.

    Coral launches Claude with --session-id matching the tmux session UUID,
    but Claude's hook payload may report a different internal session_id.
    The tmux session name is the source of truth for Coral.
    """
    if os.environ.get("TMUX"):
        try:
            result = subprocess.run(
                ["tmux", "display-message", "-p", "#{session_name}"],
                capture_output=True, text=True, timeout=2,
            )
            if result.returncode == 0:
                name = result.stdout.strip()
                m = _TMUX_UUID_RE.match(name)
                if m:
                    return m.group(1).lower()
        except Exception:
            pass
    return payload_session_id


def coral_api(base: str, method: str, path: str, data=None):
    """Send a request to the Coral web server API."""
    url = base + path
    body = json.dumps(data).encode() if data else None
    req = urllib.request.Request(url, data=body, method=method)
    if body:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=3) as r:
            return json.loads(r.read())
    except Exception:
        return None


def cache_dir() -> str:
    """Return (and create) the Coral hook cache directory."""
    d = os.path.join(os.environ.get("TMPDIR", "/tmp"), "coral_task_cache")
    os.makedirs(d, exist_ok=True)
    return d


def debug_log(msg: str) -> None:
    """Append a timestamped line to the hook debug log."""
    try:
        log_path = os.path.join(cache_dir(), "debug.log")
        # Rotate: truncate if over 500KB
        try:
            if os.path.getsize(log_path) > 512_000:
                with open(log_path, "w") as f:
                    f.write("--- log rotated ---\n")
        except OSError:
            pass
        from datetime import datetime, timezone
        ts = datetime.now(timezone.utc).strftime("%H:%M:%S.%f")[:-3]
        with open(log_path, "a") as f:
            f.write(f"[{ts}] {msg}\n")
    except OSError:
        pass


def resolve_agent_type(base: str, session_id: str | None) -> str:
    """Look up the agent_type for a session via the Coral API.

    Falls back to 'claude' if the session is not found or the API is unreachable.
    """
    if not session_id:
        return "claude"
    resp = coral_api(base, "GET", f"/api/sessions/live")
    if resp and isinstance(resp, list):
        for s in resp:
            if s.get("session_id") == session_id:
                return s.get("agent_type", "claude")
    return "claude"


def truncate(s: str, max_len: int) -> str:
    """Truncate a string, adding '...' if it exceeds max_len."""
    if len(s) <= max_len:
        return s
    return s[:max_len - 3] + "..."
