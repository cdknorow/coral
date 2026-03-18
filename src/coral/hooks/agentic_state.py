"""CLI entry point for agentic state hooks — tracks all tool use, stops, and notifications.

This is the Coral-side orchestration: read hook JSON from stdin, use the
appropriate agent to parse it, and send the resulting event to the Coral
dashboard API.
"""

import json
import os
import sys

from coral.agents import get_agent
from coral.hooks.utils import coral_api, debug_log, resolve_agent_type, resolve_session_id


def main():
    """Read hook JSON from stdin, create event for the activity timeline."""
    try:
        raw = sys.stdin.read()
    except Exception as exc:
        debug_log(f"AGENTIC_STATE STDIN_ERROR: {type(exc).__name__}: {exc}")
        return

    debug_log(f"AGENTIC_STATE RAW({len(raw)}): {raw[:300]}")

    try:
        d = json.loads(raw)
    except (json.JSONDecodeError, ValueError) as exc:
        debug_log(f"AGENTIC_STATE JSON_ERROR: {exc}")
        return

    # If launched with --session-clear, inject a marker so parse_agentic_event
    # can identify this as a SessionStart/clear event even when
    # hook_event_name is missing from the payload.
    if "--session-clear" in sys.argv:
        d["_coral_session_clear"] = True

    hook_type = d.get("hook_event_name") or d.get("type", "")
    debug_log(f"AGENTIC_STATE INPUT: hook_type={hook_type} argv={sys.argv[1:]}")

    port = os.environ.get("CORAL_PORT", "8420")
    base = f"http://localhost:{port}"

    session_id = resolve_session_id(d.get("session_id"))
    agent_type = resolve_agent_type(base, session_id)
    agent = get_agent(agent_type)

    agent_name = agent.resolve_agent_name(d)
    if not agent_name:
        debug_log(f"DROPPED (no agent_name): hook_type={hook_type}")
        return

    event = agent.parse_agentic_event(d)
    if event is None:
        debug_log(f"DROPPED (parse returned None): hook_type={hook_type} agent={agent_name} keys={list(d.keys())}")
        return

    # Send event to the Coral dashboard
    coral_api(base, "POST", f"/api/sessions/live/{agent_name}/events", {
        "event_type": event["event_type"],
        "tool_name": event.get("tool_name"),
        "summary": event["summary"],
        "session_id": event.get("session_id"),
        "detail_json": event.get("detail_json"),
    })

    debug_log(f"DONE: agent={agent_name} event_type={event['event_type']}")


if __name__ == "__main__":
    try:
        main()
    except Exception:
        pass  # Never block the agent
