"""Tests for the system tray module (src/coral/tray.py).

Since rumps requires macOS GUI and PyObjC, these tests mock rumps
and verify the tray module's logic: threading model, icon loading,
menu actions, graceful shutdown, background spawning, and import fallback.
"""

import os
import sys
import threading
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest


# ── Helpers ──────────────────────────────────────────────────────────────────

def _get_static_dir():
    """Return the path to src/coral/static/."""
    return os.path.join(
        os.path.dirname(__file__), "..", "src", "coral", "static"
    )


# ── Icon Tests ───────────────────────────────────────────────────────────────


class TestTrayIconPath:
    """Verify the tray module finds the coral.png icon."""

    def test_coral_png_exists_in_static(self):
        icon_path = os.path.join(_get_static_dir(), "coral.png")
        assert os.path.isfile(icon_path)

    def test_find_icon_returns_path(self):
        from coral.tray import _find_icon
        result = _find_icon()
        assert result is not None
        assert result.endswith(".png")
        assert "coral" in os.path.basename(result)
        assert os.path.isfile(result)

    def test_find_icon_returns_none_if_missing(self):
        from coral.tray import _find_icon
        with patch("pathlib.Path.is_file", return_value=False):
            assert _find_icon() is None


# ── _run_uvicorn Tests ───────────────────────────────────────────────────────


class TestRunUvicorn:
    """Test the _run_uvicorn helper function."""

    def test_sets_started_event(self):
        from coral.tray import _run_uvicorn
        started = threading.Event()
        holder = {}

        with patch("uvicorn.Config") as mock_config, \
             patch("uvicorn.Server") as mock_server_cls:
            mock_server = MagicMock()
            mock_server_cls.return_value = mock_server
            _run_uvicorn("0.0.0.0", 8420, started, holder)

        assert started.is_set()
        mock_server.run.assert_called_once()
        assert holder["server"] is mock_server

    def test_passes_correct_config(self):
        from coral.tray import _run_uvicorn
        started = threading.Event()
        holder = {}

        with patch("uvicorn.Config") as mock_config, \
             patch("uvicorn.Server") as mock_server_cls:
            mock_server_cls.return_value = MagicMock()
            _run_uvicorn("127.0.0.1", 9999, started, holder)

        mock_config.assert_called_once_with(
            "coral.web_server:app",
            host="127.0.0.1",
            port=9999,
            log_level="info",
        )


# ── _run_foreground Tests ────────────────────────────────────────────────────


class TestRunForeground:
    """Test _run_foreground() which contains the actual tray logic."""

    def _make_rumps_mock(self):
        """Create a mock rumps module."""
        mock_rumps = MagicMock()
        mock_rumps.App = MagicMock(return_value=MagicMock())
        mock_rumps.MenuItem = MagicMock()
        return mock_rumps

    def _make_rumps_import(self, mock_rumps):
        """Create a mock __import__ that returns mock_rumps for 'rumps'."""
        import builtins
        real_import = builtins.__import__
        def mock_import(name, *args, **kwargs):
            if name == "rumps":
                return mock_rumps
            return real_import(name, *args, **kwargs)
        return mock_import

    def test_server_thread_is_daemon(self):
        """Uvicorn must run in a daemon thread."""
        from coral.tray import _run_foreground

        mock_rumps = self._make_rumps_mock()

        with patch("threading.Thread") as MockThread, \
             patch("threading.Event.wait", return_value=None), \
             patch("coral.tray._write_pid"), \
             patch("coral.tray._remove_pid"):
            mock_thread_instance = MagicMock()
            MockThread.return_value = mock_thread_instance

            with patch("builtins.__import__", side_effect=self._make_rumps_import(mock_rumps)):
                _run_foreground("0.0.0.0", 8420)

            # First Thread call should be the uvicorn server thread
            assert MockThread.call_count >= 1, "At least one thread should be created"
            first_call_kwargs = MockThread.call_args_list[0][1]
            assert first_call_kwargs.get("daemon") is True, "Server thread must be a daemon"
            assert "_run_uvicorn" in str(first_call_kwargs.get("target", ""))

    def test_opens_browser_on_startup(self):
        """The tray app should open the dashboard in the browser on startup."""
        from coral.tray import _run_foreground

        mock_rumps = self._make_rumps_mock()

        with patch("threading.Thread") as MockThread, \
             patch("threading.Event.wait", return_value=None), \
             patch("webbrowser.open") as mock_open, \
             patch("coral.tray._write_pid"), \
             patch("coral.tray._remove_pid"), \
             patch("builtins.__import__", side_effect=self._make_rumps_import(mock_rumps)):
            MockThread.return_value = MagicMock()
            _run_foreground("0.0.0.0", 8420)

        mock_open.assert_called_once_with("http://localhost:8420")

    def test_fallback_without_rumps(self):
        """When rumps is not installed, _run_foreground falls back to web_server.main."""
        from coral.tray import _run_foreground

        import builtins
        real_import = builtins.__import__

        def mock_import(name, *args, **kwargs):
            if name == "rumps":
                raise ImportError("No module named 'rumps'")
            return real_import(name, *args, **kwargs)

        with patch("builtins.__import__", side_effect=mock_import), \
             patch("coral.web_server.main") as mock_web_main:
            _run_foreground("0.0.0.0", 8420)
            mock_web_main.assert_called_once()

    def test_fallback_sets_no_browser_flag(self):
        """Fallback mode should pass --no-browser to web_server.main."""
        from coral.tray import _run_foreground

        import builtins
        real_import = builtins.__import__

        def mock_import(name, *args, **kwargs):
            if name == "rumps":
                raise ImportError("No module named 'rumps'")
            return real_import(name, *args, **kwargs)

        captured_argv = {}

        def capture_web_main():
            captured_argv["argv"] = list(sys.argv)

        with patch("builtins.__import__", side_effect=mock_import), \
             patch("coral.web_server.main", side_effect=capture_web_main):
            _run_foreground("0.0.0.0", 8420)

        assert "--no-browser" in captured_argv["argv"]

    def test_chdir_to_home_dir(self, tmp_path):
        """_run_foreground should chdir to the specified home directory."""
        from coral.tray import _run_foreground

        mock_rumps = self._make_rumps_mock()
        target_dir = str(tmp_path / "my-coral-home")

        with patch("threading.Thread") as MockThread, \
             patch("threading.Event.wait", return_value=None), \
             patch("coral.tray._write_pid"), \
             patch("coral.tray._remove_pid"), \
             patch("builtins.__import__", side_effect=self._make_rumps_import(mock_rumps)):
            MockThread.return_value = MagicMock()
            original_cwd = os.getcwd()
            try:
                _run_foreground("0.0.0.0", 8420, target_dir)
                assert os.getcwd() == target_dir
                assert os.path.isdir(target_dir)
            finally:
                os.chdir(original_cwd)

    def test_writes_pid_file(self):
        """_run_foreground should write a PID file on startup."""
        from coral.tray import _run_foreground

        mock_rumps = self._make_rumps_mock()

        with patch("threading.Thread") as MockThread, \
             patch("threading.Event.wait", return_value=None), \
             patch("coral.tray._write_pid") as mock_write_pid, \
             patch("builtins.__import__", side_effect=self._make_rumps_import(mock_rumps)):
            MockThread.return_value = MagicMock()
            _run_foreground("0.0.0.0", 8420)

        mock_write_pid.assert_called_once()


# ── main() background spawning Tests ────────────────────────────────────────


class TestMainBackground:
    """Test main() background spawning behavior."""

    def test_main_exists_and_callable(self):
        from coral.tray import main
        assert callable(main)

    def test_main_spawns_background_process(self):
        """main() without --foreground should spawn a detached subprocess."""
        from coral.tray import main

        with patch("sys.argv", ["coral-tray"]), \
             patch("coral.tray._is_running", return_value=None), \
             patch("subprocess.Popen") as mock_popen, \
             patch("builtins.open", MagicMock()):
            main()

        mock_popen.assert_called_once()
        call_kwargs = mock_popen.call_args[1]
        assert call_kwargs.get("start_new_session") is True
        # The command should include --foreground
        cmd = mock_popen.call_args[0][0]
        assert "--foreground" in cmd

    def test_main_skips_if_already_running(self, capsys):
        """main() should not spawn if a tray is already running."""
        from coral.tray import main

        with patch("sys.argv", ["coral-tray"]), \
             patch("coral.tray._is_running", return_value=12345), \
             patch("subprocess.Popen") as mock_popen:
            main()

        mock_popen.assert_not_called()
        output = capsys.readouterr().out
        assert "already running" in output

    def test_main_stop_flag(self):
        """main() with --stop should kill a running tray process."""
        from coral.tray import main

        with patch("sys.argv", ["coral-tray", "--stop"]), \
             patch("coral.tray._is_running", return_value=99999), \
             patch("os.kill") as mock_kill:
            main()

        mock_kill.assert_called_once()

    def test_main_foreground_flag_calls_run_foreground(self):
        """main() with --foreground should call _run_foreground directly."""
        from coral.tray import main

        with patch("sys.argv", ["coral-tray", "--foreground"]), \
             patch("coral.tray._run_foreground") as mock_fg:
            main()

        # home_dir defaults to Path.home() when not specified
        mock_fg.assert_called_once_with("0.0.0.0", 8420, str(Path.home()))

    def test_main_home_flag_passed_to_foreground(self):
        """main() with --home should forward the home dir to _run_foreground."""
        from coral.tray import main

        with patch("sys.argv", ["coral-tray", "--foreground", "--home", "/tmp/my-coral"]), \
             patch("coral.tray._run_foreground") as mock_fg:
            main()

        mock_fg.assert_called_once_with("0.0.0.0", 8420, "/tmp/my-coral")

    def test_main_home_flag_in_spawn_command(self):
        """main() should pass --home to the spawned subprocess."""
        from coral.tray import main

        with patch("sys.argv", ["coral-tray", "--home", "/tmp/my-coral"]), \
             patch("coral.tray._is_running", return_value=None), \
             patch("subprocess.Popen") as mock_popen, \
             patch("builtins.open", MagicMock()):
            main()

        cmd = mock_popen.call_args[0][0]
        assert "--home" in cmd
        assert "/tmp/my-coral" in cmd


# ── PID file helpers ────────────────────────────────────────────────────────


class TestPidHelpers:
    """Test PID file management."""

    def test_is_running_returns_none_no_file(self, tmp_path):
        from coral.tray import _is_running
        with patch("coral.tray.PID_FILE", tmp_path / "nonexistent.pid"):
            assert _is_running() is None

    def test_is_running_returns_pid_for_live_process(self, tmp_path):
        from coral.tray import _is_running
        pid_file = tmp_path / "tray.pid"
        pid_file.write_text(str(os.getpid()))  # current process is alive
        with patch("coral.tray.PID_FILE", pid_file):
            assert _is_running() == os.getpid()

    def test_is_running_cleans_stale_pid(self, tmp_path):
        from coral.tray import _is_running
        pid_file = tmp_path / "tray.pid"
        pid_file.write_text("999999999")  # unlikely to be a real PID
        with patch("coral.tray.PID_FILE", pid_file):
            assert _is_running() is None


# ── pyproject.toml Tests ────────────────────────────────────────────────────


class TestEntryPoint:

    def test_coral_tray_entry_point(self):
        pyproject_path = os.path.join(os.path.dirname(__file__), "..", "pyproject.toml")
        with open(pyproject_path) as f:
            content = f.read()
        assert 'coral-tray = "coral.tray:main"' in content


class TestOptionalDependency:

    def test_rumps_not_in_core_dependencies(self):
        pyproject_path = os.path.join(os.path.dirname(__file__), "..", "pyproject.toml")
        with open(pyproject_path) as f:
            content = f.read()
        import re
        deps_match = re.search(r"^dependencies\s*=\s*\[(.*?)\]", content, re.MULTILINE | re.DOTALL)
        if deps_match:
            assert "rumps" not in deps_match.group(1)

    def test_rumps_in_tray_optional_dependency(self):
        pyproject_path = os.path.join(os.path.dirname(__file__), "..", "pyproject.toml")
        with open(pyproject_path) as f:
            content = f.read()
        import re
        tray_match = re.search(r"tray\s*=\s*\[(.*?)\]", content, re.DOTALL)
        assert tray_match is not None, "No tray optional dependency section found"
        assert "rumps" in tray_match.group(1)
