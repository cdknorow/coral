"""Claude agent implementation."""

from __future__ import annotations

import json
import os
import re
import shutil
from pathlib import Path
from typing import Any

from coral.agents.base import BaseAgent, ExtractedSession, discover_skills
from coral.hooks.utils import resolve_session_id, truncate
from coral.tools.utils import HISTORY_PATH

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


# Hooks Coral needs to inject into every Claude session.
_CORAL_HOOKS = [
    ("PostToolUse", {
        "matcher": "TaskCreate|TaskUpdate",
        "hooks": [{"type": "command", "command": "coral-hook-task-sync"}],
    }),
    ("PostToolUse", {
        "hooks": [{"type": "command", "command": "coral-hook-agentic-state"}],
    }),
    ("PostToolUse", {
        "hooks": [{"type": "command", "command": "coral-hook-message-check"}],
    }),
    ("Stop", {
        "hooks": [{"type": "command", "command": "coral-hook-agentic-state"}],
    }),
    ("Notification", {
        "hooks": [{"type": "command", "command": "coral-hook-agentic-state"}],
    }),
    ("UserPromptSubmit", {
        "hooks": [{"type": "command", "command": "coral-hook-agentic-state"}],
    }),
    ("SessionStart", {
        "matcher": "clear",
        "hooks": [{"type": "command", "command": "coral-hook-agentic-state --session-clear"}],
    }),
]


def _read_settings_file(path: Path) -> dict:
    """Read a JSON settings file, returning {} if missing or invalid."""
    try:
        if path.exists():
            return json.loads(path.read_text())
    except (OSError, json.JSONDecodeError):
        pass
    return {}


def _build_merged_settings(working_dir: str | None = None) -> dict:
    """Read the Claude settings hierarchy, merge in Coral hooks, return the result.

    Settings priority: local > project > global.
    --settings is a full override, so we must preserve all user settings.
    """
    home_claude = Path.home() / ".claude"

    # Read the three layers of settings
    global_settings = _read_settings_file(home_claude / "settings.json")
    project_settings: dict = {}
    local_settings: dict = {}

    if working_dir:
        wd = Path(working_dir)
        project_settings = _read_settings_file(wd / ".claude" / "settings.json")
        local_settings = _read_settings_file(wd / ".claude" / "settings.local.json")

    # Shallow merge: local > project > global (higher priority wins for top-level keys)
    merged = {**global_settings, **project_settings, **local_settings}

    # Deep-merge hooks: combine arrays per event key rather than replacing
    merged_hooks: dict[str, list] = {}
    for source in (global_settings, project_settings, local_settings):
        for event, groups in source.get("hooks", {}).items():
            if isinstance(groups, list):
                merged_hooks.setdefault(event, []).extend(groups)

    # Append Coral hooks, skipping any that already exist
    for event, group in _CORAL_HOOKS:
        event_list = merged_hooks.setdefault(event, [])
        command = group["hooks"][0]["command"]
        if not _hook_entry_exists(event_list, command):
            event_list.append(group)

    merged["hooks"] = merged_hooks
    return merged


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

    def available_commands(self, working_dir: str | None = None) -> list[dict[str, str]]:
        builtins = [
            {"name": "compact", "command": "/compact", "description": "Compress conversation history"},
            {"name": "clear", "command": "/clear", "description": "Clear conversation and start fresh"},
            {"name": "help", "command": "/help", "description": "Show available commands"},
            {"name": "cost", "command": "/cost", "description": "Show token usage and cost"},
            {"name": "review", "command": "/review", "description": "Review code changes"},
            {"name": "bug", "command": "/bug", "description": "Report a bug"},
            {"name": "init", "command": "/init", "description": "Initialize project CLAUDE.md"},
            {"name": "memory", "command": "/memory", "description": "Edit CLAUDE.md memory files"},
            {"name": "status", "command": "/status", "description": "Show account and session info"},
            {"name": "doctor", "command": "/doctor", "description": "Check health of Claude Code"},
            {"name": "config", "command": "/config", "description": "Open settings configuration"},
            {"name": "permissions", "command": "/permissions", "description": "View or update permissions"},
            {"name": "mcp", "command": "/mcp", "description": "Manage MCP server connections"},
            {"name": "vim", "command": "/vim", "description": "Toggle vim keybindings"},
            {"name": "terminal-setup", "command": "/terminal-setup", "description": "Install Shift+Enter terminal integration"},
            {"name": "login", "command": "/login", "description": "Authenticate with Anthropic"},
            {"name": "logout", "command": "/logout", "description": "Sign out of current account"},
        ]
        skills = discover_skills(working_dir)
        builtin_names = {c["name"] for c in builtins}
        return builtins + [s for s in skills if s["name"] not in builtin_names]

    def build_launch_command(
        self,
        session_id: str,
        protocol_path: Path | None,
        resume_session_id: str | None = None,
        flags: list[str] | None = None,
        working_dir: str | None = None,
        board_name: str | None = None,
        role: str | None = None,
        prompt: str | None = None,
        prompt_overrides: dict[str, str] | None = None,
    ) -> str:
        parts = ["claude"]
        effective_id = resume_session_id or session_id
        if resume_session_id:
            parts.append(f"--resume {resume_session_id}")
        else:
            parts.append(f"--session-id {session_id}")
        # Build merged settings with hooks and system prompt
        merged = _build_merged_settings(working_dir)
        # Combine protocol + behavior prompt + board instructions into systemPrompt
        sys_parts = []
        if protocol_path and protocol_path.exists():
            sys_parts.append(protocol_path.read_text())
        board_prompt = self._build_board_system_prompt(board_name, role, prompt, prompt_overrides=prompt_overrides)
        if board_prompt:
            sys_parts.append(board_prompt)
        if sys_parts:
            merged["systemPrompt"] = "\n\n".join(sys_parts)
        # Write to temp file to avoid shell escaping issues
        settings_file = Path(f"/tmp/coral_settings_{effective_id}.json")
        settings_file.write_text(json.dumps(merged, indent=2) + "\n")
        parts.append(f"--settings {settings_file}")
        if flags:
            parts.extend(flags)
        return " ".join(parts)

    # ── Hook Processing (Claude-specific) ─────────────────────────────────

    @staticmethod
    def make_tool_summary(tool_name: str, inp: dict, resp=None) -> str:
        """Generate a human-readable one-liner for a Claude tool use event."""
        if tool_name == "Read":
            fp = inp.get("file_path", "")
            name = os.path.basename(fp) if fp else "file"
            offset = inp.get("offset")
            limit = inp.get("limit")
            if offset and limit:
                return f"Read {name} (lines {offset}-{offset + limit})"
            return f"Read {name}"

        if tool_name == "Write":
            fp = inp.get("file_path", "")
            name = os.path.basename(fp) if fp else "file"
            return f"Wrote {name}"

        if tool_name == "Edit":
            fp = inp.get("file_path", "")
            name = os.path.basename(fp) if fp else "file"
            return f"Edited {name}"

        if tool_name == "Bash":
            cmd = inp.get("command", "")
            return f"Ran: {truncate(cmd, 80)}"

        if tool_name == "Grep":
            pattern = inp.get("pattern", "")
            path = inp.get("path", "")
            dir_name = os.path.basename(path.rstrip("/")) if path else ""
            suffix = f" in {dir_name}/" if dir_name else ""
            return f"Searched for '{truncate(pattern, 40)}'{suffix}"

        if tool_name == "Glob":
            pattern = inp.get("pattern", "")
            return f"Glob: {truncate(pattern, 60)}"

        if tool_name == "WebFetch":
            url = inp.get("url", "")
            return f"Fetched {truncate(url, 80)}"

        if tool_name == "WebSearch":
            query = inp.get("query", "")
            return f"Searched: {truncate(query, 80)}"

        if tool_name == "TaskCreate":
            subject = inp.get("subject", "")
            return f"Created task: {truncate(subject, 60)}"

        if tool_name == "TaskUpdate":
            task_id = inp.get("taskId", "")
            status = inp.get("status", "")
            return f"Updated task #{task_id} -> {status}" if task_id else "Updated task"

        if tool_name == "Task":
            desc = inp.get("description", "")
            return f"Launched subagent: {truncate(desc, 60)}"

        if tool_name == "TaskList":
            return "Listed tasks"

        if tool_name == "TaskGet":
            return f"Got task #{inp.get('taskId', '?')}"

        return f"Used {tool_name}"

    @staticmethod
    def make_tool_detail(tool_name: str, inp: dict) -> str | None:
        """Build a compact detail_json string for a Claude tool use event."""
        detail = {}
        if tool_name in ("Read", "Write", "Edit"):
            fp = inp.get("file_path", "")
            if fp:
                detail["file_path"] = fp
        elif tool_name == "Bash":
            cmd = inp.get("command", "")
            if cmd:
                detail["command"] = truncate(cmd, 200)
        elif tool_name == "Grep":
            detail["pattern"] = inp.get("pattern", "")
            if inp.get("path"):
                detail["path"] = inp["path"]
        elif tool_name == "Glob":
            detail["pattern"] = inp.get("pattern", "")
        elif tool_name == "WebFetch":
            url = inp.get("url", "")
            if url:
                detail["url"] = truncate(url, 200)
        elif tool_name == "WebSearch":
            detail["query"] = inp.get("query", "")
        elif tool_name == "Task":
            detail["description"] = truncate(inp.get("description", ""), 100)
            if inp.get("subagent_type"):
                detail["subagent_type"] = inp["subagent_type"]
        else:
            return None

        if not detail:
            return None
        raw = json.dumps(detail)
        return raw[:500] if len(raw) > 500 else raw

    def parse_agentic_event(self, hook_data: dict) -> dict | None:
        """Parse a Claude hook payload into an agentic event for the dashboard."""
        session_id = resolve_session_id(hook_data.get("session_id"))
        hook_type = hook_data.get("hook_event_name") or hook_data.get("type", "")

        # SessionStart with source="clear" fires when the user runs /clear.
        # The --session-clear CLI arg injects _coral_session_clear into hook_data
        # as a fallback when hook_event_name isn't set.
        if hook_type == "SessionStart" or hook_data.get("_coral_session_clear"):
            return {
                "event_type": "session_reset",
                "tool_name": None,
                "summary": "Session reset: /clear",
                "session_id": session_id,
            }

        # UserPromptSubmit: fall back to detecting the "prompt" field when
        # hook_event_name isn't populated.  Guard against tool_use / stop
        # payloads that might also contain a "prompt" key.
        if hook_type == "UserPromptSubmit" or (
            "prompt" in hook_data
            and not hook_data.get("tool_name")
            and not hook_data.get("stop_hook_active")
        ):
            return {
                "event_type": "prompt_submit",
                "tool_name": None,
                "summary": "User submitted prompt",
                "session_id": session_id,
            }

        tool = hook_data.get("tool_name", "")
        inp = hook_data.get("tool_input", {}) if isinstance(hook_data.get("tool_input"), dict) else {}

        if tool:
            return {
                "event_type": "tool_use",
                "tool_name": tool,
                "summary": self.make_tool_summary(tool, inp, hook_data.get("tool_response")),
                "detail_json": self.make_tool_detail(tool, inp),
                "session_id": session_id,
            }

        if hook_type == "Stop" or hook_data.get("stop_hook_active"):
            reason = hook_data.get("reason", "unknown")
            return {
                "event_type": "stop",
                "tool_name": None,
                "summary": f"Agent stopped: {reason}",
                "detail_json": None,
                "session_id": session_id,
            }

        if hook_type == "Notification" or hook_data.get("message"):
            message = hook_data.get("message", "")
            # "waiting for your input" is not a permission prompt — treat as
            # a stop (done) so the dashboard shows "Done" instead of
            # "Needs input".
            if "waiting for your input" in message.lower():
                return {
                    "event_type": "stop",
                    "tool_name": None,
                    "summary": f"Agent stopped: waiting for input",
                    "detail_json": None,
                    "session_id": session_id,
                }
            return {
                "event_type": "notification",
                "tool_name": None,
                "summary": f"Notification: {truncate(message, 100)}",
                "detail_json": None,
                "session_id": session_id,
            }

        return None

    def parse_task_event(self, hook_data: dict) -> dict | None:
        """Parse a Claude hook payload into a task sync event."""
        tool = hook_data.get("tool_name", "")
        inp = hook_data.get("tool_input", {}) if isinstance(hook_data.get("tool_input"), dict) else {}
        session_id = resolve_session_id(hook_data.get("session_id"))

        if tool == "TaskCreate":
            subject = inp.get("subject", "")
            if not subject:
                return None
            resp_parsed = self.parse_task_response(hook_data.get("tool_response", ""))
            return {
                "action": "create",
                "subject": subject,
                "task_id": resp_parsed["task_id"] or str(inp.get("taskId", "")),
                "status": "pending",
                "session_id": session_id,
            }

        if tool == "TaskUpdate":
            task_id = str(inp.get("taskId", ""))
            subject = inp.get("subject", "")
            status = inp.get("status", "")
            resp_parsed = self.parse_task_response(hook_data.get("tool_response", ""))
            return {
                "action": "update",
                "subject": subject or resp_parsed.get("subject", ""),
                "task_id": task_id,
                "status": status,
                "session_id": session_id,
            }

        return None

    def parse_task_response(self, resp) -> dict:
        """Extract task id and subject from a Claude tool_response."""
        result = {"task_id": "", "subject": ""}
        if isinstance(resp, dict):
            task = resp.get("task", {})
            if isinstance(task, dict):
                result["task_id"] = str(task.get("id", ""))
                result["subject"] = task.get("subject", "")
            if not result["task_id"]:
                result["task_id"] = str(resp.get("taskId", ""))
        resp_str = resp if isinstance(resp, str) else json.dumps(resp)
        if not result["task_id"]:
            m = re.search(r"Task #(\d+)", resp_str)
            if m:
                result["task_id"] = m.group(1)
        return result

    # ── Transcript Parsing (Claude JSONL) ────────────────────────────────

    _PULSE_RE = re.compile(r"\|\|PULSE:\w+[^|]*\|\|")

    def resolve_transcript_path(self, session_id: str, working_directory: str = "") -> Path | None:
        """Find the JSONL transcript file for a Claude session."""
        if working_directory:
            encoded = working_directory.replace("/", "-")
            candidate = HISTORY_PATH / encoded / f"{session_id}.jsonl"
            if candidate.exists():
                return candidate
        for jsonl_path in HISTORY_PATH.rglob(f"{session_id}.jsonl"):
            return jsonl_path
        return None

    @staticmethod
    def _summarize_tool_input(name: str, inp: dict) -> str:
        """Compact summary of Claude tool input for display."""
        if name in ("Read", "Edit", "Write", "NotebookEdit"):
            return inp.get("file_path", inp.get("notebook_path", ""))
        if name == "Bash":
            cmd = inp.get("command", "")
            return cmd[:120] + ("..." if len(cmd) > 120 else "")
        if name in ("Grep", "Glob"):
            pattern = inp.get("pattern", "")
            path = inp.get("path", "")
            return f"{pattern}" + (f" in {path}" if path else "")
        if name == "Agent":
            return inp.get("description", inp.get("prompt", ""))[:120]
        if name in ("TaskCreate", "TaskUpdate"):
            return inp.get("subject", inp.get("taskId", ""))
        if name == "WebSearch":
            return inp.get("query", "")
        if name == "WebFetch":
            return inp.get("url", "")
        for v in inp.values():
            if isinstance(v, str) and v:
                return v[:100]
        return ""

    def parse_transcript_entry(
        self, entry: dict, tool_use_names: dict[str, str]
    ) -> list[dict] | dict | None:
        """Convert a raw Claude JSONL entry into normalized frontend message(s)."""
        etype = entry.get("type")
        timestamp = entry.get("timestamp", "")

        if etype == "user":
            return self._parse_user_entry(entry, timestamp, tool_use_names)

        if etype == "assistant":
            return self._parse_assistant_entry(entry, timestamp, tool_use_names)

        return None

    def _parse_user_entry(
        self, entry: dict, timestamp: str, tool_use_names: dict[str, str]
    ) -> list[dict] | dict | None:
        msg = entry.get("message", {})
        content = msg.get("content", "")
        if isinstance(content, list):
            text_parts = [b.get("text", "") for b in content if b.get("type") == "text"]
            results: list[dict] = []
            for b in content:
                if b.get("type") == "tool_result":
                    tool_use_id = b.get("tool_use_id", "")
                    result_content = b.get("content", "")
                    is_error = b.get("is_error", False)
                    if isinstance(result_content, list):
                        result_content = "\n".join(
                            p.get("text", "") for p in result_content if p.get("type") == "text"
                        )
                    if not result_content:
                        continue
                    if len(result_content) > 10000:
                        result_content = result_content[:10000] + "\n... (truncated)"
                    tool_name = tool_use_names.get(tool_use_id, "") if tool_use_id else ""
                    results.append({
                        "type": "tool_result",
                        "timestamp": timestamp,
                        "content": result_content,
                        "tool_name": tool_name,
                        "tool_use_id": tool_use_id,
                        "is_error": is_error,
                    })
            if results:
                return results
            if not text_parts:
                return None
            content = "\n".join(text_parts)
        if not content.strip():
            return None
        return {"type": "user", "timestamp": timestamp, "content": content}

    def _parse_assistant_entry(
        self, entry: dict, timestamp: str, tool_use_names: dict[str, str]
    ) -> dict | None:
        msg = entry.get("message", {})
        content = msg.get("content", [])
        if isinstance(content, str):
            text = content
            tool_uses: list[dict] = []
        elif isinstance(content, list):
            text_parts: list[str] = []
            tool_uses = []
            for block in content:
                bt = block.get("type")
                if bt == "text":
                    text_parts.append(block.get("text", ""))
                elif bt == "tool_use":
                    tool_name = block.get("name", "")
                    tool_use_id = block.get("id", "")
                    tool_input = block.get("input", {})
                    tool_entry: dict = {
                        "name": tool_name,
                        "tool_use_id": tool_use_id,
                        "input_summary": self._summarize_tool_input(tool_name, tool_input),
                    }
                    if tool_name == "Bash":
                        tool_entry["command"] = tool_input.get("command", "")
                        desc = tool_input.get("description", "")
                        if desc:
                            tool_entry["description"] = desc
                    elif tool_name == "AskUserQuestion":
                        questions = tool_input.get("questions", [])
                        if questions:
                            tool_entry["questions"] = questions
                    elif tool_name == "Edit":
                        old = tool_input.get("old_string", "")
                        new = tool_input.get("new_string", "")
                        if old or new:
                            tool_entry["old_string"] = old
                            tool_entry["new_string"] = new
                    elif tool_name == "Write":
                        content_str = tool_input.get("content", "")
                        if content_str:
                            if len(content_str) > 10000:
                                content_str = content_str[:10000] + "\n... (truncated)"
                            tool_entry["write_content"] = content_str
                    tool_uses.append(tool_entry)
                    # Register for tool_result name lookup
                    if tool_use_id:
                        tool_use_names[tool_use_id] = tool_name
            text = "\n".join(text_parts)
        else:
            return None

        text = self._PULSE_RE.sub("", text).strip()
        if not text and not tool_uses:
            return None
        return {
            "type": "assistant",
            "timestamp": timestamp,
            "text": text,
            "tool_uses": tool_uses,
        }

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

    def extract_sessions(self, path: Path) -> list[ExtractedSession]:
        """Parse a Claude JSONL file and return extracted session data."""
        from coral.tools.session_manager import SUMMARY_RE as SM_SUMMARY_RE, clean_match

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
            return []

        results = []
        for sid, s in sessions.items():
            summary = s["summary_marker"] or s["first_human"] or "(no messages)"
            body = "\n".join(s["texts"])[:FTS_BODY_CAP]
            results.append(ExtractedSession(
                session_id=sid,
                source_type="claude",
                first_timestamp=s["first_ts"],
                last_timestamp=s["last_ts"],
                message_count=s["msg_count"],
                display_summary=summary,
                fts_body=body,
            ))
        return results
