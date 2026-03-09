"""Session indexer and batch summarizer for the Session Intelligence system.

Scans Claude and Gemini history files, indexes them into SQLite with FTS5,
and enqueues new sessions for auto-summarization.
"""

from __future__ import annotations

import asyncio
import logging
from typing import Any

from corral.agents import get_all_agents
from corral.store import CorralStore

log = logging.getLogger(__name__)


class SessionIndexer:
    """Scans history files for all registered agents and upserts into session_index + session_fts."""

    def __init__(self, store: CorralStore) -> None:
        self._store = store

    async def run_once(self) -> dict[str, int]:
        """Index all history files once. Returns {"indexed": N, "skipped": M}."""
        indexed: int = 0
        skipped: int = 0
        known_mtimes = await self._store.get_indexed_mtimes()

        for agent in get_all_agents():
            base = agent.history_base_path
            if not base.exists():
                continue
            for history_file in base.rglob(agent.history_glob_pattern):
                try:
                    mtime = history_file.stat().st_mtime
                except OSError:
                    continue
                if known_mtimes.get(str(history_file), 0.0) >= mtime:
                    skipped += 1
                    continue
                count = await agent.index_file(history_file, mtime, self._store)
                indexed += count
                await asyncio.sleep(0)  # yield to event loop between files

        return {"indexed": indexed, "skipped": skipped}

    async def run_forever(self, interval: float = 120) -> None:
        """Re-index in a loop. Runs until cancelled."""
        while True:
            try:
                result = await self.run_once()
                log.info("Indexer pass: indexed=%d skipped=%d", result["indexed"], result["skipped"])
            except Exception:
                log.exception("Indexer error")
            await asyncio.sleep(interval)


class BatchSummarizer:
    """Polls summarizer_queue for pending sessions and auto-summarizes them."""

    def __init__(self, store: CorralStore) -> None:
        self._store = store

    async def run_forever(self, batch_size: int = 5, delay_between: float = 2.0) -> None:
        """Process pending summaries in a loop. Runs until cancelled."""
        while True:
            try:
                pending = await self._store.get_pending_summaries(batch_size)
                if not pending:
                    await asyncio.sleep(30)
                    continue

                try:
                    from corral.background_tasks.auto_summarizer import AutoSummarizer
                    summarizer = AutoSummarizer(self._store)
                except ImportError:
                    log.warning("auto_summarizer not available, skipping batch")
                    await asyncio.sleep(120)
                    continue

                for session_id in pending:
                    try:
                        await summarizer.summarize_session(session_id)
                        await self._store.mark_summarized(session_id, "done")
                    except Exception as e:
                        log.warning("Summarization failed for %s: %s", session_id, e)
                        await self._store.mark_summarized(session_id, "failed", str(e))
                    await asyncio.sleep(delay_between)
            except Exception:
                log.exception("BatchSummarizer error")
                await asyncio.sleep(30)
