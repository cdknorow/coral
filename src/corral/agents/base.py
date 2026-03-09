"""Base agent class defining the interface for all agent implementations."""

from __future__ import annotations

from abc import ABC, abstractmethod
from pathlib import Path
from typing import Any


class BaseAgent(ABC):
    """Abstract base class for agent implementations (Claude, Gemini, etc.).

    Each subclass encapsulates the agent-specific logic for:
    - Building CLI launch commands
    - Loading / indexing history files
    - Installing hooks
    - Preparing for session resume
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

    @property
    def available_commands(self) -> dict[str, str]:
        """Map of command names to slash-commands the agent supports."""
        return {"compress": "/compact", "clear": "/clear"}

    @abstractmethod
    def build_launch_command(
        self,
        session_id: str,
        protocol_path: Path | None,
        resume_session_id: str | None = None,
        flags: list[str] | None = None,
    ) -> str:
        """Build the shell command string to launch this agent."""

    @abstractmethod
    def load_history_sessions(self) -> list[dict[str, Any]]:
        """Scan history files and return a list of session summary dicts."""

    @abstractmethod
    def load_session_messages(self, session_id: str) -> list[dict[str, Any]]:
        """Load all messages for a specific historical session."""

    @abstractmethod
    async def index_file(self, path: Path, mtime: float, store: Any) -> int:
        """Index a single history file into the store. Returns session count."""

    def install_hooks(self, working_dir: str) -> None:
        """Install agent-specific hooks into a working directory. Default: no-op."""

    def prepare_resume(self, session_id: str, working_dir: str) -> None:
        """Prepare for resuming a session (e.g. copy files). Default: no-op."""
