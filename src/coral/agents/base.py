"""Base agent class defining the interface for all agent implementations."""

from __future__ import annotations

import json
import logging
import os
import re
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

log = logging.getLogger(__name__)


def _parse_frontmatter(text: str) -> dict[str, str]:
    """Parse simple YAML frontmatter (flat key: value pairs) from a markdown file."""
    lines = text.split("\n")
    if not lines or lines[0].strip() != "---":
        return {}
    result: dict[str, str] = {}
    for line in lines[1:]:
        if line.strip() == "---":
            break
        m = re.match(r"^(\w[\w_-]*)\s*:\s*(.+)$", line)
        if m:
            key = m.group(1).strip()
            val = m.group(2).strip().strip("\"'")
            result[key] = val
    return result


def _scan_skills_dir(skills_dir: Path, seen: set[str]) -> list[dict[str, str]]:
    """Scan a .claude/skills directory for skill markdown files.

    Looks for:
    - *.md files directly in the directory
    - */SKILL.md files in subdirectories
    """
    results: list[dict[str, str]] = []
    if not skills_dir.is_dir():
        return results

    # Direct .md files
    try:
        for md_file in sorted(skills_dir.iterdir()):
            if md_file.suffix == ".md" and md_file.is_file():
                _try_add_skill(md_file, seen, results)
    except OSError:
        pass

    # Subdirectory SKILL.md files
    try:
        for sub in sorted(skills_dir.iterdir()):
            if sub.is_dir():
                skill_md = sub / "SKILL.md"
                if skill_md.is_file():
                    _try_add_skill(skill_md, seen, results)
    except OSError:
        pass

    return results


def _try_add_skill(md_path: Path, seen: set[str], results: list[dict[str, str]]) -> None:
    """Parse a skill .md file and append to results if valid and not already seen."""
    try:
        text = md_path.read_text(errors="replace")
    except OSError:
        return
    fm = _parse_frontmatter(text)
    name = fm.get("name", "")
    if not name:
        # Fallback: use parent directory name for SKILL.md, else filename stem
        if md_path.name == "SKILL.md":
            name = md_path.parent.name
        else:
            name = md_path.stem
    if not name or name in seen:
        return
    seen.add(name)
    desc = fm.get("description", "")
    results.append({
        "name": name,
        "command": f"/{name}",
        "description": desc,
    })


def discover_skills(working_dir: str | None = None) -> list[dict[str, str]]:
    """Scan .claude/skills dirs and installed plugins for available skills.

    Returns a list of {name, command, description} dicts.
    Deduplicates by name — project-level overrides user-level which overrides plugin.
    """
    seen: set[str] = set()
    results: list[dict[str, str]] = []

    # 1. Project skills: {working_dir}/.claude/skills/
    if working_dir:
        project_skills = Path(working_dir) / ".claude" / "skills"
        results.extend(_scan_skills_dir(project_skills, seen))

    # 2. User skills: ~/.claude/skills/
    user_skills = Path.home() / ".claude" / "skills"
    results.extend(_scan_skills_dir(user_skills, seen))

    # 3. Installed plugins: ~/.claude/plugins/installed_plugins.json
    plugins_file = Path.home() / ".claude" / "plugins" / "installed_plugins.json"
    if plugins_file.is_file():
        try:
            data = json.loads(plugins_file.read_text(errors="replace"))
            # Normalise to a flat list of plugin entries.
            # v2 format: {"version": 2, "plugins": {"name@source": [entries]}}
            # v1 format: [entries]
            plugin_entries = data.get("plugins", data) if isinstance(data, dict) else data
            if isinstance(plugin_entries, dict):
                all_entries: list = []
                for entry_list in plugin_entries.values():
                    if isinstance(entry_list, list):
                        all_entries.extend(entry_list)
                plugin_entries = all_entries

            if isinstance(plugin_entries, list):
                for plugin in plugin_entries:
                    install_path = plugin.get("installPath", "") if isinstance(plugin, dict) else ""
                    if not install_path:
                        continue
                    plugin_dir = Path(install_path)
                    # Plugin skills
                    results.extend(_scan_skills_dir(plugin_dir / "skills", seen))
                    # Plugin commands and agents
                    for subdir_name in ("commands", "agents"):
                        subdir = plugin_dir / subdir_name
                        if subdir.is_dir():
                            try:
                                for md_file in sorted(subdir.iterdir()):
                                    if md_file.suffix == ".md" and md_file.is_file():
                                        _try_add_skill(md_file, seen, results)
                            except OSError:
                                pass
        except (OSError, json.JSONDecodeError):
            pass

    return results

# Default board system-prompt fragments (used in systemPrompt injected via settings file).
# These are the fallback when the user hasn't configured custom prompts.
DEFAULT_ORCHESTRATOR_SYSTEM_PROMPT = (
    "Post a message with coral-board post \"<your introduction>\" that introduces yourself, "
    "then discuss your proposed plan with the operator (the human user) before posting assignments to the team."
)
DEFAULT_WORKER_SYSTEM_PROMPT = (
    "Post a message with coral-board post \"<your introduction>\" that introduces yourself, "
    "then wait for instructions from the Orchestrator."
)


@dataclass
class ExtractedSession:
    """Data extracted from a history file for a single session.

    Returned by ``BaseAgent.extract_sessions`` and consumed by the
    ``SessionIndexer`` to insert into the store.
    """

    session_id: str
    source_type: str
    first_timestamp: str | None = None
    last_timestamp: str | None = None
    message_count: int = 0
    display_summary: str = "(no messages)"
    fts_body: str = ""


class BaseAgent(ABC):
    """Abstract base class for agent implementations (Claude, Gemini, etc.).

    Each subclass encapsulates the agent-specific logic for:
    - Building CLI launch commands
    - Loading / indexing history files
    - Installing hooks
    - Preparing for session resume
    - Processing hook payloads (tool summaries, task sync, events)
    """

    @property
    @abstractmethod
    def agent_type(self) -> str:
        """Short identifier, e.g. 'claude', 'gemini'."""

    @property
    @abstractmethod
    def supports_resume(self) -> bool:
        """Whether the agent supports resuming a previous session."""

    @property
    @abstractmethod
    def history_base_path(self) -> Path:
        """Root directory where this agent stores its history files."""

    @property
    @abstractmethod
    def history_glob_pattern(self) -> str:
        """Glob pattern (relative to *history_base_path*) for history files."""

    def available_commands(self, working_dir: str | None = None) -> list[dict[str, str]]:
        """Return available slash commands and skills for this agent type."""
        return [
            {"name": "compact", "command": "/compact", "description": "Compress conversation history"},
            {"name": "clear", "command": "/clear", "description": "Clear conversation and start fresh"},
        ]

    @staticmethod
    def _build_board_system_prompt(
        board_name: str | None,
        role: str | None,
        prompt: str | None,
        prompt_overrides: dict[str, str] | None = None,
    ) -> str:
        """Build the board + behavior portion of the system prompt.

        prompt_overrides can contain 'default_prompt_orchestrator' and/or
        'default_prompt_worker' keys to replace the default tail text.
        """
        parts = []
        if prompt:
            parts.append(prompt)
        if board_name:
            is_orchestrator = role and "orchestrator" in role.lower()
            role_label = f" Your role is: {role}." if role else ""
            board_intro = (
                f"You were automatically joined to message board \"{board_name}\".{role_label} "
                f"Do NOT run coral-board join — you are already subscribed.\n\n"
                "Use the coral-board CLI to communicate with your teammates:\n"
                "  coral-board read          — read new messages from teammates\n"
                "  coral-board post \"msg\"    — post a message to the board\n"
                "  coral-board read --last 5 — see the 5 most recent messages\n"
                "  coral-board subscribers   — see who is on the board\n"
                "Check the board periodically for updates from your teammates.\n\n"
            )
            overrides = prompt_overrides or {}
            if is_orchestrator:
                tail = overrides.get("default_prompt_orchestrator") or DEFAULT_ORCHESTRATOR_SYSTEM_PROMPT
            else:
                tail = overrides.get("default_prompt_worker") or DEFAULT_WORKER_SYSTEM_PROMPT
            board_intro += tail
            parts.append(board_intro)
        return "\n\n".join(parts)

    @abstractmethod
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
        """Build the shell command string to launch this agent."""

    @abstractmethod
    def load_history_sessions(self) -> list[dict[str, Any]]:
        """Scan history files and return a list of session summary dicts."""

    @abstractmethod
    def load_session_messages(self, session_id: str) -> list[dict[str, Any]]:
        """Load all messages for a specific historical session."""

    @abstractmethod
    def extract_sessions(self, path: Path) -> list[ExtractedSession]:
        """Extract session data from a history file.

        Returns a list of ``ExtractedSession`` objects — one per session found
        in the file. The caller (``SessionIndexer``) is responsible for
        inserting them into the store.
        """

    def prepare_resume(self, session_id: str, working_dir: str) -> None:
        """Prepare for resuming a session (e.g. copy files). Default: no-op."""

    # ── Hook Processing Interface ─────────────────────────────────────────

    def resolve_agent_name(self, hook_data: dict) -> str:
        """Extract the agent/worktree name from hook payload data.

        Default implementation uses the basename of the cwd field.
        """
        cwd = hook_data.get("cwd", "")
        return os.path.basename(cwd.rstrip("/"))

    def parse_agentic_event(self, hook_data: dict) -> dict | None:
        """Parse a hook payload into an agentic event dict for the dashboard.

        Returns a dict with keys: event_type, summary, tool_name, detail_json, session_id.
        Returns None if the payload doesn't represent a recognized event.

        Subclasses override this to handle agent-specific hook formats.
        """
        return None

    def parse_task_event(self, hook_data: dict) -> dict | None:
        """Parse a hook payload into a task sync event dict.

        Returns a dict with keys: action ("create"|"update"), subject, task_id, status,
        session_id.
        Returns None if the payload doesn't represent a task event.

        Subclasses override this to handle agent-specific task formats.
        """
        return None

    def parse_task_response(self, resp) -> dict:
        """Parse a tool response to extract task id and subject.

        Returns a dict with keys: task_id, subject.
        Default returns empty strings.
        """
        return {"task_id": "", "subject": ""}

    # ── Transcript Parsing Interface ──────────────────────────────────────

    def resolve_transcript_path(self, session_id: str, working_directory: str = "") -> Path | None:
        """Find the transcript file for a session.

        *working_directory* is an optional hint for fast lookup.
        Returns the Path to the transcript file, or None if not found.
        """
        return None

    def parse_transcript_entry(
        self, entry: dict[str, Any], tool_use_names: dict[str, str]
    ) -> list[dict[str, Any]] | dict[str, Any] | None:
        """Convert a raw transcript entry into normalized frontend message(s).

        *tool_use_names* maps tool_use_id → tool_name for labeling results.
        The method should also populate this dict when it encounters tool_use blocks.

        Return format (each message dict must include a "type" key):
        - ``{"type": "user", "timestamp": str, "content": str}``
        - ``{"type": "assistant", "timestamp": str, "text": str, "tool_uses": list}``
        - ``{"type": "tool_result", "timestamp": str, "content": str, ...}``
        - A list of the above (for entries that expand to multiple messages)
        - None to skip the entry
        """
        return None
