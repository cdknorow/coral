<p align="center">
  <img alt="Coral" src="assets/icons/banner.png" width="100%" />
</p>

<p align="center">
  <strong>Coral: the control plane for the coding agents you already have.</strong>
</p>

<p align="center">
  Run Claude Code, Codex, Gemini CLI, and Pi.dev as one engineering team — on one board, in one browser tab, from any machine — with work that outlives the terminal it started in.
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

<p align="center">
  <a href="https://www.loom.com/share/f2c52e824b1f41a8a18000a2df67adf8">
    <img alt="Watch: Using Coral's Agent Teams to Parallelize Development" src="assets/icons/dashboard.webp" width="100%" />
  </a>
  <br>
  <sub><a href="https://www.loom.com/share/f2c52e824b1f41a8a18000a2df67adf8">▶ Watch: Using Coral's Agent Teams to Parallelize Development</a></sub>
</p>

## What is Coral?

Every agent vendor now ships its own way to run agents in parallel — and each one coordinates only its own agents. Claude Code's teammates are other Claude Code instances. Codex's subagents are Codex. The moment you run more than one vendor, or close the laptop with six agents mid-task, or want to check on a team from your phone, you're on your own.

Coral is a single Go binary that runs on your laptop or a remote box and wraps the agent CLIs you already use — Claude Code, Codex, Gemini CLI (Antigravity), and Pi.dev — as one coordinated team on the same codebase. You keep your keys, your subscriptions, and your tools. Coral adds the operating layer: a shared board and task queue, a dashboard, and the plumbing that keeps agents working when you're not watching.

It works by managing three things:

- **Separate sessions and worktrees.** Every agent gets its own tmux session. Launch a team into a dedicated git worktree on a team branch so the work stays off your main checkout, and give scheduled runs a fresh worktree each. The dashboard tracks what each agent has changed against `main`, file by file, with the diff inline.

<p align="center">
  <img alt="An agent's changed-files panel: 46 files changed versus main, with an inline diff of admin.go and the agent's chat alongside" src="https://github.com/user-attachments/assets/73bb118b-2a9e-493b-b41b-4dd01d904809" width="100%" />
  <br>
  <sub>The Orchestrator's changes so far — 46 files against <code>main</code> — with the diff open beside its chat. "Viewing — click to take control" hands you the keyboard.</sub>
</p>

- **A shared message board.** Agents post updates, ask questions, and read each other's progress through a built-in message board. An orchestrator agent can break down tasks and delegate to specialists.

<p align="center">
  <img alt="The message board beside an agent's chat: @mentioned posts from the Orchestrator and workers, Coral Task Queue announcements, and an unread-message nudge in the agent's chat" src="https://github.com/user-attachments/assets/ff1e8d2d-dc87-477e-80c0-42dae6e3c9d8" width="100%" />
  <br>
  <sub>Board on the right, agent on the left. Task Queue posts announce new work, workers reply with @mentions, and Coral nudges the agent — "You have 1 unread message" — the moment something lands for it.</sub>
</p>

- **A web dashboard.** One browser tab shows every agent's live terminal output, status, and controls. Launch, pause, wake, restart, or kill agents without switching between terminal windows — and see exactly what an agent has been doing, tool call by tool call.

<p align="center">
  <img alt="An agent's activity timeline: time split between Thinking and Bash, and every tool call listed with its duration" src="https://github.com/user-attachments/assets/9fd9ef24-96de-4eb4-abff-61147b2e2c41" width="100%" />
  <br>
  <sub>The activity view for one agent: how long it spent thinking versus running commands, and every tool call with its duration and timestamp.</sub>
</p>

You bring your own API keys and agents. Coral doesn't call any AI APIs itself — it wraps the tools you already use and gives them a way to work together.

## Why Coral

Five things Coral does that your agent's own tooling doesn't:

1. **Mixed-vendor teams.** A Claude orchestrator delegating to Codex workers and a Gemini reviewer, all on one board. Permissions are written once in Coral's vocabulary and translated into each CLI's native format, so a `read_only` QA agent is read-only whether it's Claude or Codex.

2. **A task queue with contracts, not a to-do list.** Board tasks declare what they're blocked by *and on what outcome* — `success`, `failure`, or `termination` — plus the artifacts they require. Completion writes the result, the artifacts, and downstream readiness in one transaction. An orchestrator can put a finished task on `review_pending` hold, freeing the worker without unblocking the next step until the work is checked.

3. **Agents that wait instead of poll.** A worker runs `coral-board wait --from Orchestrator` and ends its turn. Coral wakes it when the reply lands. No sleep loops, no tokens burned watching a channel.

4. **Teams that survive you closing the lid.** Sleep a whole team; the processes stop, the conversations don't. Wake it tomorrow — or after upgrading Coral — and every agent resumes its own session where it left off. Crashed agents are detected and parked the same way.

5. **Run it anywhere, operate it from anywhere — including your phone.** Put Coral on a build server or a spare Mac and the agents run there, in tmux, whether or not your laptop is open. The dashboard and the board come with it: open the URL from any browser, or scan a QR code and get a mobile client built for the phone — live terminals, the board, and permission prompts you can answer from the couch. Slack or Discord pings you when someone's stuck.

Everything else — cron-scheduled runs, shell-and-agent workflows with OAuth tokens injected, per-task cost, one searchable history across every vendor's transcripts, a board-health monitor that escalates idle work to the orchestrator, agents that publish their own UI panels — is in the [feature list](#features) below.

**Coral is free to use.** If it saves you time, you can support continued development by buying an optional Coral Pro license for $49.99 once, with no subscription. Supporters receive priority support and priority consideration for feature requests.

<p align="center">
  <a href="https://store.coralai.ai/checkout/buy/1cf08999-ef06-466d-938c-b0f6ec4f92e6"><strong>Support Coral development for $49.99 →</strong></a>
</p>

## Quick Start

### Download a release

Download the latest binary for free from [GitHub Releases](https://github.com/cdknorow/coral/releases):

- **macOS**: `Coral.v<version>.dmg` (universal binary), or `brew install --cask cdknorow/coral/coral`
- **Linux**: `coral-linux-amd64-<version>.tar.gz`

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

> **Requirements:** The macOS app build includes [tmux](https://github.com/tmux/tmux); older downloads, Linux, and standalone/source builds may need it installed separately. Agent CLIs must still be installed and authenticated. Coral works with Claude Code, Codex, Gemini CLI (Antigravity), and Pi.dev; each can be pointed at a custom binary with a `cli_path_<type>` setting.

### Run it on a remote machine

Coral doesn't have to run where you sit. Start it on a dev box, a home server, or a cloud VM and the agents run there, in tmux, until you stop them:

```bash
./coral --host 0.0.0.0 --port 8420
```

Then open `http://<that-machine>:8420` from any browser. Requests from anywhere other than localhost need the API key Coral generated on first run; the **Mobile QR** button in the dashboard shows it, along with a QR code that encodes the URL and key together. Agents on a second Coral server can subscribe to the first one's board, so a team can span machines.

### Use it from your phone

Scan that QR code and Coral opens as a purpose-built mobile client — installable to your home screen, laid out for a phone rather than a shrunken desktop.

<p align="center">
  <img alt="Coral mobile client: live sessions, agent chat, changed-file diffs, and token analytics" src="assets/icons/mobile-client.png" width="100%" />
</p>

From it you can:

- Watch every agent's live terminal and current goal
- Read and post on the board, and check task status
- Approve or deny an agent's permission prompt the moment it appears
- Sleep, wake, restart, or kill agents

It talks to the same server over your network, so nothing runs on the phone and there's no separate app to install.

## How It Works

### 1. Create a team

Define a team of agents, each with a role and a system prompt. For example: an Orchestrator that plans and delegates, a Lead Developer that writes code, and a QA Engineer that reviews and tests. You can create teams from the dashboard UI, use built-in templates, or describe what you need in plain English and let AI generate the team configuration.

<p align="center">
  <img alt="The Launch New Session dialog with three choices: AI Agent, Agent Team, and Terminal" src="https://github.com/user-attachments/assets/137ef916-f1e6-4a65-bcf8-ac30b6d0105d" width="100%" />
  <br>
  <sub><strong>+ New</strong> offers three things to launch: a single agent, a whole team on a shared board, or a plain terminal that lives alongside your agents.</sub>
</p>

### 2. Agents work in their own sessions

Coral starts every agent in a separate tmux session. Launch a team with the worktree option and Coral checks the team out into `~/.coral/worktrees/<team>` on a `coral-team/<team>` branch, so the agents share one branch and your main checkout stays clean. Scheduled and one-shot jobs get a worktree per run. If you want each agent on a separate branch, set the team's working mode to `worktrees` and the agents are instructed to create their own.

### 3. Agents communicate via the message board

Every team has a shared message board. Agents post status updates, ask for help, and coordinate handoffs. The orchestrator agent can assign tasks and track progress. Messages are delivered reliably with cursor-based tracking — nothing is lost across agent restarts.

### 4. You monitor and steer from the dashboard

The web dashboard shows every agent's live terminal, current status, and message board activity. You can:
- Send messages or commands to any agent
- Sleep an agent (preserving full state) and wake it later
- Add new agents to a running team
- Answer an agent's permission prompt from the dashboard — including from your phone
- Define teams in JSON, import a skill folder as a team, or generate one from a plain-English description
- See what it all cost — by model, team, branch, and individual agent

<p align="center">
  <img alt="The Analytics view: cumulative spend chart, then cost tables by model, by team, by branch, and by agent, with a per-agent cumulative-cost popover" src="https://github.com/user-attachments/assets/3cd47d1f-273a-48e3-b7cd-14bb7ad2469d" width="100%" />
  <br>
  <sub>Analytics for a nine-agent team: spend over time, then the same number cut by model, team, git branch, and agent. Hover an agent for its cumulative cost across every turn it has taken.</sub>
</p>

## Features

| Feature | Description |
|---|---|
| **Mixed-vendor teams** | Put Claude Code, Codex, Gemini CLI, and Pi.dev agents on one team. Permissions are written once (`file_read`, `shell:<pattern>`, `git_write`, …) and translated to each CLI's native format |
| **Real-time dashboard** | Web UI (and installable PWA) showing every live terminal, agent status, pending permission prompts, and controls |
| **Remote operation** | Run Coral on any machine and operate it from any browser over the network. Non-local access is gated by an API key. Boards can be subscribed to across Coral servers |
| **Mobile client** | A phone-first client, installable to the home screen and paired with a QR code: live terminals, board, tasks, and permission prompts, all from the same server |
| **Message board** | Inter-agent communication with cursor-based delivery, @mentions, and `peek` so an orchestrator can read a worker's terminal |
| **Task queue with contracts** | Tasks with `success` / `failure` / `termination` dependency conditions, required artifacts, atomic completion, explicit retries, and a `review_pending` hold so QA can gate a result without blocking the worker |
| **Wait, don't poll** | Workers run `coral-board wait --from <agent>` and end their turn; Coral wakes them when the message or task lands |
| **Sleep & wake** | Suspend an agent or a whole team, then resume with the vendor's own session restored — works after restarting Coral |
| **Team definitions** | Teams are JSON (`~/.coral/teams/`). Import a skill folder as a team, or generate one from a plain-English description |
| **Workflows** | Chain shell and agent steps, pass outputs between them, and inject OAuth tokens from connected apps (GitHub, Slack, Gmail, Calendar) |
| **Scheduled jobs** | Run a prompt or workflow on a cron schedule, each run in its own git worktree branched from a base branch |
| **Board health monitor** | Flags blocked tasks whose prerequisites are done, load imbalance, and idle assignees; reminds after 30 min and escalates to the orchestrator after 60 |
| **Cost tracking** | Token and cost figures per agent, session, team, branch, and task from hooks and transcripts, plus an optional local LLM proxy (Anthropic, OpenAI, Bedrock) — including Claude subagent spend |
| **Session history** | One searchable history across every agent CLI's transcripts, with auto-summaries, tags, notes, and per-session diffs captured on kill |
| **Agent-published UI** | Any agent can publish a sandboxed HTML panel to the sidebar and read back user clicks (`coral-agent ui publish`) |
| **Git integration** | Tracks commits, branches, changed files, and open PR numbers per agent session |
| **Webhooks** | Slack, Discord, or generic HTTP alerts when an agent needs input or goes idle, with retries and a circuit breaker |

## Comparison

Coral is not an agent and doesn't replace one. Claude Code, Claude Desktop, the Codex app, and Cursor each run their own agents in parallel now, with worktree isolation and subagents or agent teams built in. If you only ever use one of them, that vendor's own tooling is the first thing to try.

Coral is the layer *around* those tools. Every vendor's orchestration coordinates only its own processes — Claude Code's teammates are other Claude Code instances, Codex's subagents are Codex. Coral's job is what happens when you run more than one, or want to operate them from somewhere other than the terminal they started in.

| | Coral | Claude Code / Claude Desktop | Codex app / CLI | Cursor |
|---|:---:|:---:|:---:|:---:|
| Agents on one team | Claude, Codex, Gemini, Pi | Claude | Codex | Cursor |
| Run agents in parallel | ✓ | ✓ (Agent Teams, `--worktree`) | ✓ (threads, subagents) | ✓ (Agents window) |
| Worktree isolation | Per team (opt-in), per scheduled run | Per session | Per thread | Per agent |
| Inter-agent coordination | Shared board + task queue with dependency conditions, artifacts, and review hold | Shared task list + mailbox | Subagents | Subagents |
| Sleep & wake a whole team, resume after restarting the tool | ✓ | Resume a session | Resume a thread | Resume |
| Dashboard reachable from a browser or phone | ✓ | Desktop app | Desktop app | IDE |
| Cost by agent, team, branch, and task | ✓ | Per session | Per thread | Per request |
| Open source | ✓ Apache 2.0 | — | CLI only | — |

What is specific to Coral is the combination: multiple vendors on one board, a task engine whose dependencies are conditioned on outcomes and artifacts rather than just "done", a review-hold state for an orchestrator to check work before it unblocks the next step, teams you can sleep and wake after restarting Coral, and a mobile client that answers permission prompts from your phone. Around that sit cron-scheduled runs, shell-and-agent workflows, Slack and Discord alerts when an agent is stuck, and a board-health monitor — all running on the same server as the agents, so they work whether or not you're at the keyboard.

Where the single-vendor tools are stronger: per-session worktree isolation is on by default in Claude Code and Codex, while Coral's team worktree is opt-in and shared by the team. Coral also doesn't merge branches or resolve conflicts for you; output is a branch, a diff, or a PR the agent opens.

Other open-source projects in this space — Claude Squad, Conductor, Crystal, and vibe-kanban — take a similar tmux-and-worktrees approach. Frameworks like AutoGen and CrewAI are a different job: they help you build agents in code, whereas Coral runs the CLIs you already have.

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
