"""Background poller that queries git state for live corral agents."""

from __future__ import annotations

import asyncio
import logging
from pathlib import Path
from typing import Any

from corral.session_manager import discover_corral_agents, _find_pane
from corral.store import CorralStore
from corral.utils import run_cmd, HISTORY_PATH

log = logging.getLogger(__name__)


class GitPoller:
    """Periodically polls git branch/commit info for live agents and stores snapshots."""

    def __init__(self, store: CorralStore) -> None:
        self._store = store

    async def run_forever(self, interval: float = 120) -> None:
        while True:
            try:
                await self.poll_once()
            except Exception:
                log.exception("GitPoller error")
            await asyncio.sleep(interval)

    async def poll_once(self) -> dict[str, int]:
        agents = await discover_corral_agents()
        polled = 0
        seen_paths: set[str] = set()
        for agent in agents:
            try:
                session_id = agent.get("session_id")
                pane = await _find_pane(
                    agent["agent_name"], agent["agent_type"], session_id=session_id,
                )
                if not pane:
                    continue
                workdir = pane.get("current_path", "")
                if not workdir or workdir in seen_paths:
                    continue
                seen_paths.add(workdir)
                git_info = await self._query_git(workdir)
                if git_info:
                    await self._store.upsert_git_snapshot(
                        agent_name=agent["agent_name"],
                        agent_type=agent["agent_type"],
                        working_directory=workdir,
                        branch=git_info["branch"],
                        commit_hash=git_info["commit_hash"],
                        commit_subject=git_info["commit_subject"],
                        commit_timestamp=git_info["commit_timestamp"],
                        session_id=session_id,
                        remote_url=git_info.get("remote_url"),
                    )
                    polled += 1
            except Exception:
                log.exception("GitPoller error for agent %s", agent["agent_name"])
        return {"polled": polled}

    async def _query_git(self, workdir: str) -> dict[str, str] | None:
        """Query git for current branch and latest commit in a working directory."""
        try:
            # Get branch name
            rc, stdout, _ = await run_cmd(
                "git", "-C", workdir, "rev-parse", "--abbrev-ref", "HEAD", timeout=5.0
            )
            if rc != 0:
                return None
            branch = stdout

            # Get latest commit: hash|subject|timestamp
            rc, stdout, _ = await run_cmd(
                "git", "-C", workdir, "log", "-1", "--format=%H|%s|%aI", timeout=5.0
            )
            if rc != 0:
                return None
            parts = stdout.split("|", 2)
            if len(parts) < 3:
                return None

            # Get remote URL (best-effort)
            remote_url = None
            try:
                rc, stdout, _ = await run_cmd(
                    "git", "-C", workdir, "remote", "get-url", "origin", timeout=5.0
                )
                if rc == 0:
                    remote_url = stdout or None
            except Exception:
                pass

            return {
                "branch": branch,
                "commit_hash": parts[0],
                "commit_subject": parts[1],
                "commit_timestamp": parts[2],
                "remote_url": remote_url,
            }
        except (asyncio.TimeoutError, OSError, FileNotFoundError):
            return None
