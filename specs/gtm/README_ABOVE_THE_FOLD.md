# README above-the-fold — proposed rewrite

**Owner:** Content & Launch Producer · **Status: DRAFT, operator approves**
**Sources:** `POSITIONING_BRIEF.md` §3 and §7 (Strategist), the verified-strengths table in
`LAUNCH_CHECKLIST.md`, `INSTALL_VERIFICATION.md` (Dev Advocate #22).
**Every claim below appears in the verified-strengths table.** Nothing new is asserted here.

---

## ⚠️ Read this before applying

This supersedes **four** of the five standalone patches for the region it covers — docs
links (partially), Discord badge, hero image, and the `:37` any-CLI-agent line. Applying
both this and those patches will conflict.

**Apply order for the patch set (verified by stack-testing, not individually):**
`README-P0-worktree-isolation-claim` → `README-remove-any-cli-agent-claim` → `docs-links` →
`discord-badge-fallback` → `hero-A` **or** `hero-B`. The first two edit adjacent rows of the
features table; applied in the other order the second conflicts.

**Two paths, operator picks one:**

- **Path A — ship the small patches now.** Each is independently approvable and fixes
  something factually wrong today. Nothing depends on positioning. Then apply this rewrite
  later as a second pass.
- **Path B — ship this rewrite instead.** One review, one commit, everything corrected at
  once, but it carries positioning judgement that Path A does not.

I recommend **Path A**, because the README is wrong *right now* and the small patches need
no positioning sign-off. This rewrite should not hold up a broken download path.

The `README-remove-any-cli-agent-claim.patch` also touches `:81` and `:110`, which are
**below** the fold. Those hunks are still needed under either path.

---

## Proposed replacement — current lines 1–82

```markdown
<p align="center">
  <strong>Coral: run Claude Code, Codex, Gemini CLI, and Pi.dev as one team.</strong>
</p>

<p align="center">
  A local control plane for the coding agents you already pay for — Claude Code, Codex and
  Gemini CLI on one team, every agent's live terminal in one browser tab.
</p>

<p align="center">
  <a href="https://github.com/cdknorow/coral/stargazers"><img src="https://img.shields.io/github/stars/cdknorow/coral?style=social" alt="GitHub Stars"></a>
  <a href="https://github.com/cdknorow/coral/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-green" alt="Apache 2.0 License"></a>
  <a href="https://github.com/cdknorow/coral/tree/main/coral-go/agent_docs"><img src="https://img.shields.io/badge/docs-in_repo-blue" alt="Documentation"></a>
  <a href="https://store.coralai.ai/checkout/buy/1cf08999-ef06-466d-938c-b0f6ec4f92e6"><img src="https://img.shields.io/badge/support_Coral-$49.99_one--time-FF7D52" alt="Support Coral development for $49.99"></a>
  <a href="https://discord.gg/qhfgY57AZn"><img src="https://img.shields.io/badge/Discord-join-5865F2?logo=discord&logoColor=white" alt="Discord"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> &bull;
  <a href="https://github.com/cdknorow/coral/tree/main/coral-go/agent_docs">Documentation</a> &bull;
  <a href="#features">Features</a> &bull;
  <a href="#how-it-works">How It Works</a> &bull;
  <a href="https://store.coralai.ai/checkout/buy/1cf08999-ef06-466d-938c-b0f6ec4f92e6">Support Coral</a> &bull;
  <a href="https://discord.gg/qhfgY57AZn">Discord</a>
</p>

---

<p align="center">
  <a href="https://www.loom.com/share/7dce83519c8d4882af5a15bb9d727c21"><strong>▶ Watch Coral in action (2 min)</strong></a>
</p>

## What is Coral?

Coral is a local server that runs multiple AI coding agents — Claude Code, Codex, Gemini CLI,
and Pi.dev — as a coordinated team on the same codebase.

Each vendor now ships its own way to run several agents at once, but each one only
coordinates its own processes. Coral is agent-agnostic: Claude Code and Codex can work on the
same feature, on the same board, at the same time.

It manages three things:

- **Isolated workspaces.** Each agent runs in its own terminal session. You can optionally
  launch a team into its own git worktree on a dedicated `coral-team/<name>` branch, so the
  team's work stays off your main checkout.
- **A shared message board.** Agents post updates, ask questions, and read each other's
  progress. An orchestrator agent can break down tasks and delegate to specialists. Delivery is
  cursor-tracked: your read position is remembered across an agent restart — agents resume
  where they left off, with no messages repeated or skipped.
- **A web dashboard.** One browser tab shows every agent's live terminal, status, and
  controls. Launch, sleep, wake, restart, or kill agents without switching windows.

You bring your own agents and API keys. **Coral has no API keys of its own and never calls a
model on your behalf.** It runs the CLI agents you have already installed, using your
credentials. To count tokens and costs, Coral can proxy those agents' API traffic locally —
the traffic goes to the same provider it always did, and none of it comes to us.

**Coral is free and fully unlocked.** No features are gated, no account, no trial. If it
saves you time, you can support development with an optional one-time $49.99 supporter
license — no subscription. Supporters get priority support and priority consideration for
feature requests.

![Coral Dashboard](https://github.com/user-attachments/assets/6af60c92-1d72-45bd-9b46-7f1eab2ce5fe)

## Quick Start

### Download

Grab the latest release from [GitHub Releases](https://github.com/cdknorow/coral/releases):

- **macOS** — `Coral.v<version>.dmg`, e.g. `Coral.v1.0.8.dmg`
  Universal binary (Intel and Apple Silicon), signed and notarized — no Gatekeeper warning.
  Verify it yourself: `spctl -a -vv /Applications/Coral.app`
- **Linux** — `coral-linux-amd64-<version>.tar.gz`, e.g. `coral-linux-amd64-1.0.8.tar.gz`
  Statically linked x86-64 binaries; no arm64 build yet.

> **Previously installed Coral with `pip install agent-coral`?** That was the retired Python
> version. It shares command names, the `~/.coral` directory, and port 8420 with current
> Coral, so having both installed can silently start the wrong one. Run
> `which -a coral` to check, and `pip uninstall agent-coral` to remove it.

### Build from source

```bash
cd coral-go
make build
```

### Run

```bash
./bin/coral
```

Open **http://localhost:8420** and click **+New** to launch your first agent or create a team.

> **Requirements:** [tmux](https://github.com/tmux/tmux), and at least one coding agent CLI
> already installed and authenticated — Claude Code, Codex, Gemini CLI, or Pi.dev. Coral does
> not install or authenticate agents for you.
```

---

## What changed, and why

| Change | Reason |
|---|---|
| Headline names the four agents instead of "Multi-agent orchestration for AI coding tools" | The old line is a category label that describes a dozen tools. Naming the agents *is* the differentiator — Strategist §3 |
| Subhead: "the coding agents you already pay for" | Strategist §3 value proposition, verbatim intent. Speaks to the multi-vendor ICP |
| Board delivery: "nothing is lost" → the read-position wording | **Corrected 2026-08-28.** This draft still carried the retracted absolute after the README patch had already fixed it — found while marking preserved quotations, not by any sweep. Fixed one artifact and left the other proposing the same content |
| Subhead carries the **wedge** (multi-vendor, one tab), not isolation or durability | **Corrected 2026-08-28.** The first draft read "each in its own git worktree, with work that survives a restart" — **two retracted claims in the highest-traffic sentence in the document**, while `:79` of the same file was already correct. Durability is real but only within one server run, so it cannot carry a headline until the restart test lands |
| Added the agent-agnostic paragraph | Strategist's wedge (§4.1). Claude Code's own agent teams coordinate Claude Code instances; nothing there puts Codex and Gemini on one board. Stated as a contrast without naming a competitor to attack |
| Removed "or any CLI-based agent" from `:37` | **Disproven.** `agent.go:157-168` is a hardcoded four-case switch, no registry or plugin mechanism anywhere — Dev Advocate 3f |
| Docs badge + nav → `coral-go/agent_docs` | The old target instructs `pip install agent-coral`, which installs the retired Python product |
| Discord badge → static label | The current one renders the literal word "invalid" |
| Loom `<img>` → text link | The CDN thumbnail returns **403**; the image is broken today. Swap to a self-hosted asset once one is verified current (patch B) |
| Corrected download filenames + version placeholders | `Coral.dmg` has never existed; CI publishes `Coral.v<version>.dmg` |
| Added signed/notarized + the `spctl` command | Verified strength, and it answers "random binary from GitHub" with a command the reader can run rather than an adjective |
| Added "no arm64 build yet" | `release.yml:44` hardcodes `GOARCH=amd64`. Readers scanning for "Linux" won't infer the exclusion |
| Added the `pip install agent-coral` collision callout | The retired package collides on binary names, data dir, and port. This is the only place a returning Python user will see it |
| "free and fully unlocked" replaces "free to use" | Strategist §7: avoid "tier", which implies a paid tier with more features. There isn't one |
| D5 proxy wording | Strategist-approved final wording, used verbatim across README, disclosure, FAQ, landing. **⚠️ 📖 — the underlying finding is READ-ONLY** (`providers.go:24-32`); nobody has watched what the proxy sends. Approved wording does not raise the evidence tier of the claim behind it |
| "Each team works in its own git worktree" — **not** "each agent" | **Disproven.** `sessions.go:2436-2438` builds one worktree path from `body.BoardName` in the team-launch path, then assigns it to `workingDir` for every agent. One worktree per team, shared. Verified independently after Dev Advocate's `git worktree list` — see D18 |
| Requirements now say agents must be **authenticated** | Dev Advocate #23: agent CLI install + auth is the real long pole on a clean machine, and Coral does neither |
| Moved "Support Coral" later in the nav | Growth plan lines 64/124: keep the supporter ask out of the critical path until value is delivered. Still present, just not second |

## Deliberately absent

- **Homebrew** — blocked on #21; even after, the only permissible line is the two-command
  tap form, never `brew install coral`
- **Windows** — does not compile
- **Any timing claim** — "under two minutes" is true but carries a mandatory qualifier
  (agent CLI installed and authenticated) that does not survive a hero section. It belongs in
  the quickstart, not above the fold
- **Any metric** — no stars, downloads, users, or benchmarks
- **The comparison table** — being replaced with sourced prose; it lives below the fold and is
  out of scope for this section

## Open

- **Hero asset.** Currently a text link because the image is broken. The real fix is a
  self-hosted asset showing several agents from different vendors running side by side —
  the wedge, in one frame. Needs Dev Advocate capture; `corral-dashboard-tour.gif` exists but
  predates the current build.
- **`README.md:125`** carries the stale AutoGen/CrewAI premise and changes with the
  comparison table. Below the fold, not in this diff.
