# Activation Page — Replacement Copy

**Owner:** GTM Strategist · **Task:** #24, delivering Orchestrator ruling **D4**
**For:** Content and Launch Producer (owns the copy) → Growth Engineer #29 (implements it)
**Target:** the `activationPage` constant, `coral-go/internal/server/server.go` (~line 683 onward)

Per the Orchestrator: "@GTM Strategist send exact replacement copy to @Content and Launch Producer;
@Growth Engineer use theirs, do not write your own." This is that copy.

---

## The defect

The supporter column currently lists these as things $49.99 buys:

- ❌ **"Agent team templates & sharing"**
- ❌ **"Search chat history"**

**Both are free and ungated.** Verified independently three times: the license middleware "tracks
license state but does not gate any routes" (`server.go:245-246`, corroborated by the Orchestrator
at `middleware.go:18-19`), and the prod tier sets `TierMaxTeams = 0` / `TierMaxAgents = 0`
(`tier_prod.go:13-14`). Both features are listed as free product features at README.md:114 and :119.

The same page's own free column says "every feature unlocked." **The page contradicts itself**, and
it violates the growth plan's Phase 4 rule: "List the supporter benefits without implying that core
product features require payment" and "Test messaging based on outcomes, not artificial feature
scarcity."

This is also the single most quotable thing on our site if someone screenshots it next to our
"Apache 2.0 and free" claim.

---

## Replacement copy

### Header

```
Coral
Free and fully unlocked. A one-time license supports development and retires this reminder.
```
*(unchanged — it is accurate and well written)*

### Left column — free

```
Free & Fully Unlocked
The full product, forever

Free
No card required · every feature unlocked

  Every feature, no time limit
  Unlimited teams and agents
  Claude Code, Codex, Gemini CLI and Pi.dev
  Real-time dashboard and message boards
  Agent team templates — generate a team from a plain-English description, import one from a folder
  Token and cost tracking per agent and per session
  Native desktop app (macOS and Linux)

[ Continue Free ]
Skip this screen anytime
```

✅ **THIRD UPDATE — templates RESTORED, with corrected wording. @Growth Engineer use this version.**
I removed "Agent team templates and sharing" pending #42; **#42 verified both halves by execution**,
so it is back. But the wording changed, and the change matters:

⛔ **Do not write "save and share".** `/api/teams/import` **returns** a config; it does **not persist
one** — the team list still showed zero afterwards. There is no saved-templates store. "Save" would
imply a library that does not exist.
✅ **Verified and safe to say:** generate a team from a plain-English description (real — a
four-agent config came back in ~30s with valid types and full prompts), and import one from a folder
(real — three agents parsed, frontmatter honoured).

*(The earlier removal was still the right call at the time: I had moved this into the free column
treating "not paid" and "exists" as the same claim — the same mistake I made in this file with
"Search chat history". The difference is that this one turned out to be true.)*

⛔ **FIRST UPDATE after execution testing — read this before implementing.** My first draft moved
"Search chat history" into this column as a genuine free feature. **Full-text search has never
worked** (`FTSBody` is never assigned; zero FTS rows in production — #40). It must not appear in
*either* column until #40 lands. I have also cut "across vendors" from the cost line: cost tracking
works per agent, but the cross-vendor total silently omits vendors (#41).

**Changes:** the falsely-paid feature that is real — team templates and sharing — moves here, where
it belongs. Agent list corrected to the
four verified agents — the old "Claude, Codex & Gemini support" omitted Pi.dev. Two genuine free
features added, since the free column should be the impressive one.

### Right column — supporter

```
Coral Pro — Supporter License
Back the project · $49.99 one-time, no subscription

  Retires this periodic reminder
  Priority support
  Priority consideration for feature requests
  Directly funds ongoing development
  Activates on 1 machine

Coral stays free either way. A license backs the project — it doesn't unlock anything,
because nothing is locked.

[ Buy a License ]
```

**Changes:**
- ⛔ **"Agent team templates & sharing" — REMOVED** (free feature)
- ⛔ **"Search chat history" — REMOVED** (free feature)
- ⛔ **"Lifetime license with updates & email support" — REMOVED.** "Lifetime updates" on a free,
  fully-unlocked product is meaningless — everyone gets updates. "Email support" is redundant with
  "priority support" and implies free users get none.
- ⛔ **"Early adopter price rises as we add features" — REMOVED.** Manufactured urgency, and it
  implies future features will be paid. They will not be, and we should not hint otherwise.
- ✅ Closing line rewritten. The old version ("a license simply backs the project and stops this
  reminder") was fine; the new one answers the actual question — *what does it unlock?* — head on.
- ⚠️ **"Activates on 1 machine" is UNVERIFIED.** I could not confirm it in the code.
  **@Growth Engineer: verify against the Lemon Squeezy activation limit before shipping this line.
  If it is wrong or unenforced, cut it.** Do not ship an unverified constraint on a paid product —
  that is the one claim a paying customer will actually test.

### Footer

```
Already have a license key?
[ Enter your key ]        [ Activate License ]

Continue with the free version
Need help? Contact Support
```
*(unchanged)*

### Page `<title>`

```
- Coral — Activate License
+ Coral
```

**Why this still matters after #26.** "Activate License" reads as a paywall on a product that has
none. The original argument was that this was a first-time user's first impression; **#26 has landed
and that is no longer true** — the page now appears only after Coral has delivered a result. The
title change stands anyway on the weaker but sufficient ground: the tab says "Activate License" to
someone who has already been told the product is free and fully unlocked, and those two things
should not contradict each other.

---

## Two things not to change

1. **Do not remove the page.** A periodic, skippable supporter reminder is legitimate, it is the
   operator's decision, and it is *the* named benefit of the license. Removing it would delete the
   product's only reason to exist.
2. **Do not make the button copy pushier.** "Continue Free" as a plain, equally-weighted button is
   correct. A greyed-out or hard-to-find free path would contradict everything else we say, and it
   is the kind of dark pattern that gets a screenshot posted.

---

## ✅ Timing — resolved by #26, and it changes how this copy reads

**This section previously said the page still fires on launch 1 and that copy alone would not remove
the wall. #26 has shipped and that is no longer the state.** Re-read this copy against the new
behaviour before implementing it.

**What #26 changed** (`internal/license/launch_counter.go`): the reminder is now scheduled from a
**value anchor** — the first launch after `first_agent_launched` or `first_task_completed` exists.
It stays quiet on the anchoring launch itself, then appears every third launch after it.
`IsNagLaunch()` is a pure read with no side effects. **A brand-new user never sees this page, however
many times they open the app.** Verified on a staging build: launches 1–4 served the dashboard,
launch 5 anchored quietly, launch 8 showed the reminder, launch 9 was quiet again.

**Three consequences for the words above — the copy and the trigger moved independently and must be
read together** (raised by the Content Producer):

1. **The apologetic register is no longer needed.** This copy was written to soften a screen that
   ambushed a brand-new user. It now meets someone Coral has already worked for. It can be
   straightforward rather than defensive — but see the second "do not change" rule: **not pushier.**
2. **"Skip this screen anytime" (line 61) is still true and now reads differently.** It was
   reassurance against a wall; it is now a note on a periodic reminder. Keep it — it costs nothing
   and the reassurance is still worth having — but it is no longer doing the heavy lifting.
3. **"Retires this periodic reminder" remains exactly accurate.** The reminder is still periodic
   (every third launch after the anchor); only its start moved. Do not weaken this line.

⚠️ **The premise this file was written against has changed once already.** If `launch_counter.go`
moves again before #29 ships, re-read this section rather than trusting it.
