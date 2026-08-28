# PostHog Dashboard Specification

**Owner:** GTM Strategist · **Task:** #38 · Closes `GROWTH_AND_SUPPORTER_SALES_PLAN.md` Phase 1,
line 40
**Input:** `METRICS_FRAMEWORK.md` · **Event source of truth:** `coral-go/internal/tracking/events.go`
(`AllEvents`) and `coral-go/internal/tracking/milestones.go`

Precise enough to build without design decisions. Every panel names its exact event constant,
breakdown, window, math, and what healthy versus unhealthy looks like.

**Label key:** ✅ verified against code · ⚖️ my judgment, not a benchmark · 🔴 blocked or distorted

---

## 0. The event vocabulary

All eleven, verified in the source. **Constant names are split across two files** — `events.go`
declares seven, `milestones.go:17-20` declares the four `first_*`/`returned_*` ones. Both compile
into one `AllEvents` list.

| Event | Constant | Fires | Extra properties |
|---|---|---|---|
| `install` | `EventInstall` | first run on a machine | — |
| `upgrade` | `EventUpgrade` | first run after version change | — |
| `app_opened` | `EventAppOpened` | **every** server start | — |
| `session_launched` | `EventSessionLaunched` | **every** single-agent launch | — |
| `team_launched` | `EventTeamLaunched` | **every** team launch | `agent_count` |
| `first_agent_launched` | `EventFirstAgentLaunched` | **once ever** | — |
| `first_team_launched` | `EventFirstTeamLaunched` | **once ever** | `agent_count` |
| `first_task_completed` | `EventFirstTaskCompleted` | **once ever** | — |
| `returned_24h` | `EventReturned24h` | **once ever** | — |
| `supporter_checkout_clicked` | `EventSupporterCheckoutClicked` | **every** click | `surface`, `campaign`, `source`, `medium` |
| `license_activated` | `EventLicenseActivated` | **every** activation | `product_name`, `variant_name` |

**Standard properties on all eleven:** `version`, `edition`, `os`, `arch`.
**Identity:** `distinct_id` is a random UUID at `~/.coral/.install_id` — one per install, not per
human.

⚠️ **`edition` must be filtered to `prod` on every panel below.** Dev and beta builds emit the same
events. Without the filter, our own testing pollutes every number. This is the single most likely
way the dashboard produces a confident wrong answer in week one.

---

## Panel 0 — 🔴 LIVENESS. Build this first. Look at it first. Every time.

**This panel is worth more than the rest of the dashboard combined**, and it exists because of B11:
nobody has confirmed `POSTHOG_PROJECT_KEY` is set. If it is unset, **every other panel reads zero —
and zero is indistinguishable from "nobody activated."** We would conclude we have an activation
problem and grind on onboarding for weeks while measuring nothing.

### 0a. Are events arriving at all?

| | |
|---|---|
| **Type** | Number (single value) |
| **Event** | `app_opened` |
| **Math** | Total count |
| **Filter** | `edition = prod` |
| **Window** | Last 7 days |

**`app_opened` is the correct liveness probe** because it fires unconditionally on every server
start, for every user, regardless of what they do next. Nothing else in the vocabulary is
independent of user behaviour.

**Reading it:**

| `app_opened` (7d) | Meaning | Action |
|---|---|---|
| **0** | 🔴 **The pipeline is dead, or nobody ran Coral at all.** Do not read any other panel | Resolve B11 before interpreting anything |
| **> 0**, activation panels 0 | ✅ Data is arriving. **This is a real activation problem** | Read the funnel |
| **> 0**, activation panels > 0 | ✅ Everything is working | Proceed |

### 0b. When did the last event arrive?

| | |
|---|---|
| **Type** | Number, or a trend of `app_opened` by day |
| **Event** | any (use `app_opened`) |
| **Window** | Last 30 days, daily granularity |

A flat line at zero starting on a specific date means something broke **on that date** — most likely
a release that shipped without the key. That is a different diagnosis from "we never had data," and
you can only see it on a time series.

### 0c. Which builds are reporting?

| | |
|---|---|
| **Type** | Table |
| **Event** | `app_opened` |
| **Breakdown** | `version`, then `edition` |
| **Window** | Last 30 days |

Confirms release builds specifically are reporting, and catches the case where `edition = dev`
dominates because we are measuring ourselves.

> **Standing rule: if Panel 0a reads zero, the weekly review stops there.** Write "no data" in the
> Friday table. Do not fill in zeros for the other rows — a zero that means "unmeasured" and a zero
> that means "nobody did this" look identical on a dashboard three weeks later.

---

## 1. Acquisition

### 1a. New installs

| | |
|---|---|
| **Type** | Trend, weekly |
| **Event** | `install` · **Math: Total count** |
| **Filter** | `edition = prod` |
| **Breakdown** | `os` |
| **Window** | Last 12 weeks |

`install` fires once per machine, so total count is the honest number.

**Healthy** ⚖️: ≥20/week — the growth plan's Phase 5 download target.
**Unhealthy:** flat at 0–2 while channels are open → the top of the funnel is broken, most likely
the docs site.
**Watch:** the `os` breakdown is our only evidence about Linux, where we have **zero verified
install coverage**. Any Linux install at all is new information.

### 1b. 🔴 Campaign attribution — NOT YET BUILDABLE AS SPECIFIED

The growth plan wants qualified visitors by campaign. **`install` carries no campaign properties.**
Campaign data exists only on `supporter_checkout_clicked` (`campaign`, `source`, `medium`).

**So we can attribute a checkout click to a channel, but not an install.** That is a real gap
between the funnel on line 15 and what is instrumented.

**Build instead:** channel → checkout-click attribution (Panel 5b), and treat install-side
attribution as an open item. **@Growth Engineer** — flagging rather than specifying an unbuildable
panel, per the task constraint. If install-side attribution is wanted, `install` needs campaign
properties, which likely means capturing them at download time rather than first run.

---

## 2. Activation — the section that gates everything

> ### ⚠️ Structural guard: every panel in this section uses **Math = Unique users**
>
> The trap named in the task is `session_launched` (fires on **every** launch) versus
> `first_agent_launched` (fires **once**). One enthusiastic user launching forty agents reads as
> forty activations on a Total-count panel and falsely clears the 40% Phase 2 gate.
>
> **Setting Math to Unique users makes the mistake impossible rather than merely warned against**,
> because unique-users of `session_launched` and unique-users of `first_agent_launched` are the same
> population. The panel gives the right answer even if someone picks the wrong event.
>
> **Total count is permitted in exactly one place: Panel 6 (usage intensity), which is explicitly
> labelled NOT an activation metric.**

### 2a. Activation funnel — the core panel

| | |
|---|---|
| **Type** | **Funnel** |
| **Steps** | 1. `install` → 2. `first_agent_launched` → 3. `first_team_launched` → 4. `first_task_completed` |
| **Math** | Unique users |
| **Filter** | `edition = prod` |
| **Conversion window** | 30 days ⚖️ |
| **Window** | Last 12 weeks |

**Use PostHog's funnel type, not four separate number tiles.** A funnel ties each step to the same
user, so step 3 is measured against people who actually did step 1 — which is the denominator
convention in `METRICS_FRAMEWORK.md` §3, enforced by the tool instead of by memory.

**Targets — the operator's Phase 2 gate, and the hard launch gate:**

| Step | Target | Source |
|---|---|---|
| install → first agent | **40%** | Growth plan Phase 2 |
| install → first team | **20%** | Growth plan Phase 2 |
| install → first task completed | **10%** | Growth plan Phase 2 |

### 2b. 🔴 The distortion you must read alongside 2a — and it is worse than "some drop-off"

**`first_agent_launched` fires at the launch request, not when an agent does anything.**
Verified: `sessions.go:2366-2367` emits it in the launch handler, immediately after
`session_launched`.

**Consequence — and this is the most important sentence in this document:**

> **Step 2 of the funnel counts users who clicked Launch, not users who got a working agent.**
> Because of the unsurfaced trust-prompt hang (#33), an agent can emit `first_agent_launched` and
> then sit frozen forever waiting on a keystroke the user never sees.

So:

- **The 40% agent-launch gate can be cleared entirely by users whose agents all hung.** It measures
  intent, not success.
- **The real activation signal is `first_task_completed`** — the 10% target — because it is the only
  step downstream of the hang.
- **The drop between step 2 and step 4 is currently part frozen screen, part lost interest, and the
  data cannot separate them.**

**Until #33 ships:** annotate this panel in PostHog with "step 2→4 drop is inflated by the
trust-prompt hang (#33); treat `first_task_completed` as the true activation number." Do **not** read
the step 2→4 drop as disengagement.

**Once #33 ships:** add a PostHog annotation on the release date. The step 2→4 rate before and after
that line are **not comparable** and must never be pooled. If the rate jumps, that is **users being
told to look at their terminal** — not a marketing win.

⚠️ **#33 surfaces the hang; it does not fix it.** Read the shipped implementation before drawing
conclusions from this panel. The agent still blocks on the prompt; the dashboard now says "Check
terminal" so the user knows to go answer it. Two consequences for this panel:

1. **The step 2→4 drop remains uninterpretable in the data even after #33.** The Growth Engineer
   established that a blocked agent and a healthy idle agent are **not distinguishable from outside
   the CLI** — both are a live process waiting on stdin, and the only separator would be matching a
   vendor's specific prompt string, which was correctly ruled out. So the UI reports the honest
   union ("this agent hasn't done anything yet"), and **the telemetry has no signal for frozen
   versus disengaged at all.** Do not expect #33 to make that drop readable.
2. **Any improvement is behavioural, not mechanical.** It depends on users noticing a banner and
   acting on it, so it will be partial and will vary by how visible the dashboard is to them.

**The genuine fix would be an agent that does not block unattended, or a prompt surfaced inside
Coral rather than pointed at.** Neither exists. Until one does, `first_task_completed` remains the
only activation number not contaminated by this.

**⚖️ My recommendation to the operator:** treat the **10% `first_task_completed` target as the real
launch gate** and the 40%/20% steps as diagnostics. A user whose agent froze has not activated in
any sense that matters, and the current instrumentation cannot tell you they froze.

### 2c. Activation by platform

| | |
|---|---|
| **Type** | Funnel, same steps as 2a |
| **Breakdown** | `os` |
| **Window** | Last 12 weeks |

We have **one verified platform**. If Linux activation is dramatically worse, that is an install-path
defect nobody here can reproduce. Low volume will make this noisy — **report counts, not
percentages, below ~20 installs per platform.**

### 2d. Team size

| | |
|---|---|
| **Type** | Trend / table |
| **Event** | `first_team_launched` · **Breakdown:** `agent_count` |
| **Window** | Last 12 weeks |

Tells us whether people try the multi-agent workflow the positioning is built on, or launch teams of
one. ⚖️ A concentration at `agent_count = 1` would be a significant signal against the ICP thesis.

---

## 3. Retention

### 3a. 24-hour return

| | |
|---|---|
| **Type** | Funnel: `first_agent_launched` → `returned_24h` |
| **Math** | Unique users · **Conversion window:** 14 days ⚖️ |

**Measured against activated users, not installs.** Retention of someone who never launched an agent
is not a meaningful number and mixing them in flatters the metric.

**Healthy** ⚖️: ≥40%. This is my judgment, not a benchmark.

### 3b. 7-day return — derived, no event needed

| | |
|---|---|
| **Type** | **Retention** insight |
| **Cohortising event** | `first_agent_launched` |
| **Returning event** | `app_opened` |
| **Period** | Weekly, 8 periods |

There is no `returned_7d` event and **it does not need one** — PostHog's native retention insight
derives it from `app_opened` timestamps. Do not ask for a new event here.

**Healthy** ⚖️: ≥25% week-1.

---

## 4. Supporter intent

### 4a. Checkout clicks by surface

| | |
|---|---|
| **Type** | Trend / table |
| **Event** | `supporter_checkout_clicked` · **Breakdown:** `surface` |
| **Math** | **Both** — Total count *and* Unique users, side by side |

The only panel where the gap between the two is itself the insight: many clicks from few users means
one person clicking repeatedly, most likely because **checkout is broken**.

**Target:** growth plan Phase 4 — **5–10% of retained users**.

### 4b. 🔴 Read this panel against the activation-wall defect

Until #26 ships, the supporter screen appears on **launch 1**, before the user has any value
(`launch_counter.go:29`). Clicks from that surface are not supporter intent — they are people
looking at a wall.

**Segment by `surface` and never pool the pre-value surface with post-value ones.** After #26, add
an annotation; rates before and after are not comparable.

---

## 5. Revenue

### 5a. Activations

| | |
|---|---|
| **Type** | Trend, weekly |
| **Event** | `license_activated` · **Breakdown:** `variant_name` |

⚠️ **This is not the same number as Lemon Squeezy revenue.** `license_activated` fires on *every*
successful activation, including a supporter reactivating on a new machine. LS knows about
purchases; PostHog knows about activations. **Never report one as the other** — reconcile monthly
and expect them to differ.

**Milestone counter:** the goal is 10 supporter sales. Sales come from **Lemon Squeezy**, not here.

### 5b. Channel → sale

| | |
|---|---|
| **Type** | Funnel: `supporter_checkout_clicked` → `license_activated` |
| **Breakdown** | `campaign` (also `source`, `medium`) |
| **Conversion window** | 7 days ⚖️ |

**The single most valuable panel for channel decisions**, because it is the only place a channel
connects to money. Requires #19's campaign properties to be flowing.

**Target** ⚖️: 20–30% of clicks convert. **This is my judgment, not a verified benchmark, and I do
not want it quoted back as researched.** Its honest use is as a **tripwire**: below ~10% suggests
checkout is broken rather than that pricing is wrong.

---

## 6. Usage intensity — ⚠️ NOT an activation metric

| | |
|---|---|
| **Type** | Trend |
| **Events** | `session_launched`, `team_launched` · **Math: Total count** |

**The only place Total count is permitted.** This panel answers "how heavily do people who use Coral
use it," which is a genuine question and a different one from "how many people activate."

> **Name this panel "Usage intensity — NOT activation" in PostHog.** The name is the guard. Someone
> will eventually screenshot a panel from this dashboard into a launch discussion, and the title is
> what travels with it.

**⚖️ Interesting reading:** `session_launched` total ÷ `first_agent_launched` unique = average
launches per activated user. High is good — it means people came back. It is a **retention** signal,
never an activation one.

---

## 7. Panels deliberately NOT specified

| Wanted | Why not |
|---|---|
| Qualified visitors by campaign | No install-side campaign attribution — §1b |
| Downloads | GitHub release API, not PostHog. Track separately |
| Time-to-first-agent | No duration instrumentation. Would need a new event; not worth it before #33 |
| Abandonment reason | Growth plan Phase 2 wants one optional question on abandonment. Not built |
| Stars / forks / watchers | GitHub, not PostHog — see the weekly loop |

---

## 8. Build order

1. **Panel 0 (liveness)** — before anything else. Without it every other panel is unreadable.
2. **Panel 2a (activation funnel)** — the gate.
3. **Panel 1a (installs)** — the denominator.
4. **Panels 3a/3b (retention).**
5. **Panels 4a, 5a, 5b (supporter path)** — after #19 lands.
6. **Panel 6 (usage intensity)** — last, and name it as specified.

**Annotations to add on day one**, so future readings are interpretable: the date #33 ships (trust
prompt), the date #26 ships (nag timing), the date #29 ships (activation-page copy), and every
release date.
