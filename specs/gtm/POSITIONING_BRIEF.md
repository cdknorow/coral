# Coral Positioning Brief

**Owner:** GTM Strategist · **Task:** #24 · **Status:** authoritative for Wave 1
**Rule:** every claim below is either verified in this repo (file:line given) or sourced to a
primary public document (URL given). Claims I could not verify are in
[Section 9](#9-claims-i-could-not-verify) and must not be used in copy.

---

## 1. The ICP

**Adopted, with one sharpening.** The Orchestrator's hypothesis was right and I am not arguing
against it. The ICP is not "developers curious about AI agents." It is:

> A developer who is **already running two or more coding agents** and has already hit the
> operational wall: terminal sprawl, agents clobbering each other's edits, no memory of what ran
> last week, no single view of what five agents are doing right now.

They have bought into agentic coding. They have not solved **operating** it.

### The sharpening: it is specifically the *multi-vendor* developer

Not every developer with two agents is our buyer, and the difference decides whether we win.

A developer running **two Claude Code sessions** has a native answer now, and it is free and
first-party. Claude Code shipped `claude --worktree <name>` for isolated parallel sessions, and
Agent Teams — a lead session that spawns teammates with a shared, dependency-aware task list and a
file-based mailbox. ([worktrees](https://code.claude.com/docs/en/worktrees),
[agent teams](https://code.claude.com/docs/en/agent-teams)) Selling that developer "run agents in
parallel" is selling them something they already have. We will lose that argument in public.

A developer running **Claude Code and Codex and Gemini CLI** has no first-party answer, because
each vendor's orchestration coordinates only its own processes. Claude Code's teammates are, in
Anthropic's own words, "separate Claude Code instances." Nothing in it puts Codex and Gemini on
the same team.

So the ICP tightens to:

| | |
|---|---|
| **Primary** | Solo devs and 2–5 person teams running **agents from more than one vendor** on the same repo — Claude Code + Codex + Gemini CLI in some combination |
| **Secondary** | Single-vendor power users who have outgrown ephemeral sessions and want work to **survive a restart** — the durability wedge, Section 4.2 |
| **Explicitly not** | Enterprise platform teams wanting governance, RBAC, audit. That is CrewAI AMP's market and we have none of it |
| **Explicitly not** | Developers who want a framework to *build* agents in code. That is a different job |

### Qualifying signals

They have tmux installed. They have a worktree habit already, or have been bitten by not having
one. They have more than one agent CLI on `PATH`. They have complained publicly about juggling
terminals or about losing an agent's context.

---

## 2. The pain, stated as they'd state it

1. **"I have eleven terminal tabs and I don't know which agent is stuck."**
2. **"They overwrote each other."** Solved by worktrees — but only if you set them up yourself,
   per agent, every time.
3. **"I closed the terminal and lost the thread."**
4. **"I have no idea what this cost."** Three vendors, three billing pages, no per-task number.
5. **"Getting them to hand off is a manual copy-paste job."**

---

## 3. Value proposition

**One sentence:**

> Coral is a local control plane for the coding agents you already pay for — Claude Code, Codex,
> Gemini CLI and Pi.dev running as one team, in one browser tab, with work that survives restarting
> Coral.

⚠️ **Two words in that sentence are load-bearing and were both fixed after review.** "Pi.dev" —
because our activation page says "Claude, Codex & Gemini support" (`server.go:840`) and undersells
the fourth agent we actually ship. And **"restarting Coral", not "a restart"** — what was tested is
a **server-process** restart; a machine reboot was not. Unqualified, "a restart" invites the reading
we cannot support, and that ambiguity is the exact class of error that cost us three claims in a
day.

**Category:** an *operational tool*, not an agent framework. We do not ask you to rewrite your
workflow in our abstraction. We run the CLIs you already have.

**The wedge, in priority order** (set by the Orchestrator, and I agree):

1. **Heterogeneous agents on one board.** The thing no single-vendor tool can do.
2. **Durability.** Teams that survive restarts.
3. **One dashboard for N live terminals**, in a browser rather than a terminal multiplexer.

**The three-word version:** *Agents you already have, operated properly.*

**Positioning statement:**

> For developers already running more than one coding agent, Coral is a local control plane that
> runs Claude Code, Codex, Gemini CLI and Pi.dev as one coordinated team. Unlike each vendor's own
> orchestration, which coordinates only its own processes, Coral is agent-agnostic, keeps team
> state in a database so teams survive restarts, and shows every agent's live terminal in one
> browser tab.

---

## 4. Proof points

⚠️ **Six were drafted. Three survived execution testing. Read this before quoting any of them.**

| | Proof point | Verdict |
|---|---|---|
| 4.1 | Four agents from three vendors on one board | ✅ **verified** — the wedge |
| 4.2 | Sleep a team, restart Coral, wake it with context | ✅ **verified** |
| 4.3 | One browser tab for every agent's live terminal | ✅ **verified** |
| 4.4 | One cost figure across vendors | ⚠️ **downgraded** — per-agent only; the cross-vendor total silently omits vendors |
| 4.5 | Per-agent worktree isolation | ⛔ **disproven** — reframed onto coordination |
| 4.6 | Full-text search across sessions | ⛔ **disproven** — never worked |

**This opener previously read "Every one maps to a capability that exists today."** That was true
when written and false within hours. I corrected 4.4, 4.5 and 4.6 individually as each was
disproven and never returned to the sentence that summarises all six — the fourth time today I
fixed instances and left the summary standing.

⚖️ It was caught by the Developer Advocate's test, and it is the shape they predicted exactly:
**summary sentences are written first, corrected last, and quoted most.** They state a claim without
its mechanism, because stating the mechanism is what the body is for — so the defect concentrates in
precisely the sentences most likely to be lifted. **When a claim is retracted, fix the summary
sentences first.**

The contrast side of each surviving proof point is sourced. Per the task constraint, none depends on
an install path the Developer Advocate has not verified.

### 4.1 Heterogeneous agents on one board — THE WEDGE

**Ours, and now precisely bounded:** Coral supports **four named agents** — Claude Code, Codex,
Gemini CLI and Pi.dev — each with a real implementation in `coral-go/internal/agent/`. They share
one board and one dashboard.

**The contrast, sourced:** Claude Code Agent Teams coordinates "multiple Claude Code instances";
teammates are "separate Claude Code instances." Its mailbox lives at
`~/.claude/teams/{team-name}/inboxes/{agent-name}.json` — a Claude-Code-internal structure.
([agent teams](https://code.claude.com/docs/en/agent-teams))

**Say:** "Claude Code can run a team of Claude Code sessions. Coral runs Claude Code, Codex,
Gemini CLI and Pi.dev on the same board."

⛔ **Never say "works with any CLI agent."** The Developer Advocate resolved this to answer (iii):
`internal/agent/agent.go:157-168` is a hardcoded switch over four types with a `default:` arm;
`agenttypes/types.go:8-12` defines five constants, one of which (`terminal`) is a raw shell, not an
agent. A grep for `custom_agent|customagent|user_defined|plugin|registerAgent` across `internal/`
returns nothing. **Adding an agent means writing Go and recompiling** — a contribution to the
project, not a user feature. README.md:37, :81 and the features table all claim otherwise and are
being cut.

**This does not weaken the wedge.** "Four agents from three vendors on one board" is checkable,
defensible, and still something no single-vendor tool does. The unbounded version bought us nothing
and could be disproven by opening one file — which is exactly the test every claim we ship has to
pass.

### 4.2 Durability: teams that survive a restart

**Ours, verified:** sleep/wake at both agent and team scope —
`/api/sessions/live/{sessionID}/sleep` and `/wake`,
`/api/sessions/live/team/{boardName}/sleep` and `/wake`, plus `sleep-all` / `wake-all`.
State is in SQLite, not process memory. Board delivery is cursor-tracked, so messages survive an
agent restart (README.md:95).

**The contrast, sourced, in Anthropic's own words** — from the Agent Teams *Limitations* section:

> "**No session resumption with in-process teammates**: `/resume` and `/rewind` do not restore
> in-process teammates."

Also documented there: "The team config directory is removed when the session ends,"
"**One team per session**," "**No nested teams**," and "**Lead is fixed** — you can't promote a
teammate to lead." ([agent teams](https://code.claude.com/docs/en/agent-teams))

This is the strongest proof point we have **because we are not the ones asserting the limitation.**

**✅ VERIFIED BY EXECUTION, INCLUDING THE RESTART CASE** (Developer Advocate, demo 3, on the shipped
v1.0.8 binary). The Coral server **process was killed**, not detached; a **fresh** process reported
`{"sleeping":true}` *before* the wake — so sleep state came from disk, not memory — and the woken
agent **re-answered from restored context** rather than echoing replayed scrollback.

**Approved wording — use this exactly:**

> "Sleep an agent team, quit Coral, restart it later, and wake the team with its conversation
> context intact."

⛔ **Still banned, and the boundary moved rather than disappeared:**

| Banned | Why |
|---|---|
| "survives reboots" / machine reboot | The **server process** was killed, not the host. A reboot also kills the tmux server; the test confirmed tmux gets recreated, so it is *likely* to hold — and "likely" is not what we ship |
| "come back tomorrow" / long intervals | Tested over a short interval |
| Large multi-agent teams | Tested with a single agent |
| Any claim about how much scrollback survives | Unmeasured |

**Note on my earlier correction:** I cut this claim entirely when the first caveat landed, which was
right at the time. The right move now is to **regenerate against the approved wording, not restore
what I cut** — the tested boundary is "server restart," not "restart" in general, and those are
different sentences.

When pressed on the contrast, quote Anthropic's docs.

### 4.3 One browser tab for N live terminals

**Ours:** the dashboard streams every agent's live terminal, status and controls over websockets.

**The contrast, sourced:** Claude Code's multi-pane view "requires tmux, or iTerm2," and their docs
state split-pane mode "isn't supported in VS Code's integrated terminal, Windows Terminal, or
Ghostty." The default `in-process` mode shows one teammate at a time — you arrow to a teammate and
press Enter to view it. ([agent teams](https://code.claude.com/docs/en/agent-teams))

**Say:** "Every agent's terminal, side by side, in a browser tab — including from your other
machine or your phone." A browser tab is also editor-agnostic, which the terminal-bound approach
structurally is not.

### 4.4 ⚠️ DOWNGRADED — cost tracking works; "one figure across vendors" does not

**Ours, verified in code:** `internal/store/token_usage.go` records input/output/cache tokens,
`cost_usd` and `num_turns`, keyed by `session_id`, `agent_name` **and `agent_type`**.

**✅ What execution confirmed:** cost tracking works and renders **real numbers**. Keep that.

**⛔ What execution disproved** (Developer Advocate, task #41): in a controlled same-team comparison,
**Claude reported $0.97 and Codex reported nothing.** The single-figure-across-vendors claim is not
demonstrable today.

**The part the operator needs to hear, and it is worse than a missing feature:** the total **looks
complete and is not.** A user comparing a three-vendor team's spend gets a confident number that
silently omits one or more vendors. A blank would be honest; a wrong total is not.

**Approved wording, until #41 lands:** "See token usage and cost per agent and per session."
⛔ **Never say:** "one cost figure across Anthropic, OpenAI and Google," "cross-vendor cost," or
anything implying the total spans every agent in a team.

### 4.5 Worktree isolation — CORRECTED, and demoted twice

⚠️ **This proof point was wrong in my first draft and I have rewritten it.** The Developer Advocate
found it empirically; I verified it in the code before rewriting.

**What actually happens:** `handleLaunchTeam` creates **one worktree per team, not per agent.**
`sessions.go:2438` builds the path from `body.BoardName`; `:2439` creates a single branch
`coral-team/{boardName}`; `:2449` makes it the working directory; and the launch loop stores that
same path on **every** session (`:2515-2517`). Worktree creation is also **optional** — it only
happens when the caller passes `worktree: true` (`:2431`).

**And it is off by default.** `frontend/templates/includes/modals.html:240` — the checkbox has **no
`checked` attribute**, and its container `#team-worktree-option` is `display:none` (`:238`) until
the directory is detected as a git repo. I verified both.

So the true statement has three levels, and the default is the worst one:

| What the user does | What they get |
|---|---|
| Clicks through normal team creation (**the default**) | **No worktree at all.** Agents run directly in the user's real working directory, on their current branch |
| Ticks "Launch in Git Worktree" | One worktree and one branch (`coral-team/<name>`) **shared by the whole team**, isolated from the main checkout |
| Anything | Each agent does get its own tmux session — that half of the claim is true |

**The team is (optionally) isolated from your main checkout. The agents are never isolated from each
other.**

### ⚠️ Correction to my own correction: Coral has TWO worktree behaviours

Found by the Content Producer in the #37 sweep; **I verified both paths in the code before writing
this.** My earlier blanket statement — "per-agent isolation does not exist" — was wrong.

| Path | Worktree granularity | Default | Status | Code |
|---|---|---|---|---|
| **Scheduled jobs** | one per **run**, at `<repo>_task_run_<runID>` | **ON** — hardcoded | 🔴 **fails on the documented default** (#43) | `scheduler.go:272`, `:550-551` |
| **Tasks via API** | one per **run** | **ON** — overridable | same git call, same failure mode | `tasks.go:109` (`createWT := true`) |
| **Team launch** | one per **team**, keyed on board name, shared by all agents | **OFF** | works when enabled; agents not isolated from each other | `sessions.go:2431`, `:2438`; `modals.html:240` |
| **Workflows** | ⛔ **none — `worktree_path` is null** | — | works, but **never call a workflow run isolated** | Dev Advocate, #42 |

**Four paths, four different answers, and the fourth was found last.** Workflow runs create **no
worktree at all** — verified by execution in #42. Nothing in our copy claimed they did, but the
path-qualifier rule below existed precisely so nobody would assume it, and until now the rule had a
three-row table behind a four-row product.

**A job run is a single agent, so per-run genuinely is per-agent isolation — and it is on by
default.**

🔴 **AND IT FAILS ON THE DEFAULT CONFIGURATION. Do not use this as a proof point.** Developer
Advocate, task #42: `jobs.md:32` documents `base_branch` defaulting to `main` and `create_worktree`
defaulting to `true`, and `git worktree add` refuses a branch already checked out in the main working
copy. **A user following our documentation exactly gets a job that fires on schedule and fails 100%
of the time** — and fails silently, because the scheduler shows the job as active and firing; only
the run records show failure. A daily job would look fine for a day and then quietly never have
worked.

The **cron half is verified true** — jobs fire on schedule with `trigger_type` `cron`. It is the
**isolated-worktree half** that fails. The fix is `-b <per-run-branch>` or `--detach` on that git
call; the workaround today is a `base_branch` not checked out anywhere. `agent_docs/jobs.md:200` says "each job gets an isolated copy of the repo," and that
statement is **true** for that path.

**This is how the false README claim survived three careful readers.** Anyone spot-checking "does
Coral do isolated worktrees?" against the jobs path would have confirmed it and moved on — and been
right, about the wrong path. The team behaviour is the anomaly; the true version of the claim exists
twenty files away.

⛔ **Standing rule, now binding:** never write "isolated worktree" without naming **which path** it
describes. Unqualified, it is true of jobs and false of teams, and the reader will assume whichever
one we are selling at the time.

⛔ **Positioning consequence — RETRACTED. I called this "a small gain rather than a loss" and it was
neither.** I wrote that the isolation story was real and simply belonged to jobs rather than teams,
and that this was the honest place to tell it. That was based on reading `scheduler.go:550-551` and
seeing the `runID` keying. **The mechanism is correct and never gets to run**, because the git call
before it fails on the default configuration.

**So there is currently no path in Coral that delivers working per-agent worktree isolation.** Teams
share one worktree, off by default; jobs would isolate per run and fail before they get there. Do
not tell an isolation story on either path until #42's finding is fixed.

⚖️ **Worth recording how I got it wrong**, because it is the third time today the same evidence type
misled someone: I did not claim a route existed and assume it worked — I read the actual worktree
construction and the `runID` keying, which is *more* evidence than a route number and still not
execution. **A correct-looking mechanism proves the code would do the right thing if reached.** It
says nothing about whether anything reaches it. The Developer Advocate hit the identical trap on the
same feature and had recommended jobs in `agent_docs` as the workaround for the team gap.

⛔ **These README claims are false and must be cut** (already flagged to the Producer):
- README.md:41 — "Each agent runs in its own tmux session with **its own git worktree**"
- README.md:41 — "so agents can write code in parallel **without merge conflicts**"
- README.md:91 — "Coral creates a git worktree **for each agent**… Each agent has **its own copy of
  the repo** and can read, write, and run commands **without interfering with others**"

The second one is the dangerous one, and the default makes it worse than "agents share a worktree":
**by default they share the user's actual working directory.** We promise protection that, on the
path most users take, is absent entirely — and the repository at risk is theirs.

**Narrower fix than it first appeared.** The in-product copy is already correct:
`modals.html:243` reads "Create an isolated git worktree so the team works on a **separate
branch**" — team-level, accurate. The code is right and the UI is right; **only the README
overclaims.** For this line there is no code-vs-docs decision to make.

**What is supportable:** "Each team works in its own git worktree on its own branch, isolated from
your main checkout."

**Now proven empirically, not just read from the code.** The Developer Advocate ran two agents
(Claude Code and Codex) on the same task in one team. Both wrote a complete, correct implementation
of the same function into the same file in the shared worktree. Neither saw the other's work.
Result: `Truncate redeclared in this block`, `go build` fails, `go test` fails — **a broken
repository**. The two implementations even differed on an edge case (`n <= 0` vs `n < 0`), so this
is two genuine authors silently stacked, not a trivial duplicate.

Their own caveat, which I am keeping because it is the honest one: they engineered a worst case by
assigning both agents the identical function, and a real team with well-separated tasks would
collide less often. **But the README's claim is architectural** — "its own git worktree," "its own
copy of the repo," "without interfering with others" — and the architecture does not provide it.
There was no safeguard to defeat.

**The reframe — and it makes the positioning better, not worse.** If isolation were the wedge, this
finding would gut us. It never was: worktree isolation stopped being differentiating the moment
Claude Code, Cursor 3, Conductor, Crystal and Claude Squad all shipped it (§5.2). **Our
differentiator is coordination, not isolation** — a shared branch, a shared message board, agents
from different vendors handing work to each other, one dashboard over all of it. That is what §4.1
through §4.4 already say, and none of them depend on per-agent worktrees.

**Do not say:** anything about per-agent isolation **on the team path**, "no merge conflicts," or
agents not interfering. ⛔ **And do not fall back on the jobs path either** — per-run isolation is
implemented there but fails on the default configuration (#42). There is no working isolation story
to tell today.
**Do say:** "a team gets its own branch and worktree; the agents on it collaborate on that branch
through a shared board."

⛔ **One guard on that reframe, and it is the trap I fell into in the FAQ.** "Coordination is the
differentiator" sits one sentence away from "coordination solves the collision problem," and the
second is false. The board is a **communication channel, not a lock** — the two agents in demo 1
*had* a board and still both wrote `Truncate` into the same file. The mitigation for collisions is
**scoping work to non-overlapping files**; the board is what lets agents from different vendors hand
work to each other, which is a different job.

Both facts are true and they must travel together, because the pull will always be to let the first
absorb the second: the board is the wedge (§4.1), **and** it is not a concurrency control. Never
write copy where a reader can take it for one.

**Open product question, not mine to settle** (Growth Engineer / operator): implement per-agent
worktrees to match the docs, or change the docs to match the code. Until it is resolved, copy
follows the code.

### 4.6 ⛔ RETRACTED — full-text search has never worked

**Do not use this as a proof point. Do not put it in any copy.**

**What I originally wrote:** "a real SQLite FTS5 virtual table with a porter tokenizer —
`store/connection.go:175`." That was true and it was 📖 — verified in code, never observed.

**What execution found** (Developer Advocate, task #40; I verified the code path myself):
`FTSBody` is **declared** at `agent/agent.go:66` and **read** at `indexer.go:114-115` behind
`if s.FTSBody != ""` — **and never assigned anywhere in the repository.** The field is always the
empty string, so the guard never passes, so `UpsertFTS` is never called. Production has **56 indexed
sessions and zero FTS rows**, and has for months.

**The table exists. Nothing has ever been written to it.** An advertised feature at README:119 has
never worked for anyone. The README claim is being cut.

⚖️ **Why this is the single best argument for the SEEN gate.** I verified the schema, correctly,
and cited a real line of code. The virtual table is genuinely there with a genuinely correct
tokenizer. **Reading the code confirmed the mechanism exists and told me nothing about whether it
runs.** No amount of static review would have caught this — only opening the product and searching.

## 5. The competitive reality

The comparison table at README.md:127-138 is being **deleted** (Orchestrator ruling D2), and
AutoGen/CrewAI are being dropped as the named contrast (D3). Here is what replaces that framing.

### 5.1 The old frame is dated

README.md:125 frames Coral against AutoGen and CrewAI. **Microsoft moved AutoGen to maintenance
mode and shipped Microsoft Agent Framework 1.0 on 2026-04-03 as its successor**; the repo now
directs new users to MAF. ([microsoft/autogen](https://github.com/microsoft/autogen)) Anchoring our
positioning to a sunset project dates us on sight.

Those frameworks were also never our competitors. They are "build agent pipelines in code" — a
different job from "operate agent processes you already run."

### 5.2 The competitive set that is actually real

| Competitor | What it is | Why someone picks it | Our answer |
|---|---|---|---|
| **Claude Code native** (worktrees + Agent Teams + cross-session messaging) | First-party, free, zero install | Already there; nothing to adopt | Claude-only; teammates don't survive `/resume`; one team per session; lead is fixed; **experimental and off by default** (requires `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`) |
| **Cursor 3 Agents Window** | Up to 8 parallel agents across worktrees/cloud/SSH, agent tabs | Already in the IDE | IDE-bound and Cursor's own agents; not your CLI tools |
| **Conductor / Crystal** | Desktop apps, parallel Claude Code in worktrees | Polished, focused | Claude-centric; no cross-vendor board or cost view |
| **Claude Squad** | tmux + worktrees, multiple agents incl. Codex and Aider | Same shape as us; genuinely multi-agent | Closest real competitor. We differ on the web dashboard, durable sleep/wake, and **workflows** (✅ #42). ⛔ Not scheduling — broken by default |
| **vibe-kanban** | Kanban board over coding agents | Task-board mental model | Bloop announced shutdown 2026-04-10; continues community-maintained and fully local |
| **AutoGen / CrewAI / MAF** | Frameworks for building agent systems | Different job | Not a competitor. Stop naming them |

**Honest read:** this is a crowded field, and several tools share our exact architecture
(tmux + worktrees). Our defensible ground is narrow and specific: **cross-vendor**, **durable**,
**browser dashboard**, plus **workflows** — ✅ verified by execution (#42), including the
across-agents half the claim actually names. ⛔ **Scheduled jobs are out**: broken in the documented
default. ⛔ **Webhooks are out**: unverifiable without an external endpoint.

### 5.3 The rule this implies

**Never claim a category first.** Every "the first tool to…" or "unlike anything else…" is a
falsifiable claim in a field with a dozen entrants, and on Hacker News someone will name three.
Claim specific, checkable differences instead.

---

## 6. The `pip install agent-coral` problem

Some fraction of everyone who has heard of Coral has heard of the **Python** one. The docs site
still tells visitors to `pip install agent-coral`; that command **succeeds** and installs a version
numbered **4.4.1** while we ship **v1.0.8**. (Producer's finding, board 2026-08-28.)

This is a positioning problem, not only a links problem: a returning visitor can reasonably believe
our current release is *older* than what they already have.

### The narrative — one paragraph, used everywhere

> Coral was originally a Python package (`agent-coral`). It was rewritten in Go and is now
> distributed as a native app you download from GitHub Releases — no Python, no pip. The Python
> package is retired and is not maintained. Version numbers restarted with the rewrite, so the Go
> release (v1.0.8) is newer than the last Python release (4.4.1) despite the lower number. If you
> installed `agent-coral`, you can `pip uninstall agent-coral`; it shares no state with the new app.

### Rules

- **Lead with the version explanation** wherever both numbers can be seen together. Do not make
  the reader work it out.
- **Never say "Coral 4.4.1 was buggy."** It was a different implementation, and disparaging our own
  past shipped work reads badly.
- **Do not use the word "migration."** There is no migration path and no shared state; implying one
  creates a support burden we cannot honor.
- **Blocked on the Dev Advocate:** whether a stale `agent-coral` install interferes with the Go
  product. Until that is answered, the `pip uninstall` line above stays as written — a suggestion,
  not a requirement.

---

## 7. Messaging: say / don't say

| Say | Don't say | Why |
|---|---|---|
| "Run Claude Code, Codex, Gemini CLI and Pi.dev as one team" | "Run any CLI agent" | **Disproven** — Section 9 |
| "Teams survive a restart — sleep them and wake them" | "The only tool with persistent agents" | Category-first claim we can't defend |
| "One browser tab for every agent's live terminal" | "No more terminal juggling" | The second is true of half the field |
| "Worktree isolation across agents from different vendors" | "We give you worktree isolation" | Not differentiating on its own — 4.5 |
| "One cost figure across Anthropic, OpenAI and Google" | "Cost tracking" | The generic version is unremarkable |
| "Free and fully unlocked — nothing is gated" | "Free tier" | "Tier" implies a paid tier with more features. There isn't one |
| "Coral has no API keys of its own" | "Coral doesn't call any AI APIs" | Precision — Section 8 |

---

## 8. Sign-off on the Producer's D5

**Approved, with one edit.** Their diagnosis is right: README.md:47's "Coral doesn't call any AI
APIs itself" is true in spirit but invites a reader who finds `internal/proxy/` to conclude we hid
it. Precision here is a *better* privacy story, not a weaker one.

**Approved wording** *(revised — see the note below on "none of it" vs "nothing")*:

> Coral has no API keys of its own and never calls a model on your behalf. It runs the CLI agents
> you have already installed, using your credentials. To count tokens and costs, Coral can proxy
> those agents' API traffic locally — the traffic goes to the same provider it always did, and
> none of it comes to us.

The edit is the final clause. Their draft explained the mechanism; the reader's actual question is
"does anything reach *you*." Answer it in the same breath.

✅ **Now VERIFIED by execution (D5, rescanned at corrected scope).** Zero provider-key hits across
**all nine executables shipped in the DMG** — not just `coral` — using structured patterns
(`sk-ant-api`, `sk-proj-`, `AIzaSy`, `xoxb-`). Keys come only from `os.Getenv`; caller credentials
pass through.

**But the lesson from how it got here stands, and it is the part worth keeping.** My sign-off was on
the **wording**, not on the **claim**, and I did not say so at the time. I reviewed whether the
sentence was precise and whether it answered the reader's real question — both true, both irrelevant
to whether the fact had been checked. **Approved wording is not the same as a verified claim**, and
once wording is approved it stops being treated as a claim and starts being treated as a decision.
That is how this sentence reached five documents unmarked, and it would have been just as invisible
if the claim had turned out false.

⚠️ **Completeness requirement, permanent:** never publish this sentence without the telemetry
disclosure nearby. "No API keys of its own" invites "Coral sends nothing," which is false.

🔴 **"none of it comes to us", never "nothing is sent to us" — and the flawed clause was mine.** I
wrote that final clause and signed it off. Lifted alone, *"nothing is sent to us"* is **false**:
Coral posts to PostHog by default. It is only true scoped to its antecedent, the agents' API
traffic. **"None of it" binds to that antecedent; "nothing" floats free.** Two words, identical
meaning in place, and it survives the lift test that the original fails. (Caught by the Content
Producer, on the sentence they had proposed and I had approved.) This must stay consistent with the
telemetry disclosure (#20) — see the FAQ.

---

## 9. Claims I could not verify

**Do not use these in any copy until they are resolved.**

| Claim | Status | Owner |
|---|---|---|
| "Any CLI-based agent can be added" (README.md:81, :37, features table) | ⛔ **DISPROVEN — answer (iii).** Hardcoded switch, no extension mechanism. Cut on sight; replace with the four named agents | Resolved — Dev Advocate |
| Any install path — DMG, tarball, MSI, Homebrew | Not yet verified; excluded from all proof points by task constraint | Dev Advocate #22, Growth Eng #21 |
| Windows support | **Dropped.** Dev Advocate cannot verify; per the operator's own "verify, then promote," it is out | Settled |
| "Activates on 1 machine" (activation page) | Not verified | Growth Eng #29 |

**Resolved: it was (iii).** As predicted, the wedge in Section 3 does not change — "Claude Code,
Codex, Gemini CLI and Pi.dev on one board" is verified today and is sufficient on its own. Only the
breadth claim goes away, and it was the one cell nobody could stand behind.

**Related defect, worth knowing before anyone demos:** an unrecognised `agent_type` returns
HTTP 200 `ok:true` and silently starts a **Claude** session (the `default:` arm). A user who tries
the advertised "any CLI agent" gets no error — they get a Claude session burning their tokens on an
agent they did not ask for. Task #32.

---

## 10. What positioning cannot fix

Two product facts currently contradict this brief. Both are assigned; naming them here so no copy
is written that assumes they are already fixed.

1. **The first screen a new user sees is a price.** `IsNagLaunch()` returns true on launch 1
   (`internal/license/launch_counter.go:29`), and `serveIndex` serves the activation page instead
   of the dashboard (`internal/server/server.go:663-667`). This contradicts "free and fully
   unlocked" experientially even though it is literally true, and it violates Hacker News's Show HN
   guidance to "make it easy for users to try your thing out, ideally without barriers."
   → Growth Engineer #26.
2. **Our activation page claims two free features are supporter benefits.** → Growth Engineer #29,
   copy in `ACTIVATION_PAGE_COPY.md`.

3. **Agents on a team can overwrite each other's work, and we say they can't.** Proven by
   execution, not inference — see §4.5. Task #35 cuts the claim; the underlying product decision
   (per-agent worktrees, or better in-board coordination) is unresolved.

**Positioning is downstream of all three.** No channel should open until they land.

---

## 11. Two of the operator's own demonstration topics are not demonstrable

`GROWTH_AND_SUPPORTER_SALES_PLAN.md` Phase 5 lists six demonstration topics. As of today:

| # | Topic | Status |
|---|---|---|
| 1 | "Run Claude Code and Codex on the same feature" | ⚠️ **Runs, but the honest result is a broken build.** Demonstrable only as a *coordination* story — two vendors under one dashboard — not as a "they don't clobber each other" story |
| 2 | "Separate implementation and review between independent agents" | ✅ Plausible — sequential roles, no shared-file collision. Untested |
| 3 | "Monitor five coding agents without managing five terminal windows" | ✅ The strongest available demo. Directly shows proof point §4.3 |
| 4 | "Resume an AI coding team with its history intact" | ✅ Proof point §4.2 — **verified by execution, including a server restart** |
| 5 | "Measure the cost of parallel coding-agent workflows" | ⚠️ **Downgraded** — §4.4. Per-agent cost renders real numbers; the cross-vendor total silently omits vendors (#41) |
| 6 | **"Use isolated worktrees to prevent agents from overwriting one another"** | ⛔ **CANNOT BE DEMONSTRATED. The product does not do this.** Agents on a team share one worktree |

**Topic 6 must be struck from the plan.** It is not a messaging problem or a demo we haven't got to
— it describes a capability that does not exist, and it is the topic the false README claim came
from. Topic 1 needs rewording to a coordination demo.

**Recommended replacement for topic 6**, demonstrable today and arguably a better story:
*"Run agents from three different vendors on one repo and watch them hand work to each other on the
message board."* That is proof point §4.1, it is the wedge, and nothing else in the field can show
it.
