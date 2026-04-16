# Enterprise version of Coral is available at [Coral AI](https://coralai.ai)
<p align="center">
  <img width="1299" height="336" alt="Coral" src="https://github.com/user-attachments/assets/4fb16dff-fa46-4189-837f-cc88b610849b" />
</p>

<p align="center">
  <strong>Orchestrate multiple AI agents working in parallel — with full visibility and control.</strong>
</p>

<p align="center">
  <a href="https://github.com/cdknorow/coral/stargazers"><img src="https://img.shields.io/github/stars/cdknorow/coral?style=social" alt="GitHub Stars"></a>
  <a href="https://pypi.org/project/agent-coral/"><img src="https://img.shields.io/pypi/v/agent-coral?color=blue" alt="PyPI Version"></a>
  <a href="https://github.com/cdknorow/coral/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-green" alt="Apache 2.0 License"></a>
  <a href="https://cdknorow.github.io/coral/"><img src="https://img.shields.io/badge/docs-live-blue" alt="Documentation"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> &bull;
  <a href="https://cdknorow.github.io/coral/">Documentation</a> &bull;
  <a href="#features">Features</a> &bull;
  <a href="#agent-teams">Agent Teams</a> &bull;
  <a href="https://github.com/cdknorow/coral/discussions">Community</a> &bull;
  <a href="DEVELOP.md">Developer Guide</a>
</p>

---

<!-- TODO: Replace with animated GIF/video showing: launch 3 agents → agents collaborating via message board → real-time dashboard -->
![Coral Dashboard](https://github.com/user-attachments/assets/6af60c92-1d72-45bd-9b46-7f1eab2ce5fe)

## Why Coral?

Running one AI agent is easy. Running five in parallel on the same project — tracking what each one is doing, keeping them coordinated, and not losing context when things restart — is chaos without the right tool.

**Coral is your mission control for AI agents.** It orchestrates multiple agents across isolated environments, gives you a real-time dashboard to monitor everything at a glance, and lets your agents communicate with each other through a built-in message board.

### Three reasons to use Coral

1. **Run agents in parallel, not in sequence.** Launch a team of AI agents — each in its own isolated git worktree — so they can work on different parts of your project simultaneously without stepping on each other.

2. **See everything, miss nothing.** The real-time dashboard shows every agent's status, current task, and confidence level. Sleep agents, wake them up, restart them, or send commands — all from one screen.

3. **Agents that talk to each other.** Built-in message board with cursor-based delivery lets agents coordinate work, share progress, and avoid duplicating effort. No messages lost, no duplicates, even across restarts.

## Quick Start

```bash
# Install from PyPI
pip install agent-coral

# Launch the dashboard
coral
```

Open **http://localhost:8420** in your browser. Click **+New** to launch your first agent or team.

> **Requirements:** Python 3.8+, tmux. Works with Claude Code and Gemini CLI out of the box. Extensible to any CLI-based agent.

## Features

### Real-Time Dashboard
Monitor all your agents from a single web interface with live status updates, activity timelines, and one-click controls.

- **Live agent status** via the PULSE protocol — see what each agent is doing right now
- **Command input** — send prompts, navigate modes, trigger macros from the dashboard
- **Attach/Restart/Kill** — full lifecycle control for every agent session
- **Themes & customization** — built-in themes, custom macros, AI-generated themes

### Agent Teams
Launch coordinated teams of agents with roles, behavior prompts, and a shared message board. [Learn more below.](#agent-teams)

### Session History & Search
Browse your complete AI session history with full-text search, tags, notes, and auto-generated summaries.

- **Full-text search** across all sessions (SQLite FTS5 with porter stemming)
- **Auto-summarization** — sessions are summarized automatically using AI
- **Markdown notes & tags** — annotate any session for future reference
- **Git integration** — tracks commits, branches, and changed files per session

### Scheduled Jobs & Webhooks
Automate recurring tasks and get notified when agents need attention.

- **Cron-scheduled jobs** — launch agents on a schedule in isolated worktrees
- **Webhook notifications** — Slack, Discord, or any HTTP endpoint
- **Idle detection** — automatic alerts when agents are stuck waiting for input

## Agent Teams

Agent teams are Coral's most powerful feature. Launch a group of specialized agents that communicate through a shared message board:

```bash
# Or use the +New → Agent Team modal in the dashboard
```

Each agent gets:
- An **isolated git worktree** (no merge conflicts)
- A **role and behavior prompt** (e.g., "You are the QA Engineer...")
- **Automatic message board subscription** with cursor-based delivery
- **Sleep/wake persistence** — the agent's state, prompt, and message position are preserved

Teams can be managed collectively (Sleep All, Wake All, Kill All) or individually from the dashboard sidebar.

**[Learn more about agent teams in the documentation.](https://cdknorow.github.io/coral/multi-agent-orchestration/)**

## Agent Protocol (PULSE)

Agents automatically report what they're doing using inline markers that Coral parses from terminal output in real time:

```
||PULSE:STATUS Implementing auth middleware||
||PULSE:SUMMARY Refactoring the database layer||
||PULSE:CONFIDENCE Low Unfamiliar with this library||
```

The protocol is automatically injected when launching agents. Any CLI-based agent can participate — just print the markers to stdout. See [`PROTOCOL.md`](src/coral/PROTOCOL.md) for the full specification.

## Advanced Usage

<details>
<summary><strong>Bulk agent launcher</strong></summary>

```bash
# Launch agents for every worktree subdirectory + start the dashboard
launch-coral

# Specify path and agent type
launch-coral <path-to-root> gemini
```

</details>

<details>
<summary><strong>Remote server (SSH port forwarding)</strong></summary>

```bash
ssh -L 8420:localhost:8420 user@remote-host
```

Then open `http://localhost:8420` locally. Add to `~/.ssh/config` for persistence.

</details>

<details>
<summary><strong>Manual tmux management</strong></summary>

```bash
tmux attach -t claude-<session-uuid>   # Attach to an agent
Ctrl+b n / Ctrl+b p                    # Switch windows
Ctrl+b d                               # Detach
```

</details>

## Documentation

Full documentation is available at **[cdknorow.github.io/coral](https://cdknorow.github.io/coral/)**.

For project structure, API endpoints, and database schema, see [DEVELOP.md](DEVELOP.md).

## Contributing

We welcome contributions! Whether it's adding support for new AI agents, improving the dashboard, or fixing bugs — please open an issue or submit a pull request.

## License

Apache 2.0 License. See [LICENSE](LICENSE) for details.

---

<p align="center">
  <strong>Built for developers who work with AI, not against it.</strong><br>
  <a href="https://github.com/cdknorow/coral">Star the repo</a> if Coral helps your workflow.
</p>
