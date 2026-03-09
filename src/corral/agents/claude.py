"""Claude agent implementation."""

from __future__ import annotations

import json
import re
import shutil
from pathlib import Path
from typing import Any

from corral.agents.base import BaseAgent
from corral.utils import HISTORY_PATH

SUMMARY_RE = re.compile(r"^[\s\u25cf\u23fa]*\|\|PULSE:SUMMARY (.*?)\|\|", re.MULTILINE)

FTS_BODY_CAP = 50_000


def _clean_match(text: str) -> str:
    return " ".join(text.split())


def _hook_entry_exists(matcher_groups: list, command: str) -> bool:
    """Check if a hook command already exists in a list of matcher groups."""
    for group in matcher_groups:
        for hook in group.get("hooks", []):
            if hook.get("command") == command:
                return True
    return False


def install_hooks(target_dir: Path | str | None = None):
    """Ensure Claude hooks for Corral are installed in settings.local.json.

    When *target_dir* is provided, hooks are written to
    ``<target_dir>/.claude/settings.local.json`` (per-worktree).
    When omitted, falls back to ``~/.claude/settings.local.json`` (global).

    Merges corral hooks into existing user hooks without overriding them.
    Skips any hook that is already present (matched by command name).
    """
    if target_dir is not None:
        target = Path(target_dir)
        # Only install into git worktree roots (.git file or directory)
        if not (target / ".git").exists():
            return
        settings_path = target / ".claude" / "settings.local.json"
    else:
        settings_path = Path.home() / ".claude" / "settings.local.json"
    settings_path.parent.mkdir(parents=True, exist_ok=True)

    if settings_path.exists():
        settings = json.loads(settings_path.read_text())
    else:
        settings = {}

    # Clean up any old-format keys from previous versions
    for old_key in ("agenticStateHook", "taskStateHook"):
        settings.pop(old_key, None)

    hooks = settings.setdefault("hooks", {})

    # Hooks we want to ensure exist: (event, matcher_group_to_add)
    desired = [
        ("PostToolUse", {
            "matcher": "TaskCreate|TaskUpdate",
            "hooks": [{"type": "command", "command": "corral-hook-task-sync"}],
        }),
        ("PostToolUse", {
            "hooks": [{"type": "command", "command": "corral-hook-agentic-state"}],
        }),
        ("Stop", {
            "hooks": [{"type": "command", "command": "corral-hook-agentic-state"}],
        }),
        ("Notification", {
            "hooks": [{"type": "command", "command": "corral-hook-agentic-state"}],
        }),
    ]

    modified = False
    for event, group in desired:
        event_list = hooks.setdefault(event, [])
        command = group["hooks"][0]["command"]
        if not _hook_entry_exists(event_list, command):
            event_list.append(group)
            modified = True

    if modified:
        settings_path.write_text(json.dumps(settings, indent=2) + "\n")


def _extract_text_from_entry(entry: dict) -> str:
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


class ClaudeAgent(BaseAgent):
    """Claude Code agent."""

    @property
    def agent_type(self) -> str:
        return "claude"

    @property
    def supports_resume(self) -> bool:
        return True

    @property
    def history_base_path(self) -> Path:
        return HISTORY_PATH

    @property
    def history_glob_pattern(self) -> str:
        return "*.jsonl"

    def build_launch_command(
        self,
        session_id: str,
        protocol_path: Path | None,
        resume_session_id: str | None = None,
        flags: list[str] | None = None,
    ) -> str:
        parts = ["claude"]
        if resume_session_id:
            parts.append(f"--resume {resume_session_id}")
        else:
            parts.append(f"--session-id {session_id}")
        if protocol_path and protocol_path.exists():
            parts.append(f"--append-system-prompt \"$(cat '{protocol_path}')\"")
        if flags:
            parts.extend(flags)
        return " ".join(parts)

    def install_hooks(self, working_dir: str) -> None:
        install_hooks(working_dir)  # module-level function defined above

    def prepare_resume(self, session_id: str, working_dir: str) -> None:
        """Copy session JSONL into the target project dir so ``claude --resume`` finds it."""
        encoded = working_dir.replace("/", "-").replace("_", "-")
        target_project = HISTORY_PATH / encoded
        target_jsonl = target_project / f"{session_id}.jsonl"

        if target_jsonl.exists():
            return

        source_jsonl: Path | None = None
        for candidate in HISTORY_PATH.iterdir():
            if not candidate.is_dir():
                continue
            f = candidate / f"{session_id}.jsonl"
            if f.exists():
                source_jsonl = f
                break

        if source_jsonl is None:
            return

        target_project.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source_jsonl, target_jsonl)

        source_dir = source_jsonl.parent / session_id
        target_dir = target_project / session_id
        if source_dir.is_dir() and not target_dir.exists():
            shutil.copytree(source_dir, target_dir)

    def load_history_sessions(self) -> list[dict[str, Any]]:
        history_base = Path.home() / ".claude" / "projects"
        if not history_base.exists():
            return []

        sessions: dict[str, dict[str, Any]] = {}
        for history_file in history_base.rglob("*.jsonl"):
            try:
                with open(history_file, "r", errors="replace") as f:
                    for line in f:
                        line = line.strip()
                        if not line:
                            continue
                        try:
                            entry = json.loads(line)
                        except json.JSONDecodeError:
                            continue

                        session_id = entry.get("sessionId")
                        if not session_id:
                            continue

                        if session_id not in sessions:
                            sessions[session_id] = {
                                "session_id": session_id,
                                "messages": [],
                                "first_timestamp": entry.get("timestamp"),
                                "last_timestamp": entry.get("timestamp"),
                                "source_file": str(history_file),
                                "source_type": "claude",
                                "summary": None,
                            }

                        ts = entry.get("timestamp")
                        if ts:
                            if not sessions[session_id]["first_timestamp"] or ts < sessions[session_id]["first_timestamp"]:
                                sessions[session_id]["first_timestamp"] = ts
                            if not sessions[session_id]["last_timestamp"] or ts > sessions[session_id]["last_timestamp"]:
                                sessions[session_id]["last_timestamp"] = ts

                        sessions[session_id]["messages"].append(entry)
            except OSError:
                continue

        result = []
        for sid, data in sessions.items():
            summary_marker = ""
            first_human = ""
            for msg in data["messages"]:
                if not summary_marker and msg.get("type") == "assistant":
                    content = msg.get("message", {}).get("content", "")
                    text = ""
                    if isinstance(content, str):
                        text = content
                    elif isinstance(content, list):
                        text = " ".join(
                            b.get("text", "") for b in content
                            if isinstance(b, dict) and b.get("type") == "text"
                        )
                    m = SUMMARY_RE.search(text)
                    if m:
                        summary_marker = _clean_match(m.group(1))

                if not first_human and msg.get("type") in ("human", "user"):
                    content = msg.get("message", {}).get("content", "")
                    if isinstance(content, str):
                        first_human = content[:100]
                    elif isinstance(content, list):
                        for block in content:
                            if isinstance(block, dict) and block.get("type") == "text":
                                first_human = block.get("text", "")[:100]
                                break
            data["summary"] = summary_marker or first_human or "(no messages)"
            data["message_count"] = len(data["messages"])
            listing = {k: v for k, v in data.items() if k != "messages"}
            result.append(listing)

        return result

    def load_session_messages(self, session_id: str) -> list[dict[str, Any]]:
        history_base = Path.home() / ".claude" / "projects"
        if not history_base.exists():
            return []

        messages = []
        for history_file in history_base.rglob("*.jsonl"):
            try:
                with open(history_file, "r", errors="replace") as f:
                    for line in f:
                        line = line.strip()
                        if not line:
                            continue
                        try:
                            entry = json.loads(line)
                        except json.JSONDecodeError:
                            continue
                        if entry.get("sessionId") == session_id:
                            messages.append(entry)
            except OSError:
                continue

        return messages

    async def index_file(self, path: Path, mtime: float, store: Any) -> int:
        """Parse a JSONL file and upsert each session found."""
        from corral.session_manager import SUMMARY_RE as SM_SUMMARY_RE, clean_match

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

                    text = _extract_text_from_entry(entry)
                    if text.strip():
                        s["texts"].append(text)

                    if not s["summary_marker"] and entry.get("type") == "assistant":
                        m = SM_SUMMARY_RE.search(text)
                        if m:
                            s["summary_marker"] = clean_match(m.group(1))

                    if not s["first_human"] and entry.get("type") in ("human", "user"):
                        s["first_human"] = text[:100]
        except OSError:
            return 0

        for sid, s in sessions.items():
            summary = s["summary_marker"] or s["first_human"] or "(no messages)"
            await store.upsert_session_index(
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
            await store.upsert_fts(sid, body)
            await store.enqueue_for_summarization(sid)

        return len(sessions)
