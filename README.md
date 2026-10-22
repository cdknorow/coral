<p align="center">
  <strong>Coral: the control plane for the coding agents you already have.</strong>
</p>

<h2 align="center">
  <a href="https://coralhub.dev">Coral Hub: coralhub.dev</a>
</h2>

<p align="center">
  Reach your Coral servers from any browser, your phone included. Invite-only beta.
</p>

<p align="center">
  Run Claude Code, Codex, Antigravity CLI, and Pi.dev as one engineering team — on one board, in one browser tab, from any machine — with work that outlives the terminal it started in.
</p>

<p align="center">
  <a href="https://github.com/subgentic/coral/releases/latest"><strong>Download the latest release</strong></a><br>
  <a href="https://github.com/subgentic/coral/releases/latest/download/Coral.dmg">macOS (.dmg)</a> &bull;
  <a href="https://github.com/subgentic/coral/releases/latest/download/Coral-x64.msi">Windows installer (.msi)</a> &bull;
  <a href="https://github.com/subgentic/coral/releases/latest/download/Coral-x64-portable.zip">Windows portable (.zip)</a> &bull;
  <a href="https://github.com/subgentic/coral/releases/latest/download/coral-linux-amd64.tar.gz">Linux x86-64</a> &bull;
  <a href="https://github.com/subgentic/coral/releases/latest/download/coral-linux-arm64.tar.gz">Linux ARM64</a><br>
  <sub>or with Homebrew: <code>brew install --cask cdknorow/coral/coral</code></sub>
</p>

<p align="center">
  <a href="https://github.com/subgentic/coral"><img src="https://img.shields.io/github/stars/subgentic/coral?style=social" alt="GitHub Stars"></a>
  <a href="https://github.com/subgentic/coral/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-green" alt="Apache 2.0 License"></a>
  <a href="https://subgentic.github.io/coral/"><img src="https://img.shields.io/badge/docs-live-blue" alt="Documentation"></a>
  <a href="https://discord.gg/qhfgY57AZn"><img src="https://img.shields.io/badge/Discord-join-5865F2?logo=discord&logoColor=white" alt="Discord"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> &bull;
  <a href="https://subgentic.github.io/coral/">Documentation</a> &bull;
  <a href="#features">Features</a> &bull;
  <a href="#how-it-works">How It Works</a> &bull;
  <a href="https://discord.gg/qhfgY57AZn">Discord</a>
</p>

---

<p align="center">
  <img alt="The Coral dashboard: a team of agents on the left, an Orchestrator's chat in the middle, and the team's message board on the right" src="assets/readme/dashboard.webp" width="100%" />
  <br>
  <sub>The dashboard with a team of agents at work: the agent list, one agent's chat, and the team's message board. (Sample data.)</sub>
</p>

## What is Coral?

Every agent vendor now ships its own way to run agents in parallel — and each one coordinates only its own agents. Claude Code's teammates are other Claude Code instances. Codex's subagents are Codex. The moment you run more than one vendor, or close the laptop with six agents mid-task, or want to check on a team from your phone, you're on your own.

Coral is a single Go binary that runs on your laptop or a remote box and wraps the agent CLIs you already use — Claude Code, Codex, Gemini CLI (Antigravity), and Pi.dev — as one coordinated team on the same codebase. You keep your keys, your subscriptions, and your tools. Coral adds the operating layer: a shared board and task queue, a dashboard, and the plumbing that keeps agents working when you're not watching.

It works by managing three things:

- **Separate sessions and worktrees.** Every agent gets its own tmux session. Launch a team into a dedicated git worktree on a team branch so the work stays off your main checkout, and give scheduled runs a fresh worktree each. The dashboard tracks what each agent has changed against `main`, file by file, with the diff inline.

<p align="center">
  <img alt="An agent's changed-files panel: four files changed versus the previous commit, with an inline diff of cooldown.go and the agent's chat alongside" src="assets/readme/changed-files.png" width="100%" />
  <br>
  <sub>An agent's changes so far — here four files against the previous commit — with the diff open beside its chat. (Sample data.)</sub>
</p>

- **A shared message board.** Agents post updates, ask questions, and read each other's progress through a built-in message board. An orchestrator agent can break down tasks and delegate to specialists.

<p align="center">
  <img alt="The message board beside an agent's chat: posts from the Orchestrator and workers, and the Pause Board and Export buttons" src="assets/readme/board-and-chat.png" width="100%" />
  <br>
  <sub>Board on the right, agent on the left. Task Queue posts announce new work, workers reply with @mentions, and Coral nudges an agent the moment something lands for it. (Sample data.)</sub>
</p>

- **A web dashboard.** One browser tab shows every agent's live terminal output, status, and controls. Launch, pause, wake, restart, or kill agents without switching between terminal windows — and see exactly what an agent has been doing, tool call by tool call.

<p align="center">
  <img alt="An agent's activity timeline: time split between Bash and Write, and every tool call listed with its duration" src="assets/readme/activity-timeline.png" width="100%" />
  <br>
  <sub>The activity view for one agent: how long it spent on each kind of work, and every tool call with its duration and timestamp. (Sample data.)</sub>
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

**Coral is open source (Apache-2.0).**

## Quick Start

### Download a release

Download the latest release from [GitHub Releases](https://github.com/subgentic/coral/releases/latest). These links always point to the newest version:

- **macOS**: [`Coral.dmg`](https://github.com/subgentic/coral/releases/latest/download/Coral.dmg) (universal binary), or `brew install --cask cdknorow/coral/coral`
- **Linux**: [`coral-linux-amd64.tar.gz`](https://github.com/subgentic/coral/releases/latest/download/coral-linux-amd64.tar.gz) (x86-64) or [`coral-linux-arm64.tar.gz`](https://github.com/subgentic/coral/releases/latest/download/coral-linux-arm64.tar.gz) (ARM64, e.g. Graviton or Docker on Apple silicon)
- **Windows**: [`Coral-x64.msi`](https://github.com/subgentic/coral/releases/latest/download/Coral-x64.msi) (installer) or [`Coral-x64-portable.zip`](https://github.com/subgentic/coral/releases/latest/download/Coral-x64-portable.zip). These are currently **not code-signed**, so Windows SmartScreen warns that the publisher is unknown. Verify the file with the attestation check below before you run it.

Each release also keeps its versioned file names (`Coral.v<version>.dmg`, `coral-linux-<arch>-<version>.tar.gz`, `Coral-<version>-x64.msi`, `Coral-<version>-x64-portable.zip`): the same files.

#### Verify your download

Every release is built and signed by Coral's release workflow on GitHub, with no long-lived keys (Sigstore keyless signing). You can check that a download came from that workflow, from a release tag of `subgentic/coral`. Releases up to v1.4.141 were built before the repository moved, in `cdknorow/coral`: for one of them use `--owner cdknorow` and `cdknorow/coral/.github/workflows/release.yml` in the commands below (GitHub keeps a release's attestation with the owner that built it, so `--repo` finds nothing for them).

With the [GitHub CLI](https://cli.github.com/) (any package):

```bash
gh attestation verify coral-linux-amd64.tar.gz --owner subgentic \
  --signer-workflow subgentic/coral/.github/workflows/release.yml
```

Offline, with [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) v2.4 or later: download `SHA256SUMS.txt` and `SHA256SUMS.txt.sigstore.json` from the same release, then:

```bash
cosign verify-blob SHA256SUMS.txt --bundle SHA256SUMS.txt.sigstore.json --new-bundle-format \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/subgentic/coral/\.github/workflows/release\.yml@refs/tags/v'
sha256sum --check --ignore-missing SHA256SUMS.txt   # macOS: shasum -a 256 --check --ignore-missing
```

The checksum command must print `<your file>: OK` for the file you downloaded; if it prints nothing, nothing was checked.

On Windows, check the MSI or the portable ZIP you downloaded the same way (PowerShell, with the GitHub CLI); it must report the attestation as verified for `release.yml` on a tag of `subgentic/coral`:

```powershell
gh attestation verify Coral-x64.msi --owner subgentic `
  --signer-workflow subgentic/coral/.github/workflows/release.yml
```

This proves the file came from Coral's release workflow. It is not Windows code signing: the MSI and the programs in it carry no Authenticode signature yet. The `SHA256SUMS.txt` check above covers the Windows files too.

Each Linux tarball also has its own bundle (`coral-linux-<amd64|arm64>.tar.gz.sigstore.json`, and the same for the versioned name), verified the same way. The bundles are in Sigstore's current bundle format: cosign 2.x reads them only with `--new-bundle-format` (without it, it fails with "bundle does not contain cert for verification"); cosign 3 reads them either way. Either check failing means the file is not an official build: don't run it.

### Install and start

First install and sign in to at least one agent CLI: [Claude Code](https://code.claude.com/docs/en/overview), [Codex](https://github.com/openai/codex), [Antigravity (`agy`)](https://antigravity.google), or [Pi](https://pi.dev). Coral runs these tools; it does not install or authenticate them.

- **macOS:** Open the DMG, drag `Coral.app` to Applications, then open Coral. The app includes tmux. For terminal commands, run `/Applications/Coral.app/Contents/MacOS/install-cli.sh` and add `~/.local/bin` to your `PATH`.
- **Windows:** Run the MSI, then open **Coral** from the Start menu. For the portable ZIP, extract it, open PowerShell in that folder, and run `.\coral.exe --no-browser`.
- **Linux:** Download the archive for your architecture, then extract and start it (ARM64: replace `amd64` with `arm64`):

  ```bash
  mkdir -p "$HOME/.local/share/coral"
  tar -xzf coral-linux-amd64.tar.gz -C "$HOME/.local/share/coral"
  export PATH="$HOME/.local/share/coral:$PATH"
  coral --backend pty --no-browser
  ```

  Add that `PATH` line to your shell's startup file for later terminals. To use the default tmux backend instead, install tmux and omit `--backend pty`.

On first launch, review the Terms of Service and accept them if you agree. For a terminal launch, keep the terminal open while Coral runs. Open the local address printed at startup. If the browser shows **Create a password**, enter and confirm a password, then choose **Create password and open Coral**. The app can open a signed-in browser directly; you can also run `coral open` from a terminal on the server.

By default, open **http://localhost:8420** on macOS/Linux or **http://localhost:8450** on Windows. On Windows, replace `8420` with `8450` in these examples, or pass `--port` to choose another port. The app and tray open the correct address themselves.

In the dashboard, choose **Launch agent**, enter an existing project folder on the computer running Coral (or use **Browse**), and choose **Launch**. To choose an agent CLI, open **More options > Agent** first. In the agent's **Terminal**, answer any folder-trust or sign-in prompt, then send a task from **Chat** with Enter. Use **Launch team** on Home to start several agents together.

See [Getting started](https://subgentic.github.io/coral/getting-started/) for the full installation and first-agent walkthrough.

### Build from source

Install Git, Make, Go 1.26.10 or later, and a C compiler (on macOS, Xcode Command Line Tools), then:

```bash
git clone https://github.com/subgentic/coral.git
cd coral/coral-go
make build
./bin/coral --backend pty --no-browser
```

The build puts the tools in `bin/`. For the commands below, add that directory to your `PATH` or use `./bin/coral` in place of `coral`.

### Run it on a remote machine

Coral doesn't have to run where you sit. Start it on a dev box, a home server, or a cloud VM and the agents run there in their own terminal sessions until you stop them:

```bash
coral --remote --host 0.0.0.0 --port 8420
```

Then open `http://<that-machine>:8420` from any browser. Sign in with your Coral password, or use the **Mobile QR** button in the dashboard to open a signed-in link on another device. See [Remote access](https://subgentic.github.io/coral/remote-access/) for setup. Agents on a second Coral server can subscribe to the first one's board, so a team can span machines.

### Use it from your phone

Scan that QR code and Coral opens as a purpose-built mobile client — installable to your home screen, laid out for a phone rather than a shrunken desktop.

<p align="center">
  <img alt="The dashboard on a phone: the live sessions list, and an agent's chat" src="assets/readme/dashboard-on-a-phone.png" width="100%" />
  <br>
  <sub>The same dashboard on a phone: the agent list, and one agent's chat. (Sample data.)</sub>
</p>

From it you can:

- Watch every agent's live terminal and current goal
- Read and post on the board, and check task status
- Approve or deny an agent's permission prompt the moment it appears
- Sleep, wake, restart, or kill agents

It talks to the same server over your network, so nothing runs on the phone and there's no separate app to install.

### Coral Hub (beta)

Coral Hub (app.coralhub.dev, invite-only beta) lets you reach your Coral servers from any browser without opening a port. A fresh install never connects, or asks you to, on its own: connect a server from the home screen or **Settings › Coral Hub**, or run `coral cloud connect` on it. Coral offers it at first launch (the dashboard's Connect screen and a prompt in the terminal) only when you pass a hub URL (`--cloud-url https://app.coralhub.dev` or `CORAL_CLOUD_URL`) or start it with `--cloud-connect prompt` (`CORAL_CLOUD_CONNECT=prompt`); `--cloud-connect off` turns that off. See [Coral Hub](coral-go/agent_docs/hub.md#connecting-a-server-to-coral-hub).

## How It Works

### 1. Create a team

Define a team of agents, each with a role and a system prompt. For example: an Orchestrator that plans and delegates, a Lead Developer that writes code, and a QA Engineer that reviews and tests. You can create teams from the dashboard UI, use built-in templates, or describe what you need in plain English and let AI generate the team configuration.

<p align="center">
  <img alt="The Launch New Session dialog with four choices: AI Agent, Agent Team, Terminal, and Open Folder" src="assets/readme/launch-new-session.png" width="100%" />
  <br>
  <sub><strong>+ New</strong> offers four things to launch: a single agent, a whole team on a shared board, a plain terminal that lives alongside your agents, or a folder pinned to your Agents list.</sub>
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
  <img alt="The Analytics view: token and cost totals, a cumulative spend chart, then cost tables by provider and by model" src="assets/readme/analytics.png" width="100%" />
  <br>
  <sub>Analytics: token and cost totals, spend over time, then the same cost cut by provider and model, and further down by thinking level, team, git branch, and agent. (Sample data.)</sub>
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

Full documentation at **[subgentic.github.io/coral](https://subgentic.github.io/coral/)**.

## Contributing

We welcome contributions! Whether it's adding support for new AI agents, improving the dashboard, or fixing bugs — please open an issue or submit a pull request.

Dashboard changes should follow the [Coral UI style guide](coral-go/docs/ui-style-guide.md).

## License

Apache 2.0 License. See [LICENSE](LICENSE) for details.

---

<p align="center">
  <a href="https://github.com/subgentic/coral">Star the repo</a> &bull;
  <a href="https://discord.gg/qhfgY57AZn">Join Discord</a> &bull;
  <a href="https://subgentic.github.io/coral/">Read the docs</a>
</p>
