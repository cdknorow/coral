"""SubgentWSListener — maintains WebSocket connections to Subgent boards.

Connects to one or more Subgent boards via WebSocket, listens for incoming
messages, checks @mentions against local agents, and sends tmux nudges
when agents are mentioned.
"""

from __future__ import annotations

import asyncio
import json
import logging
from dataclasses import dataclass, field

from coral.tools.session_manager import discover_coral_agents
from coral.tools.tmux_manager import send_to_tmux

log = logging.getLogger(__name__)

# Exponential backoff parameters for reconnection
_INITIAL_BACKOFF = 1.0
_MAX_BACKOFF = 60.0
_BACKOFF_FACTOR = 2.0


def _is_mentioned(content: str, session_id: str, job_title: str = "") -> str | None:
    """Check if message content mentions a specific agent.

    Returns the mention type ("broadcast", "direct", "role") or None.
    """
    lower = content.lower()

    # Direct mention: @session_id
    if f"@{session_id.lower()}" in lower:
        return "direct"

    # Role mention: @job_title
    if job_title and f"@{job_title.lower()}" in lower:
        return "role"

    # Broadcast mentions
    for pattern in ("@all", "@notify-all", "@notify_all", "@notifyall"):
        if pattern in lower:
            return "broadcast"

    return None


@dataclass
class BoardConnection:
    """Tracks state for a single WebSocket board connection."""

    url: str
    board_id: str
    api_key: str = field(repr=False)
    session_id: str
    job_title: str
    task: asyncio.Task | None = field(default=None, repr=False)


class SubgentWSListener:
    """Manages WebSocket connections to Subgent boards for real-time mentions."""

    def __init__(self) -> None:
        self._boards: dict[str, BoardConnection] = {}  # keyed by "url:board_id"

    async def start_board(
        self,
        ws_url: str,
        board_id: str,
        api_key: str,
        session_id: str,
        job_title: str = "",
    ) -> None:
        """Start listening to a Subgent board via WebSocket.

        Args:
            ws_url: WebSocket URL (ws:// or wss://) of the Subgent server.
            board_id: Board UUID to connect to.
            api_key: Agent API key for authentication.
            session_id: This listener's session identity.
            job_title: This listener's role/job title.
        """
        key = f"{ws_url}:{board_id}"
        if key in self._boards and self._boards[key].task and not self._boards[key].task.done():
            log.debug("Already connected to %s/%s", ws_url, board_id)
            return

        conn = BoardConnection(
            url=ws_url,
            board_id=board_id,
            api_key=api_key,
            session_id=session_id,
            job_title=job_title,
        )
        conn.task = asyncio.create_task(self._listen_forever(conn))
        self._boards[key] = conn
        log.info("Started WebSocket listener for %s/%s", ws_url, board_id)

    async def stop_board(self, ws_url: str, board_id: str) -> None:
        """Stop listening to a Subgent board."""
        key = f"{ws_url}:{board_id}"
        conn = self._boards.pop(key, None)
        if conn and conn.task and not conn.task.done():
            conn.task.cancel()
            try:
                await conn.task
            except asyncio.CancelledError:
                pass
        log.info("Stopped WebSocket listener for %s/%s", ws_url, board_id)

    async def stop_all(self) -> None:
        """Stop all board listeners."""
        # Collect tasks before popping to avoid empty dict lookup
        conns = list(self._boards.values())
        self._boards.clear()
        tasks = []
        for conn in conns:
            if conn.task and not conn.task.done():
                conn.task.cancel()
                tasks.append(conn.task)
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)

    async def _listen_forever(self, conn: BoardConnection) -> None:
        """Maintain a persistent WebSocket connection with auto-reconnect."""
        try:
            import websockets
        except ImportError:
            log.error("websockets package not installed — cannot use SubgentWSListener")
            return

        backoff = _INITIAL_BACKOFF

        while True:
            try:
                ws_endpoint = f"{conn.url}/api/board/ws/{conn.board_id}"
                log.debug("Connecting to %s", ws_endpoint)

                async with websockets.connect(ws_endpoint) as ws:
                    # Send auth message
                    auth_msg = json.dumps({
                        "type": "auth",
                        "token": conn.api_key,
                        "session_id": conn.session_id,
                        "last_seen_id": 0,
                    })
                    await ws.send(auth_msg)

                    # Wait for auth response
                    raw = await asyncio.wait_for(ws.recv(), timeout=10)
                    resp = json.loads(raw)
                    if resp.get("type") == "error":
                        log.error(
                            "WebSocket auth failed for %s/%s: %s",
                            conn.url, conn.board_id, resp.get("message", "unknown error"),
                        )
                        return  # Don't retry auth failures

                    log.info("WebSocket authenticated for %s/%s", conn.url, conn.board_id)
                    backoff = _INITIAL_BACKOFF  # Reset on successful connect

                    # Listen for messages
                    async for raw in ws:
                        try:
                            msg = json.loads(raw)
                        except json.JSONDecodeError:
                            continue

                        if msg.get("type") != "message":
                            continue

                        await self._handle_message(conn, msg)

            except asyncio.CancelledError:
                log.debug("WebSocket listener cancelled for %s/%s", conn.url, conn.board_id)
                return
            except Exception:
                log.debug(
                    "WebSocket disconnected from %s/%s, reconnecting in %.1fs",
                    conn.url, conn.board_id, backoff,
                )
                await asyncio.sleep(backoff)
                backoff = min(backoff * _BACKOFF_FACTOR, _MAX_BACKOFF)

    async def _handle_message(self, conn: BoardConnection, msg: dict) -> None:
        """Process an incoming WebSocket message and send tmux nudges for mentions."""
        content = msg.get("content", "")
        sender = msg.get("session_id", "")

        # Don't notify about our own messages
        if sender == conn.session_id:
            return

        # Discover local agents and check mentions for each
        agents = await discover_coral_agents()

        for agent in agents:
            agent_sid = agent.get("session_id", "")
            board_sid = agent.get("tmux_session") or agent_sid
            agent_job = agent.get("job_title", "")

            if not board_sid:
                continue

            mention_type = _is_mentioned(content, board_sid, agent_job)
            if mention_type is None:
                continue

            nudge = (
                f"You have 1 unread message on the message board. "
                f"Run 'coral-board read' to see them."
            )
            err = await send_to_tmux(
                agent["agent_name"], nudge, session_id=agent_sid,
            )
            if err:
                log.debug("Failed to nudge %s: %s", agent["agent_name"], err)
            else:
                log.debug(
                    "Nudged %s (%s mention from %s)",
                    agent["agent_name"], mention_type, sender,
                )
