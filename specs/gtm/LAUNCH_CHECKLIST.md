# Coral Launch Checklist

**Owner of this file:** Content & Launch Producer
**Status:** Wave 1 — nothing external ships
**Last updated:** 2026-08-28 (rev 40)

> **Hard gate (operator decision, settled):** No Show HN, no Reddit, no X thread, no
> newsletter until Phase 2 activation targets are hit on real data — 40% of new installs
> launch an agent, 20% launch a team, 10% complete a task. All launch copy is **drafted
> only**. There is no launch date.

---

# ⛔ DO THIS AT RELEASE TIME — README rows that go stale when a release ships

**If you are cutting a release and have read nothing else in this file, read this.**

The README currently describes **v1.0.8**, the artifact a user can download. That was a
deliberate ruling, not an accident. Several rows were **cut or narrowed because the shipped
product fails**, while the fix already exists on a branch.

**The moment a release ships containing those fixes, these rows become wrong by omission —
and nothing in the repository will tell you.** The files will not have changed. No diff, no
grep, and no verifier detects this, because the artifact is unchanged and the change is
external to it. This is the one staleness with no automated detector.

| README row | Why it was cut/narrowed | Restore when |
|---|---|---|
| **Scheduled jobs** (row removed entirely) | On v1.0.8, `git worktree add <dir> main` fails whenever the user's own copy has the base branch checked out — and the agent launch is **gated behind worktree creation**, so no agent ever runs on the documented default | the #43 fix is **in a shipped release** (not merged — released), **and** ledger row 5 is re-verified by someone who did not write the fix, **and** the wording names which path |
| **Webhooks** | Loopback/private refused at send time; `CreateWebhook` is unguarded so a local webhook saves and never fires | delivery is observed by a human against a real external endpoint — note this is **structurally unverifiable** from a sandboxed environment, so it needs a machine that can reach the internet |
| **Token & cost tracking** | Codex usage is not ingested, so a mixed-team total looks complete and is not | #41 ships **and** a mixed-team run shows both vendors' costs |
| **Git integration** | ~~Never verified~~ — **now ✅**, all three nouns, via `git_changed_files` and `/files` on a terminal session | no action; kept for the record |
| **Full-text search** (row currently CUT) | `FTSBody` was declared and read and **assigned by nothing**, in all four extractors — `session_fts` empty since creation. Production still shows **0 rows**. Fixed by `fe67896`, which also **backfills**: without it only 3 of 25 pre-existing sessions became searchable and the rest would have stayed invisible forever | **all three, not "the fix landed"**: (a) `fe67896` is **in a shipped release** — verified **0 tags contain it** as of 2026-08-28; (b) the backfill has run, which is one indexer pass after upgrade, not user action; (c) note `MEMORY-TOKEN-7731`-style **hyphenated terms still return 0 unquoted** — bare hyphens are FTS5 operators. That is pre-existing sanitizer behaviour, not part of this fix, and *"search across all past sessions"* is true despite it |

### ⚠️ "Free and fully unlocked" has a specific way to become false silently

This is the claim the entire free/supporter story rests on, and it is ✅ verified today. **The
risk is not that it decays — it is that someone implements a mechanism the comments already
describe.**

`config.go:104` and `tier_prod.go:7` both state that prod demo limits are *"controlled by
runtime LS plan"*. **No such mechanism exists.** `MaxLiveTeams`/`MaxLiveAgents` are written in
exactly one place (`config.go:106-107`), guarded by `TierDemoLimits`, which is `false` in prod,
and nothing reads a Lemon Squeezy plan. `tier_test.go:22` encodes the same belief in its
assertion message while asserting the flag is false.

**"Unlimited teams and agents" is true *because* the mechanism is absent.** So the comments
document an intention as though it were behaviour — and the next person to read them will
reasonably believe prod limits are already wired to the LS plan.

**The hazard:** if anyone implements that wiring, or flips `TierDemoLimits` in prod, then
*nothing is gated*, *free and fully unlocked*, and *unlimited teams and agents* all become
false at once — across the README, the activation page, the FAQ and the positioning brief —
**without a single line of copy being edited**. Nothing in this checklist or any verifier
would fire.

**Before shipping any release that touches `internal/license/` or the tier files:** re-verify
that `middleware.go` still passes every request through and that prod still zeroes both limits.
That check is one command and it protects five documents.

**Also revisit at release time:** the first-run experience fixes (#26 nag, #33 trust prompt),
`#31` tmux isolation and `#34` board port — all real, all invisible to a v1.0.8 user, and all
absent from the README **deliberately**.

**The general obligation:** when a release ships, re-run the shipped-artifact test on every
row above. The README's correctness is currently a function of *which build we decided to
describe*, not only of its own contents.

---

## Verified baseline (checked in-repo / against GitHub on 2026-08-28)

These are measured facts, not estimates. Re-verify before any copy ships.

| Fact | Value | Source |
|---|---|---|
| Latest release | `v1.0.8`, published 2026-08-27 | `gh release list` |
| Release assets (v1.0.8) | `Coral.v1.0.8.dmg`, `coral-linux-amd64-1.0.8.tar.gz` | `gh release view v1.0.8` |
| Windows assets in latest release | **none** | `gh release view v1.0.8` |
| GitHub stars / forks | 31 / 6 | `gh repo view` |
| Repo created | 2026-02-18 | `gh repo view` |
| Downloads, v1.0.8 | DMG 0, Linux tarball 1 | `gh release view v1.0.8` |
| Docs site | live, HTTP 200 | `curl cdknorow.github.io/coral/` |
| Supporter checkout | live, HTTP 200 | `curl store.coralai.ai/...` |
| License | Apache 2.0 | `LICENSE` |
| Price | $49.99 one-time, optional; product free and unlocked | `README.md`, growth plan |
| Telemetry events (after #18) | `install`, `app_opened`, `session_launched`, `team_launched`, `first_agent_launched` (sessions.go:2367), `first_team_launched` (:2553), `first_task_completed` (board.go:895), `returned_24h` (tracking/milestones.go:171), `supporter_checkout_clicked` (routes/tracking.go:57), `license_activated` (license/middleware.go:78) | Growth Engineer #18 |
| **A cask file in this repo is NOT an install path** | Homebrew now **refuses** casks not in a tap — `brew install --cask ./Casks/coral.rb` is rejected outright. `Casks/` has never been a distribution channel, however correct the file | Growth Engineer #21, verified |
| Homebrew copy — **the only permissible form**, and not until the operator pushes the fixed cask (the remote cask 404s today) | `brew tap cdknorow/coral https://github.com/cdknorow/coral` then `brew install --cask cdknorow/coral/coral`. The explicit URL is **required** — the repo is not named `homebrew-*`. Never plain `brew install coral`. Trade-off: the tap clones the whole 154MB repo, so it is slow | Growth Engineer #21, verified working |
| homebrew-cask notability — **two different bars, keep both** | **75** stars = *third-party* submission. **225 stars / 90 forks / 90 watchers (any one)** = *self*-submission, which is what Coral submitting Coral is. We are at **31 stars, 6 forks**. Upstream is not a milestone — **the tap is the channel** | GTM Strategist + Growth Engineer, settled |
| Build-time PostHog key | `config.PostHogKey` injected via ldflags; source builds send nothing | `coral-go/internal/config/config.go:11-12` |

---

## Verified strengths — cleared for copy

The defect lists are long, so this table exists to stop the audit becoming the house style.
Everything here is verified, attributed, and safe to write. **This is the raw material the
launch copy gets built from** — a claim is only allowed in an asset if it appears here.

**GATE (Orchestrator, adopted from the Strategist):** *every differentiator must be
DEMONSTRATED ON A RUNNING BUILD before it ships in copy.* Sourced is not seen. Static
verification confirms a mechanism is **present**; only execution reveals its **shape**, and
every claim we make is about shape. Today we confirmed four agents by reading and got the
worktree count wrong by reading.

### What this table counts — and why it differs from the exit brief

Two ledgers exist and they count **different populations**. Both are correct; neither is
wrong. Stated here so nobody has to derive it:

| Ledger | Population | Counts |
|---|---|---|
| **This table** | **Candidate strengths only** — claims we considered *using in copy* | 10 ✅ / 4 📖 / 2 ⛔ |
| **`WAVE1_EXIT_BRIEF.md` Part 4** | **All claims examined**, including ones that were only ever false | 9 ✅ / 6 📖 / 6 ⛔ |

The gap is entirely in the ⛔ column, and it is meaningful rather than an accounting quirk.
Four retracted claims **never appeared here at all** — "any CLI agent", per-agent worktree
isolation, "without merge conflicts", "Native desktop app (macOS & Linux)" — because they
were inherited defects found in existing copy, never candidates I was proposing to use.
Only two retractions were things this table had already cleared as candidate strengths:
**full-text search** and **cross-vendor cost**.

**That distinction is the one worth reading:** of everything this table nominated for copy,
two did not survive execution. Of everything the product publicly claimed, six did not.

**The exit brief is canonical for the operator.** This table is the copy-assembly gate.

### 📖 means SUSPECT, not "probably fine"

Today's hit rate on this category was **two for six** — full-text search and cross-vendor
cost were both 📖, both read as fully implemented, and both did nothing. The table cannot
distinguish *sourced and true* from *sourced and false*. Four 📖 entries remain. They are
suspects, with a named way each could fail:

| 📖 entry | How it could fail, specifically |
|---|---|
| **Task management — create, assign, track; agents mark complete as they finish** | ✅ | **Verified by continuous multi-agent use, not by a designed test.** Counted directly, not quoted: **27 tasks, 23 completed, 4 distinct agents** across a full working day. Every clause exercised — the Orchestrator created and assigned each task to a named agent; `coral-board task list` showed live status throughout; I ran `coral-board task complete` on #25, #27, #35, #37 and the board reflected each. **Caveat: CLI path only.** Nobody has created, assigned or completed a task through the dashboard UI, which is where a user would. ✅ for the mechanism, 📖 for the browser surface | Producer + GTM Strategist, independently |
| **Features table claims are real** | **Highest risk — same evidence type as both retractions.** It rests on *routes existing* (`server.go:440/429/466/484`). FTS also had a table, `UpsertFTS`, query params and a sanitizer, and did nothing. A registered route proves a handler is wired, not that the feature works. **Workflows, scheduled jobs, webhooks and templates have never been run by anyone.** |
| **Linux statically linked, runs on musl/Alpine** | Nobody has executed the Linux binary at all — `file` output is not a run. "Should run" is doing load-bearing work in that sentence |
| **Genuinely free — nothing is gated** | Read from `middleware.go:18-19`. Never tested by exercising a gated-looking route on a prod-tier build without a license |
| **No API keys of its own / never calls a model** | Read from `providers.go:24-32`. Never confirmed by watching what the proxy actually sends |

**`SEEN` column is the enforcement point.** ✅ = observed on a running build. 📖 = verified in
code or in a published artifact only. **A 📖 claim may not ship in launch copy** — it can be
drafted, but it must go ✅ before it goes out.

| Strength | SEEN | Evidence | Verified by |
|---|:---:|---|---|
| **macOS build is signed and notarized — no Gatekeeper warning** | ✅ | `spctl` reports `accepted`, `source=Notarized Developer ID` on the real published v1.0.8 DMG | Dev Advocate #22 |
| **Universal binary** — native on both Intel and Apple Silicon | ✅ | x86_64 + arm64 in the published DMG | Dev Advocate #22 |
| **Fast** — dashboard serves in the same second; agent launch in **0.58s** | ✅ | measured on the published artifact, not a local build | Dev Advocate #22 |
| **Linux binary is statically linked** — no glibc floor, should run on musl/Alpine and old distros | 📖 | `file` reports statically linked; `CGO_ENABLED=0` at `release.yml:44` | Dev Advocate #22 (**gated: nobody has run it**) |
| **Genuinely free — nothing is gated** | ✅ | **Verified by execution on the shipped prod binary.** The gating middleware is a deliberate no-op — it takes the manager and never consults it. Also: `demo_limits=false` on prod, so the `max_teams`/`max_agents` caps only bind on beta builds | Dev Advocate |
| **Apache 2.0, real open source** | ✅ | `LICENSE` | Producer |
| **Coral holds no API keys of its own — keys come from your environment** | ✅ | **Verified against the shipped binary**: zero embedded key prefixes, with a positive-quantity assertion proving the scan ran. Keys only from `os.Getenv`, no fallback. `proxy.go:166-172` passes the caller's own credentials through rather than replacing them | Dev Advocate |
| ⚠️ **Wording note** | — | Write **"no API keys of its own; keys come from your environment"** — checkable. *"Never calls a model on your behalf"* is a promise about **all possible code paths**; both are true, only the first survives an adversarial reader cheaply. If the absolute is used, the mechanism must be in the same breath | GTM Strategist |
| **Four coding agents from four vendors — two of them run side by side, verified** | ✅ | `agentCLIs` at `agent.go:217-222` is the authoritative map: Anthropic, Google, OpenAI, Pi.dev — **four distinct vendors, not three**. *Corrected 2026-08-28: this row said 'three vendors' and the composed prose inherited the error from it. Only Claude Code + Codex have been run simultaneously; Gemini and Pi.dev are supported, never launched by anyone here.* | Producer (error) + Dev Advocate (caught it) |
| **Each team works in its own git worktree on its own branch** — isolated from your main checkout, **when enabled; OFF by default** | ✅ | `sessions.go:2436-2438`, branch `coral-team/<name>` | Dev Advocate + Producer — **per-AGENT isolation does NOT exist; never write it** |
| **Mixing vendors in one team is the wedge** | ✅ | Claude Code's own agent teams coordinate Claude Code instances; nothing there puts Codex and Gemini on one team | GTM Strategist (sourced to Anthropic's docs) |
| **Sleep an agent team, quit Coral, restart it later, and wake the team with its conversation context intact** | ✅ | Demo 3 on the **shipped v1.0.8 binary**: tmux server gone (not detached), session restored under the same id, agent answered `MEMORY-TOKEN-7731` from its own prior turn. Contrast is Anthropic's documented limitation: "/resume and /rewind do not restore in-process teammates" | Dev Advocate — **server-process restart now VERIFIED** (fresh process reported `sleeping:true` *before* the wake, ruling out a lingering process). Still NOT tested: **machine reboot**, long intervals, large multi-agent teams, scrollback retention. Write **"restarting Coral"** — never the ambiguous "survives a restart" |
| ~~**Full-text session search**~~ | ⛔ | **HAS NEVER WORKED.** `FTSBody` is declared (`agent.go:66`), read (`indexer.go:114-115`), and **assigned nowhere** — I grepped every assignment form repo-wide including tests: zero. So `UpsertFTS` is never called and `session_fts` is empty. Production: **56 indexed sessions, 0 fts rows, for months.** Demo: the session list *displays* "Coralville"; `q=Coralville` returns 0 hits | Dev Advocate + Producer — **cut from README, patch ready** (Growth Engineer #40) |
| **Session history — kept, and it is true** | ✅ | Sessions retained with auto-summaries, tags and notes; all visible in the UI. It is specifically *search* that is dead | Dev Advocate |
| **Token and cost tracking per agent, per session, and per team** | ✅ | Real numbers, input/output/cache-read/cache-write broken out, tagged by board — `store/token_usage.go` | Dev Advocate demo |
| ~~**Cross-vendor cost in one figure**~~ | ⛔ | **NOT supportable.** Controlled test: `claude-impl` and `codex-impl` in the *same team*, launched and killed together. Claude reported **$2.97**; Codex reported **nothing** — 8 token-usage records, 8 claude, 0 codex, despite the Codex agent doing real work and its usage sitting on disk in `~/.codex/sessions/`. Not unimplemented — `extractCodexUsage()` exists; it failed at runtime | Dev Advocate — **worse than showing nothing: the total looks complete and is not** |
| **Fast to a real result** | ✅ | *always with the qualifier:* "under two minutes from launching Coral to a committed, tested change — **once your agent CLI is installed and authenticated**" | Dev Advocate ran it: 90s from server start to a 12-line function + 11-case table-driven test suite, `go test ./...` passing when re-run independently | Dev Advocate #23 — **bare "90 seconds" is not approved; the clock excludes download, install, and agent auth** |
| **Task management — create, assign, track; agents mark complete as they finish** | ✅ | **Verified by continuous multi-agent use, not by a designed test.** Counted directly, not quoted: **27 tasks, 23 completed, 4 distinct agents** across a full working day. Every clause exercised — the Orchestrator created and assigned each task to a named agent; `coral-board task list` showed live status throughout; I ran `coral-board task complete` on #25, #27, #35, #37 and the board reflected each. **Caveat: CLI path only.** Nobody has created, assigned or completed a task through the dashboard UI, which is where a user would. ✅ for the mechanism, 📖 for the browser surface | Producer + GTM Strategist, independently |
| **Features table claims are real** | 📖 | verified routes: workflows `:440`, scheduled jobs `:429`, webhooks `:466`, sleep/wake `:331-340`, templates `:484` | Producer |

### The three ✅ that carry the launch

Demonstrated on a running build, and between them they are the whole pitch:
**two different vendors' CLIs (Claude Code + Codex) running simultaneously on one repo under
one dashboard, each producing competent work** — the wedge, executed, not argued.
Plus signed-and-notarized, and 90s to a committed tested change.

### The 📖 that most need retiring
`4.2` sleep/wake across a real restart, `4.4` the cost figure rendering an actual number,
`4.6` FTS5 search returning cross-session results. All three are load-bearing in the
positioning brief and none has been seen. Dev Advocate demos 2 and 3 target these.

---

## The rule all the others are instances of

> **A VERIFICATION IS SCOPED TO THE STATE IT WAS RUN AGAINST. When that state changes, the
> verification is VOID — not stale-but-probably-fine.**

Every control in this file is a check with a shelf life, and none of them announce when they
expire. Four instances on 2026-08-28, all the same shape:

| The check | Correct when run | Invalidated by |
|---|---|---|
| gh-pages draft reviewed and marked done | yes | the worktree finding, two hours later |
| "survives a restart" retracted | in the memorable line | five structural instances left above it |
| `git apply --check` against HEAD, per patch | yes | the next patch landing first |
| grep of `agent_docs` returned clean | yes | 700 lines of new docs added the same day |

**Assert on a positive quantity the check should produce, not on the absence of failures.**
`broken == 0` is satisfied by a check that ran zero comparisons. `total == 30 AND broken == 0`
is not. This is the operational form of the rule below — it says what to *do*, where the rest
only say what goes wrong. The patch verifier works because it asserts the patched file
**must differ from HEAD**, a positive quantity, rather than merely "no patch errored".

**A check that cannot distinguish "passed" from "did not run" is not a check.** Bitten three
times today by the same shell quirk: zsh does not word-split unquoted variables, so
`grep … $FILES` and `for p in $patches` silently iterate over one nonexistent name. The worst
instance produced two *identical* md5s for two patch routes — which read as proof of
equivalence and was actually proof that **neither route applied anything**. A verification
that returns a pass on zero work is worse than no verification, because it manufactures
confidence. Every check must assert it did work: line counts, a required diff, an expected hit.

**Re-verify at the boundary** — before handing over, after any upstream change, and whenever
you are about to say "already checked." What saved the patch set was stack-testing at handoff
rather than trusting the individual checks already run.

**Corollary — a passing check is not evidence until you know it ran.** A sweep of the three
new `agent_docs` files returned "clean" for all four families I tested first. It was a false
pass: zsh does not word-split unquoted variables, so `grep … $FILES` searched for one
nonexistent filename and matched nothing. The tell was `wc -l` printing no output. **Make the
check prove it did work** — print line counts, expect a known hit, fail loudly on a missing
file. A verification that cannot fail is not a verification.

---

## ⚠️ What the patch set does NOT fix — read before assuming the README is clean

Applying **all nine** artifacts still leaves the features table (`README.md:107-121`) making
**capability claims nobody has executed.** My patches were scoped to claims proven *false*;
these are claims merely *unverified*, and none of my hunks touch them.

| Row still standing | Status | Evidence base |
|---|---|---|
| **Workflows** — "multi-step agent pipelines that run automatically… with dependencies" | 📖 | route `server.go:440` |
| **Scheduled jobs** — "on a cron schedule in isolated worktrees" | ⛔ | **FAILS 100% ON THE DEFAULT CONFIG (#43).** `scheduler.go:557` runs `git worktree add <dir> main`, which fails whenever the user's own copy has `main` checked out — the normal case. Reproduced: `fatal: 'main' is already used by worktree`. The per-run keying at `:551` is correct and **never executes**. The UI shows the job healthy because the scheduling half works. **I previously recorded this row as "correct by accident" — that was wrong** |
| **Team templates** — "save and share… generate teams from plain-English descriptions" | 📖 | route `:484`. The generate-from-English half is a second, separate claim |
| **Webhooks** — "notifications to Slack, Discord, or any HTTP endpoint" | 📖 | route `:466`. Names two third-party services by name |
| **Task management**, **Git integration** | 📖 | never examined by anyone today |
| **Token tracking** — "cost and consumption **in real time**" | ⚠️ **likely wrong now** | Per-agent works, but a mixed team silently omits Codex (#41). "Real time" over a mixed team shows a total that looks complete and is not |

**Do not tell the operator the README is clean once the patches land.** The correct statement
is: *every claim we proved false is fixed; seven rows remain unverified and one is probably
inaccurate.* Gated on Dev Advocate task #42.

## Do NOT hedge these — ✅ claims that must ship at full strength

The named risk for the next copy pass: **we cut fourteen claims today, and the muscle memory
that produces is "when uncertain, cut."** That will feel like rigour while it understates a
product with three verified differentiators. The failure mode is a mood, not a missing
artifact, and it will not announce itself — so it needs a list, not a resolution.

These were **demonstrated on a running build**. Nothing cut today touches any of them. They
ship at full strength, and any hedge added to one is a defect:

| Claim | Why it may not be softened |
|---|---|
| **Claude Code and Codex running simultaneously on one repo under one dashboard, each producing competent work** | **The wedge (4.1).** Watched running, in a controlled two-vendor team. The single strongest true thing we have, and the one no single-vendor tool can match. **If this sentence gets hedged out of caution, we lose our best claim to a habit acquired fixing false ones.** |
| **Signed and notarized — no Gatekeeper warning** | `spctl: accepted, source=Notarized Developer ID`, on the published DMG. Verifiable by the reader in one command |
| **Sleep a team, quit Coral, restart it, wake it with context intact** | Verified across a real server-process restart (fresh process, `sleeping:true` before wake) |
| **Under two minutes to a committed, tested change** | Measured. Ships with its qualifier attached, not hedged into vagueness |
| **Free and fully unlocked — nothing gated** | True, and "free tier" would be the *under*claim |

**A hedge is a claim too, and it can be false.** "May help you run multiple agents" is not a
safer version of a demonstrated fact — it is a less accurate one.

## Accuracy is a separate axis from flattery — and bias runs both ways

The obvious risk in copy is overclaiming. The less obvious one, observed twice today, is
**overcorrecting**: the Strategist had the evidence for "the gate quarantined both claims" and
wrote the harsher "our gate let two through", because self-critical *felt* like the honest
read. The Dev Advocate nearly made the mirror-image error — softening the worktree finding
because it felt harsh.

**The burden of proof does not drop for a restoration.** The both-directions rule is a reason
to **re-examine** a cut, not a reason to **restore** one. **A restored claim is a new claim**
and goes through the same gate as any other — lift test, set test, evidence tier.

Observed on 2026-08-28, and the failure mode is subtle: the Developer Advocate argued to
restore the scheduled-jobs cron half *by invoking the anti-overcorrection rule*, and the claim
was false. Their own diagnosis: **"the rule made restoring feel like rigour"**, so the
re-examination got skipped and only the restore happened. The Orchestrator made the same move
in the same hour, ruling to restore a row that did not survive its own shipped-artifact test.

So the rule has a failure mode symmetric to the one it guards against: it can launder an
unverified restoration as diligence, exactly as caution can launder an unnecessary cut as
rigour. Both directions need evidence; neither direction is the safe default.

**Self-critical is not the same as accurate.** When a correction lands, check it in both
directions: the retracted claim, and the replacement. A replacement that undersells is wrong
too, and it is harder to catch because it feels virtuous.

This matters most for me. Every retracted claim today had a **stronger** true version
underneath it, so the correct move at the copy pass is almost never "say less" — it is "say
the specific thing." See the five pairs in the lift-test table.

## Approval launders evidence tier — follow the SENTENCE, not just the entry

**When a claim is downgraded, chase its derived wording, not only its ledger row.**

Once wording is approved it stops being treated as a claim and starts being treated as a
*decision* — so the SEEN gate stops catching it. The gate operates on claims; by the time a
sentence is in five documents everyone handles it as settled copy and nobody re-checks the
evidence behind it.

Live instance: **D5 is 📖** on the suspect list — nobody has watched what the proxy sends —
while the wording it produced ships verbatim in four of my artifacts plus the FAQ, **unmarked**.
The finding carries its tier; the sentence does not.

Same shape as *grep handed-off artifacts first*, one level up: **that rule chases artifacts,
this one chases sentences.** A downgrade must propagate as far as the claim did.

## Universal negatives are the worst claim type for a read-based method

*"Never calls a model on your behalf."* **Reading confirms what is present, not what is
absent.** To establish a universal negative you must have looked everywhere; to break it, one
path suffices. It is a structural mismatch with verification-by-reading — and it is our
strongest privacy claim, which makes it the one most likely to be attacked by exactly the
reader who opens the source.

Treat every "never / no / nothing / only" claim as requiring execution, not inspection.

## Self-audit cannot find a blind spot that is a property of the auditor's method

Three artifacts on 2026-08-28 — the Orchestrator's operator reporting, Part 1 of the exit
brief, and this defect list — each audited by its own author, each carrying the **identical**
hole, and self-audit found none of them. All three authors were checking whether their claims
were *true*. None was checking whether they had recorded **how they knew**.

Each was found only because the previous person said theirs out loud. **The naming is the
mechanism** — a failure described in public is the only thing that reliably makes someone else
look at their own work with a borrowed lens.

## A correction is a new claim and inherits none of the scrutiny that produced it

Three instances on 2026-08-28, three authors, **each invisible to its own writer and obvious
to the next reader**:

- the Strategist's five surviving "survives a restart" instances — inside the correction to
  the sleep/wake overclaim
- my subhead — fixed the bullet, left the same two claims in the largest text on the page
- the D5 rewrite — proposed specifically to fix *"every sentence true, the impression false"*
  and shipped with the same defect in a stronger form

**A correction carries the authority of having just been thought about, and that authority is
exactly what stops the next person thinking about it.** In all three cases the correction was
accepted on the strength of the *diagnosis* and never checked as *copy*.

**So: run a correction through the same gate as the thing it replaces.** Lift test, set test,
evidence tier. Every time. The replacement is not exempt because the diagnosis was good.

## True paragraphs resting on a false premise — we have no check for this

The hardest category found today, and it is recorded **because there is no test for it**.

When a claim is retracted, the sweeps we run hunt **false statements**. But a retracted claim
leaves behind *reasoning built on it* — paragraphs in which **every sentence is true** and the
whole edifice stands on a premise that has since fallen. No banned-phrase grep touches them,
because there is no banned phrase in them.

Live instance: one retracted PTY claim propagated to **six** places, and **three were
downstream conclusions rather than restatements**. Fixing the assertion would have left three
paragraphs of guidance standing on it — all readable, all wrong. My own D10 was a fourth: it
offered `--backend pty` as a *mitigation* for the tmux requirement, which is only a mitigation
if PTY works.

**Summary-position defects are findable by position. This one is findable only by knowing what
a paragraph depends on** — which nothing in this file captures. Recording the gap rather than
pretending to close it.

**And when correcting one: do not swap an unsupported claim for another.** The instinct is to
replace "fell back to PTY" with "fell back to tmux". If you do not know, say what you do know
and mark the rest undetermined. A correction carries authority; a wrong one carries it too.

## The set test — apply to every page before it ships

The lift test asks whether a **sentence** survives alone. The set test asks the other
question: **what would a reader conclude from all of these sentences together?**

> **Not "is each claim true" but "what does the set imply".**

A page can be composed entirely of true sentences and still leave a false impression. Live
instance (📌 **preserved quotation, do not "fix"**): the gh-pages draft carried
*"nothing is sent to us"* — true of the agents' API
traffic — and mentioned telemetry **zero times**. Every sentence true; the impression false.

This is the same completeness failure the D5 rewrite was written to fix, **recurring one level
up**: we fixed it inside the sentence and it reappeared between sentences. Expect it to recur
at the next level too — between sections, and between a page and the thing it links to.

**Standing pairing rule:** the privacy claim and the telemetry disclosure ship together,
always, in every artifact. Neither may be trimmed, shortened, or relocated alone.

## The lift test — apply to every sentence before it ships

> **If someone lifted this sentence alone into a Show HN post, would it be true?**

**A qualifier that lives in a different paragraph from its claim is not a qualifier.** It is a
defence you can point at afterwards, which is a different and much less useful thing. Copy is
assembled by lifting headline sentences — that is how "survives a restart" and the per-agent
worktree line both travelled.

The drafting habit that produces this: *write the strong version, append the honesty
afterwards.* The caveat ends up structurally downstream of the claim, so the claim can leave
without it. Fix it at the point of writing, not in review — put the qualifier **inside the
sentence**.

Claims in our copy that may never travel unqualified:

| Claim | Qualifier that must be in the same sentence |
|---|---|
| "under two minutes to a committed, tested change" | "once your agent CLI is installed and authenticated" |
| "sleep a team and wake it later with context intact" | "restarting Coral" — **not** machine reboot, long intervals, or large teams |
| "your read position is remembered across an agent restart" | "with no messages repeated or skipped" — names what was ruled out |
| "token and cost tracking per agent, session and team" | single-vendor only — **never** imply a cross-vendor total |
| any Homebrew line | the two-command tap form, and not until the fixed cask is pushed |
| "isolated worktree" | which path — **jobs** (per run, on) or **teams** (per team, off) |

## Two independent counters over a moving population will disagree without anyone being wrong

Neither counter is incorrect, neither changed anything, and the discrepancy appears anyway
because the population moved under both. **There is no grep for this.** The only fix is for
each document to state what it counts, inside itself. Applied above to this table vs.
`WAVE1_EXIT_BRIEF` Part 4.

## Do not say — banned phrasings

> **Use this as a grep pattern, not just a style reference.** When a claim is retracted, grep
> **every** artifact — including drafts already marked complete. A finished file is invisible
> to every subsequent correction, which is exactly where a retracted claim survives. Caught
> live on 2026-08-28: the per-agent worktree claim was still in `drafts/gh-pages/index.html`
> two hours after it was disproven, because that draft had been marked done.
>
> **Order: grep everything you have HANDED TO SOMEONE ELSE before your own working files.**
> A handed-off artifact carries more risk than a working draft — the recipient reasonably
> assumes it is current and has no reason to re-audit it. (Binding, per the Orchestrator.)
>
> **Grep for the CLAIM, not for the sentence you remember writing.** Observed twice on
> 2026-08-28, in both directions: the Strategist cut the vivid line ("Close the lid. Come
> back tomorrow.") and left five structural instances of "survives a restart" above it; I
> fixed the worktree bullet at `:79` and left the same claim in the subhead at `:40`. Both
> retractions were applied where the claim was most *memorable* rather than most
> *load-bearing*. The headline is the last place you look and the first place it ships.
>
> **The newest retraction is the most dangerous one** — it has had the least time to
> propagate into the do-not-say list you are grepping with.
>
> **Scope includes the product's own docs and UI strings, not just marketing copy.**
> `agent_docs/` ships inside the product *and is read by agents at runtime* — a false claim
> there can be consumed as ground truth by the agent operating the product. Repo-wide sweep
> results: `specs/gtm/REPO_SWEEP_RETRACTED_CLAIMS.md`.
>
> **"Isolated worktree" requires a qualifier every time it is written** — and the reason has
> changed. It is **not** that one path works and the other does not. **Neither delivers
> per-agent isolation as shipped:** teams share one worktree (off by default), and jobs build
> a correct per-run worktree that **never materialises**, because `git worktree add <dir> main`
> fails on the default config (#43). **The #43 fix exists but is on an UNPUSHED BRANCH** — the
> shipped product still fails every run, so nothing changes for copy until it is merged and
> row 5 is re-verified by someone who did not write the fix. Until then, do not write
> "isolated worktree" about
> any path.


Each of these was in our copy today, or was the natural way to write the correction, and each
is false or unverified. This list is the accumulated cost of a day's verification; consult it
before drafting, not after.

| Never write | Write instead | Why |
|---|---|---|
| "any CLI agent", "any CLI-based tool", "can be added" | "Claude Code, Codex, Gemini CLI, and Pi.dev" | Hardcoded 4-case switch, no registry — adding one means writing Go |
| "each agent in its own git worktree" | "each agent in its own tmux session; optionally one worktree per team" | One worktree per *team*, keyed on board name, **off by default** |
| "without merge conflicts", "without interfering with others" | "they are not isolated from each other — scope each agent to its own files" | **Safety claim.** Demo 1: two agents, same file, build broken |
| "coordinate through the message board" *as the mitigation* | mitigation is file scoping; the board is a channel, not a lock | Demo 1's agents had a board and still collided |
| "survives reboots", "come back tomorrow" | "wake it later with context intact" | Only tested within one server run |
| "Coral doesn't call any AI APIs" | the approved D5 wording, verbatim | The proxy exists; precision is the better privacy story |
| "brew install coral" | nothing yet — and never the short form | Cask broken; homebrew-cask notability unmet, so it is the two-command tap form |
| "free tier" | "free and fully unlocked" | "Tier" implies a paid tier with more features. There isn't one |
| "Native desktop app (macOS & Linux)" | macOS only | Linux tarball ships no tray or app binary |
| "Linux" unqualified | "Linux (x86-64)" | `GOARCH=amd64` hardcoded; no arm64 build |
| "Windows supported" | nothing | `coral.exe` does not compile |
| "the PTY backend already works" / "tmux is not required on Windows" | **"Windows defaults to the PTY backend, and that backend is unexercised."** Not "already works", not "is broken" | The Dev Advocate retracted their own claim: PTY cannot spawn on their host (EPERM), and they cannot distinguish sandbox-denies-it from code-is-broken from inside the sandbox. Two CI jobs would clear *compiling* and *linking* and leave *executed* untouched |
| a bare "90 seconds" / "10 minutes" | "under two minutes — once your agent CLI is installed and authenticated" | The clock excludes download, install, and agent auth |
| "Coral.dmg" | `Coral.v<version>.dmg` | That filename has never been published |
| Anthropic's file-conflict quote *before* the concession | concede first, context second | **Binding ruling.** Reversed, it reads as deflection |
| "survives a restart" / "across a restart" *unqualified* | "survives restarting Coral" | **A grep list matches STRINGS; claims travel as MEANINGS.** "survives reboots" was banned; the synonym walked straight through it. Server-process restart is verified, machine reboot is not |
| "nothing is lost" (board delivery) | "your read position is remembered across an agent restart — agents resume where they left off, with no messages repeated or skipped" | **Now verified by execution** (cursor persisted at `last_read_id=6` across a full process kill; 1-3 not re-delivered, 4-5 not skipped). The absolute still goes: it invites a counterexample and one exists — the board silently fails on a non-default port. Patch ready |

> **Synonym drift is now the main leak.** Three claims escaped this list today by paraphrase,
> not by being missed: "a restart" for "reboots", "each team works in its own worktree" for
> "each agent", "coordinate through the board" for the board as a safeguard. **When you add a
> banned phrase, add the meaning** — list the paraphrases you would naturally reach for, and
> grep for the concept, not the sentence.

### Verified install coverage — state it this precisely

**One platform, binary-only.** macOS is verified from the real published DMG as a *binary*;
the Finder drag-to-Applications flow and the first-run EULA are **not** verified (the EULA was
already accepted on our only machine). Linux is **inspected, never executed**. Windows does
not compile. Homebrew is broken. Nobody may write an install instruction that exceeds this.

**Do not write any number not in this table.** No user counts, no benchmarks, no
"trusted by" claims, no time-saved figures. If copy needs a number, ask on the board.

---

## Blockers on copy I own

Every one of these blocks a specific asset. None are mine to fix.

| # | Blocker | Blocks | Owner | Status |
|---|---|---|---|---|
| **B0** | **#1 TRACKED BLOCKER — `cdknorow.github.io/coral` is a live install funnel for the retired Python product.** Instructs `pip install agent-coral`; that command succeeds (PyPI 4.4.1). Never mentions the DMG, Go, or GitHub Releases. gh-pages last built 2026-03-19, 934 commits behind main, from a `docs/` dir that no longer exists — not regenerable. Ahead of every other item: every channel we open routes traffic through it. | **Every external channel.** README links it from 4 places (2 above fold) | Producer (draft #27) → **operator approves the push** | Replacement drafted, `specs/gtm/drafts/gh-pages/` — **awaiting operator**. Rev 3: now includes `which -a coral` collision detection + `pip uninstall` after Dev Advocate proved the Python package **shadows our binaries and shares `~/.coral` and port 8420** |
| B1 | ~~**README download filenames are wrong.**~~ README says `Coral.dmg` and `coral-linux-amd64.tar.gz`. CI publishes `Coral.v<version>.dmg` and `coral-linux-amd64-<version>.tar.gz`. Neither README filename has ever existed. | README above-the-fold, landing copy, docs quickstart | Content Producer | **FIXED** — working tree, uncommitted, awaiting operator |
| B2 | **Homebrew cask cannot install.** `Casks/coral.rb` pins `version "2.3.1"` (no such tag; latest is v1.0.8), builds URL `.../v2.3.1/Coral.dmg` (filename pattern never produced), and `sha256 ""` is empty. Three independent failures. | Any `brew install` line in any asset | Growth Engineer (Wave 1) | **Blocked — no brew copy until install proven end to end** |
| B3 | **Windows unverified.** `release.yml` builds MSI + portable ZIP but only on tags ending `-windows` / `-all`. Latest release has no Windows asset. | Windows as a supported platform in README/landing/launch copy | Developer Advocate (verify), Growth Engineer (ship tagged build) | **Blocked — omit Windows until MSI installs and runs** |
| B11 | **Nobody has confirmed the `POSTHOG_PROJECT_KEY` secret exists.** `release.yml:32` reads it; no one on the team can see repo secrets. If it is unset or wrong, every event from #18 lands nowhere — **the Phase 2 activation gate could never clear, and we would not know why.** Also makes my telemetry disclosure inaccurate: it would say release builds send events when they send nothing. | Phase 1 exit criteria, launch gate, disclosure copy | Operator (only they can check) | **Escalated — blocks the gate itself** |
| **B13** | **Per-agent worktree isolation is claimed but does not exist.** One worktree per *team*, shared by all its agents (`sessions.go:2436-2438`, keyed on board name). README `:41`, `:97`, `:108` all claim per-agent. Disproven by `git worktree list` in one command. | The core differentiator, README, positioning, every demo | Dev Advocate (found), Producer (copy), Growth Engineer (code-vs-docs decision) | **P0 — highest-severity claim defect found** |
| B12 | **First agent launch hangs on an unannounced prompt.** Claude Code's own trust-folder prompt (*"Is this a project you created or one you trust?"*) blocks the first launch on a fresh machine. Coral's UI gives no indication the agent is waiting on a keystroke rather than thinking. Hits **every new user on their first launch**. | The 10-minute quickstart claim, and the activation gate itself | Growth Engineer (first-run queue), Dev Advocate (documents the step in #23) | **Open — most likely single point of quickstart death** |
| B4 | **Telemetry disclosure copy does not exist.** Operator: on by default, clear first-run disclosure, no opt-out. Must state plainly that release builds send events, and that source builds have no key and send nothing. | First-run disclosure, privacy section of README/docs | Content Producer (copy), Growth Engineer (surface) | Draft pending — needs event list frozen |
| B5 | **Comparison table — RULED: delete, replace with sourced prose.** Claude Code now ships native agent teams, which makes several rows indefensible. Also `README.md:125` carries the same stale AutoGen/CrewAI premise and must change with it. Strategist sourced six defensible differentiators to replace it. | README compares Coral to Claude Code, Cursor, AutoGen, CrewAI across 10 rows. Not independently verified; competitor behavior changes fast. This is the single highest-risk item for Show HN. | README comparison, Show HN post, landing page | GTM Strategist (prose) → Producer (copy) | Awaiting Strategist prose |
| B6 | **Discord badge renders the literal word "invalid".** shields.io's own `<title>` says `Discord: invalid`. Guild ID cannot be derived from an invite code. | README above-the-fold | Producer (patch ready) / operator (guild ID) | Patch prepared with no-blocker static fallback |
| B7 | **Hero image is BROKEN — Loom CDN thumbnail returns 403** (verified incl. browser UA, so camo gets the same). Largest above-the-fold element renders broken today. | README hero, landing hero | Producer (both patches ready) / operator (choice) | Options A + B prepared, awaiting operator |
| B9 | ~~**"Works with any CLI agent"**~~ — **DISPROVEN, removal patch ready.** — `internal/agent/` has 5 impls (claude, codex, gemini, pi, **shell**) and a `CLIPath` override, but no visible UI path to register a new agent type. `README.md:81` claims any CLI agent can be added. This is our strongest differentiator and nobody can source it. | README :37/:81/:110, positioning, Show HN lead | Dev Advocate (answered 3f = option iii) → Producer (patch ready) | `README-remove-any-cli-agent-claim.patch` — awaiting operator |
| B0b | **Retired PyPI package actively breaks a correct install.** `agent-coral` ships identical binary names (`coral`, `coral-board`, `launch-coral`, …), uses the same `~/.coral` dir, same `sessions.db`/`messageboard.db` filenames, and same port 8420. `~/.local/bin` (pip) outranks `/usr/local/bin` (our `install-cli.sh`) on a default PATH, so `coral` silently starts the Python one. Different schemas on the same DB filename is a corruption vector. | Every install instruction we publish | Dev Advocate (found), operator (PyPI yank decision) | **Escalated — strengthens the case for deprecating `agent-coral`** |
| B10 | **False supporter benefits in-product.** "Agent team templates & sharing" and "Search chat history" are advertised as supporter benefits but are free and ungated. | Supporter copy, activation page | Growth Engineer #29, copy from Strategist | Ruled: cut |
| B8 | **No campaign attribution on supporter links.** Every supporter link in README is bare. Phase 1 of the growth plan requires tracked campaign params. | All CTAs in all assets | Growth Engineer | Open — I will not hardcode UTMs until the scheme is defined |

---

## Asset status

Draft only. Nothing here is approved to publish.

| Asset | Depends on | Owner | Status |
|---|---|---|---|
| README above-the-fold rewrite (one-line value prop + install path) | positioning ✅ | Content Producer | **DRAFTED** — `README_ABOVE_THE_FOLD.md`; note it supersedes 4 of the 5 patches, see Path A/B |
| Landing page copy | Strategist positioning brief | Content Producer | Not started |
| Docs copy (quickstart + install) | Dev Advocate verified walkthrough | Content Producer | Not started |
| Telemetry / privacy disclosure copy | B4 | Content Producer | **DRAFTED** — `TELEMETRY_DISCLOSURE.md`; surface TBD by #20 |
| Show HN post | Wave 3 gate, B5 | Content Producer | Blocked by launch gate |
| Reddit launch thread | Wave 3 gate, per-subreddit rules | Content Producer | Blocked by launch gate |
| X launch thread | Wave 3 gate | Content Producer | Blocked by launch gate |
| Launch-day email / newsletter | Wave 3 gate, list existence unconfirmed | Content Producer | Blocked by launch gate — **is there a list?** |
| Release notes for next tagged version | Growth Engineer's Wave 1 merges | Content Producer | Not started |
| Screenshot & asset checklist | see below | Content Producer | Drafted below |

---

## Screenshot & asset inventory

**Correction to my brief:** there is no `icons/` directory at the repo root. The real path
is **`assets/icons/`**. (`CLAUDE.md` and `release.yml:239` both reference a root `icons/`;
`release.yml` falls back to root `Coral.icns`, which does exist, so the build is fine — but
the docs reference is stale.)

### What exists in `assets/icons/`

| File | Dimensions | Size | Launch use |
|---|---|---|---|
| `corral-dashboard-tour.gif` | 1400x677 | 2.5M | Candidate README hero — needs Dev Advocate sign-off that UI is current |
| `main_loop.gif` | 1318x732 | 1.1M | Candidate "how it works" loop |
| `main_loop_small.gif` | 640x354 | 588K | Inline / social |
| `main_loop_small.mp4` | — | 3.5M | Better than GIF for landing hero |
| `history.gif` | 1318x732 | 3.2M | Session-history feature shot |
| `Screenshot 2026-03-15 at 4.31.41 PM.png` | 2598x672 | 2.2M | Unnamed, undated in filename — **needs triage, probably stale (March)** |
| `cora-icon-color-1024x1024.png` | 1024x1024 | 72K | Store / social avatar (note: filename typo "cora") |
| `coral-icon-padded-1024.png` | 1024x1024 | 144K | App icon, padded |
| `coral-icon-88x88.png` / `-22x22.png` | 88 / 22 | 4K | Tray / favicon |
| `corral.png`, `corral_dark.png`, `corral_simple.png` | 2048x2048 | 6.5–6.7M each | Logo masters — oversized for web, need export |
| `coral.icns`, root `Coral.icns` | — | 293K | macOS bundle icon |
| `svgmaker-editor-*` (2 files) | 1024 / 44 | — | Editor leftovers — **candidates for deletion** |

### Gaps to fill before any launch

- [ ] **Verify every GIF still matches shipping UI.** All predate the current build; a stale screenshot on HN is a credibility hit. — *Dev Advocate*
- [ ] Hero asset that shows the actual value in one frame: multiple agents running side by side with live status. — *Dev Advocate to capture*
- [ ] First-run / onboarding screenshots — cannot exist until Wave 2 builds the flow. — *blocked*
- [ ] Open Graph / social card (1200x630). None exists. — *Content Producer*
- [ ] Rename `cora-icon-color-1024x1024.png` (typo) and triage the March screenshot. — *Content Producer*
- [ ] Decide GIF vs. mp4 for README hero and replace the Loom embed (B7).
- [ ] Web-sized logo exports; 2048x2048 / 6.7M masters must not ship in a README.

### Naming convention going forward

`coral-<surface>-<subject>-<width>.<ext>` — e.g. `coral-readme-hero-1400.gif`,
`coral-social-card-1200x630.png`. Applies to new assets only; no renaming churn on
files already referenced by the build.

---

## Standing rules for every asset I write

1. Every claim traces to the verified-baseline table, a Dev Advocate demo I can point to,
   or Strategist research. Otherwise I ask on the board.
2. No install path appears in copy until someone has run it end to end on a clean machine.
3. Voice: direct, technical, no hype, no invented metrics.
4. Draft only. The operator approves; I never publish, post, or send.
