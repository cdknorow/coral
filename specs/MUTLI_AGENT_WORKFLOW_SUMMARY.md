I've finished the analysis of the `death-or-trade-ai-auto` team. The short version: **most of what the agents write on the board is real work, but a large share of their turns goes to reading and writing it, and the bookkeeping inside those turns is concentrated in a few repeatable patterns: landing receipts, the landing queue, and waiting.**

## What I measured
- **Board:** all 6,861 messages over 3.8 days (Sep 22 to Sep 26), from 14 agents plus Coral's own task queue.
- **Agents' own logs:** 17 Claude Code sessions, about 40,000 tool calls.
- **Git history:** 1,143 commits landed on `main` in the same window.
- **Classified sample:** 400 random agent posts, split between four subagents that each labelled every post by what it was doing and whether Coral could have produced it. About 7% of all posts. These are model judgments, and posts often mix several kinds, so treat the percentages as rough.

## How much of the team's effort goes to the board

| | Total | On the board |
|---|---|---|
| Agent tool turns | 39,681 | **14,764 (37%)** |
| Orchestrator's turns | 4,393 | **3,728 (85%)** |
| Board text read into agents' contexts | about 8M tokens | |
| Nudges typed into agents' prompts | 5,098 | |

Per agent, the share of turns on the board ranged from 22% (Design Director) to 54% (Card Maker). Only 371 of 9,286 reads came back empty, so they aren't idle polling; they're mostly a nudge arriving and the agent reading what's new.

## What the posts are

| Primary purpose of the post | Share of sample |
|---|---|
| Real content: findings, reviews, rulings, corrections | 64% |
| Coordination: claims, landing order, hand-offs, dispatching tasks | 19% |
| Status: landed, test results, slot taken or released | 15% |
| Chasing, relaying, acknowledging | 2% |

- Only **9% of posts could be produced entirely by Coral**. Another **49% are partly bookkeeping** wrapped around real content. By volume of text, about **21% of what agents write is bookkeeping**.
- The bookkeeping appears in far more posts than it leads: coordination shows up in 55% of posts and status in 43%.
- Across all 5,372 agent posts, **57% name a commit hash** and **51% talk about landing**. That's about 2.7 posts per commit that actually landed, mostly announcing or passing on what git already records.

**Conclusion:** the board isn't mostly waste. Agents are doing real work there. The overhead is a layer of bookkeeping on top of that work that every agent has to write and every recipient has to read.

## The recurring patterns, largest first
1. **Landing receipts.** Nearly every landing post opens with a hand-written proof block: the hash, the base it landed on, "branch tree equals verified tree", test counts, exit codes and times, "branch is free". Coral could see all of it.
2. **Managing the landing queue by hand.** "Hold fast-forwards", "I go after X", "the branch is yours", "queued behind Y", plus claiming migration numbers and shared files in advance. Agents also take tasks by name because `task claim` hands out the head of the queue. This is a lock and a queue living in prose.
3. **Orchestrator relays.** The orchestrator's typical post: "X landed at <hash>, QA please review, and here's each agent's next task". Apart from a short review focus, that's mechanical, and it's why the orchestrator spends 85% of its turns on the board.
4. **Waiting.** Agents park work until a named commit lands or someone replies. The logs show **744 sleep or wait loops** and **1,176 git checks** for whether something had landed. One agent re-checked a wait for two hours that could never come true, because the commits it waited for had been folded into a different one.
5. **Silence.** "You look idle with queued messages", "status please", and reassigning work after a role goes quiet. It's small in volume, but it's where the orchestrator (and you) end up stepping in.

Coral's own task-queue messages (1,489 of them) show the system already handles one slice of this well: task created, claimed, completed. The patterns above are the slices it doesn't handle.

## What this suggests for Coral
In order of how much of the measured overhead each would remove:
1. **Automatic landing receipts.** When `main` moves, Coral posts the hash, author and files, and the test results if the run went through Coral: exit code, elapsed time, full log. Agents stop writing proof blocks, and the "X landed" relays disappear.
2. **A visible landing queue.** Coral holds the order, who's next and the hand-off. This is the lock from earlier, justified now by volume rather than by the document's rules.
3. **Registered waits.** "Wake me when this change lands" or "when this agent replies". It replaces the sleep loops and git checks, and it can expire when the thing waited for no longer exists, as with the two-hour wait.
4. **Automatic review and next-task dispatch.** When a task's commit lands, request QA's review and hand the author their next task from the queue. That's the bulk of the orchestrator's mechanical posts.
5. **Silence detection.** Flag an agent that holds a task, a claim or the landing slot but has gone quiet, instead of relying on the orchestrator or you to notice.

Items 1 and 3 build on pieces Coral already has (the git poller, nudges, task dependencies). They'd also make the most of what's left on the board be the real content.

The data and scripts are in my scratchpad folder (`report/`): the per-agent log counts, and the 400 labelled posts with the classifiers' notes. I can turn this into a shareable page if it's going to anyone else.