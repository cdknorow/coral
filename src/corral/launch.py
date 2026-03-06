"""CLI entry point that executes the bundled launch_agents.sh script."""

import os
import sys
from pathlib import Path


def main():
    from corral.utils import install_hooks

    # Install hooks into each worktree's .claude/settings.local.json
    # (only when launching agents)
    launch_agents = len(sys.argv) > 3 and sys.argv[3] == "agents"
    if launch_agents:
        target_dir = Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
        for child in sorted(target_dir.iterdir()):
            if child.is_dir():
                install_hooks(child)

    script = Path(__file__).parent / "launch_agents.sh"
    if not script.exists():
        print(f"Error: launch_agents.sh not found at {script}", file=sys.stderr)
        sys.exit(1)
    os.execvp("bash", ["bash", str(script)] + sys.argv[1:])
