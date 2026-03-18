"""Needs-input detector — sends a single webhook when an agent waits for input too long."""

from __future__ import annotations

import asyncio
import logging
import time
from pathlib import Path

from coral.tools.session_manager import discover_coral_agents

log = logging.getLogger(__name__)

NEEDS_INPUT_THRESHOLD = 300  # 5 minutes


class IdleDetector:
    """Periodically checks for agents waiting for input and creates webhook deliveries."""

    def __init__(self, store) -> None:
        self._store = store
        # Track which agents we've already notified so we only fire once per
        # waiting period. Cleared when the agent leaves the "needs input" state.
        self._notified: set[str] = set()

    async def run_forever(self, interval: float = 60) -> None:
        while True:
            try:
                await self.run_once()
            except Exception:
                log.exception("IdleDetector error")
            await asyncio.sleep(interval)

    async def run_once(self) -> dict[str, int]:
        """Check all agents. Returns {"notifications": n}."""
        configs = await self._store.list_webhook_configs(enabled_only=True)
        if not configs:
            return {"notifications": 0}

        agents = await discover_coral_agents()
        session_ids = [a["session_id"] for a in agents if a.get("session_id")]
        latest_events = await self._store.get_latest_event_types(session_ids)

        count = 0
        active_waiting: set[str] = set()

        for agent in agents:
            name = agent["agent_name"]
            sid = agent.get("session_id")
            ev_tuple = latest_events.get(sid) if sid else None
            latest_ev = ev_tuple[0] if ev_tuple else None
            waiting = latest_ev == "notification"

            if not waiting:
                # Agent is active — clear notification state
                self._notified.discard(name)
                continue

            # Agent is waiting for input — check staleness via stat (no file read)
            try:
                staleness = time.time() - Path(agent["log_path"]).stat().st_mtime
            except OSError:
                staleness = 0

            if staleness < NEEDS_INPUT_THRESHOLD:
                continue

            active_waiting.add(name)

            if name in self._notified:
                continue  # Already sent notification for this waiting period

            # Send one notification per enabled webhook
            for cfg in configs:
                if cfg["agent_filter"] and cfg["agent_filter"] != name:
                    continue
                minutes = int(staleness // 60)
                await self._store.create_webhook_delivery(
                    webhook_id=cfg["id"],
                    agent_name=name,
                    session_id=sid,
                    event_type="needs_input",
                    event_summary=f"Agent needs input — waiting for {minutes} minutes",
                )
                count += 1

            self._notified.add(name)

        return {"notifications": count}
