<p align="center">
  <strong>Coral: Multi-agent orchestration for AI coding tools.</strong>
</p>

<p align="center">
  Run Claude Code, Codex, Gemini CLI, and Pi.dev as a coordinated engineering team—without losing visibility or control.
</p>

<p align="center">
  <a href="https://github.com/cdknorow/coral/stargazers"><img src="https://img.shields.io/github/stars/cdknorow/coral?style=social" alt="GitHub Stars"></a>
  <a href="https://github.com/cdknorow/coral/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-green" alt="Apache 2.0 License"></a>
  <a href="https://cdknorow.github.io/coral/"><img src="https://img.shields.io/badge/docs-live-blue" alt="Documentation"></a>
  <a href="https://store.coralai.ai/checkout/buy/1cf08999-ef06-466d-938c-b0f6ec4f92e6"><img src="https://img.shields.io/badge/support_Coral-$49.99_one--time-FF7D52" alt="Support Coral development for $49.99"></a>
  <a href="https://discord.gg/qhfgY57AZn"><img src="https://img.shields.io/discord/placeholder?label=Discord&color=5865F2" alt="Discord"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> &bull;
  <a href="https://store.coralai.ai/checkout/buy/1cf08999-ef06-466d-938c-b0f6ec4f92e6">Support Coral</a> &bull;
  <a href="https://cdknorow.github.io/coral/">Documentation</a> &bull;
  <a href="#features">Features</a> &bull;
  <a href="#how-it-works">How It Works</a> &bull;
  <a href="https://discord.gg/qhfgY57AZn">Discord</a>
</p>

---

<!-- TODO: Replace with hosted mp4 once uploaded to GitHub -->
<p align="center">
  <a href="https://www.loom.com/share/7dce83519c8d4882af5a15bb9d727c21">
    <img src="https://cdn.loom.com/sessions/thumbnails/7dce83519c8d4882af5a15bb9d727c21-with-play.gif" alt="Watch Coral in action" width="720" />
  </a>
</p>

## What is Coral?

Coral is a local server that lets you run multiple AI coding agents — Claude Code, Gemini CLI, Codex, Pi.dev, or any CLI-based agent — as a coordinated team on the same codebase.

It works by managing three things:

- **Isolated workspaces.** Each agent runs in its own tmux session with its own git worktree, so agents can write code in parallel without merge conflicts.
- **A shared message board.** Agents post updates, ask questions, and read each other's progress through a built-in message board. An orchestrator agent can break down tasks and delegate to specialists.
- **A web dashboard.** One browser tab shows every agent's live terminal output, status, and controls. Launch, pause, wake, restart, or kill agents without switching between terminal windows.

You bring your own API keys and agents. Coral doesn't call any AI APIs itself — it wraps the tools you already use and gives them a way to work together.

**Coral is free to use.** If it saves you time, you can support continued development by buying an optional Coral Pro license for $49.99 once, with no subscription. Supporters receive priority support and priority consideration for feature requests.

<p align="center">
  <a href="https://store.coralai.ai/checkout/buy/1cf08999-ef06-466d-938c-b0f6ec4f92e6"><strong>Support Coral development for $49.99 →</strong></a>
</p>

![Coral Dashboard](https://github.com/user-attachments/assets/6af60c92-1d72-45bd-9b46-7f1eab2ce5fe)

## Quick Start

### Download a release

Download the latest binary for free from [GitHub Releases](https://github.com/cdknorow/coral/releases):

- **macOS**: `Coral.dmg` (universal binary)
- **Linux**: `coral-linux-amd64.tar.gz`

No purchase is required. If you want to support development, [buy an optional Coral Pro supporter license](https://store.coralai.ai/checkout/buy/1cf08999-ef06-466d-938c-b0f6ec4f92e6).

### Build from source

```bash
cd coral-go
make build
```

### Run

```bash
./coral
```

Open **http://localhost:8420** in your browser. Click **+New** to launch your first agent or create a team.

> **Requirements:** [tmux](https://github.com/tmux/tmux). Coral works with Claude Code, Codex, Gemini CLI, and Pi.dev out of the box. Any CLI-based agent can be added.

## How It Works

### 1. Create a team

Define a team of agents, each with a role and a system prompt. For example: an Orchestrator that plans and delegates, a Lead Developer that writes code, and a QA Engineer that reviews and tests. You can create teams from the dashboard UI, use built-in templates, or describe what you need in plain English and let AI generate the team configuration.

### 2. Agents work in isolated worktrees

When you launch a team, Coral creates a git worktree for each agent and starts them in separate tmux sessions. Each agent has its own copy of the repo and can read, write, and run commands without interfering with others.

### 3. Agents communicate via the message board

Every team has a shared message board. Agents post status updates, ask for help, and coordinate handoffs. The orchestrator agent can assign tasks and track progress. Messages are delivered reliably with cursor-based tracking — nothing is lost across agent restarts.

### 4. You monitor and steer from the dashboard

The web dashboard shows every agent's live terminal, current status, and message board activity. You can:
- Send messages or commands to any agent
- Sleep an agent (preserving full state) and wake it later
- Add new agents to a running team
- Save team configurations as reusable templates

## Features

| Feature | Description |
|---|---|
| **Multi-agent orchestration** | Run multiple agents in parallel, each in its own git worktree and tmux session |
| **Any CLI agent** | Works with Claude Code, Gemini CLI, Codex, Pi.dev, and any CLI-based tool |
| **Real-time dashboard** | Web UI showing live terminal output, agent status, and controls |
| **Message board** | Inter-agent communication with cursor-based delivery and @mentions |
| **Sleep & wake** | Suspend agents and resume with full state — prompts, messages, session context |
| **Team templates** | Save and share team configurations; generate teams from plain-English descriptions |
| **Workflows** | Define multi-step agent pipelines that run automatically — chain tasks across agents with dependencies |
| **Scheduled jobs** | Launch agents or workflows on a cron schedule in isolated worktrees |
| **Task management** | Create, assign, and track tasks on the message board; agents mark tasks complete as they finish |
| **Token tracking** | Monitor token usage per agent and per session — see cost and consumption in real time |
| **Session history** | Full-text search across all past sessions with auto-summaries, tags, and notes |
| **Git integration** | Tracks commits, branches, and changed files per agent session |
| **Webhooks** | Notifications to Slack, Discord, or any HTTP endpoint |

## Comparison

Frameworks like AutoGen and CrewAI help developers build agent pipelines in code. Coral is different — it's an operational tool that coordinates existing, independent agent processes and gives you one place to see and control everything.

| | Coral | Claude Code | Cursor | AutoGen | CrewAI |
|---|:---:|:---:|:---:|:---:|:---:|
| Multiple agents in parallel | ✓ | Limited | ✓ | ✓ | ✓ |
| Isolated execution environments | ✓ | ✓ | ✓ | — | — |
| Works with any CLI agent | ✓ | — | — | — | — |
| Real-time monitoring dashboard | ✓ | — | IDE only | — | — |
| Inter-agent message board | ✓ | Basic | — | API-level | API-level |
| Sleep & wake with full state | ✓ | — | — | — | — |
| Search chat history | ✓ | — | — | — | — |
| Dynamic team composition | ✓ | Limited | — | ✓ | ✓ |
| Process-level isolation | ✓ | ✓ | ✓ | — | — |
| Open source | ✓ | — | — | ✓ | ✓ |

## Documentation

Full documentation at **[cdknorow.github.io/coral](https://cdknorow.github.io/coral/)**.

## Contributing

We welcome contributions! Whether it's adding support for new AI agents, improving the dashboard, or fixing bugs — please open an issue or submit a pull request.

## License

Apache 2.0 License. See [LICENSE](LICENSE) for details.

---

<p align="center">
  <a href="https://store.coralai.ai/checkout/buy/1cf08999-ef06-466d-938c-b0f6ec4f92e6">Support Coral</a> &bull;
  <a href="https://github.com/cdknorow/coral">Star the repo</a> &bull;
  <a href="https://discord.gg/qhfgY57AZn">Join Discord</a> &bull;
  <a href="https://cdknorow.github.io/coral/">Read the docs</a>
</p>
