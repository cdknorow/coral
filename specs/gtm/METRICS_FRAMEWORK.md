# Coral Metrics Framework

**Owner:** GTM Strategist · **Task:** #24
**Status:** ✅ **RECONCILED against Growth Engineer #18** (branch `growth/wave1-funnel-instrumentation`,
commit `84a0338`, not yet pushed). Section 2 is their event list with their file:line references,
which is now the metrics source of truth. Targets in Section 3 are re-derived from it.

---

## 1. The funnel

From `specs/GROWTH_AND_SUPPORTER_SALES_PLAN.md:15`, unchanged:

```
qualified visitor → download → first open → first agent → first team
→ first completed task → return visit → supporter checkout → license activation
```

Nine stages. **Today we can measure four of them.**

---

## 2. What is emitted — the definitive list

Source: Growth Engineer, task #18. All file:line references are theirs.

| Event | Location | Fires |
|---|---|---|
| `app_opened` | `internal/tracking/posthog.go:61` | every server start |
| `install` | `internal/tracking/posthog.go:133` | first ever start on a machine |
| `upgrade` | `internal/tracking/posthog.go:148` | start after the version string changes |
| `session_launched` | `internal/server/routes/sessions.go:2366` | **every** single-agent launch |
| `first_agent_launched` **NEW** | `internal/server/routes/sessions.go:2367` | **once** — first single-agent launch |
| `team_launched` | `internal/server/routes/sessions.go:2552` | **every** team launch |
| `first_team_launched` **NEW** | `internal/server/routes/sessions.go:2553` | **once** — first team launch |
| `first_task_completed` **NEW** | `internal/server/routes/board.go:895` | **once** — first board task completed |
| `returned_24h` **NEW** | `internal/tracking/milestones.go:171` | **once** — first open >24h after first open |
| `supporter_checkout_clicked` **NEW** | `internal/server/routes/tracking.go:57` | **every** supporter/store link click |
| `license_activated` **NEW** | `internal/license/middleware.go:78` | **every** successful activation |

**Standard properties on every event:** `version`, `edition`, `os`, `arch`.
**Extra properties:** `agent_count` on `team_launched` / `first_team_launched`; `product_name` and
`variant_name` on `license_activated`; `surface` on `supporter_checkout_clicked`, plus
campaign/source/medium once #19 lands.
**Identity:** random UUID at `~/.coral/.install_id`.
**Once-only state:** `<coralDir>/.milestones.json`, atomic write, mutex-serialised.
**Delivery failures:** logged to `<coralDir>/tracking-failures.log`.

### Two properties of this design that matter for how we read the numbers

**1. `first_*` milestones are not burned when the PostHog key is empty.** A source build sends
nothing *and keeps its first-run events available*, so it cannot silently consume them. Practical
consequence: **a user who builds from source and later installs a release build still produces a
clean funnel** — their milestones were never spent. This removes a whole class of undercounting we
would otherwise have had to guess at.

**2. `first_agent_launched` is distinct from `session_launched`.** Use the `first_*` events for
every activation *rate* in Section 3. Using `session_launched` would count one enthusiastic user as
forty activations, and at our volume that single mistake would make the Phase 2 gate look cleared
when it is not.

### Every funnel stage is now instrumented

All four gaps I flagged before #18 — `first_task_completed`, `returned_24h`,
`supporter_checkout_clicked`, `license_activated` — are closed. **All nine funnel stages are now
measurable in principle**, subject to the one open risk in Section 7.

## 3. Stage-by-stage targets

Targets marked **[plan]** are the operator's, from the growth plan. Targets marked **[proposed]**
are mine, and are the ones I want challenged.

| # | Stage | Metric (post-#18) | Target | Source | Measurable |
|---|---|---|---|---|---|
| 1 | Qualified visitor | Tracked visits by campaign | **100/wk** | [plan] Phase 5 | ⚠️ needs #19 |
| 2 | Download | Release asset downloads | **20/wk** (20%) | [plan] Phase 5 | ⚠️ GitHub API only, no per-campaign attribution |
| 3 | First open | `install` | **≥70% of downloads** | [proposed] | ✅ |
| 4 | First agent | **`first_agent_launched`** ÷ `install` | **40% of new installs** | [plan] Phase 2 | ✅ |
| 5 | First team | **`first_team_launched`** ÷ `install` | **20% of new installs** | [plan] Phase 2 | ✅ |
| 6 | First completed task | **`first_task_completed`** ÷ `install` | **10% of new installs** | [plan] Phase 2 | ✅ |
| 7 | Return visit | **`returned_24h`** ÷ `first_agent_launched`; 7d derived from `app_opened` | **24h ≥40% of activated; 7d ≥25%** | [proposed] | ✅ / ⚠️ 7d derived |
| 8 | Supporter checkout | **`supporter_checkout_clicked`** ÷ retained | **5–10% of retained users** | [plan] Phase 4 | ✅ |
| 9 | License activation | **`license_activated`** ÷ `supporter_checkout_clicked` | **20–30% of checkout clicks** | [proposed] | ✅ |

**Denominator convention — apply it consistently or the numbers are not comparable week to week:**
stages 4, 5 and 6 are measured against `install` (new installs), exactly as the growth plan words
them. Stage 7 is measured against **activated** users, not installs. Stage 8 is measured against
**retained** users. Always report the raw count next to the percentage.

**On stage 7's 7-day figure:** there is no `returned_7d` event, and it does not need one — it is
derivable from `app_opened` timestamps per `distinct_id`. Build it as a PostHog insight, not a code
change.

### Notes on the proposed targets

**Stage 3 (≥70% download→open).** Anything below this is an install-path defect, not a marketing
problem. Gatekeeper warnings on an unsigned DMG are the likely culprit if we miss it. This is the
stage most likely to be silently broken and it currently has no target at all in the plan.

**Stage 7 (24h ≥40% of activated).** Deliberately measured against *activated* users, not all
installs. Retention of someone who never launched an agent is not a meaningful number, and mixing
them in flatters the metric.

**Stage 9 (20–30% of clicks → activation).** A one-time $49.99 purchase with a fully functional
free product. I hold this **loosely** — it is a judgment call, not a benchmark I verified against
comparable products, and I will not present it as one. Its real value this quarter is as a
**tripwire**: below ~10% suggests checkout is broken rather than that pricing is wrong, and that is
a diagnosis worth having.

---

## 4. From funnel to the first ten sales

The goal is **10 supporter sales**. Working backwards through the targets above:

```
10 sales ÷ 25% activation-of-click      →   40 checkout clicks
40 clicks ÷ 7.5% click-rate-of-retained →  533 retained users
533 retained ÷ 40% 24h-retention        → 1333 activated users
1333 activated ÷ 40% activation         → 3333 installs
3333 installs ÷ 20% download-rate       → 16,665 qualified visitors
```

**Read that number carefully, because it is the most important output of this document.**

At the plan's target of 100 qualified visitors/week, 16,665 visitors is **over three years**.

This does **not** mean the goal is wrong. It means one of three things is true, and we should
decide which:

1. **The conversion assumptions are too pessimistic for a niche high-intent tool.** Plausible.
   Someone who installs a multi-agent orchestrator has self-selected hard, and stages 4-8 could run
   well above these rates. **As of #18 we can finally find out** - every stage is instrumented, so
   after three or four weeks of real release data these assumptions get replaced by measurements.
   Treat the arithmetic above as provisional until then.
2. **100 visitors/week is too low.** Also plausible — a Show HN front page alone can exceed a
   year's worth of that target in a day. The plan's weekly numbers describe the *steady state*
   after launch, not the launch itself.
3. **The first ten sales will not come from this funnel at all.** Most likely, and worth saying:
   at our size, early supporters are usually people who talked to the maintainer — Discord members,
   issue authors, the Phase 3 interviewees. **Phase 3's five user interviews are probably a more
   direct route to the first ten sales than any channel in the channel plan.**

**My recommendation:** treat the first ten sales as a **qualitative** milestone driven by Phase 3
conversations, and treat this funnel arithmetic as the instrument for deciding *which stage to fix
next*. Do not manage to the 16,665 number. Do use it to refuse "we need more traffic" as an answer
before the funnel converts.

---

## 5. The one number to watch each week

**Not** stars, **not** visitors, **not** downloads.

> **New installs that launched an agent, as a percentage of new installs, this week.**

Both halves are measurable today (`install` and `session_launched`). It is the plan's own Phase 2
gate. And it is the number that decides whether any traffic we generate is worth generating.

Post-#18 this is `first_agent_launched` divided by `install`. Both are once-only events, so the
ratio is a true rate and cannot be inflated by one power user.

**Today it is unknown.** 31 stars and 0 DMG downloads on v1.0.8 means the denominator may
currently be near zero - and until the PostHog key is confirmed (Section 7), a reading of zero
would not distinguish 'no users' from 'no telemetry'.

---

## 6. Weekly review

Per growth plan Phase 6, every Friday, in this order:

1. Find the **largest measurable drop-off**, by absolute users lost.
2. Read the qualitative feedback attached to that stage.
3. Ship **one** focused improvement.
4. Measure for a full week.
5. Keep, revise, or remove based on evidence.

**One change at a time.** Two simultaneous changes make the week's data uninterpretable, which is
worse than shipping nothing.

### Failure modes to name in advance

- **Reporting stars or visitors as progress.** Neither appears in the funnel.
- **Reporting Lemon Squeezy revenue as `license_activated`.** Different events, different
  denominators.
- **Comparing weeks across a release boundary** without segmenting by `version` — the property is
  on every event, so segment.
- **Treating `app_opened` as a distinct user.** It fires every launch. Count distinct
  `distinct_id`s.
- **Using `session_launched` instead of `first_agent_launched` for an activation rate.** One
  enthusiastic user launching forty agents would read as forty activations and would falsely
  clear the Phase 2 gate.
- **Reading a single week's rate off fewer than ~20 installs.** At our current volume most weekly
  movements will be noise. Report the count alongside every percentage.

---

## 7. Blockers

| Blocker | Effect | Owner | Status |
|---|---|---|---|
| ~~`first_task_completed` not emitted~~ | — | #18 | ✅ closed |
| ~~`supporter_checkout_clicked` not emitted~~ | — | #18 | ✅ closed |
| ~~`license_activated` not emitted~~ | — | #18 | ✅ closed |
| ~~`returned_24h` not emitted~~ | — | #18 | ✅ closed |
| **`POSTHOG_PROJECT_KEY` may not exist or may be wrong** | **Every event lands nowhere. Whole framework reads zero** | Operator | 🔴 **open — highest** |
| No campaign attribution on supporter links | Cannot attribute a sale to a channel | #19 | 🟡 open |
| #18 not pushed or merged | No events in any release build | Growth Eng | 🟡 open |
| Docs site sends traffic to the Python package | Stages 1–3 corrupted at the source | #27 → operator | 🔴 open |
| First-run activation wall | Depresses stage 4 — the number that gates everything | #26 | 🔴 open |

### The PostHog key risk deserves its own paragraph

The Content Producer raised this as B11 and they are right that it is bigger than a caveat. Nobody
on this team can read repo secrets, so nobody has confirmed that `POSTHOG_PROJECT_KEY`
(`release.yml:32`) is set and correct.

**If it is unset, every number in this document reads zero — and a zero is indistinguishable from
"nobody activated."** We would conclude we have an activation problem, grind on onboarding for
weeks, and be measuring nothing the entire time. That failure mode is worse than having no
instrumentation, because it produces confident wrong conclusions instead of acknowledged ignorance.

**Verification, before any release build is treated as a data source:** cut a release build, run
it once on a clean machine, and confirm an `install` event arrives in the PostHog project. Not
"check the secret exists" — check that an event lands. Verify the thing, not the claim about the
thing.

**The three red rows matter most.** Until they clear, every funnel number we collect either lands
nowhere or measures a product we already know is misconfigured. Drawing conclusions from that data
would be worse than having none.
