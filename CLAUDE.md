# CLAUDE.md - Project Guide

## Project Overview
**Corral** is a multi-agent orchestration system for managing AI coding agents (Claude and Gemini) running in parallel git worktrees using tmux. It features a web dashboard, real-time logging, complete session history with FTS5 search, git state polling, task management, scheduled jobs, webhook notifications, agent notes, and event tracking.

## Project Structure Highlights
- `src/corral/`: Main package directory
  - `launch_agents.sh`: Bash script to discover worktrees, launch tmux sessions, and start the web server.
  - `launch.py`: Launcher entry point for the `launch-corral` CLI command.
  - `web_server.py`: FastAPI web dashboard (REST + WebSocket endpoints).
  - `PROTOCOL.md`: Protocol for agents to follow (status/summary reporting).
  - `agents/`: Agent implementations (`base.py`, `claude.py`, `gemini.py`).
  - `api/`: REST API route modules (`live_sessions.py`, `history.py`, `system.py`, `tasks.py`, `schedule.py`, `uploads.py`, `webhooks.py`).
  - `store/`: SQLite storage layer (`connection.py`, `sessions.py`, `git.py`, `tasks.py`, `schedule.py`, `webhooks.py`).
  - `tools/`: Core utilities (`session_manager.py`, `tmux_manager.py`, `log_streamer.py`, `pulse_detector.py`, `jsonl_reader.py`, `cron_parser.py`, `run_callback.py`, `utils.py`).
  - `background_tasks/`: Background services (`session_indexer.py`, `auto_summarizer.py`, `git_poller.py`, `idle_detector.py`, `scheduler.py`, `webhook_dispatcher.py`).
  - `hooks/`: Claude Code integration hooks (`task_state.py`, `agentic_state.py`, `utils.py`).
  - `templates/`: Jinja2 HTML templates (`index.html`, `diff.html`, `includes/`).
  - `static/`: JavaScript, CSS, images, and favicon assets.
- `tests/`: Test suite (Python and JavaScript tests).
- `docs/`: MkDocs documentation site (Material theme), published at https://cdknorow.github.io/corral/.
- `DEVELOP.md`: Detailed developer guide containing full project structure, API endpoints, and database schema.
- `pyproject.toml`: Project configuration and dependencies.

## Key Commands

### Setup & Installation
```bash
# Create a virtual env in the worktree and install
python3 -m venv .venv
.venv/bin/pip install -e .
.venv/bin/pip install pytest pytest-asyncio httpx
```

### Running Tests
```bash
# Always use the worktree venv to run tests
.venv/bin/python -m pytest tests/ -v
```

### Launching the Corral
```bash
# Launch Claude agents and web dashboard for worktrees in the current directory
./src/corral/launch_agents.sh .

# Launch Gemini agents from a specific path
./src/corral/launch_agents.sh <path-to-root> gemini

# Override the web dashboard port (default: 8420)
CORRAL_PORT=9000 ./src/corral/launch_agents.sh .
```

### Running the Web Dashboard (standalone)
```bash
# Start the web dashboard (default: http://localhost:8420)
corral

# Custom host/port
corral --host 127.0.0.1 --port 9000
```

### Managing Agents
- **Attach to tmux (Claude):** `tmux attach -t claude-agent-1`
- **Attach to tmux (Gemini):** `tmux attach -t gemini-agent-1`
- **Attach to web server:** `tmux attach -t corral-web-server`
- **Switch window:** `Ctrl+b n` (next) / `Ctrl+b p` (previous)
- **Detach tmux:** `Ctrl+b d`

## Agent Protocol
All agent events use the `||PULSE:<EVENT_TYPE> <payload>||` format. The dashboard parses these from agent output in real time.

- `||PULSE:STATUS <Short Description>||`: Current task (emit before/after each subtask).
- `||PULSE:SUMMARY <Goal Description>||`: High-level goal (emit once at start or when goal changes).
- `||PULSE:CONFIDENCE <Low|High> <specific reason>||`: Flag uncertainty (`Low`) or non-obvious confidence (`High`) with a specific reason.

## Development Guidelines
- **Build System:** Setuptools with `pyproject.toml`.
- **Dependencies:** `fastapi`, `uvicorn`, `jinja2`, `aiosqlite`, `httpx`, `python-multipart` (Python 3.8+).
- **Database:** SQLite (`~/.corral/sessions.db`) using WAL mode.
- **Logs:** Agents stream output to `/tmp/<agent_type>_corral_<folder_name>.log` via `tmux pipe-pane`.
- **Entry Points:** `corral` / `corral-dashboard` (web server), `launch-corral` (agent launcher), `corral-hook-task-sync` (task sync hook), `corral-hook-agentic-state` (agentic state hook).

## Documentation
- Documentation uses MkDocs with Material theme, configured in `docs/mkdocs.yml`.
- Local preview: `cd docs && mkdocs serve`
- Deploy to GitHub Pages: `cd docs && mkdocs gh-deploy`
- Published at https://cdknorow.github.io/corral/
