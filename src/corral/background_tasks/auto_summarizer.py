"""Auto-summarizer: generates markdown summaries for historical sessions using Claude Code CLI."""

from __future__ import annotations

import asyncio
import shutil
from typing import Any

from corral.session_manager import load_history_session_messages


SYSTEM_PROMPT = """You are a session summarizer. You will be given a chat transcript or log of chats between a user and an AI coding assistant, produce a concise markdown summary with:

1. A one-line **title** (## heading)
2. A brief **summary** paragraph (2-3 sentences describing what was accomplished)
3. A **task checklist** of what was done (using - [x] for completed, - [ ] for incomplete)
4. **Key files** modified (bulleted list, if identifiable)

Keep it concise — under 300 words total. Use markdown formatting. Do not ask for more information, do the best with what you have been given."""


def _condense_messages(messages: list[dict[str, Any]], max_chars: int = 30000) -> str:
    """Extract message text and condense to fit within max_chars."""
    parts = []
    for entry in messages:
        msg_type = entry.get("type", "unknown")
        msg = entry.get("message", {})
        content = ""

        if isinstance(msg.get("content"), str):
            content = msg["content"]
        elif isinstance(msg.get("content"), list):
            content = "\n".join(
                b.get("text", "")
                for b in msg["content"]
                if isinstance(b, dict) and b.get("type") == "text"
            )

        if not content.strip():
            continue

        role = "User" if msg_type in ("human", "user") else "Assistant"
        parts.append(f"### {role}\n{content}")

    full_text = "\n\n".join(parts)

    # Truncate if too long, keeping beginning and end
    if len(full_text) > max_chars:
        half = max_chars // 2
        full_text = full_text[:half] + "\n\n[... middle of conversation truncated ...]\n\n" + full_text[-half:]

    return full_text


class AutoSummarizer:
    """Generates auto-summaries for sessions using the Claude Code CLI."""

    def __init__(self, store: Any) -> None:
        self._store = store

    async def summarize_session(self, session_id: str) -> str:
        """Load messages, call Claude to summarize, save result. Returns summary text."""
        # Check if user has already edited — don't overwrite
        notes = await self._store.get_session_notes(session_id)
        if notes.get("is_user_edited"):
            return notes.get("notes_md", "")

        # Load messages
        messages = load_history_session_messages(session_id)
        if not messages:
            return ""

        transcript = _condense_messages(messages)
        if not transcript.strip():
            return ""

        # Call Claude for summarization
        try:
            summary = await self._call_claude(transcript)
        except Exception as e:
            summary = f"*Auto-summarization failed: {e}*"

        # Save the auto-summary
        await self._store.save_auto_summary(session_id, summary)
        return summary

    async def _call_claude(self, transcript: str) -> str:
        """Call Claude Code CLI in print mode to generate a summary."""
        claude_path = shutil.which("claude")
        if not claude_path:
            return self._fallback_summary(transcript)

        prompt = f"{SYSTEM_PROMPT}\n\nPlease summarize this coding session:\n\n{transcript}"

        from corral.utils import run_cmd

        rc, stdout, stderr = await run_cmd(
            claude_path,
            "--print",
            "--model", "haiku",
            "--no-session-persistence",
            prompt,
            timeout=60.0
        )

        if rc != 0:
            err = stderr.strip() if stderr else "Unknown error"
            raise RuntimeError(f"claude CLI exited {rc}: {err}")

        return stdout.strip() if stdout else ""

    def _fallback_summary(self, transcript: str) -> str:
        """Generate a basic extractive summary when Claude CLI is not available."""
        lines = transcript.split("\n")
        user_msgs: list[str] = []
        for i, line in enumerate(lines):
            if line.strip() == "### User":
                msg_lines = []
                for j in range(i + 1, min(i + 4, len(lines))):
                    if lines[j].strip().startswith("### "):
                        break
                    msg_lines.append(lines[j])
                text = " ".join(msg_lines).strip()
                if text:
                    user_msgs.append(text[:100])

        if not user_msgs:
            return "*No summary available — install Claude Code for AI-powered summaries.*"

        summary = "## Session Summary\n\n"
        summary += "**User requests:**\n"
        for i in range(min(10, len(user_msgs))):
            summary += f"- {user_msgs[i]}\n"
        summary += "\n*Install Claude Code for AI-powered summaries.*"
        return summary
