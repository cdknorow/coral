# Wave 1 Exit Brief — Operator Decisions

**Prepared by:** GTM Strategist · **Reviewed by:** Orchestrator before delivery
**Date:** 2026-08-28 · **Status:** nothing external has shipped · **No launch date is proposed**

---

## Read this first

**Fifteen decisions need you. Nothing external ships until you make them.** Part 1 is the whole
brief — if you read only that, it works. Parts 2–4 are the evidence behind it.

**Four of them are one-minute checks that unblock everything else:** D2 (does telemetry actually
arrive), D3 (does a test purchase attribute), D10 (a clean macOS user account), D11 (two CI jobs).

**The single most important thing in this document:** the metric you approved as the launch gate —
40% of installs launching an agent — **can be cleared by users whose agents all froze and who got
nothing out of Coral.** See D1.

**The number that most argues for how we worked:** of the six differentiators the positioning was
built on, **three survived execution testing.** Two were disproven and one was downgraded — all
three by running the product, after a full day of static review found none of them. Part 4.

**Legend:** ⚖️ = judgment, not a verified benchmark · 🔴 = blocks other work · ✅ = seen running ·
📖 = verified in code only

### 🔴 How to read the evidence below — added last, and it closes a real gap

Part 4 grades every *claim* by evidence tier. **Part 1 did not, and that was the wrong way round** —
this is the part you act on. Three of today's retractions came from reading code correctly and being
wrong anyway, so the tier matters more here than anywhere else.

**Most decisions below rest on ✅ evidence — something a person ran, reproduced, or requested over
HTTP.** The exceptions are called out inline where they occur. The three tiers, and what each is
worth:

| Tier | Means | Failed today |
|---|---|---|
| ✅ **Executed** | Someone ran it and watched the result | — |
| 📖 **Read** | Verified in code or a published artifact; never run | **Three times** — FTS, Codex cost, scheduled jobs |
| ❓ **Unverifiable here** | Nobody on this team can check it; needs you | D2, D3 |

⚠️ **A file:line citation below is not automatically ✅.** Where a decision rests on reading rather
than running, it says so. The scheduled-jobs case (D7) is the cautionary one: three people read the
correct mechanism, at the correct line, and all three were wrong — the failure depended on the state
of the user's repository, not on the code.

---

# Part 1 · Decisions awaiting you

## 🔴 D1 — Move the launch gate from `first_agent_launched` to `first_task_completed`

**Decision:** make the **10% first-task-completed** target the launch gate, and treat the 40% agent
and 20% team targets as diagnostics.

**Evidence:** `first_agent_launched` fires at the **launch request**, not when an agent does
anything (`coral-go/internal/server/routes/sessions.go:2366-2367`). Separately, every new user's
first agent blocks on an unsurfaced trust prompt (#33) — it is not Claude-specific; Codex blocks too,
so a four-agent team launch means four frozen agents with no indication why.

**Evidence tier:** ✅ for the hang — the Developer Advocate observed both Claude and Codex blocking
on the trust prompt. 📖 for the emit point — `sessions.go:2366-2367` is a direct read of where the
line sits, which is not runtime-dependent. **The combination is inference**, and it is sound, but
nobody has yet watched a user clear the 40% gate while frozen. It would be visible in the data as a
high `first_agent_launched` rate with a near-zero `first_task_completed` rate.

**Together:** an agent emits `first_agent_launched`, then sits frozen forever waiting on a keystroke
the user never sees. **The 40% gate measures intent, not success — and it fails in the dangerous
direction.** It will read healthy while nobody is getting value. `first_task_completed` is the only
step downstream of the hang.

**Recommendation:** GTM Strategist. Endorsed by the Orchestrator, who escalated it as changing the
gate itself.
**Blocked until you answer:** whether any future gate reading means anything.
**If you do nothing:** we could clear 40%, open channels on it, and pour traffic into a product
where the median user's first agent freezes.

## 🔴 D2 — Confirm telemetry actually arrives (B11)

**Decision:** cut a release build, run it once on a clean machine, and confirm an `install` event
**lands in PostHog**. Not "does the secret exist."

**Evidence:** nobody on the team can read repo secrets, so nobody has confirmed
`POSTHOG_PROJECT_KEY` (`.github/workflows/release.yml:32`) is set and correct.

**Why it outranks everything cosmetic:** if it is unset, every panel reads zero — **and zero is
indistinguishable from "nobody activated."** We would conclude we have an activation problem and
grind on onboarding for weeks while measuring nothing. Note the telemetry disclosure is **not** a
canary: with no key it shows nothing *and* sends nothing, consistent with each other and silent
about the fault.

**Recommendation:** unanimous. **Blocked until you answer:** the activation gate, therefore the
launch gate, therefore every channel.
**If you do nothing:** we ship a disclosure describing a pipeline that goes nowhere, and every
number produced this quarter is uninterpretable.

## 🔴 D3 — Make one real test purchase

**Decision:** buy one license through the live store and confirm the campaign `custom_data` reaches
the webhook.

**Why:** without it, every supporter sale is unattributed and D-series channel decisions have no
revenue signal. This is the only place a channel connects to money.

**Recommendation:** Growth Engineer, endorsed. **If you do nothing:** we can measure which channel
produces supporter *intent* but never which produces *sales*.

## D4 — PyPI `agent-coral`: yank, deprecate, or final release

**Decision:** yours alone — external and irreversible.

**Evidence:** `agent-coral` is live on PyPI at **4.4.1** (uploaded 2026-03-21). We ship **v1.0.8**.
The abandoned product has the **higher version number**, so anyone comparing concludes ours is
stale. `pip install agent-coral` **succeeds** and installs real, unmaintained software.

**Options:** yank · deprecate · publish a final release whose description points at the Go product.
⚖️ **Recommendation (GTM Strategist):** the final-release-pointer is the least destructive and the
most useful to someone already on it.
**If you do nothing:** the wrong-product funnel stays open even after the docs site is fixed.

## 🔴 D5 — Approve the gh-pages replacement — with an archive tag as a precondition

**Decision:** approve the drafted replacement page (`specs/gtm/drafts/gh-pages/`) and **tag the
current gh-pages HEAD as `gh-pages-mkdocs-archive` before overwriting it.**

**Evidence:** the live docs site tells every visitor to run `pip install agent-coral`. It has no
mention of the DMG, Go, or GitHub Releases. The README linked it from four places.

**The precondition is not optional:** the MkDocs source that built that site is **not an ancestor of
main** and cannot be reconstructed from the current repo. Overwriting without tagging destroys it
permanently.

**Recommendation:** Content Producer, endorsed. **If you do nothing:** every channel routes some
traffic to an installer for the wrong product.

## D6 — Per-agent worktrees: change the docs, or change the code

**Decision:** the README described per-agent worktree isolation; the product does not do it. Pick a
direction.

**Evidence:** `sessions.go:2438` keys the worktree on **board name**; `:2439` creates one branch
`coral-team/<name>`; `:2449` assigns it to every agent. It is also **off by default**
(`modals.html:240` has no `checked` attribute). Demo 1 proved the consequence: two agents wrote the
same function into the same file and **broke the build**. On the default path that collision happens
in the user's **real checkout**.

⚖️ **Recommendation (GTM Strategist, with Dev Advocate neutral):** **change the docs.** Per-agent
branches sound better and are worse — you get N branches and a merge step nobody has built. Per-team
is coherent: agents collaborate on one branch and coordinate through the board.

**Important:** this decision does **not** block the README fix. The **product's own UI copy is
already correct** (`modals.html:243`), so only the README overclaimed. Approve D8 now regardless.

## D7 — Strike or replace Phase 5 line 145 of your own growth plan

**Decision:** `GROWTH_AND_SUPPORTER_SALES_PLAN.md:145` lists as a demonstration topic: *"Use
isolated worktrees to prevent agents from overwriting one another."* **That capability does not
exist on the team path**, and this line is plausibly where the false README claim originated.

⚖️ **Recommended replacement (adopted by the Orchestrator):** *"Run agents from three different
vendors on one repo and watch them hand work to each other on the message board."* Demonstrable
today, and it is the actual wedge.

🔴 **Note — CORRECTED, and the correction matters for how you decide this.** An earlier version of
this note told you Coral has two worktree behaviours and that the jobs path delivers genuine
per-agent isolation, on by default. **That was wrong, and if you read it you may be considering
retargeting the demo at jobs rather than striking it. Do not.**

Coral does have two worktree behaviours: teams share one worktree (off by default), while scheduled
jobs and API tasks construct one **per run** (`scheduler.go:272`, `:550-551`; `tasks.go:109`). The
per-run mechanism is correctly written. **It never runs.** `agent_docs/jobs.md:32` documents
`base_branch` defaulting to `main`, and `git worktree add` refuses a branch already checked out in
the user's own working copy — so **a user following our documentation exactly gets a job that fires
on schedule and fails 100% of the time**, silently, because the scheduling half works and the UI
shows the job healthy. Task #43, one-line fix (`-b` or `--detach`).

> **The accurate statement: there is currently no path in Coral that delivers working per-agent
> worktree isolation.** Teams share one; jobs would isolate and fail before they get there.

**Strike the demo topic. There is nowhere to retarget it to today.**

⚖️ **Why this correction is worth your attention beyond the decision itself:** three of us
independently concluded the jobs path worked — by reading the actual worktree construction and the
per-run keying, not merely a route number. It failed one line earlier, on a git call, for a reason
that depends on **the state of the user's repository rather than on the code at all**. No amount of
more-careful reading reaches it. This is the strongest single argument in this document for
verifying claims by running the product.

## D8 — Approve the README patches, in order

Seven prepared patches, all `git apply --check` clean, **none applied**.

| Order | Patch | What |
|---|---|---|
| 1 | `README-P0-worktree-isolation-claim.patch` | 🔴 the safety claim |
| 2 | *(applied, uncommitted)* | download filenames that never existed |
| 3 | `README-docs-links.patch` | four links off the wrong-product site |
| 4 | `README-remove-any-cli-agent-claim.patch` | unsupportable extensibility claim |
| 5 | `README-hero-A` **or** `README-hero-B` | see D9 |
| 6 | `README-discord-badge-fallback.patch` | badge renders the word `invalid` |

`README-COMBINED-apply-all.patch` exists if you would rather approve once.

⚠️ **A seventh patch was added after the combined patch was sealed** —
`README-cut-fts-search-claim.patch`. It cuts the full-text search claim from **both** places it
appears: `README.md:119` (the features table) and `README.md:135` (the comparison table's "Search
chat history ✓" row). The feature has never worked — Part 4, task #40.

**Apply it as a separate line, not folded into the combined patch.** The combined patch was verified
byte-identical and handed over; silently changing what an approved artifact contains, under the same
name, would be a verification scoped to a state that then changed. You get one more line to apply,
not a redefined patch.

⚖️ **The `:135` instance is the worse of the two** and was nearly missed: it claims a dead capability
as an **advantage over four named competitors**, which invites a reader to check it against tools
where the feature actually works.

**Patch 1 is the only item in this entire brief where a user acting on our current copy can lose
work in their own repository.** Everything else costs them time or sends them to a wrong download.
**If you approve one thing today, approve patch 1.**

### 🔴 Do not conclude the README is true once these land

**Approving every patch fixes every claim we proved FALSE. It does not make the front page
verified.** The patches were scoped to disproven claims; the features table contains a different
category — **claims nobody has ever executed** — and no hunk touches them. What survives the full
patch set:

| Row still standing | Entire evidence base |
|---|---|
| **Workflows** — "multi-step agent pipelines that run automatically… with dependencies" | route `:440` |
| **Scheduled jobs** — "on a cron schedule in isolated worktrees" | route `:429` |
| **Team templates** — "save and share… generate teams from plain-English descriptions" | route `:484` |
| **Webhooks** — "notifications to Slack, Discord, or any HTTP endpoint" | route `:466` |
| **Task management**, **Git integration** | never examined |

**Every one traces to a route number — the evidence base that failed twice today** (FTS had a
virtual table, an upsert function and a tokenizer; Codex cost extraction has a function that never
ingests). A registered route proves a handler is wired, not that the feature works. Task #42 is
exercising the first four now.

**One row is probably not merely unverified but wrong.** *Token tracking — "see cost and consumption
in real time"*: per-agent tracking works, but a mixed team silently omits Codex (#41), so over a
mixed team that figure looks complete and is not. It is the same defect as D-series #41, sitting in
the table a reader consults to learn what the product does.

**And one is right by accident.** *Scheduled jobs — "in isolated worktrees"* happens to be **true**,
because jobs really are per-run (`scheduler.go:551`). It survived the day because nobody checked it,
not because anyone confirmed it.

> ⚖️ **The accurate summary of the patch set is: every claim we proved false is fixed; seven rows
> remain unverified and one is probably inaccurate.** (Finding and wording: Content Producer.)

**No further patches are proposed while #42 is in flight** — cutting seven rows that may all turn out
to work would be exactly the overcorrection this brief warns about in Part 3.

## D9 — Hero image, Discord guild ID, email list

Three small unblocks:

- **Hero:** the README's largest above-the-fold element is a **403** — the Loom thumbnail is blocked
  even with a browser user-agent, so GitHub's proxy gets the same. Patch **A** removes it; patch
  **B** swaps in a self-hosted asset. ⚖️ Prefer **B** if an asset exists, **A** today if not — a
  broken image is worse than no image.
- **Discord guild ID:** the badge uses `discord/placeholder` and renders "Discord | invalid". A
  numeric guild ID cannot be derived from an invite code. Fallback prepared if you don't have it.
- **Email list:** does one exist? Several drafted assets assume an answer.

## D10 — A clean macOS user account (zero cost)

Closes two verification gaps immediately: the **first-run EULA has never been observed by anyone**
(it was pre-accepted on our only machine), and the Finder drag-to-Applications flow is unverified.

## D11 — A `windows-latest` and a Linux CI smoke job

⚠️ **This decision's summary sentence was wrong and contradicted its own next line.** It read *"the
blocker on both platforms is verification capability, not code"* — while the sentence immediately
below it said Windows does not compile, which is a code blocker. Corrected below; the two platforms
are not the same problem and lumping them understated Windows.

**Linux — a verification blocker.** The binary is built and statically linked. Nobody has ever
**executed it**. One CI smoke job or one machine closes it.

**Windows — three unknowns stacked, not one scoped fix.** Each is independent and all three must
clear:

| # | State |
|---|---|
| 1 | **No Windows artifact has ever been built.** No MSI has ever existed |
| 2 | **The server does not compile for Windows.** One file, six call sites, needing a `_unix.go`/`_windows.go` split |
| 3 | **The terminal backend Windows would default to — native PTY — has never been observed working by anyone** |

**On (3), the honest statement for you, in the Developer Advocate's words:**

> The native PTY backend has never been observed working by anyone on this team. On our only test
> machine it fails at process spawn with `EPERM`, which is **consistent with a sandbox restriction
> rather than a code defect** — but we cannot tell those apart from here. It is not known-broken and
> it is not known-working. **It is unexercised.**

That is stronger than "unknown" because it says *why* it is unknown and what resolves it: one launch
on an unrestricted macOS or Windows machine, about a minute's work.

⚖️ **Two CI jobs remain the cheapest path to honest platform claims**, and for Linux that is the
whole fix. For Windows a green build would clear (1) and (2) and leave (3) untouched — a compiling
binary whose terminal has never run is not a supported platform. **Windows stays out of all copy,
and the reason is now three-deep rather than one.**

## D12 — Seven subreddit sidebars (human-only, ~20 minutes)

**Blocks any Reddit post.** Reddit refuses programmatic fetch and no browser was available, so all
seven candidate subreddits are marked **UNVERIFIED** in `CHANNEL_PLAN.md` §3.5. Someone with a
browser must read each sidebar and paste the verbatim self-promotion rules in.

I would rather hand you a marked gap than a guess. **If you do nothing:** no Reddit channel opens.

## D13 — Resource Phase 3 interviews ahead of Wave 3

**Decision:** whether user interviews get priority over demand generation.

**Evidence — the arithmetic:** at the plan's own conversion targets, 10 supporter sales needs
**~16,665 qualified visitors**. At 100/week that is **over three years**.

⚖️ **Recommendation (GTM Strategist):** the first ten sales will not come from the channel funnel.
At 31 stars and 1 recorded download, early supporters are people who talked to the maintainer. The
kit is built and waiting (`USER_INTERVIEW_KIT.md`).

**This does not mean the goal is wrong** — it means the funnel is the wrong instrument for the first
ten, and the right instrument for deciding what to fix next.

## D14 — `.claude/skills/release.md` still publishes `agent-coral` to PyPI

Lines 55–89 are a `twine upload` runbook for the legacy package. **Running that skill ships another
legacy release.** The team has been told not to invoke it; it needs deleting or rewriting.

**Evidence tier: 📖, and deliberately so.** Nobody has executed this and nobody should — the only way
to confirm it publishes would be to publish. This is the one place in the brief where "verified by
reading" is the correct and final answer.

## D15 — `CLAUDE.md` is wrong about two things, and it is the first file a contributor reads

Neither is launch-blocking; both are one-line fixes. Nobody on the team edited it, correctly — the
standing rule limits us to `coral-go/` and `tests/`, and `CLAUDE.md` is project instructions.

- **Project-structure block lists `Formula/  # Homebrew Formula`.** That directory does not exist and
  **should not** — Coral ships a `.app`, which is a **cask**, not a formula.
- **Build-tier table says Prod requires a license.** It does not. Nothing is gated
  (`license/middleware.go:18-19`, `server.go:245-246`). **Evidence tier: 📖** — read by two people,
  never tested by hitting a gated-looking route on a prod build without a licence. It is on the
  Developer Advocate's verification queue, and it is closeable on hardware we have.

⚖️ **Why it is worth your minute:** this is the file that seeds a new contributor's — or a new
agent's — model of the product. The licence line in particular is the premise behind every
"what does $49.99 buy" answer, and today it was wrong.

---

# Part 1b · What the next release inherits — an obligation with no detector

**This is not a decision. It is a job that attaches to whoever ships the next release, and nothing
in the repository will remind them.** It is here rather than in a ledger header because this is the
document you actually read.

Every corrected row in the README is correct **because of a working ruling**, not because of the
code: *the README describes what a user can download*, and today that is **v1.0.8**.

Four verified fixes have already landed on branches since — #26 (supporter reminder moved behind
value), #31 (instance isolation), #33 (blocked-agent surfacing), #43 (scheduled jobs). Each row we
cut was cut on the grounds that **the shipped product fails**. Every one of those grounds now has a
repository reading where it does not.

**Nothing in those commits is wrong today. If the ruling changes when a release ships, their
correctness changes without a single line of them being edited.**

⚖️ **This is the inverse of every staleness we caught today, and it is the only one with no
detector.** The others were artifacts drifting away from a fixed truth — findable by a diff, a grep,
or a verifier. This is **an artifact holding still while the truth moves underneath it.** No diff
fires, because the file is unchanged. No grep fires, because no banned phrase appears. The verifier
passes, because the change is external to everything it can read. (Shape identified by the Content
Producer.)

### The concrete obligation

**At the next release, revisit these rows** — they may become claimable, and nothing will prompt it:

| Row | Becomes true when |
|---|---|
| Scheduled jobs — *"in isolated worktrees"*, and the feature at all | #43 ships in a release |
| Supporter reminder timing claims | #26 ships |
| Anything describing multi-instance safety | #31 ships |
| First-run experience claims | #33 ships |

**The failure mode if this is missed is silent and one-directional:** the README simply understates
the product, indefinitely, and no reader or tool ever complains. That is the correct direction to be
wrong in — a reader can check the download and cannot check our branches — but it is only correct
*temporarily*, and "README understates main" is fine for days and corrosive for months.

---

# Part 2 · What shipped in Wave 1

**Nothing external has shipped. No push, no post, no deploy, no email.**

## Code — merged to a branch, **not pushed** (4 commits ahead of `origin/main`)

| Commit | What | State |
|---|---|---|
| `84a0338` | Complete Phase 1 funnel instrumentation — 11 events, once-only milestones | ✅ tested, unpushed |
| `780c8d7` | Campaign attribution on in-product supporter links | ✅ tested, unpushed |
| `b58a382` | First-run telemetry disclosure + `agent_docs/telemetry.md` | ✅ built — **ship-gated on D2** |
| `03cc533` | Homebrew cask repair | ✅ unpushed |

`go test ./...` passes apart from two failures that pre-exist on `main`, verified identical on a
clean `main` worktree.

**A control worth knowing about:** `AllEvents` in `tracking/events.go` is the single source of truth
for the disclosure, enforced by tests in both directions. **Adding an event without disclosing it
now fails the build.**

## Documents

| Owner | Artifacts |
|---|---|
| GTM Strategist | `POSITIONING_BRIEF`, `CHANNEL_PLAN`, `LAUNCH_SEQUENCE`, `OBJECTION_FAQ`, `METRICS_FRAMEWORK`, `ACTIVATION_PAGE_COPY`, `COMPARISON_PROSE`, `USER_INTERVIEW_KIT`, `INTERVIEW_SYNTHESIS_TEMPLATE`, `POSTHOG_DASHBOARD_SPEC`, `WEEKLY_OPERATING_LOOP` |
| Content Producer | `LAUNCH_CHECKLIST` (rev 14+), `README_DEFECTS` (18), `REPO_SWEEP_RETRACTED_CLAIMS`, `TELEMETRY_DISCLOSURE`, `README_ABOVE_THE_FOLD`, gh-pages draft, 7 patches |
| Dev Advocate | `INSTALL_VERIFICATION`, verified quickstart, demos 1–3 |

## Prepared, awaiting you

Seven README patches · gh-pages replacement · telemetry disclosure · activation-page copy.

## Blocked

Windows (does not compile, no machine) · Linux execution (no machine) · Reddit (D12) · all
outbound copy (D8) · every funnel number (D2).

---

# Part 3 · What we learned that changes the plan

## 1. The funnel arithmetic does not reach the goal — and that is useful

10 sales ← 40 checkout clicks ← 533 retained ← 1,333 activated ← 3,333 installs ← **16,665
visitors**. Over three years at 100/week.

⚖️ Either the assumptions are pessimistic for a niche high-intent tool — **now finally measurable**,
since every stage is instrumented — or the first ten sales come from conversations, not channels.
The arithmetic's real job is to **refuse "we need more traffic"** as an answer before the funnel
converts. See D13.

## 2. Three instrumentation problems masqueraded as product signals

This is the pattern I would most want you to take away.

| # | Looks like | Actually is |
|---|---|---|
| B11 | "Nobody is activating" | Telemetry may deliver nowhere (D2) |
| B12 | "Users lose interest after launching" | Agents freeze on an invisible trust prompt (#33) |
| D1 | "40% activation — gate cleared" | 40% clicked Launch; unknown how many got anything |

**All three fail in the same direction: they make a broken product look fine.** The mitigation is
built into `POSTHOG_DASHBOARD_SPEC.md` Panel 0 — `app_opened` fires unconditionally on every server
start, so `app_opened > 0` with activation `= 0` proves a *real* activation problem, while
`app_opened = 0` proves a dead pipeline. **That one panel separates two failure modes we could not
distinguish all day.**

## 3. We have zero fully-verified install paths on a clean machine

**macOS is the strongest and it is still partial:** the published DMG is signed and notarized, the
binary is universal, and a 90-second run to a committed, tested change was measured on it — but the
**Finder flow and the first-run EULA have never been observed** (D10). Linux has never been
executed. Windows does not compile. Homebrew was broken and is repaired but unproven.

**This is why no install instruction has been published.**

## 4. The claim-defect pattern — and why static review missed all of it

**Three false public claims, all found by *running* the product after a full day of static review
found none.**

- *"Any CLI-based agent can be added"* — never true. Hardcoded four-case switch, no registry.
- *"Each agent in its own git worktree… without merge conflicts"* — a **safety** claim, disproven by
  a broken build.
- *"Sleep… come back tomorrow"* — untested at the time. **Since verified**, including a server
  restart.

⚖️ **The generalisable finding:** in every case the overclaim sat on top of a **better, checkable
claim** — four named agents from three vendors; a team branch plus board coordination; a verified
server-restart wake. The true version was always more specific and harder to disprove.

**A fourth, found last:** `README.md:95` — *"Messages are delivered reliably with cursor-based
tracking — **nothing is lost** across agent restarts."* The mechanism is real (`board_subscribers`
has a persisted `last_read_id`, `store.go:147`, `:397`, `:523`), so the defensible version is *"your
read position is remembered across an agent restart."* But **"nothing is lost" is an absolute**, and
we already have the counterexample: with `CORAL_PORT` missing from the agent launcher env (#34),
`coral-board` talks to port 8420 regardless, so on any non-default port messages are — from the
agent's side — entirely lost.

**It survived every sweep today** because our grep patterns say *"survives a restart"* and this says
*"across agent restarts"*. Which is the real lesson: **a do-not-say list matches strings; claims
travel as meanings.** Three claims escaped by paraphrase today. The list now requires that adding a
banned phrase also means listing the paraphrases you would naturally reach for.

### Ways a check returned a confident wrong answer today — and the one rule that fixes them

⚖️ Worth stating together, because each produced a number or a pass that *looked* like verification.
The first three are diagnoses; the fourth is the fix.

| Form | What happened |
|---|---|
| **A verification that cannot fail is not a verification** | A grep for a claim's *string* passes while the claim survives as a paraphrase |
| **A pattern that approximates the thing you are counting is not the thing you are counting** | Correcting "fourteen decisions", I grepped `^## D` to check the count and got **sixteen** — the extra was `## Documents`. Trusting it would have replaced one wrong number with another |
| **Confirming a mechanism exists tells you nothing about whether it runs** | The FTS5 table and tokenizer are genuinely at `connection.go:175`. Nothing has ever been written to them |
| ✅ **Assert on a positive quantity the check should produce — not on the absence of failures** | `broken == 0` is satisfied by a check that ran **zero comparisons**. `total == 30 AND broken == 0` is not. A link check that silently matched nothing reported "all links resolve" (Developer Advocate) |

**The fourth is the actionable one** and it is what makes the others catchable: a check that cannot
distinguish *passed* from *did-not-run* is not a check. The same shell quirk hit three people today —
once producing a false **pass** (silence read as clean) and once a false **fail** (a nonsense
filename). The identical defect was caught in seconds or not at all, depending only on which
direction it broke. **Design checks to fail loudly.**

**The third diagnosis is the one no amount of more-careful reading closes.** The first two are
catchable by reading harder; that one required opening the product.

**And the hardest class:** *"isolated worktrees"* is **true** on the jobs path and false on the team
path. Anyone spot-checking it would have confirmed it and moved on — correctly, about the wrong
path. **Verification succeeding is not the same as the claim being right.**

---

# Part 4 · Claims ledger

Maintained in `LAUNCH_CHECKLIST.md`. **✅ = seen running. 📖 = verified in code only. A 📖 claim may
be drafted; it may not ship.**

## ✅ Seen — cleared for copy

🔴 **This list was missing a field, and the omission matters more than it looks.** Every entry says
*verified* without saying **which artifact it was verified against**. Four of the Growth Engineer's
P0 fixes now exist on unpushed branches, so "verified" has split into two different claims:

> **Verified on the shipped v1.0.8 DMG** — true for a reader who downloads today.
> **Verified on a branch build** — true for the repository, and **false for every artifact a user
> can obtain.**

A ledger that does not distinguish them will be read as the first and may mean the second. Per the
Orchestrator's working ruling — *the README describes what a user can download* — only the first
kind clears copy today.

| # | Claim | Verified against |
|---|---|---|
| 1 | macOS build signed and notarized | ✅ **shipped v1.0.8 DMG** (`spctl` on the published artifact) |
| 2 | Universal binary, Intel + Apple Silicon | ✅ **shipped v1.0.8 DMG** |
| 3 | Agent launch in 0.58s; dashboard same-second | ✅ **shipped binary** — note the *backend* label on that run was later disproven; the timing stands, the PTY inference does not |
| 4 | Apache 2.0 | ✅ `LICENSE` |
| 5 | **Four agents from three vendors on one board** — the wedge | ⚠️ **artifact not recorded** |
| 6 | Each **team** gets its own worktree and branch — *when enabled; off by default* | ⚠️ **artifact not recorded** |
| 7 | Mixing vendors in one team | ⚠️ **artifact not recorded** |
| 8 | **Sleep a team, quit Coral, restart it, wake it with context intact** | ✅ **shipped v1.0.8 binary**, explicitly |
| 9 | Under two minutes to a committed, tested change — *once the agent CLI is installed and authenticated* | ✅ **published artifact, not a local build**, explicitly |
| 10 | Workflows chain tasks across agents | ⚠️ **artifact not recorded** (#42) |
| 11 | Templates — generate from a description, import from a folder | ⚠️ **artifact not recorded** (#42) |
| 12 | Nothing is gated | ⚠️ **a prod-tier build**, not the shipped DMG |

⚖️ **I am not guessing at the six unrecorded rows.** The Developer Advocate ran them and is the only
one who knows which binary each used; asking is cheap and inferring is exactly what produced three
retractions today. **Rows 5 and 7 matter most — they are the wedge.**

**If any of them turn out to have been run against a branch build, they do not thereby become
false** — they become claims about the repository whose status for a downloading reader is unknown.
That is a different verdict from ⛔ and should not be collapsed into one.

## 📖 Sourced but never seen — may not ship

1. Linux static binary runs on musl/old distros — **nobody has run it**
2. Nothing is gated / genuinely free
3. Coral holds no API keys and never calls a model on your behalf
4. Features-table claims (workflows, scheduled jobs, webhooks, templates)

## ⛔ Retracted

1. "Any CLI-based agent can be added"
2. "Each agent in its own git worktree" / "without merge conflicts"
3. "Coral doesn't call any AI APIs itself" — imprecise; corrected
4. Comparison table (README:127-138) — 5 of 10 rows false or stale
5. "Survives reboots" — **still banned**; only a *server* restart is verified
6. "Nothing is lost across agent restarts" (`README.md:95`) — absolute claim with a known
   counterexample; **P1, belongs with the #34 fix**
7. 🔴 **"Full-text search across all past sessions" (`README.md:119`) — HAS NEVER WORKED.**
   `FTSBody` is declared (`agent/agent.go:66`) and read (`indexer.go:114-115`) but **never assigned
   anywhere**, so the guard never passes and nothing is ever indexed. Production: **56 sessions,
   zero FTS rows**, for months. An advertised feature that has never worked for anyone. Task #40 —
   **the README claim is being cut**
8. 🔴 **"Scheduled jobs run in isolated worktrees" — the isolation half FAILS on the documented
   defaults.** The cron half is verified true. Per-run worktree construction is correctly written and
   never reached: `git worktree add` refuses `main`, already checked out. **100% failure, silent** —
   the UI shows the job healthy. Task #43. *Three of us concluded this path worked by reading a
   correct mechanism.*
9. 🔴 **"Cost tracking across vendors" — DOWNGRADED.** Cost tracking works and renders real numbers;
   the *cross-vendor* figure does not. Controlled test: Claude reported **$0.97**, Codex reported
   **nothing**. Task #41. **The total looks complete and is not — worse than showing nothing.**
   Approved wording is now "token usage and cost per agent and per session"

**Counts: 9 ✅ · 4 📖 · 9 ⛔.**

### Two ledgers, both correct — read this before comparing them

`LAUNCH_CHECKLIST.md` keeps its own ledger, and its numbers differ from the ones above. **Both are
right** — they count different populations, and the difference is more interesting than either
number.

*(This document deliberately does **not** restate the checklist's counts. It is a living document
with its own maintainer, and a number copied here would be correct on the day it was copied and
silently wrong afterwards — which is precisely how the two ledgers came to disagree in the first
place. Read its counts there.)*

| Ledger | Counts | Role |
|---|---|---|
| `LAUNCH_CHECKLIST.md` strengths table | **Candidate strengths only** — claims we considered *using in copy* | the copy-assembly gate |
| **This document, Part 4** | **All claims examined**, including ones that were only ever false | **canonical for you** |

**The entire gap sits in ⛔, and it separates two very different things:**

> **Of everything we nominated for copy, two did not survive execution. Of everything the product
> publicly claimed, six did not.**

Four retractions never appeared in the candidate table at all — "any CLI agent", per-agent worktree
isolation, "without merge conflicts", "Native desktop app (macOS & Linux)". Those were **inherited
defects found in existing copy**, never things we proposed to use. The other two — full-text search
and cross-vendor cost — were claims the gate had marked **📖 (verified in code, never observed)**.

⚖️ **That split is the strongest argument for the SEEN gate**, better than either raw number — and
it is worth being precise about what it shows, because the obvious reading is wrong.

**The gate did not let those two through. It quarantined them.** 📖 claims may be drafted and may
not ship, so neither ever reached copy; execution then confirmed why the gate was right to hold
them. The four inherited defects were all findable by **reading** — a switch statement, a checkbox
missing a `checked` attribute, a tarball listing. The two the gate held were findable **only by
running**: `indexer.go` reads like working search machinery, and the Codex cost-extraction function
exists, so reading tells you both features work.

**A gate that only caught readable defects would have caught four and shipped two. This one caught
four and quarantined two.** That is the gate working as designed, not failing.

*(Framing: Content Producer. Reframe: Developer Advocate — I had originally written this as "our own
gate let two through", which was self-critical and inaccurate. Both directions of bias are
available.)*

### Six differentiators in, three out

| # | Differentiator | Verdict |
|---|---|---|
| 4.1 | Four agents from three vendors on one board | ✅ **survives** — the wedge |
| 4.2 | Sleep a team, restart Coral, wake it with context | ✅ **survives** |
| 4.3 | One browser tab for every agent's live terminal | ✅ **survives** |
| 4.4 | One cost figure across vendors | ⚠️ **downgraded** — per-agent cost only (#41) |
| 4.5 | Per-agent worktree isolation | ⛔ **disproven** — reframed onto coordination |
| 4.6 | Full-text search across sessions | ⛔ **disproven** — never worked (#40) |

⚖️ **Three of six.** Every loss was found by *running* the product; static review of the same six
found none of them. **4.6 is the sharpest case:** the FTS5 virtual table is genuinely there, with a
genuinely correct tokenizer, at a real line of code I cited accurately. Reading the code confirmed
the mechanism existed and told me nothing about whether it ran.

**The three survivors are enough.** The wedge — heterogeneous agents on one board — is intact and
verified, and it is the one no single-vendor tool can match. ⚖️ A high 📖 count is honest. A *falling* one is progress.

---

## One thing I would ask you to notice

Three people independently found something we publicly claimed that was not true, and each reported
it rather than quietly fixing or defending it. Two of them — including me — found errors in their
**own** finished work and said so on the board.

The controls that came out of that are mechanical rather than aspirational: a test that **breaks the
build** if an event ships undisclosed; a **SEEN column** that stops an unobserved claim reaching
copy; a dashboard whose panel definitions make the activation-metric mistake **impossible rather
than warned against**.

⚖️ That is the part of Wave 1 I would most want to keep. The findings are worth less than the habit
that produced them.
