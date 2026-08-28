# Coral Launch-Week Sequence

**Owner:** GTM Strategist · **Task:** #24

> ## ⛔ There is no launch date, and nobody should ask for one.
>
> External launch is **hard-gated by the operator** on the Phase 2 activation targets, measured on
> real release data: **40% of new installs launch an agent, 20% launch a team, 10% complete a
> task.** This document describes the sequence **once that gate clears**. Every day below is
> relative — Day 1 is "the day the operator says go," not a date.

---

## Part 1: Entry criteria

Do not begin Day 1 until **every** box is checked. Each maps to a channel-plan precondition.

### Product
- [ ] **P1** First-run activation wall gone (#26) — the first screen is the dashboard, not a price
- [ ] **P2** False supporter benefits cut from the activation page (#29) — copy below
- [ ] Unknown `agent_type` no longer silently starts Claude (#32)
- [ ] Activation targets met on real data — **operator's gate**

### Measurement
- [ ] **P3** #18 merged and shipped in a release build
- [ ] **B11: a real release build produces an `install` event in PostHog.** Not "the secret exists" —
      an event lands. Until this is verified, every number is unreadable
- [ ] #19 campaign attribution live, with the canonical tracked-link list distributed

### Truth
- [ ] **P0** `cdknorow.github.io/coral` no longer says `pip install agent-coral` — **operator approves the push**
- [ ] All four README docs links repointed
- [ ] Comparison table deleted; sourced prose in its place (D2/D3)
- [ ] "Any CLI-based agent" removed from README.md:37, :81 and the features table
- [ ] Proxy wording corrected per D5
- [ ] Hero image renders (currently a 403) · Discord badge no longer says `invalid`

### Install paths
- [ ] **P4** macOS DMG verified through the **full Finder flow on a clean user account**, including
      first-run EULA and Gatekeeper
- [ ] Linux tarball **executed**, not just inspected
- [ ] Homebrew tap installs end to end, or Homebrew is cut from all copy
- [ ] Windows: **stays out.** Not "coming soon" — out

### Assets
- [ ] 90-second demo, self-hosted, showing the wedge (Channel Plan §3.3)
- [ ] Show HN post + first comment drafted
- [ ] Objection FAQ read by everyone who will be replying
- [ ] Verified 10-minute quickstart (#23)

**If any box is unchecked on Day 1, the correct action is to not start.** A Show HN fires once.

---

## Part 2: The week

**Staffing reality:** the single highest-value activity all week is **being present in the
comments**. Everything is scheduled around keeping a human free to answer.

### Day −7 to −1: quiet preparation

- Cut the release the launch will point at. Let it sit **at least 48 hours** — never launch on a
  build published the same day.
- Confirm the `install` event arrives from *that* build. Then confirm again after the tag.
- Dry-run the quickstart on a clean machine, from the link in the post.
- Pre-write the four hard answers (FAQ Q1–Q4) so nobody is composing telemetry copy under pressure.
- **Do not tease.** No "big news coming." It converts nothing and looks like marketing.

### Day 1 — Tuesday to Thursday, morning US Eastern: **Show HN**

Weekday mornings; avoid Friday and weekends.

- **Title:** `Show HN: Coral – Run Claude Code, Codex, and Gemini CLI as one team`
- **Link to the repo**, not a landing page — landing pages don't qualify as Show HN.
- Post the first comment yourself immediately: what it is, why you built it, what it does **not** do
  (Claude-only users may not need it; four agents, not arbitrary ones; macOS and Linux only), and
  what feedback you want.
- **Then stay in the thread for six hours.** This is the whole job.
- ⛔ **Do not ask anyone to upvote or comment.** HN's guidelines say so explicitly and it is the one
  mistake that cannot be recovered from.
- Nothing else ships today. One channel, one signal, clean attribution.

**Leading with the "what it doesn't do" list is not modesty, it is strategy.** The top comment will
otherwise be someone pointing out that Claude Code has agent teams. Saying it first converts the
thread's strongest objection into evidence that you are honest.

### Day 2 — read the data, fix one thing

- Pull the funnel by campaign. The number that matters is `first_agent_launched ÷ install`, not
  visitors.
- Collect every objection from the thread verbatim. Add any new one to the FAQ **the same day**.
- If a real defect surfaced, fix it and say so in the thread. Nothing converts a skeptical HN reader
  like a same-day fix attributed to their comment.
- **Do not open a second channel today.** Attribution stays clean and the team stays responsive.

### Day 3 — first Reddit post, single subreddit

**Only if the verbatim rules for that subreddit have been read and recorded** in
`CHANNEL_PLAN.md` §3.5. That research is still outstanding and is a blocking prerequisite.

- Highest-ICP-density sub first (candidate: r/ClaudeAI), **one sub only**.
- Lead with the workflow, not the product. Distinct tracked link.
- Same presence rule: stay and answer.

### Day 4 — GitHub Discussions + the Phase 3 interview invitation

- Post an Announcements thread with the demo and an honest "what's next / what's missing."
- **Invite people to a 15-minute conversation.** Growth plan Phase 3 needs five interviews and three
  observed onboardings, and this is the single most legitimate route to them.
- These conversations are also, realistically, where the first supporter sales come from
  (Metrics §4). Treat this day as revenue work, not community work.

### Day 5 — assess, then stop

- Full funnel review against Metrics §3.
- **Decision:** if `first_agent_launched ÷ install` is at or above 40%, queue the second subreddit
  and the newsletter pitches for the following week. **If it is below 40%, open no further
  channels** — fix the drop-off first. That is the growth plan's own rule: "Increase promotional
  effort only after activation improves enough to make additional traffic useful."
- Write down what happened while it is fresh: what converted, what the objections were, what broke.

### Day 6–7 — off

No posting. Answer anything that comes in. Launch week's second half is for reading, not shipping.

---

## Part 3: Deliberately not in launch week

| Not doing | Why |
|---|---|
| Multiple subreddits | Rules violations, and unreadable attribution |
| Newsletter pitches | They cover things with an existing signal. Pitch in week 2 |
| A Product Hunt launch | Wrong audience for a local CLI-adjacent dev tool |
| Paid ads | We do not yet know what a conversion is worth |
| A YouTube video | Channel Plan §3.11 — deferred |
| Announcing the supporter license | It is in the product and the README. Leading with it during launch reframes a free tool as a paid one |

---

## Part 4: Abort conditions

**Stop the sequence immediately if any of these occur.** Stopping is cheap; a bad launch is not.

1. **A first-run defect appears** — anything blocking a new user from launching an agent. Pull back,
   fix, relaunch later. The audience will still be there.
2. **`install` events stop arriving.** Launching blind is worse than not launching.
3. **A claim in our copy is publicly disproven.** Correct it in-thread within the hour, then pause
   further channels for the day. Three separate false public claims have already been caught
   internally — that process worked, and it has to keep working in public.
4. **The docs site regresses to the pip instruction.** Every visitor is being misdirected.

---

## Part 5: What success looks like

**Not** front page, **not** stars, **not** a traffic spike.

> **Five activated users and three retained users in launch week** — the growth plan's own weekly
> targets — with attribution good enough to say which channel produced them.

A Show HN that produces 5,000 visitors and 2 activated users is a **failed** launch that will look
like a successful one in every screenshot. Report the activation number first, every time.
