# Weekly Operating Loop

**Owner:** GTM Strategist · **Task:** #38, Part B
**Implements:** `GROWTH_AND_SUPPORTER_SALES_PLAN.md` Phase 6, lines 168–190
**Companion:** `POSTHOG_DASHBOARD_SPEC.md` — every number below names the panel it comes from

**Run it every Friday. Copy this file per week** (`WEEK_2026-09-04.md`); do not edit in place. The
value is in the series, not in any one week.

**Time budget: 30 minutes.** If it takes longer, it will stop happening.

---

## Step 0 — Is the data real? (2 min)

**Do this before you look at anything else.** From Dashboard Panel 0a.

- `app_opened`, prod only, last 7 days: **______**

| Reading | What to do |
|---|---|
| **0** | 🔴 **STOP.** Write "NO DATA — pipeline unverified (B11)" across the whole table and skip to Step 5. Do **not** enter zeros below | 
| **> 0** | ✅ Continue |

> A zero meaning "unmeasured" and a zero meaning "nobody did this" look identical in a spreadsheet
> three weeks later. Never write one where you mean the other.

**Builds reporting this week** (Panel 0c): ______

---

## Step 1 — The numbers (10 min)

**Filter every panel to `edition = prod`.** Enter counts *and* rates — a percentage off five
installs is noise, and the count is what tells you that.

### Funnel

| Stage | Metric | Panel | This wk | Last wk | Rate | Target |
|---|---|---|---|---|---|---|
| Acquisition | `install` | 1a | | | — | 20/wk |
| Activation | `first_agent_launched` | 2a | | | ___% of installs | **40%** |
| Activation | `first_team_launched` | 2a | | | ___% of installs | **20%** |
| **Activation** | **`first_task_completed`** | 2a | | | **___% of installs** | **10%** |
| Retention | `returned_24h` | 3a | | | ___% of activated | 40% ⚖️ |
| Retention | 7-day return | 3b | | | ___% | 25% ⚖️ |
| Support intent | `supporter_checkout_clicked` | 4a | | | ___% of retained | 5–10% |
| Revenue | `license_activated` | 5a | | | — | — |
| Revenue | **LS completed purchases** | Lemon Squeezy | | | — | 10 total |

⚠️ **`license_activated` ≠ LS purchases.** Different events, different denominators. Record both,
never substitute one.

🔴 **`first_task_completed` is the real activation number**, not the 40% row. `first_agent_launched`
fires when a user clicks Launch, before the agent does anything — so until #33 ships, a user whose
agent froze on the trust prompt still counts as activated. Read the bottom activation row first.

### Platform coverage

| OS | Installs | Reached first agent | Reached first task |
|---|---|---|---|
| darwin | | | |
| linux | | | |
| windows | | | |

*Any Linux or Windows row above zero is new information — we have no verified coverage there.*

### GitHub — not in PostHog, check manually

Two minutes on the repo page. **We track none of these today, and one of them is a gate.**

| Metric | This wk | Last wk | Why |
|---|---|---|---|
| **Stars** | | | Homebrew bar: **225** |
| **Forks** | | | Homebrew bar: **90** |
| **Watchers** | | | Homebrew bar: **90** |
| Release downloads | | | The real download number |
| Open issues | | | |

> **Track all three GitHub metrics, not just stars.** The homebrew-cask self-submission bar is
> **any *one* of** 90 forks / 90 watchers / 225 stars
> ([Package-Acceptance-Policy, Notability](https://raw.githubusercontent.com/Homebrew/brew/master/docs/Package-Acceptance-Policy.md)).
> Watchers is plausibly the cheapest of the three and nobody has ever looked at it. **We could clear
> a bar nobody is watching.**

### Claims ledger — the SEEN gate

From `LAUNCH_CHECKLIST.md`. Reviewed on a cadence rather than when someone remembers.

| | Count |
|---|---|
| ✅ **SEEN** — demonstrated on a running build by a human | |
| 📖 **SOURCED** — verified in code, never observed | |
| ⛔ **RETRACTED** this week | |

- Claims that moved 📖 → ✅ this week: ______
- Claims **retracted** this week: ______
- **If any claim was retracted:** did you grep everything **handed to someone else** first, then your
  own working files? ☐ yes ☐ **no — do it now**

> 📖 may be drafted. 📖 may not ship. A high 📖 count is honest; a *falling* 📖 count is progress.
> Three claims were retracted in one day on 2026-08-28, all found by running the product rather than
> reading it.

---

## Step 2 — Find the largest drop-off (5 min)

Per Phase 6: **by absolute users lost, not by percentage.** A 90% drop on 3 users is noise; a 40%
drop on 200 is the problem.

| Transition | Users in | Users out | **Lost** |
|---|---|---|---|
| install → first agent | | | |
| first agent → first team | | | |
| first agent → **first task** | | | |
| first task → returned_24h | | | |
| retained → checkout click | | | |
| click → activation | | | |

**Largest absolute loss:** ______

### Before you act on it — two disqualifiers

- [ ] **Is this stage distorted by a known defect?** If the biggest loss is `first agent → first
      task` and **#33 has not shipped**, the answer is already known: agents are freezing on an
      unsurfaced trust prompt. **Ship #33; do not open an investigation.**
- [ ] **Is the count large enough to act on?** ⚖️ Below ~20 users through the stage, treat any
      movement as noise. Write "insufficient volume" and pick the next-largest stage where the count
      supports a conclusion. **Say this out loud rather than acting on four users.**

---

## Step 3 — Qualitative evidence (5 min)

Phase 6 requires reading feedback attached to that drop-off before shipping anything.

| Source | Anything about this stage? |
|---|---|
| Phase 3 interviews (`USER_INTERVIEW_KIT.md`) | |
| GitHub issues opened this week | |
| Discord | |
| Abandonment question (🔴 not built) | |

- **Does the qualitative evidence agree with the number?** ☐ yes ☐ no ☐ no evidence either way
- **If no evidence:** ⚖️ prefer spending next week getting one interview about this stage over
  guessing at a fix. At our volume, one real conversation outweighs a week of low-n data.

> 🔴 **A defect users don't recognise as a defect never becomes a bug report.** Four of our known
> defects are invisible from the user's side — a frozen agent looks slow, a broken board looks like
> agents ignoring each other. **Silence in this section is not evidence that a stage is healthy.**

---

## Step 4 — Ship ONE thing (5 min to decide)

**One improvement. Not two.** Two simultaneous changes make next week's data uninterpretable, which
is worse than shipping nothing.

### Decision rule, in order — stop at the first that applies

1. **Is Panel 0a zero?** → Fix the telemetry pipeline (B11). Nothing else is measurable.
2. **Is there an open P0 on the drop-off stage?** → Ship that. Do not investigate a stage with a
   known defect on it.
3. **Is the drop-off stage distorted by an unshipped fix?** → Ship the fix, then measure for a full
   week before concluding anything.
4. **Do you have qualitative evidence agreeing with the number?** → Ship the smallest change that
   addresses it.
5. **Do you have a number but no evidence?** → **Do not guess.** Spend the week getting one
   conversation about that stage.
6. **Is everything above target?** → Only now consider opening a channel. Check the gates in
   `CHANNEL_PLAN.md` §0.

**This week's one thing:** ______
**Which stage it targets:** ______
**What I expect to change, and by how much:** ______ ⚖️ *(write it down before shipping — it is the
only protection against declaring any outcome a success)*
**Shipped on:** ______ → **add a PostHog annotation**

---

## Step 5 — Close the loop on last week (3 min)

Phase 6 steps 4–5: measure for a full cycle, then keep, revise, or remove based on evidence.

- **Last week's one thing:** ______
- **Predicted:** ______ · **Actual:** ______
- **Verdict:** ☐ keep ☐ revise ☐ **remove**
- **If it did not work, did we remove it?** ☐ yes ☐ **no — why not?** ______

> **Removing a change that did not work is the step everyone skips.** Failed experiments left in
> place accumulate into a product nobody can reason about, and they quietly break the next
> measurement.

---

## Step 6 — Gate status (2 min)

Not a decision, just a status line. **No launch date; the gate is the operator's.**

| Gate | Target | Actual | Clear? |
|---|---|---|---|
| Agent launch | 40% | | ☐ |
| Team launch | 20% | | ☐ |
| **Task completed** | **10%** | | ☐ |
| Data verified real (B11) | — | | ☐ |
| Docs site fixed (P0) | — | | ☐ |
| Install paths verified | — | | ☐ |

**All clear?** ☐ yes → external launch is *available* to the operator ☐ no → **no channels open**

---

## Weekly one-line log

Append one line per week. **This series is the point of the whole exercise.**

| Week | Installs | 1st agent | **1st task** | Ret 24h | Clicks | Sales | Shipped | Verdict |
|---|---|---|---|---|---|---|---|---|
| | | | | | | | | |

---

## Failure modes — re-read quarterly

- **Reporting stars or visitors as progress.** Neither is in the funnel.
- **Writing 0 when you mean "unmeasured."**
- **Using `session_launched` for an activation rate.** Dashboard §2 makes it structurally hard;
  do not defeat the guard by switching a panel to Total count.
- **Reading `first_agent_launched` as "an agent worked."** It means "a user clicked Launch."
- **Pooling weeks across a release or a fix.** Segment by `version`; use the annotations.
- **Comparing a rate off <20 users to last week's.** Report the count next to every percentage.
- **Substituting LS revenue for `license_activated`, or vice versa.**
- **Shipping two things and learning nothing.**
- **Concluding a stage is healthy because nobody complained.** Our worst defects are the ones users
  cannot see.
