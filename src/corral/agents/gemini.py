"""Gemini agent implementation."""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any

from corral.agents.base import BaseAgent
from corral.utils import GEMINI_HISTORY_BASE

SUMMARY_RE = re.compile(r"^[\s\u25cf\u23fa]*\|\|PULSE:SUMMARY (.*?)\|\|", re.MULTILINE)

FTS_BODY_CAP = 50_000


def _clean_match(text: str) -> str:
    return " ".join(text.split())


def _extract_gemini_text(content: list[dict]) -> str:
    """Extract plain text from a Gemini message content array."""
    parts = []
    for item in content:
        if isinstance(item, dict) and item.get("text"):
            parts.append(item["text"])
    return "\n".join(parts)


def _normalize_gemini_message(msg: dict) -> dict[str, Any]:
    """Convert a Gemini message to the Claude-compatible format used by the UI."""
    msg_type = msg.get("type", "unknown")
    content = msg.get("content", [])
    text = _extract_gemini_text(content) if isinstance(content, list) else ""

    if msg_type == "user":
        role = "human"
    else:
        role = "assistant"

    return {
        "sessionId": msg.get("id", ""),
        "timestamp": msg.get("timestamp"),
        "type": role,
        "message": {"content": text},
    }


class GeminiAgent(BaseAgent):
    """Gemini agent."""

    @property
    def agent_type(self) -> str:
        return "gemini"

    @property
    def supports_resume(self) -> bool:
        return False

    @property
    def history_base_path(self) -> Path:
        return GEMINI_HISTORY_BASE

    @property
    def history_glob_pattern(self) -> str:
        return "session-*.json"

    def build_launch_command(
        self,
        session_id: str,
        protocol_path: Path | None,
        resume_session_id: str | None = None,
        flags: list[str] | None = None,
    ) -> str:
        if protocol_path and protocol_path.exists():
            cmd = f'GEMINI_SYSTEM_MD="{protocol_path}" gemini'
        else:
            cmd = "gemini"
        if flags:
            cmd += " " + " ".join(flags)
        return cmd

    def load_history_sessions(self) -> list[dict[str, Any]]:
        if not GEMINI_HISTORY_BASE.exists():
            return []

        result = []
        for session_file in GEMINI_HISTORY_BASE.rglob("session-*.json"):
            try:
                data = json.loads(session_file.read_text(errors="replace"))
            except (OSError, json.JSONDecodeError):
                continue

            session_id = data.get("sessionId")
            if not session_id:
                continue

            messages = data.get("messages", [])
            first_ts = data.get("startTime")
            last_ts = data.get("lastUpdated")

            summary_marker = ""
            first_user = ""
            for msg in messages:
                msg_type = msg.get("type", "")
                content = msg.get("content", [])
                if not isinstance(content, list):
                    continue
                text = _extract_gemini_text(content)
                if not summary_marker and msg_type == "gemini":
                    m = SUMMARY_RE.search(text)
                    if m:
                        summary_marker = _clean_match(m.group(1))
                if not first_user and msg_type == "user":
                    first_user = text[:100]

            result.append({
                "session_id": session_id,
                "first_timestamp": first_ts,
                "last_timestamp": last_ts,
                "source_file": str(session_file),
                "source_type": "gemini",
                "summary": summary_marker or first_user or "(no messages)",
                "message_count": len(messages),
            })

        return result

    def load_session_messages(self, session_id: str) -> list[dict[str, Any]]:
        if not GEMINI_HISTORY_BASE.exists():
            return []

        for session_file in GEMINI_HISTORY_BASE.rglob("session-*.json"):
            try:
                data = json.loads(session_file.read_text(errors="replace"))
            except (OSError, json.JSONDecodeError):
                continue

            if data.get("sessionId") != session_id:
                continue

            return [_normalize_gemini_message(m) for m in data.get("messages", [])]

        return []

    async def index_file(self, path: Path, mtime: float, store: Any) -> int:
        """Parse a Gemini session JSON file and upsert. Returns 0 or 1."""
        from corral.session_manager import SUMMARY_RE as SM_SUMMARY_RE, clean_match

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
                m = SM_SUMMARY_RE.search(text)
                if m:
                    summary_marker = clean_match(m.group(1))

            if not first_user and msg_type == "user":
                first_user = text[:100]

        summary = summary_marker or first_user or "(no messages)"
        await store.upsert_session_index(
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
        await store.upsert_fts(sid, body)
        await store.enqueue_for_summarization(sid)
        return 1
