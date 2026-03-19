"""CLI for the Coral message board.

Usage:
    coral-board [--server URL] join <project> --as <job-title> [--webhook <url>]
    coral-board [--server URL] post <message>
    coral-board check [--quiet]
    coral-board read [--limit N] [--last N]
    coral-board leave
    coral-board projects
    coral-board subscribers
    coral-board delete

After joining a project, all commands operate on that project automatically.
Only one project can be active at a time.

Session ID is resolved from the tmux session name, with hostname as fallback.

Server resolution priority:
    1. --server flag (highest)
    2. server_url saved in state file (from last join)
    3. CORAL_URL environment variable
    4. http://localhost:8420 (default)
"""

from __future__ import annotations

import argparse
import json
import os
import platform
import subprocess
import sys
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import quote as urlquote
from urllib.request import Request, urlopen


_STATE_DIR = Path.home() / ".coral"

# Set by main() when --server is passed on the command line.
_server_override: str | None = None


def _resolve_server() -> str:
    """Resolve the Coral server URL using priority chain:
    --server flag > state file server_url > CORAL_URL env var > default."""
    if _server_override:
        return _server_override.rstrip("/")
    state = _load_state()
    if state and state.get("server_url"):
        return state["server_url"].rstrip("/")
    return os.environ.get("CORAL_URL", "http://localhost:8420").rstrip("/")


def _session_id() -> str:
    """Resolve session ID from tmux session name, with hostname fallback."""
    if os.environ.get("TMUX"):
        try:
            result = subprocess.run(
                ["tmux", "display-message", "-p", "#S"],
                capture_output=True, text=True, timeout=5,
            )
            name = result.stdout.strip()
            if name:
                return name
        except (FileNotFoundError, subprocess.TimeoutExpired):
            pass
    return platform.node()


def _state_file() -> Path:
    """Per-session state file so each agent gets its own state."""
    sid = _session_id()
    safe_name = sid.replace("/", "_").replace("\\", "_")
    return _STATE_DIR / f"board_state_{safe_name}.json"


def _load_state() -> dict | None:
    sf = _state_file()
    if sf.exists():
        try:
            return json.loads(sf.read_text())
        except (json.JSONDecodeError, OSError):
            return None
    return None


def _save_state(project: str, job_title: str) -> None:
    """Save the active project for this worktree."""
    _STATE_DIR.mkdir(parents=True, exist_ok=True)
    data: dict = {
        "project": project,
        "job_title": job_title,
        "session_id": _session_id(),
        "server_url": _resolve_server(),
    }
    _state_file().write_text(json.dumps(data))


def _clear_state() -> None:
    """Remove the active project for this worktree."""
    sf = _state_file()
    if sf.exists():
        sf.unlink()


def _active_project() -> str:
    """Get the active project or exit with an error."""
    state = _load_state()
    if not state:
        print("Not joined to any project. Run: coral-board join <project> --as <role>", file=sys.stderr)
        sys.exit(1)
    return state["project"]


def _api(method: str, path: str, body: dict | None = None) -> dict | list:
    """Make an HTTP request to the message board API."""
    url = f"{_resolve_server()}/api/board{path}"
    data = json.dumps(body).encode() if body else None
    req = Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    try:
        with urlopen(req, timeout=10) as resp:
            return json.loads(resp.read())
    except HTTPError as e:
        detail = ""
        try:
            detail = json.loads(e.read()).get("detail", "")
        except Exception:
            pass
        print(f"Error {e.code}: {detail or e.reason}", file=sys.stderr)
        sys.exit(1)
    except URLError as e:
        print(f"Cannot reach Coral server at {_resolve_server()}: {e.reason}", file=sys.stderr)
        sys.exit(1)


# ── Commands ────────────────────────────────────────────────────────────────


def _is_remote_join() -> bool:
    """Check if the current join is to a non-local server."""
    server = _resolve_server()
    # Consider it remote if it doesn't point to localhost
    from urllib.parse import urlparse
    parsed = urlparse(server)
    hostname = parsed.hostname or ""
    return hostname not in ("localhost", "127.0.0.1", "::1")


def _local_coral_url() -> str:
    """URL of the local Coral server for remote subscription registration."""
    port = os.environ.get("CORAL_PORT", "8420")
    return f"http://localhost:{port}"


def _register_remote_with_local(session_id: str, remote_server: str, project: str, job_title: str) -> None:
    """Register a remote board subscription with the local Coral server."""
    url = f"{_local_coral_url()}/api/board/remotes"
    body = json.dumps({
        "session_id": session_id,
        "remote_server": remote_server,
        "project": project,
        "job_title": job_title,
    }).encode()
    req = Request(url, data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    try:
        with urlopen(req, timeout=5) as resp:
            resp.read()
    except Exception:
        print("Warning: Could not register with local Coral for remote notifications.", file=sys.stderr)


def _unregister_remote_from_local(session_id: str) -> None:
    """Remove remote board subscription from the local Coral server."""
    url = f"{_local_coral_url()}/api/board/remotes"
    body = json.dumps({"session_id": session_id}).encode()
    req = Request(url, data=body, method="DELETE")
    req.add_header("Content-Type", "application/json")
    try:
        with urlopen(req, timeout=5) as resp:
            resp.read()
    except Exception:
        pass  # Best-effort cleanup


def cmd_join(args: argparse.Namespace) -> None:
    """Subscribe to a project's message board."""
    sid = _session_id()

    # Error if already joined to a project
    current = _load_state()
    if current:
        print(
            f"Already joined '{current['project']}' as '{current['job_title']}'. "
            f"Run 'coral-board leave' first.",
            file=sys.stderr,
        )
        sys.exit(1)

    body: dict = {
        "session_id": sid,
        "job_title": args.job_title,
    }
    if args.webhook:
        body["webhook_url"] = args.webhook
    result = _api("POST", f"/{urlquote(args.project, safe='')}/subscribe", body)
    _save_state(args.project, args.job_title)

    # If joining a remote board, register with local Coral for tmux notifications
    if _is_remote_join():
        _register_remote_with_local(sid, _resolve_server(), args.project, args.job_title)

    print(f"Joined '{args.project}' as '{args.job_title}' (session: {result['session_id']})")
    print("Tip: Run 'coral-board read --last 5' to see recent context without flooding your conversation.")
    print("     Do NOT run 'coral-board read' to catch up on all old messages — it will fill your context.")
    print("     You will be notified of new messages that @mention you or use @notify-all.")


def cmd_leave(args: argparse.Namespace) -> None:
    """Unsubscribe from the current project."""
    project = _active_project()
    sid = _session_id()

    # If leaving a remote board, unregister from local Coral
    if _is_remote_join():
        _unregister_remote_from_local(sid)

    _api("DELETE", f"/{urlquote(project, safe='')}/subscribe", {"session_id": sid})
    _clear_state()
    print(f"Left '{project}'")


def cmd_post(args: argparse.Namespace) -> None:
    """Post a message to the current project board."""
    project = _active_project()
    message = " ".join(args.message)
    result = _api("POST", f"/{urlquote(project, safe='')}/messages", {
        "session_id": _session_id(),
        "content": message,
    })
    print(f"Message #{result['id']} posted to '{project}'")


def cmd_read(args: argparse.Namespace) -> None:
    """Read new messages from the current project board."""
    project = _active_project()
    sid = _session_id()

    if args.last:
        # Fetch enough messages and show only the last N (no cursor advancement)
        result = _api("GET", f"/{urlquote(project, safe='')}/messages/all?limit=200")
        messages = result.get("messages", result) if isinstance(result, dict) else result
        if not messages:
            print("No messages on this board.")
            return
        for msg in messages[-args.last:]:
            title = msg.get("job_title", msg["session_id"])
            ts = msg["created_at"][:16].replace("T", " ")
            print(f"[{ts}] {title}: {msg['content']}")
        return

    messages = _api("GET", f"/{urlquote(project, safe='')}/messages?session_id={sid}&limit={args.limit}")
    if not messages:
        print("No new messages.")
        return
    for msg in messages:
        title = msg.get("job_title", msg["session_id"])
        ts = msg["created_at"][:16].replace("T", " ")
        print(f"[{ts}] {title}: {msg['content']}")


def cmd_check(args: argparse.Namespace) -> None:
    """Check for unread messages without advancing the read cursor."""
    project = _active_project()
    sid = _session_id()
    result = _api("GET", f"/{urlquote(project, safe='')}/messages/check?session_id={sid}")
    count = result.get("unread", 0)
    if getattr(args, "quiet", False):
        print(count)
    else:
        if count == 0:
            print("No unread messages.")
        else:
            print(f"{count} unread message{'s' if count != 1 else ''} in '{project}'")


def cmd_projects(args: argparse.Namespace) -> None:
    """List all active projects."""
    projects = _api("GET", "/projects")
    if not projects:
        print("No active projects.")
        return
    state = _load_state()
    current = state["project"] if state else None
    for p in projects:
        marker = " *" if p["project"] == current else ""
        print(f"  {p['project']}  ({p['subscriber_count']} subscribers, {p['message_count']} messages){marker}")


def cmd_subscribers(args: argparse.Namespace) -> None:
    """List subscribers in the current project."""
    project = _active_project()
    subs = _api("GET", f"/{urlquote(project, safe='')}/subscribers")
    if not subs:
        print(f"No subscribers in '{project}'.")
        return
    sid = _session_id()
    for s in subs:
        marker = " (you)" if s["session_id"] == sid else ""
        webhook = f"  webhook: {s['webhook_url']}" if s.get("webhook_url") else ""
        print(f"  {s['job_title']} ({s['session_id']}){marker}{webhook}")


def cmd_delete(args: argparse.Namespace) -> None:
    """Delete the current project and all its messages."""
    project = _active_project()
    _api("DELETE", f"/{urlquote(project, safe='')}")
    _clear_state()
    print(f"Deleted project '{project}'")


# ── Argument parser ─────────────────────────────────────────────────────────


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="coral-board",
        description="CLI for the Coral inter-agent message board. Join a project once, then post/read without repeating the project name.",
    )
    parser.add_argument(
        "--server", default=None,
        help="Coral server URL (overrides state file and CORAL_URL env var)",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    # join
    p_join = sub.add_parser("join", help="Join a project board (must leave current first)")
    p_join.add_argument("project", help="Project name")
    p_join.add_argument("--as", dest="job_title", required=True, help="Your role/job title")
    p_join.add_argument("--webhook", help="Webhook URL for push notifications")
    p_join.set_defaults(func=cmd_join)

    # leave
    p_leave = sub.add_parser("leave", help="Leave the current project")
    p_leave.set_defaults(func=cmd_leave)

    # post
    p_post = sub.add_parser("post", help="Post a message")
    p_post.add_argument("message", nargs="+", help="Message content")
    p_post.set_defaults(func=cmd_post)

    # check
    p_check = sub.add_parser("check", help="Check unread message count (does not advance cursor)")
    p_check.add_argument("--quiet", "-q", action="store_true", help="Print only the count (for scripting)")
    p_check.set_defaults(func=cmd_check)

    # read
    p_read = sub.add_parser("read", help="Read new messages")
    p_read.add_argument("--limit", type=int, default=50, help="Max messages to fetch (default: 50)")
    p_read.add_argument("--last", type=int, default=0, help="Show the last N messages (does not advance cursor)")
    p_read.set_defaults(func=cmd_read)

    # projects
    p_projects = sub.add_parser("projects", help="List all active projects")
    p_projects.set_defaults(func=cmd_projects)

    # subscribers
    p_subs = sub.add_parser("subscribers", help="List subscribers in your current project")
    p_subs.set_defaults(func=cmd_subscribers)

    # delete
    p_del = sub.add_parser("delete", help="Delete current project and all its data")
    p_del.set_defaults(func=cmd_delete)

    return parser


def main() -> None:
    global _server_override
    parser = build_parser()
    args = parser.parse_args()
    if args.server:
        _server_override = args.server
    args.func(args)


if __name__ == "__main__":
    main()
