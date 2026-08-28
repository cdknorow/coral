# Interview Synthesis — Phase 3

**Companion to `USER_INTERVIEW_KIT.md`.** Fill this in after every five conversations, per the spec.
**Copy this file per round** (`INTERVIEW_SYNTHESIS_ROUND_1.md`) rather than editing it in place.

**Purpose:** turn five conversations into a **ranked barrier list with evidence**, not five
anecdotes. Written as a narrative, five conversations will over-weight whichever one you did most
recently and whichever user was most articulate. This structure is what stops that.

**Round:** ___ · **Dates:** ___ – ___ · **Build(s) users were on:** ___

---

## 1. Who you actually talked to

Fill this in before analysing anything. **It determines what the round can and cannot tell you.**

| # | Source | Platform | Agents they run | Got to | Type |
|---|---|---|---|---|---|
| 1 | issue author / Discord / … | macOS / Linux / Win | Claude, Codex… | never installed / installed / launched agent / launched team / completed task | interview / observed |
| 2 | | | | | |
| 3 | | | | | |
| 4 | | | | | |
| 5 | | | | | |

**Coverage check — answer honestly, these limit every conclusion below:**

- Platforms represented: ______ · **Missing:** ______
- How many ran **more than one vendor's agent**? ___ / 5
  → *If this is 0–1, the multi-vendor ICP in `POSITIONING_BRIEF.md` §1 is not supported by this
  round and you should say so out loud.*
- How many **never got it installed**? ___ → *this group is the most valuable and the hardest to
  recruit; if it's 0, the round is biased toward people who succeeded*
- How many had tried a competitor? ___ Which: ______
- **Who is missing from this sample?** ______

---

## 2. Where people actually stopped

One row per person. **Use their words.**

| # | Furthest point reached | What was on screen when they stopped | Their words, verbatim |
|---|---|---|---|
| 1 | | | " " |
| 2 | | | " " |
| 3 | | | " " |
| 4 | | | " " |
| 5 | | | " " |

**Drop-off tally** — count, do not estimate:

| Funnel stage | Reached it | Stopped here |
|---|---|---|
| Found the download | /5 | |
| Installed **the right product** | /5 | |
| Opened it | /5 | |
| Launched one agent | /5 | |
| Launched a team | /5 | |
| Completed a task | /5 | |
| Would open it again | /5 | |

**Largest single drop-off this round:** ______

---

## 3. Did the invisible defects show up?

We knew about these before the round. **Record whether users hit them, in their words** — this is
how we find out whether a known bug is actually a barrier or just ugly.

| Known defect | Hit it? | Their words | Did they realise it was a bug? |
|---|---|---|---|
| Agents hang on trust prompt (#33) | ☐ | " " | ☐ yes ☐ **thought it was normal/slow** |
| Board silently wrong port (#34) | ☐ | " " | ☐ yes ☐ **thought agents just ignore each other** |
| Installed Python `agent-coral` instead | ☐ | " " | ☐ yes ☐ **thought it was Coral** |
| Supporter screen on launch 1 (#26) | ☐ | " " | ☐ yes ☐ **thought it was paid software** |
| Agents overwrote each other | ☐ | " " | ☐ yes ☐ **blamed themselves or the agent** |

> **The right-hand column is the finding.** A defect a user does not recognise as a defect never
> becomes a bug report — it becomes a silent churn, and it is invisible in the funnel data. Any row
> ticked "thought it was normal" should outrank a louder problem people at least understood.

---

## 4. Things we did not know

**The point of the round.** Anything a user said that nobody here predicted.

| # | What they said or did | Why it was surprising | Contradicts anything we claim? |
|---|---|---|---|
| | | | |

**Specifically capture:**
- Behaviour on a platform we have never tested: ______
- Anything they believed Coral does that it does not: ______
- Anything they expected that we've never considered building: ______
- Any moment they were **delighted** — we have no evidence of what lands: ______

---

## 5. Ranked barriers — the deliverable

**Exit criterion: the top three activation or retention barriers, documented with evidence.**

Rank by **how many of the five it stopped**, not by how strongly anyone complained. The most
articulate complaint is usually not the biggest barrier.

### Barrier 1
- **What:** ______
- **Stopped:** ___ / 5 · **Stage:** ______
- **Evidence:** "______" *(interview #_)* · "______" *(interview #_)*
- **Known already?** ☐ yes, task #___ ☐ **new**
- **Recognised as a bug by users?** ☐ yes ☐ no — silent
- **Smallest fix that would have prevented it:** ______

### Barrier 2
*(same fields)*

### Barrier 3
*(same fields)*

### Ranked below the top three, recorded so it isn't rediscovered
| Barrier | Stopped n/5 | Note |
|---|---|---|
| | | |

---

## 6. What this round changes

Be explicit, including where it changes nothing.

- **Positioning** (`POSITIONING_BRIEF.md`): ☐ confirmed ☐ needs revision — what: ______
  - Was the multi-vendor ICP supported? ______
  - Did anyone describe the pain in §2 unprompted, in their own words? ______
- **A claim of ours that a user contradicted:** ______
- **Channel plan:** did anyone arrive through a channel we don't track? ______
- **Pricing:** did anyone raise the $49.99 unprompted? ☐ yes ☐ no · What: ______
  - *Do not read much into this. You did not ask, and you should not have.*
- **The one improvement to ship next** (spec Phase 6: **one** at a time): ______

---

## 7. Honesty checks

Answer these before circulating. They are here because each is a way five conversations can produce
a confident wrong answer.

- [ ] **Did I hear this, or infer it?** Every barrier above has a verbatim quote attached.
- [ ] **Did I count, or remember?** Every n/5 comes from the tables, not recollection.
- [ ] **Did the product change mid-round?** If yes, which interview: ___. Treat as two cohorts.
- [ ] **Did I lead any of these answers?** Note any question where you named the problem first —
      discount that answer.
- [ ] **Am I over-weighting the most recent conversation?** Re-read interview 1's notes before
      finalising the ranking.
- [ ] **Am I over-weighting the most articulate user?** The person who explained the problem best is
      not necessarily describing the biggest one.
- [ ] **Did I rescue anyone during an observed session?** If so, that session's drop-off data is
      unusable — say so rather than including it.
- [ ] **Is a barrier missing because nobody in this sample could have hit it?** (e.g. no Linux users
      → no Linux barriers. That is a coverage gap, not an absence.)

---

## 8. What I would need to be more confident

Name it rather than leaving the reader to assume the round was conclusive.

- Sample size and who was missing: ______
- Questions I should have asked: ______
- What I'd change in the kit for round 2: ______

> **Five conversations is enough to find barriers and not enough to size them.** Report what stopped
> people, not what percentage of users it affects. A barrier that stopped 3/5 here is real; "60% of
> users hit this" is not a claim this instrument can support.
