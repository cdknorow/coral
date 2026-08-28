# Coral Channel Plan

**Owner:** GTM Strategist · **Task:** #24
**Ranked by likelihood of producing ACTIVATED users — not traffic.** The growth plan is explicit:
"Traffic alone is not the primary success metric. A channel is useful when it produces activated
and retained users." Every ranking below follows that, and in two cases it inverts what a
traffic-first ranking would say.

---

## 0. Hard preconditions — no channel opens until all of these clear

These are gates, not recommendations. Opening a channel before they clear spends a
one-time-only launch on a broken funnel.

| # | Gate | Why it blocks *every* channel | Owner |
|---|---|---|---|
| **P0** | **`cdknorow.github.io/coral` no longer serves `pip install agent-coral`** | Every channel routes some traffic through a live install funnel for the abandoned Python product. Fixing copy while that page stands just delivers people to it faster | Producer #27 → operator |
| **P1** | **First-run activation wall removed** | The first screen a new user sees is a $49.99 price (`launch_counter.go:29`). HN's own Show HN guidance is "make it easy for users to try your thing out, ideally without barriers" | Growth Eng #26 |
| **P2** | **False supporter benefits cut from the activation page** | We currently advertise two free features as paid. One screenshot of that ends the thread | Growth Eng #29 |
| **P3** | **Funnel instrumented end to end** | Without it we cannot tell a good channel from a bad one, which is the entire point of this plan | Growth Eng #18 |
| **P4** | **Every install path in our copy verified on a real machine** | Standing rule. A broken install command in a launch post is unrecoverable | Dev Advocate #22 |
| **P5** | **Activation targets met on real data** — 40% launch an agent, 20% launch a team, 10% complete a task | Operator's hard gate. Not mine to move | Operator |

**P0–P4 gate *any* outbound activity. P5 gates external launch specifically** (Show HN, Reddit,
newsletters). The always-on channels in Tier 1 can be brought up as soon as P0–P4 clear.

**There is no launch date and nobody should ask for one.**

---

## 1. The ranking

Effort is engineer/advocate days. Impact is **activated users**, defined as
`first_agent_launched` — not visits, not stars.

| # | Channel | Effort | Traffic | **Activation impact** | Repeatable | Gate |
|---|---|---|---|---|---|---|
| 1 | **GitHub repo itself** | 1–2d | Medium | **Very high** | Always on | P0–P4 |
| 2 | **Show HN** | 2d prep | Very high, one-shot | **High** | **Once** | P0–P5 |
| 3 | **A 90-second demo video** | 2–3d | Low alone | **Very high as a multiplier** | Reusable | P0–P4 |
| 4 | **Helpful replies where the pain is discussed** | Ongoing, ~1h/day | Low | **Highest per visitor** | Always on | P0–P4 |
| 5 | **Reddit** | 1d + rules research | High | **Medium, high variance** | Limited | P0–P5 + rules |
| 6 | **GitHub Releases & Discussions** | 0.5d/release | Low | **High per visitor** | Every release | P0–P4 |
| 7 | **Homebrew tap** | 1–2d | Low | **High per visitor** | Always on | P4 |
| 8 | **Dev newsletters** | 0.5d | Medium | **Medium** | Occasional | P0–P5 |
| 9 | **X / Twitter** | Ongoing | Low→Med | **Low** | Always on | P0–P4 |
| 10 | **Dev.to / Hashnode** | 1d/post | Low | **Low** | Repeatable | P0–P4 |
| 11 | **YouTube (long-form)** | 5d+ | Low | **Low near-term** | — | Defer |

---

## 2. Why the ranking inverts the traffic ranking

**Show HN would top a traffic ranking and it is #2 here.** It is a **single non-repeatable shot**
at the best-qualified audience we will ever get, and its conversion is entirely determined by
things that are currently broken. The same post is worth several times more after P0–P5 than
before. Ranking it #1 would create pressure to fire it early, which is precisely the failure the
growth plan is designed to prevent.

**The repo is #1 because it converts every other channel.** Nobody installs from a tweet; they
install from the repo the tweet points to. Every one of the other ten channels terminates there.
It is also the only channel that is always on, compounds, and costs nothing per visitor.

**Helpful replies (#4) have the highest per-visitor activation of anything on this list** and near
zero traffic. Someone who just wrote "I've got five Claude tabs and I've lost track" is closer to
activating than a hundred HN front-page visitors. It scores low on impact only because volume is
low — the *rate* is the best we have. The growth plan already sanctions this: "Helpful direct
responses to developers publicly discussing multi-agent coordination problems."

**The demo video (#3) is not really a channel — it is a multiplier.** README, HN, Reddit, X and
newsletters all convert measurably better with a working demo, and our README hero image is
**currently a 403** (Producer D3). Fixing that is worth more than adding an eleventh channel.

**YouTube long-form (#11) is deferred outright.** A week of production for a channel with no
subscribers, at a stage where our own funnel targets are unmet, is the worst use of a week
available to us.

---

## 3. Channel detail

### 1. The GitHub repo — effort 1–2d, impact very high

The single highest-leverage surface we own. Current state per the Producer's `README_DEFECTS.md`:
13 defects including a **403 hero image** and a Discord badge that literally renders the word
`invalid`. Every visitor from every channel sees this.

**Work:** land the defect fixes; delete the comparison table and replace with sourced prose
(Positioning Brief §5); repoint the four docs links; fix the repo description and topics.

**Topics to set** (GitHub search + sidebar discovery, free): `ai-agents`, `claude-code`,
`coding-agents`, `developer-tools`, `multi-agent`, `tmux`, `git-worktree`, `agent-orchestration`.

**Measure:** stars are a vanity metric here. Measure **repo → Releases page → download → first
open**. Today: **31 stars, 0 DMG downloads on v1.0.8** — a 0% conversion that the docs-site defect
now largely explains.

### 2. Show HN — effort 2d prep, impact high, **fires once**

**Verified rules** ([Show HN](https://news.ycombinator.com/showhn.html)):
- Must be "something you've made that other people can play with" that people can "run on their
  computers." Coral qualifies — it is a downloadable binary.
- **"Make it easy for users to try your thing out, ideally without barriers."** ← P1 is not
  optional. A $49.99 screen on first run is exactly the barrier this names.
- Blog posts, sign-up pages, landing pages **do not qualify**. Link to the repo.
- **"Please don't ask friends to upvote or comment. That's not ok on HN."** No coordination,
  no seeding. Not negotiable.
- Version bumps ("Foo 1.3.1 is out") do not qualify — this must be framed as the tool, not a release.
- Be around to discuss it in the comments.

**Title:** `Show HN: Coral – Run Claude Code, Codex, and Gemini CLI as one team`
Describes what it does, names the differentiator, no adjectives, no "why it's amazing."

**Prepare for these comments in advance — they will appear:**
1. "Claude Code has agent teams and worktrees now." → Positioning Brief §4.1/4.2. Concede the
   overlap immediately and precisely, then give the cross-vendor and durability answer. Do not
   argue the premise; it is correct.
2. "This is Claude Squad / Conductor / Crystal with a web UI." → Partly fair. Name the specific
   differences. Do not claim to be first.
3. "Telemetry with no opt-out?" → FAQ Q3, verbatim, no spin.
4. "Apache 2.0 but $49.99?" → FAQ Q4.

**Risk:** our first-run experience is the thing HN judges hardest, and we currently fail its own
stated bar. Fire this only after P5.

### 3. The 90-second demo — effort 2–3d, impact very high as a multiplier

**Content, in this order** (matches the wedge): three different agents — Claude, Codex, Gemini —
on one board, working the same repo; the dashboard showing all three live terminals; a handoff on
the message board; sleep the team, close the browser, reopen, wake it with state intact; the cost
figure spanning all three vendors.

That sequence *is* the positioning. Nothing else we can produce demonstrates the wedge as fast.

**Requirement:** self-hosted. The current Loom thumbnail 403s (Producer D3). An asset we do not
control has already broken our front page once.

### 4. Helpful replies where the pain is discussed — ~1h/day, highest rate

Rules, non-negotiable, straight from the growth plan: "Do not send unsolicited bulk messages or
manufacture engagement."

**Do:** answer the actual question first, with something useful even if they never try Coral;
disclose authorship every time ("I build Coral, so take this with salt"); link only when it is a
real answer to what was asked.
**Never:** drive-by links, sockpuppets, comments that exist only to mention Coral, replying to
anything older than a few days.

**Where:** HN threads on multi-agent coding, Reddit threads asking how to run several agents,
GitHub discussions on the competitor tools in Positioning Brief §5.2. Being genuinely useful in a
competitor's issue tracker is legitimate; hijacking it is not.

### 5. Reddit — effort 1d + rules research, medium impact, **high variance**

⚠️ **I could not verify subreddit rules and I am not going to guess.** `reddit.com` and
`old.reddit.com` both refused programmatic fetch, and the browser extension is not connected in
this session. Search results about specific subreddit rules were secondhand and I will not source a
promotion policy to a third-party SEO blog.

**Hard requirement before any Reddit post — assign to a human:**
open each subreddit, read the sidebar rules and any self-promotion wiki page, and paste the
**verbatim** rule text into this file. Then decide. The general pattern (subreddit rules override
site-wide norms; many require mod approval, account age, or a designated thread) is well
established, but the specifics differ per sub and the specifics are what get you banned.

**Candidates, in the order I would research them** — ordered by ICP density, not size:

| Subreddit | Why | Rules status |
|---|---|---|
| r/ClaudeAI | Highest ICP density; has a "Built with Claude" flair | **UNVERIFIED** |
| r/ChatGPTCoding | Multi-vendor coding agent users — our exact ICP | **UNVERIFIED** |
| r/commandline | Terminal-native, tmux-literate | **UNVERIFIED** |
| r/selfhosted | Local-first, runs its own servers | **UNVERIFIED** |
| r/opensource | Apache 2.0 fits; will scrutinize the $49.99 | **UNVERIFIED** |
| r/programming | Large, low ICP density, hostile to self-promo | **UNVERIFIED — likely skip** |
| r/devops | Weak fit | **Skip** |

**Approach when it opens:** one sub at a time, spaced out. Never the same post in several subs.
Lead with a workflow, not the product. Distinct tracked link per sub, per the growth plan.

### 6. GitHub Releases & Discussions — 0.5d per release, high per visitor

Anyone reading release notes has already decided to try it. Cheap and repeatable.

**Work:** real release notes, not autogenerated commit lists — lead with what a user can now do.
Enable Discussions with an Announcements category, and use it for the Phase 3 interview invitation
(the growth plan names stargazers, issue authors and contributors as the audience). This is our
most legitimate route to the five user interviews Phase 3 requires.

### 7. Homebrew — effort 1–2d, high per visitor, **but calibrate the ambition**

⚠️ **Verified constraint that changes the plan.** Primary source, quoted verbatim from
[Homebrew's Package Acceptance Policy — Notability](https://raw.githubusercontent.com/Homebrew/brew/master/docs/Package-Acceptance-Policy.md)
(which `docs/brew.sh/Acceptable-Casks` now defers to for all numerical thresholds):

> "A new package must demonstrate public interest beyond its author. A GitHub project normally
> satisfies this requirement by meeting **one of** these thresholds:
> * at least 30 forks, 30 watchers or 75 stars.
> * at least 90 forks, 90 watchers or 225 stars for a self-submission by the repository owner.
>
> […] A code repository less than 30 days old is normally not eligible."

**Read the thresholds as OR, not AND** — any *one* of forks, watchers or stars clears the bar. Two
rows, two different bars: **75 stars** is the general threshold, **225 stars** the self-submission
threshold. A submission from the Coral maintainer is a self-submission, so **225 stars — or 90
forks, or 90 watchers — is our bar.**

**Coral has 31 stars.** Watchers and forks are the cheaper of the three routes and worth tracking,
but on any measure we are far from the self-submission bar.

**Therefore:** the goal is a **working third-party tap**, e.g. `brew install cdknorow/coral/coral`,
not `brew install coral`. Growth Engineer #21 owns making it real; `Casks/coral.rb` currently
claims version **2.3.1** against a shipped **v1.0.8** — a third conflicting version lineage.

**Copy rule, from the Orchestrator:** nobody writes any `brew install` line in any copy until an
install has been proven end to end. And nobody ever writes bare `brew install coral` — it will not
work and never will at our size.

⚠️ **Verdict upgraded (Growth Engineer, adopted by the Orchestrator): stop treating upstream as a
milestone at all. The tap IS the channel.** We are at **31 stars and 6 forks** — the self-submission
bar is an order of magnitude away on every one of the three metrics, and chasing it distorts
priorities toward vanity numbers.

Size the follow-on work as **maintaining our own tap indefinitely**, which also makes the automated
`sha256` step essential rather than nice-to-have: this cask rotted in the first place because a human
had to remember to update a hash.

A genuine third party submitting on our behalf would face the lower 75-star bar — noted only to rule
it out, since engineering that is exactly the manufactured engagement the growth plan forbids.

### 8. Dev newsletters — 0.5d, medium

Realistic targets are newsletters that cover new developer tools and accept reader submissions.
**I have not verified any specific newsletter's current submission policy or rates**, so I am not
naming targets as if I had. Assign someone to check each target's submission page and record the
policy here before pitching.

**What actually gets picked up:** a working demo and a specific, novel claim. "Coral runs Claude
Code, Codex and Gemini CLI as one team" is a sentence an editor can use. "Multi-agent orchestration
platform" is not.

**Sequencing:** after Show HN, never before. Newsletters cover things that already have a signal.

### 9. X / Twitter — ongoing, low activation

Real but weak. Best use is **replies, not posts** — same discipline as #4. Demo clips outperform
text. Do not buy followers, do not engagement-farm, do not post daily into a void.

**Expectation-setting:** at our size this will not move the funnel this quarter. It is a place to
be findable, not a growth channel. Budget minutes, not days.

### 10. Dev.to / Hashnode — 1d per post, low

Low activation, but genuinely useful as **durable evergreen documentation with a distribution
side-effect**, and the growth plan names it. The strongest post is not about Coral — it is about the
problem: "What I learned running Claude Code, Codex, and Gemini on the same repo." Coral appears as
the tool used, near the end.

### 11. YouTube long-form — defer

Five-plus days of production, no channel, no subscribers, unmet activation targets. The 90-second
demo (#3) captures nearly all the value at a fraction of the cost. Revisit after the first
supporter sales.

---

## 4. Attribution

Every channel gets a **distinct tracked link**, per the growth plan ("Each campaign should lead
with a useful workflow, include a distinct tracked link"). Growth Engineer #19 owns the parameters.

Suggested scheme — one campaign per channel per attempt:
`?utm_source=<channel>&utm_medium=<post|reply|release>&utm_campaign=<slug>`
e.g. `hn-showhn-01`, `reddit-claudeai-01`, `newsletter-<name>-01`, `brew-tap`.

**The attribution must survive to `license_activated`**, or we will know which channel produced
visitors and not which produced supporters — which is the only question that matters at the end of
this funnel.

---

## 5. Weekly targets

From the growth plan, Phase 5, unchanged: **100 qualified visitors → 20 downloads → 5 activated
users → 3 retained users** per week.

**Read them as a funnel shape, not a traffic goal.** 100 → 20 → 5 → 3 implies 20% download,
25% activation-of-download, 60% retention-of-activated. If we hit 400 visitors and 5 activated,
the channel did not work — and we should not report it as a win.

**Increase promotional effort only after activation improves enough to make additional traffic
useful.** That is the plan's rule and it is the right one.
