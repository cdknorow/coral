# Coral User Interview Kit — Phase 3

**Owner:** GTM Strategist (built the instrument) · **Run by:** the operator, in person
**Task:** #36 · **Spec:** `GROWTH_AND_SUPPORTER_SALES_PLAN.md` Phase 3, lines 74–104

---

## ⚠️ Read this first

**This kit is run by a human, with real users, one conversation at a time.** No part of it can be
executed by an agent, and nothing in it should be simulated. A synthetic interview is worse than no
interview: it produces fabricated evidence that then steers the roadmap, and it is indistinguishable
from real evidence three weeks later when someone cites it.

**If you cannot get five real conversations, report four.** An honest four beats a padded five.

---

## Why this is worth your time — the reasoning, not just the instrument

At **31 stars and 1 recorded download**, there is no funnel to optimise yet. My own arithmetic in
`METRICS_FRAMEWORK.md` §4 says reaching 10 supporter sales through the channel funnel needs roughly
16,665 qualified visitors — over three years at the plan's target rate.

**These conversations are worth more per unit than any channel in `CHANNEL_PLAN.md`.** Three reasons:

1. **At this size, early supporters are people who talked to the maintainer.** That is the realistic
   path to the first ten sales, not traffic.
2. **We have one verified platform and no Linux or Windows coverage at all.** Users are on machines
   we do not have. They will describe behaviour nobody here has ever observed.
3. **Today we found four defects a user cannot report**, because from their side they are invisible:
   an agent that hangs on an unsurfaced trust prompt looks like a slow agent; a message board that
   silently talks to the wrong port looks like agents ignoring each other; a docs site that installs
   a different product looks like the product; a supporter screen on first launch looks like a
   paywall. **A user will not file a bug for any of these.** They will quietly stop using it.

**Interviews are the only instrument that finds the third category.** That is what makes them worth
more than another channel.

---

# Part 1 · Outreach

## The honest framing

**You are a maintainer asking for fifteen minutes. You are not a company running research.** At 31
stars, the person you are writing to can see the star count too — anything that sounds like a
research programme will read as absurd, and rightly so.

**Rules, from the spec:** public, non-spammy project channels only. No unsolicited bulk messaging.
No manufactured engagement. Nothing that looks automated.

**Practical constraints:**

- ⛔ **Do not DM stargazers.** Starring a repo is not consent to be contacted. It is the fastest way
  to turn 31 goodwill signals into 31 people who find us intrusive.
- ✅ **Do post publicly** where people opted in: Discord, GitHub Discussions, a release note.
- ✅ **Do reply personally** to someone who already contacted us — an issue author, a PR
  contributor, someone who asked a question in Discord. They opened the door.
- **Send them one at a time, written individually.** Five personal messages beat fifty templated
  ones, and at this scale fifty is not available anyway.
- **One ask per person. Ever.** No follow-up if they don't reply. Silence is an answer.

## Message 1 — GitHub Discussions / Discord announcement (public, opt-in)

> **Subject: Can I borrow 15 minutes? (I'm the person who builds Coral)**
>
> I'm trying to work out where Coral actually falls down for people, and I've run out of ways to
> find that on my own machine.
>
> If you've tried Coral — **including if you bounced off it, gave up, or never got it running** —
> I'd like 15 minutes to hear what happened. The ones where it didn't work are genuinely the more
> useful conversations; I'm not looking for encouragement.
>
> No pitch, no demo, no signup. Just questions. Reply here or DM me and we'll find a time.
>
> If you'd rather type than talk, that works too — tell me what happened in a message and I'll take
> it just as seriously.

**Why it's shaped this way:** naming the failure cases explicitly is what makes people who failed
willing to answer. Most research invitations quietly select for people who succeeded, which is
exactly the wrong sample when you are trying to find where people stop.

## Message 2 — personal reply to an issue author or contributor

> Hey — you opened [#N / asked about X] a while back, so you've clearly had Coral in front of you.
>
> I'm trying to understand where it actually breaks for people rather than guessing from my own
> setup. Would you be up for 15 minutes sometime? Mostly me asking what happened when you tried it.
>
> Completely fine to say no, and it won't affect your issue either way.

**The last line matters.** Without it, someone waiting on a bug fix may feel obliged.

## Message 3 — the onboarding-session ask (higher bar, ask separately)

Only ask this of someone who has **already** had a conversation, or who volunteers that they haven't
installed it yet.

> Would you be willing to install it while I watch? Screen share, about 20 minutes.
>
> I'd stay quiet the whole time — the point is to see where it's confusing without me jumping in to
> explain, because I can't ship myself alongside the download. It's fine and genuinely useful if it
> goes badly.

**"I'd stay quiet the whole time" sets the expectation you need for Part 3 to work.** Say it up
front or the session becomes a support call.

## Message 4 — the in-product invitation (spec: "release users who voluntarily respond")

For a release note or a low-key in-app link. **Must not appear before the user has got value** —
same rule as the supporter prompt.

> Using Coral? I'd like 15 minutes to hear how it's going, especially if it isn't. → [link]

## Sourcing the five conversations, in order of likelihood

| Source | Why | Consent status |
|---|---|---|
| Issue authors and PR contributors | Already engaged us; highest reply rate | ✅ opened the door |
| Discord members | Opted into a channel | ✅ post publicly |
| GitHub Discussions respondents | Self-selected | ✅ |
| Anyone who emailed about the license | Already in a conversation | ✅ |
| Stargazers | 31 people, nameable | ⛔ **do not DM** — reach via public posts only |

---

# Part 2 · The interview guide (15 minutes)

## How to run it

- **Ask what they did, never what they would do.** Hypotheticals produce fiction.
- **Shut up after the question.** Count to five in your head. The second thing they say is the
  useful one.
- **Never explain, defend, or correct.** If they describe Coral doing something it doesn't do, that
  is your most valuable data point. Write it down and move on.
- **Never name a defect.** Not "did it hang?" — the whole design below is to let them tell you.
- **Take verbatim notes.** Their words, not your summary. Record with permission.
- **You will want to help. Don't.**

## Opening (1 min)

> Thanks for doing this. I build Coral. There's nothing I want you to say — if it was frustrating
> or you gave up, that's the most useful version of this call.
>
> Mind if I record so I'm not typing the whole time? It won't go anywhere public.

## Q1 — What attracted them *(spec line 91)*

> **How did you come across Coral, and what made you look twice?**

Follow-ups:
- What were you doing at the time that made it seem relevant?
- **What did you think it would do?** ← *this one catches positioning failures*
- Were you already running more than one coding agent? Which ones?

*Listening for:* whether the multi-vendor ICP is real. If they only run one agent, our whole
positioning premise needs revisiting.

## Q2 — Did they download and open it *(spec line 92)*

> **Walk me through what you actually did — from the page you landed on to the last thing you tried.**

Follow-ups, asked plainly and without leading:
- **How did you install it?** ← 🔴 *see the "invisible defects" section — do not prompt*
- What machine were you on? ← 🔴 *we have one verified platform*
- **What was the first screen you saw after it started?** ← 🔴
- Did anything make you stop and think, or go looking for instructions?

*Listening for:* `pip`, a version number, a wrong download, Gatekeeper, the activation screen.

## Q3 — Where did they stop *(spec line 93)*

> **Where did you get to, and what were you doing when you stopped?**

Follow-ups:
- What was on screen at that moment?
- **How long did you wait before you did something else?** ← 🔴 *surfaces a hang without naming it*
- What did you try next?
- Is it still installed?

*Listening for:* waiting, "it seemed stuck," "I wasn't sure if it was working," reinstalling,
giving up silently.

**If they got agents running:**
- Did the agents ever seem aware of each other? How could you tell? ← 🔴
- Did you look at more than one agent at once? How?
- Did anything they wrote get lost or overwritten? ← 🔴

## Q4 — What outcome did they expect *(spec line 94)*

> **If it had worked exactly the way you hoped — what would have happened?**

Follow-ups:
- What would you have used it for first, on a real project?
- What did it do instead?
- Was there a moment it did something you liked?

## Q5 — What would make it part of their workflow *(spec line 95)*

> **What would have to be true for you to open this again next week?**

Follow-ups:
- What are you doing instead right now?
- Have you tried anything else for this? What made you keep or drop it?  ← *competitive reality*
- What would make you tell someone else about it?

## Closing (1 min)

> Anything I should have asked and didn't?
>
> Would you be up for me coming back in a month or two once some of this is fixed?

**Do not pitch the supporter license. Not once, not at the end, not "while I have you."** It
converts a conversation into a sales call retroactively and poisons the next one.

---

## 🔴 The invisible defects — how to surface them without leading

We found four things today that **a user cannot report**, because from their side they don't look
like defects. Each has a non-leading question above. This table exists so you know what you are
listening *for* — **never read the right-hand column out loud.**

| What we know | Non-leading question | What it sounds like when they hit it |
|---|---|---|
| **Agents hang on an unsurfaced trust prompt** (#33). Every agent in a team launch, silently | "What was on screen when you stopped?" / "How long did you wait?" | "It just sat there." "I couldn't tell if it was thinking." "I left it and came back and nothing had happened." |
| **Message board silently talks to port 8420** (#34), so it breaks on any non-default port | "Did the agents ever seem aware of each other? How could you tell?" | "They seemed to ignore each other." "I don't think the team part worked." |
| **The docs site installed the abandoned Python package.** `pip install agent-coral` succeeds and reports version 4.4.1 | **"How did you install it?"** — nothing more | "pip." "I think I got 4.something." "The docs said…" |
| **Supporter screen appears on launch 1** (#26) | "What was the first screen you saw after it started?" | "It asked me to buy something." "I thought it was a trial." "I assumed it was paid." |
| **Agents can overwrite each other** on a shared branch | "Did anything get lost or overwritten?" | "One of them undid the other's work." "I ended up with duplicate functions." |

**If a user says "I installed it with pip" — stop and get the whole story.** How they found that
page, what version they got, whether they ever saw a DMG. That single answer sizes the entire
wrong-product problem, and it is the one thing they will never volunteer as a complaint because from
their side the install *worked*.

---

# Part 3 · Observed onboarding protocol

**Three sessions, per the spec's exit criteria. This is the highest-signal item in Phase 3 and the
easiest to ruin by being helpful.**

## Setup (before they share their screen)

Say this out loud, every time:

> I'm going to stay quiet while you do this. If you get stuck, that's the point — please don't wait
> for me to rescue you. Try whatever you'd normally try, including giving up.
>
> Say what you're thinking as you go, if you can. Especially when something is confusing.

Then: **start a timer, and shut up.**

## What you must NOT do

⛔ These are not guidelines. Each one destroys the session's value.

| Don't | Why |
|---|---|
| Explain what a screen means | You cannot ship yourself with the download |
| Answer "is it supposed to do that?" | Say: "What would you expect?" |
| Point at the thing they're looking for | The fact that they can't find it **is the finding** |
| Say "you'll want to click X" | You have just deleted the drop-off you came to measure |
| Defend a design | Write it down and move on |
| Fill a silence | Confusion takes longer than it feels. Let it run |
| Stop them giving up | **A user giving up is the single most valuable event in this session** |

**The one exception:** if they are about to do something destructive to their own machine or repo,
stop them. Note that you intervened and why.

## What to record

For each session, note times against a stopwatch:

- [ ] `t=0` — they start
- [ ] First hesitation — what was on screen?
- [ ] **First screen after launch** — dashboard, or the activation screen?
- [ ] Time to first agent launched
- [ ] Time to first agent **doing something visible**
- [ ] Any period **>30s where nothing appeared to happen** — what were they looking at, what did
      they do? 🔴 *this is the trust-prompt hang*
- [ ] Every moment they re-read instructions, scrolled looking for something, or opened a browser
      tab to search
- [ ] Every question they asked out loud — **verbatim**
- [ ] Time to first completed task, or the point they stopped
- [ ] `t=end` — did they finish, or give up?

**Their exact words at the moment of confusion are worth more than your explanation of it.**

## Afterwards (5 min, still recording)

- What were you expecting there?
- What would you have done if I hadn't been on the call?
- Would you have kept going?
- Was there a point you thought it was broken?

---

# Part 4 · Synthesis

Structure and worksheet: **`INTERVIEW_SYNTHESIS_TEMPLATE.md`**.

The spec requires summarising after every five conversations, and exiting with **the top three
activation or retention barriers documented with evidence**. The template turns five anecdotes into
a ranked list; use it rather than writing a narrative, because a narrative will over-weight the most
recent conversation.

---

## Exit criteria

- [ ] Five interviews completed
- [ ] Three onboarding sessions observed
- [ ] Top three barriers documented **with evidence** — a quote and a session reference each

## One thing to protect

**Do not fix things quietly between interviews.** If you ship a fix after interview 2, interviews
3–5 measure a different product and you cannot pool them. Note the fix, finish the five, then ship.
If something is so severe you must fix it immediately, record which interview it changed after and
treat the set as two cohorts.
