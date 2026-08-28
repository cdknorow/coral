# Coral Growth and Supporter Sales Plan

## Goal

Reach the first 10 optional supporter-license sales by improving activation and retention before investing heavily in traffic.

Coral remains free and fully unlocked. The $49.99 one-time Coral Pro license is an optional way to support continued development. Supporters receive priority support, priority consideration for feature requests, and removal of the periodic support reminder.

---

## Guiding Funnel

Measure and improve this sequence:

`qualified visitor → download → first open → first agent → first team → first completed task → return visit → supporter checkout → license activation`

Traffic alone is not the primary success metric. A channel is useful when it produces activated and retained users.

---

## Phase 1: Establish Reliable Analytics

**Timeline:** 1–2 days

### Work

- Track these anonymous events:
  - `app_opened`
  - `first_agent_launched`
  - `first_team_launched`
  - `first_task_completed`
  - `returned_24h`
  - `supporter_checkout_clicked`
  - `license_activated`
- Include version, edition, operating system, architecture, and campaign attribution where applicable.
- Do not collect prompts, source code, repository names, agent output, personal information, or license keys.
- Add a user-facing telemetry disclosure and opt-out control.
- Record non-sensitive delivery failures locally during development instead of silently discarding them.
- Add tracked campaign parameters to public supporter links.
- Build a PostHog dashboard covering acquisition, activation, retention, supporter intent, and completed purchases.
- Confirm release builds contain the intended PostHog project key while local builds remain untracked unless explicitly configured.

### Exit Criteria

- The full funnel is visible for release users.
- Delivery failures can be diagnosed.
- Telemetry behavior is documented and user-controllable.

---

## Phase 2: Improve First-Run Activation

**Timeline:** 3–5 days

### Work

- Add a guided first-run experience that targets a successful result within 10 minutes.
- Detect supported installed agents automatically.
- Offer a one-click starter team with a clear explanation of each agent's role.
- Provide a small, useful starter task rather than an empty dashboard.
- Explain what Coral will create and execute before launching agents.
- Show a clear success state after the first task completes.
- Ask one short, optional question when a user abandons onboarding.
- Keep supporter messaging out of the critical path until the user has received value.

### Targets

- At least 40% of new installs launch an agent.
- At least 20% of new installs launch a team.
- At least 10% of new installs complete a task.

---

## Phase 3: Interview Interested Users

**Timeline:** First week

### Audience

- GitHub stargazers
- Discord members
- Issue authors
- Contributors
- Release users who voluntarily respond to an in-product invitation

### Work

- Invite users through public, non-spammy project channels.
- Offer a 15-minute conversation or personal onboarding session.
- Ask:
  - What attracted them to Coral?
  - Did they download and open it?
  - Where did they stop?
  - What outcome did they expect?
  - What would make Coral part of their regular workflow?
- Observe onboarding directly when users consent.
- Summarize repeated objections and failure points after every five conversations.

### Exit Criteria

- Five user interviews completed.
- Three onboarding sessions observed.
- The top three activation or retention barriers are documented with evidence.

---

## Phase 4: Repair Supporter Conversion

**Timeline:** First week

### Storefront

- Rename the Lemon Squeezy store from `subgentic` to `Coral`.
- Add the Coral logo, product image, and dashboard media.
- State prominently that Coral is free and purchasing a license is optional.
- Explain exactly what supporter funds enable.
- List the supporter benefits without implying that core product features require payment.
- Reinforce that the price is $49.99 once, with no subscription.
- Add campaign attribution and checkout analytics.

### In-Product Messaging

- Show supporter prompts only after demonstrated value, such as a completed task or repeat use.
- Use a persistent but unobtrusive supporter link in settings.
- Thank activated supporters and stop showing support reminders.
- Test messaging based on outcomes, not artificial feature scarcity.

### Target

- 5–10% of retained users click the supporter offer.

---

## Phase 5: Create Qualified Demand

**Timeline:** Weeks 2–4

### Demonstration Topics

- Run Claude Code and Codex on the same feature.
- Separate implementation and review between independent agents.
- Monitor five coding agents without managing five terminal windows.
- Resume an AI coding team with its history intact.
- Measure the cost of parallel coding-agent workflows.
- Use isolated worktrees to prevent agents from overwriting one another.

### Channels

- GitHub releases and Discussions
- Coral Discord
- Hacker News
- Relevant Reddit communities, following their promotion rules
- Dev.to or Hashnode
- Short demo videos
- Helpful direct responses to developers publicly discussing multi-agent coordination problems

Do not send unsolicited bulk messages or manufacture engagement. Each campaign should lead with a useful workflow, include a distinct tracked link, and respect the rules of its destination community.

### Weekly Targets

- 100 qualified visitors
- 20 downloads
- Five activated users
- Three retained users

---

## Phase 6: Weekly Operating Loop

Review these metrics every Friday:

| Stage | Metric |
|---|---|
| Acquisition | Qualified visitors by campaign |
| Interest | Release downloads |
| Activation | First opens, agent launches, team launches, completed tasks |
| Retention | 24-hour and seven-day return rates |
| Support intent | Supporter checkout clicks |
| Revenue | License activations and completed purchases |
| Research | Most common abandonment reason |

For each weak stage:

1. Identify the largest measurable drop-off.
2. Review qualitative feedback related to that drop-off.
3. Ship one focused improvement.
4. Measure it for a complete weekly cycle.
5. Keep, revise, or remove it based on evidence.

Increase promotional effort only after activation improves enough to make additional traffic useful.

---

## Milestones

1. Five activated users
2. Three retained users
3. First supporter sale
4. Three supporter sales
5. Ten supporter sales

The immediate priority is Phase 1. Without reliable funnel measurement, additional traffic will produce downloads but little insight into why people do or do not become regular users and supporters.
