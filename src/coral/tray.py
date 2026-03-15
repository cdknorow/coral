"""macOS menu bar (system tray) app for Coral.

Requires the `rumps` package: pip install agent-coral[tray]

Shows a coral icon in the macOS menu bar with options to open the
dashboard in a browser or quit the server.  Runs as a background process
so the launching terminal is immediately freed.
"""
from __future__ import annotations

import argparse
import atexit
import os
import signal
import subprocess
import sys
import threading
import webbrowser
from pathlib import Path

PID_FILE = Path.home() / ".coral" / "tray.pid"


def _find_icon() -> str | None:
    """Locate coral.png in the package's static directory."""
    from coral.tools.utils import get_package_dir
    icon_path = get_package_dir() / "static" / "coral.png"
    if icon_path.is_file():
        return str(icon_path)
    return None


def _write_pid() -> None:
    """Write current PID to the pid file."""
    PID_FILE.parent.mkdir(parents=True, exist_ok=True)
    PID_FILE.write_text(str(os.getpid()))


def _remove_pid() -> None:
    """Remove the pid file on exit."""
    try:
        PID_FILE.unlink(missing_ok=True)
    except OSError:
        pass


def _is_running() -> int | None:
    """Return the PID of an already-running tray process, or None."""
    if not PID_FILE.exists():
        return None
    try:
        pid = int(PID_FILE.read_text().strip())
        # Check if process is alive
        os.kill(pid, 0)
        return pid
    except (ValueError, OSError):
        # Stale pid file
        _remove_pid()
        return None


def _run_uvicorn(host: str, port: int, started: threading.Event, server_holder: dict) -> None:
    """Run uvicorn in a background thread."""
    import uvicorn

    config = uvicorn.Config("coral.web_server:app", host=host, port=port, log_level="info")
    server = uvicorn.Server(config)
    server_holder["server"] = server

    # Signal that the server is starting
    started.set()

    server.run()


def _run_foreground(host: str, port: int) -> None:
    """Run the tray app in the foreground (called by the detached subprocess)."""
    try:
        import rumps
    except ImportError:
        print(
            "rumps is not installed. Install it with: pip install agent-coral[tray]\n"
            "Falling back to standard dashboard mode."
        )
        from coral.web_server import main as web_main
        sys.argv = [sys.argv[0], "--host", host, "--port", str(port), "--no-browser"]
        web_main()
        return

    _write_pid()
    atexit.register(_remove_pid)

    # Ensure PATH includes common macOS binary locations (for tmux)
    import coral.tools.utils  # noqa: F401 — triggers PATH setup

    url = f"http://localhost:{port}"

    # Check if tmux is installed
    import shutil
    _tmux_available = shutil.which("tmux") is not None
    if not _tmux_available:
        rumps.notification(
            "Coral",
            "tmux not found",
            "Agent management requires tmux. Install with: brew install tmux",
        )

    # Start uvicorn in a daemon thread
    started = threading.Event()
    server_holder: dict = {}
    server_thread = threading.Thread(
        target=_run_uvicorn, args=(host, port, started, server_holder), daemon=True
    )
    server_thread.start()
    started.wait(timeout=10)

    # Open the dashboard in the browser on launch
    webbrowser.open(url)

    icon_path = _find_icon()

    class CoralTray(rumps.App):
        def __init__(self) -> None:
            super().__init__(
                "Coral",
                icon=icon_path,
                quit_button=None,  # We provide our own quit
            )
            self._server_running = True
            menu_items = [
                rumps.MenuItem("Open Dashboard", callback=self.open_dashboard),
                None,  # separator
            ]
            if not _tmux_available:
                menu_items.append(
                    rumps.MenuItem("Install tmux...", callback=self.install_tmux)
                )
            menu_items.extend([
                rumps.MenuItem("Shutdown — Kill Agents & Stop Server", callback=self.shutdown),
                rumps.MenuItem("Quit — Exit Coral", callback=self.quit_app),
            ])
            self.menu = menu_items

        def open_dashboard(self, _sender: rumps.MenuItem) -> None:
            webbrowser.open(url)

        def install_tmux(self, _sender: rumps.MenuItem) -> None:
            """Open a terminal and run brew install tmux."""
            try:
                subprocess.Popen([
                    "osascript", "-e",
                    'tell application "Terminal" to do script "brew install tmux"',
                ])
            except Exception:
                webbrowser.open("https://github.com/tmux/tmux/wiki/Installing")

        def _kill_agents(self) -> int:
            """Kill all running coral agent tmux sessions via the REST API."""
            import json
            import urllib.parse
            import urllib.request

            req = urllib.request.Request(f"{url}/api/sessions/live")
            with urllib.request.urlopen(req, timeout=5) as resp:
                sessions = json.loads(resp.read())

            killed = 0
            for session in sessions:
                name = session.get("name")
                if not name:
                    continue
                body = json.dumps({
                    "session_id": session.get("session_id"),
                    "agent_type": session.get("agent_type"),
                }).encode()
                kill_req = urllib.request.Request(
                    f"{url}/api/sessions/live/{urllib.parse.quote(name, safe='')}/kill",
                    data=body,
                    headers={"Content-Type": "application/json"},
                    method="POST",
                )
                try:
                    urllib.request.urlopen(kill_req, timeout=5)
                    killed += 1
                except Exception:
                    pass
            return killed

        def _stop_server(self) -> None:
            """Signal uvicorn to shut down gracefully."""
            server = server_holder.get("server")
            if server:
                server.should_exit = True
            self._server_running = False

        def shutdown(self, _sender: rumps.MenuItem) -> None:
            """Shut down all agents and the dashboard server."""
            try:
                killed = self._kill_agents()
                self._stop_server()
                rumps.notification(
                    "Coral", "", f"Shut down {killed} agent(s) and dashboard server."
                )
            except Exception as e:
                # Server may already be down, just stop it
                self._stop_server()
                rumps.notification("Coral", "", f"Dashboard stopped. Agent shutdown error: {e}")

        def quit_app(self, _sender: rumps.MenuItem) -> None:
            self._stop_server()
            _remove_pid()
            rumps.quit_application()

    tray_app = CoralTray()
    tray_app.run()


def main() -> None:
    parser = argparse.ArgumentParser(description="Coral Dashboard (menu bar)")
    parser.add_argument("--host", default="0.0.0.0", help="Host to bind to (default: 0.0.0.0)")
    parser.add_argument("--port", type=int, default=8420, help="Port to bind to (default: 8420)")
    parser.add_argument("--foreground", action="store_true", help="Run in foreground (used internally)")
    parser.add_argument("--stop", action="store_true", help="Stop a running tray instance")
    args = parser.parse_args()

    # Handle --stop
    if args.stop:
        pid = _is_running()
        if pid:
            os.kill(pid, signal.SIGTERM)
            print(f"Stopped Coral tray (PID {pid})")
        else:
            print("No running Coral tray found.")
        return

    # If --foreground, run directly (this is the detached child)
    if args.foreground:
        _run_foreground(args.host, args.port)
        return

    # Check if already running
    pid = _is_running()
    if pid:
        print(f"Coral tray is already running (PID {pid}). Use --stop to stop it.")
        return

    # Spawn ourselves as a detached background process
    cmd = [sys.executable, "-m", "coral.tray", "--foreground",
           "--host", args.host, "--port", str(args.port)]

    log_dir = Path.home() / ".coral"
    log_dir.mkdir(parents=True, exist_ok=True)
    log_file = log_dir / "tray.log"

    with open(log_file, "a") as lf:
        subprocess.Popen(
            cmd,
            stdout=lf,
            stderr=lf,
            stdin=subprocess.DEVNULL,
            start_new_session=True,
        )

    print(f"Coral tray started in background (dashboard on port {args.port})")
    print(f"  Logs: {log_file}")
    print(f"  Stop: coral-tray --stop")


if __name__ == "__main__":
    main()
