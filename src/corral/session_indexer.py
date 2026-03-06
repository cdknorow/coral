"""Session indexer and batch summarizer for the Session Intelligence system.

Scans Claude and Gemini history files, indexes them into SQLite with FTS5,
and enqueues new sessions for auto-summarization.
"""

from __future__ import annotations

import asyncio
import json
import logging
from pathlib import Path
from typing import Any

from corral.session_manager import (
    SUMMARY_RE,
    clean_match,
    _extract_gemini_text,
)
from corral.session_store import SessionStore

from corral.utils import HISTORY_PATH, GEMINI_HISTORY_BASE

log = logging.getLogger(__name__)

FTS_BODY_CAP = 50_000


def _extract_text_from_claude_entry(entry: dict) -> str:
    """Extract plain text from a Claude JSONL message entry."""
    msg = entry.get("message", {})
    content = msg.get("content", "")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "\n".join(
            b.get("text", "")
            for b in content
            if isinstance(b, dict) and b.get("type") == "text"
        )
    return ""


class SessionIndexer:
    """Scans Claude and Gemini history files and upserts into session_index + session_fts."""

    def __init__(self, store: SessionStore) -> None:
        self._store = store

    async def run_once(self) -> dict[str, int]:
        """Index all history files once. Returns {"indexed": N, "skipped": M}."""
        indexed: int = 0
        skipped: int = 0
        known_mtimes = await self._store.get_indexed_mtimes()

        # -- Claude files --
        if HISTORY_PATH.exists():
            for jsonl_file in HISTORY_PATH.rglob("*.jsonl"):
                try:
                    mtime = jsonl_file.stat().st_mtime
                except OSError:
                    continue
                if known_mtimes.get(str(jsonl_file), 0.0) >= mtime:
                    skipped += 1
                    continue
                count = await self._index_claude_file(jsonl_file, mtime)
                indexed += count
                await asyncio.sleep(0)  # yield to event loop between files

        # -- Gemini files --
        if GEMINI_HISTORY_BASE.exists():
            for session_file in GEMINI_HISTORY_BASE.rglob("session-*.json"):
                try:
                    mtime = session_file.stat().st_mtime
                except OSError:
                    continue
                if known_mtimes.get(str(session_file), 0.0) >= mtime:
                    skipped += 1
                    continue
                count = await self._index_gemini_file(session_file, mtime)
                indexed += count
                await asyncio.sleep(0)  # yield to event loop between files

        return {"indexed": indexed, "skipped": skipped}

    async def _index_claude_file(self, path: Path, mtime: float) -> int:
        """Parse a JSONL file and upsert each session found. Returns count."""
        sessions: dict[str, dict[str, Any]] = {}

        try:
            with open(path, "r", errors="replace") as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        entry = json.loads(line)
                    except json.JSONDecodeError:
                        continue

                    sid = entry.get("sessionId")
                    if not sid:
                        continue

                    if sid not in sessions:
                        sessions[sid] = {
                            "texts": [],
                            "first_ts": entry.get("timestamp"),
                            "last_ts": entry.get("timestamp"),
                            "msg_count": 0,
                            "summary_marker": "",
                            "first_human": "",
                        }

                    s = sessions[sid]
                    s["msg_count"] += 1
                    ts = entry.get("timestamp")
                    if ts:
                        if not s["first_ts"] or ts < s["first_ts"]:
                            s["first_ts"] = ts
                        if not s["last_ts"] or ts > s["last_ts"]:
                            s["last_ts"] = ts

                    text = _extract_text_from_claude_entry(entry)
                    if text.strip():
                        s["texts"].append(text)

                    # Extract summary marker from assistant messages
                    if not s["summary_marker"] and entry.get("type") == "assistant":
                        m = SUMMARY_RE.search(text)
                        if m:
                            s["summary_marker"] = clean_match(m.group(1))

                    if not s["first_human"] and entry.get("type") in ("human", "user"):
                        s["first_human"] = text[:100]
        except OSError:
            return 0

        for sid, s in sessions.items():
            summary = s["summary_marker"] or s["first_human"] or "(no messages)"
            await self._store.upsert_session_index(
                session_id=sid,
                source_type="claude",
                source_file=str(path),
                first_timestamp=s["first_ts"],
                last_timestamp=s["last_ts"],
                message_count=s["msg_count"],
                display_summary=summary,
                file_mtime=mtime,
            )
            body = "\n".join(s["texts"])[:FTS_BODY_CAP]
            await self._store.upsert_fts(sid, body)
            await self._store.enqueue_for_summarization(sid)

        return len(sessions)

    async def _index_gemini_file(self, path: Path, mtime: float) -> int:
        """Parse a Gemini session JSON file and upsert. Returns 0 or 1."""
        try:
            data = json.loads(path.read_text(errors="replace"))
        except (OSError, json.JSONDecodeError):
            return 0

        sid = data.get("sessionId")
        if not sid:
            return 0

        messages = data.get("messages", [])
        first_ts = data.get("startTime")
        last_ts = data.get("lastUpdated")

        summary_marker = ""
        first_user = ""
        texts: list[str] = []

        for msg in messages:
            msg_type = msg.get("type", "")
            content = msg.get("content", [])
            if not isinstance(content, list):
                continue
            text = _extract_gemini_text(content)
            if text.strip():
                texts.append(text)

            if not summary_marker and msg_type == "gemini":
                m = SUMMARY_RE.search(text)
                if m:
                    summary_marker = clean_match(m.group(1))

            if not first_user and msg_type == "user":
                first_user = text[:100]

        summary = summary_marker or first_user or "(no messages)"
        await self._store.upsert_session_index(
            session_id=sid,
            source_type="gemini",
            source_file=str(path),
            first_timestamp=first_ts,
            last_timestamp=last_ts,
            message_count=len(messages),
            display_summary=summary,
            file_mtime=mtime,
        )
        body = "\n".join(texts)[:FTS_BODY_CAP]
        await self._store.upsert_fts(sid, body)
        await self._store.enqueue_for_summarization(sid)
        return 1

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

    def __init__(self, store: SessionStore) -> None:
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
                    from corral.auto_summarizer import AutoSummarizer
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
