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

from coral.config import get_data_dir


def get_pid_file() -> Path:
    return get_data_dir() / "tray.pid"


PID_FILE = Path.home() / ".coral" / "tray.pid"
PYPI_URL = "https://pypi.org/pypi/agent-coral/json"
GITHUB_RELEASES = "https://github.com/cdknorow/coral/releases"


def _check_for_update() -> tuple[str, str] | None:
    """Check PyPI for a newer version. Returns (current, latest) or None."""
    import json
    import urllib.request
    from importlib.metadata import version as installed_version

    try:
        current = installed_version("agent-coral")
    except Exception:
        return None

    try:
        req = urllib.request.Request(PYPI_URL)
        with urllib.request.urlopen(req, timeout=5) as resp:
            data = json.loads(resp.read())
        latest = data["info"]["version"]
    except Exception:
        return None

    try:
        current_parts = tuple(int(x) for x in current.split("."))
        latest_parts = tuple(int(x) for x in latest.split("."))
    except (ValueError, AttributeError):
        return None

    if latest_parts > current_parts:
        return (current, latest)
    return None


def _find_icon() -> str | None:
    """Locate the menu bar template icon in the package's static directory."""
    from coral.tools.utils import get_package_dir
    # Prefer the template icon (monochrome silhouette for menu bar)
    for name in ("coral_tray.png", "coral.png"):
        icon_path = get_package_dir() / "static" / name
        if icon_path.is_file():
            return str(icon_path)
    return None


def _write_pid() -> None:
    """Write current PID to the pid file."""
    pid_file = get_pid_file()
    pid_file.parent.mkdir(parents=True, exist_ok=True)
    pid_file.write_text(str(os.getpid()))


def _remove_pid() -> None:
    """Remove the pid file on exit."""
    try:
        get_pid_file().unlink(missing_ok=True)
    except OSError:
        pass


def _is_running() -> int | None:
    """Return the PID of an already-running tray process, or None."""
    pid_file = get_pid_file()
    if not pid_file.exists():
        return None
    try:
        pid = int(pid_file.read_text().strip())
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


def _run_foreground(host: str, port: int, home_dir: str | None = None) -> None:
    """Run the tray app in the foreground (called by the detached subprocess)."""
    # Set working directory so coral_root picks it up
    if home_dir:
        target = Path(home_dir).expanduser().resolve()
        target.mkdir(parents=True, exist_ok=True)
        os.chdir(target)
    try:
        import rumps
    except ImportError:
        print(
            "Error: rumps is not installed. The menu bar tray app requires rumps.\n"
            "\n"
            "Install it with:\n"
            "  pip install rumps\n"
            "\n"
            "Or use the standard dashboard instead:\n"
            "  coral",
            file=sys.stderr,
        )
        sys.exit(1)

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

    # Start uvicorn in a background thread (not daemon — we join it on quit
    # so aiosqlite can finish its cleanup before the event loop closes)
    started = threading.Event()
    server_holder: dict = {}
    server_thread = threading.Thread(
        target=_run_uvicorn, args=(host, port, started, server_holder),
    )
    server_thread.start()
    started.wait(timeout=10)

    # Open the dashboard in the browser on launch
    webbrowser.open(url)

    # Check for updates in a background thread (non-blocking)
    _update_info: dict = {}

    def _bg_update_check():
        result = _check_for_update()
        if result:
            _update_info["current"] = result[0]
            _update_info["latest"] = result[1]
            rumps.notification(
                "Coral",
                f"Update available: v{result[1]}",
                f"You have v{result[0]}. Visit Releases to download, or run: pip install --upgrade agent-coral",
            )

    threading.Thread(target=_bg_update_check, daemon=True).start()

    icon_path = _find_icon()

    class CoralTray(rumps.App):
        def __init__(self) -> None:
            super().__init__(
                "Coral",
                icon=icon_path,
                template=True,  # macOS renders as white on dark bar, black on light
                quit_button=None,  # We provide our own quit
            )
            self._server_running = True
            menu_items = [
                rumps.MenuItem("Open Dashboard", callback=self.open_dashboard),
                rumps.MenuItem("Check for Updates", callback=self.check_updates),
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

        def check_updates(self, _sender: rumps.MenuItem) -> None:
            """Check for updates and notify or open releases page."""
            def _check():
                result = _check_for_update()
                if result:
                    _update_info["current"] = result[0]
                    _update_info["latest"] = result[1]
                    rumps.notification(
                        "Coral",
                        f"Update available: v{result[1]}",
                        "pip install --upgrade agent-coral  •  Or download from Releases",
                    )
                    webbrowser.open(GITHUB_RELEASES)
                else:
                    rumps.notification("Coral", "", "You're on the latest version.")
            threading.Thread(target=_check, daemon=True).start()

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
            """Signal uvicorn to shut down gracefully and wait for cleanup."""
            server = server_holder.get("server")
            if server:
                server.should_exit = True
            # Wait for the server thread to finish so aiosqlite background
            # threads can close their connections before the loop is gone.
            if server_thread.is_alive():
                server_thread.join(timeout=5)
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
    parser.add_argument("--home", default=None,
                        help="Home directory for Coral (default: user home directory)")
    parser.add_argument("--foreground", action="store_true", help="Run in foreground (used internally)")
    parser.add_argument("--stop", action="store_true", help="Stop a running tray instance")
    args = parser.parse_args()

    # Resolve home directory: explicit flag > default to user home
    home_dir = args.home or str(Path.home())

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
        _run_foreground(args.host, args.port, home_dir)
        return

    # Check if already running
    pid = _is_running()
    if pid:
        print(f"Coral tray is already running (PID {pid}). Use --stop to stop it.")
        return

    # Spawn ourselves as a detached background process
    cmd = [sys.executable, "-m", "coral.tray", "--foreground",
           "--host", args.host, "--port", str(args.port),
           "--home", home_dir]

    log_dir = get_data_dir()
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
    print(f"  Home: {home_dir}")
    print(f"  Logs: {log_file}")
    print(f"  Stop: coral-tray --stop")


if __name__ == "__main__":
    main()
