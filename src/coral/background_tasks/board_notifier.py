"""MessageBoardNotifier — nudges idle agents when they have unread board messages.

Polls for agents that are subscribed to a message board project and have
unread messages. Sends a short prompt into their tmux terminal so they
wake up and run ``coral-board read``.
"""

from __future__ import annotations

import asyncio
import logging
from typing import Any

from coral.messageboard.store import MessageBoardStore
from coral.tools.session_manager import discover_coral_agents
from coral.tools.tmux_manager import send_to_tmux

log = logging.getLogger(__name__)


class MessageBoardNotifier:
    """Background task that notifies idle agents about unread board messages."""

    def __init__(self, board_store: MessageBoardStore, coral_store: Any = None) -> None:
        self._board_store = board_store
        self._coral_store = coral_store
        # session_id -> unread count at time of last notification
        self._notified: dict[str, int] = {}

    async def run_forever(self, interval: float = 30) -> None:
        while True:
            try:
                await self.run_once()
            except Exception:
                log.exception("MessageBoardNotifier error")
            await asyncio.sleep(interval)

    async def run_once(self) -> dict[str, int]:
        """Check all live agents for unread messages. Returns {"notified": n}."""
        agents = await discover_coral_agents()
        notified_count = 0
        live_board_ids: set[str] = set()

        for agent in agents:
            sid = agent.get("session_id")
            if not sid:
                continue

            # The CLI subscribes using the tmux session name (e.g.
            # "claude-<uuid>"), which is what the board stores as session_id.
            # discover_coral_agents() returns the bare UUID as "session_id"
            # and the full tmux name as "tmux_session".
            board_sid = agent.get("tmux_session") or sid
            live_board_ids.add(board_sid)

            # Check if this agent is subscribed to a board
            sub = await self._board_store.get_subscription(board_sid)
            if not sub:
                continue

            # Skip remote subscribers — the RemoteBoardPoller handles those
            if sub.get("origin_server"):
                continue

            project = sub["project"]

            # Skip agents on paused/sleeping boards
            from coral.messageboard.api import _paused_projects
            if project in _paused_projects:
                continue

            unread = await self._board_store.check_unread(project, board_sid)

            if unread == 0:
                # Agent has read their messages — clear notification state
                self._notified.pop(board_sid, None)
                continue

            # Only notify if unread count changed since last notification
            if self._notified.get(board_sid) == unread:
                continue

            # Send nudge
            plural = "s" if unread != 1 else ""
            nudge = f"You have {unread} unread message{plural} on the message board. Run 'coral-board read' to see them."
            err = await send_to_tmux(
                agent["agent_name"], nudge, session_id=sid,
            )
            if err:
                log.debug("Failed to nudge %s: %s", agent["agent_name"], err)
                continue

            self._notified[board_sid] = unread
            notified_count += 1
            log.debug("Nudged %s about %d unread message(s)", agent["agent_name"], unread)

        # Clean up _notified entries for sessions that are no longer live
        stale = set(self._notified) - live_board_ids
        for sid in stale:
            self._notified.pop(sid, None)

        return {"notified": notified_count}
