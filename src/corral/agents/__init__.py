"""Agent registry and factory.

Usage::

    from corral.agents import get_agent, get_all_agents

    claude = get_agent("claude")
    cmd = claude.build_launch_command(session_id, protocol_path)

    for agent in get_all_agents():
        sessions = agent.load_history_sessions()
"""

from __future__ import annotations

from corral.agents.base import BaseAgent
from corral.agents.claude import ClaudeAgent
from corral.agents.gemini import GeminiAgent

# Singleton instances keyed by agent_type
_REGISTRY: dict[str, BaseAgent] = {}


def _ensure_registry() -> None:
    if not _REGISTRY:
        for cls in (ClaudeAgent, GeminiAgent):
            instance = cls()
            _REGISTRY[instance.agent_type] = instance


def get_agent(agent_type: str) -> BaseAgent:
    """Return the agent instance for the given type. Falls back to Claude."""
    _ensure_registry()
    return _REGISTRY.get(agent_type.lower(), _REGISTRY["claude"])


def get_all_agents() -> list[BaseAgent]:
    """Return all registered agent instances."""
    _ensure_registry()
    return list(_REGISTRY.values())


def register_agent(agent: BaseAgent) -> None:
    """Register a custom agent implementation."""
    _ensure_registry()
    _REGISTRY[agent.agent_type] = agent


__all__ = ["BaseAgent", "ClaudeAgent", "GeminiAgent", "get_agent", "get_all_agents", "register_agent"]
