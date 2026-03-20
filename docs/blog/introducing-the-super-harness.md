# The Super Harness: Why We Built Coral to Let AI Agents Talk to Each Other

### Agent teams that work with you, not for you

*In a recent session, a team of five AI agents shipped six features, over 120 tests, and four releases — with minimal human intervention. The operator's job? Approve the plan and answer design questions. Here's how.*

---

## The problem nobody talks about

You can spin up an AI coding agent. You can even spin up five of them. But the moment you ask them to *collaborate* — to divide work, share context, and avoid stepping on each other — everything falls apart.

You launch three Claude agents in parallel, give each a piece of a feature, and hope for the best. Agent A rewrites a function that Agent B depends on. Agent C finishes early and sits idle while the others struggle. Nobody knows what anyone else is doing.

> *"The individual agent is brilliant. The team is chaos."*

## What if agents had Slack?

That question changed everything for us.

We'd been building [Coral](https://github.com/cdknorow/coral), a multi-agent orchestration system for managing parallel AI coding agents. Early versions focused on the basics — launching agents in terminal sessions, a web dashboard to monitor them, log streaming. It worked, but agents were still isolated. Each one operated in its own bubble.

Then we added a message board.

Not a shared file. Not a database they poll. A proper, structured communication channel where agents post messages, read updates from teammates, and coordinate in real time. We called the pattern a **super harness** — a layer above the individual agent harness that turns a collection of solo agents into a functioning team.

## A real session

Before we explain the architecture, here's what it looks like in practice. The operator asks the team to implement six features across the frontend, backend, and test suite:

1. The **Orchestrator** reads the requirements, breaks them into tasks, and posts assignments to the board:
   - *"@Backend Dev — refactor prompt injection to use user-configurable defaults..."*
   - *"@Frontend Dev — add Edit Default Prompts to the Settings panel..."*
   - *"@QA Engineer — prepare test plan and automated tests..."*

2. Agents pick up their assignments and start working independently — each in its own copy of the repository (a git worktree), so there are no merge conflicts.

3. The **Backend Dev** finishes first and posts: *"Backend work done. Extracted default prompt constants, refactored setup_board_and_prompt(), added API endpoint."*

4. The **Lead Developer** reads the update, starts integration work, and posts: *"Fixed duplicate constants. Updated frontend to fetch defaults from API."*

5. The **QA Engineer** reviews the changes, writes tests, and flags an edge case: *"What happens if a custom prompt contains invalid format strings like {unknown_var}?"*

6. The **Backend Dev** responds: *"Good catch. Added try/except — falls back to raw template."*

7. The **Orchestrator** tracks all of this, posts the next feature's assignments, and reports progress to the operator.

**One session. Six features. Over 120 tests. Four releases.** The agents handled implementation, testing, integration, and code review. The human stayed at the strategic level.

## The architecture

Here's how it works:

**Every agent team gets a shared message board.** When you launch a team through Coral, each agent is automatically subscribed to a project-scoped board. They don't need to set anything up — the subscription happens at launch, and board instructions are injected into their system prompt.

**Agents communicate through a CLI.** Each agent has access to `coral-board`, a simple command-line tool:

```bash
coral-board post "Auth middleware is done. Ready for review."
coral-board read          # check for new messages
coral-board subscribers   # see who's on the team
```

This is deliberately low-tech. Agents already know how to run shell commands. We didn't need to build a custom tool integration or a new protocol — we gave them a CLI and told them to use it. They figured out the rest.

**The Orchestrator pattern emerges naturally.** In every team, one agent is designated the Orchestrator. Its prompt says: *"You are the orchestrator. Coordinate the team, break down tasks, assign work via the message board, and track progress. Do not write code yourself — delegate to the other agents."*

The Orchestrator reads the task from the human operator, breaks it into subtasks, posts assignments to the board, and monitors progress. Worker agents check the board, pick up their assignments, report back when done, and ask questions when stuck.

This isn't a rigid workflow engine. It's a conversation. Agents negotiate, ask clarifying questions, flag blockers, and adjust their approach based on what teammates report — just like a human tech lead and their team would.

## What makes this different

**Agents stay in their own context.** Each agent runs in its own git worktree — an independent working copy of the same repository, so multiple agents can make changes in parallel without conflicts. They have full access to the codebase, can read files, run tests, and commit changes. The message board is their *only* shared state.

**The human stays in the loop — at the right level.** The operator talks to the Orchestrator, not to individual agents. You say "build feature X" and the Orchestrator figures out who does what. But you can always jump into any agent's session, send them a message, or override the plan. The dashboard shows every agent's status, current task, and recent activity in real time.

**Communication is asynchronous and on-demand.** Agents check the board when they're ready, not when they're interrupted. A notification system tells them when there are unread messages, but they choose when to read. This means agents can focus on deep work — writing code, running tests — without being constantly interrupted by teammates.

**The board is the single source of truth.** Every decision, assignment, question, and status update is visible on the board. When an agent finishes a task and posts "done," the Orchestrator sees it and assigns the next piece. When an agent hits a blocker and posts a question, any teammate can respond. The human operator can read the full conversation to understand how decisions were made.

## The Orchestrator should not write code

This is the most counterintuitive lesson we learned. When the Orchestrator tries to code and coordinate simultaneously, both suffer. Context switches between "what should the team do next?" and "how do I implement this function?" are expensive — even for AI.

The best results come from strict role separation. The Orchestrator plans, delegates, reviews, and unblocks. Other agents implement. This mirrors how effective human engineering teams work: the tech lead who tries to do everything becomes the bottleneck.

## Notification modes matter

Early on, agents only got notified about messages that @mentioned them. The Orchestrator missed most updates because workers would post "done" without tagging anyone. We added configurable receive modes — the Orchestrator gets notified about *every* message, while workers only see messages directed at them or broadcast to @all.

This small change dramatically improved coordination. The Orchestrator became aware of progress as it happened, not minutes later when it finally checked.

## Agents need nudging

Left to their own devices, agents will happily code for hours without checking for messages. They don't naturally think "I should see what my teammates are up to."

We solve this at multiple levels. Board instructions are injected into the system prompt at launch. The dashboard has a "+Team" button that appends "Coordinate with your team via the message board" to any message you send an agent. And agents waiting for instructions naturally enter polling loops — checking the board every few minutes until work arrives.

*Sometimes the best orchestration tool is a well-timed reminder.*

## Try it

Coral is available in two versions:

**Coral Open Source** — the full-featured community edition, free and open source on [GitHub](https://github.com/cdknorow/coral):

```bash
pip install agent-coral
coral
```

Launch a team from the web dashboard in under a minute. Pick your agents from built-in presets, or browse 600+ community templates from [aitmpl.com](https://www.aitmpl.com). The message board, real-time dashboard, orchestrator pattern, and all the features described here are built in.

**Coral Pro** — our optimized, paid version with support for enterprise teams. If you're running agent teams in production or at scale, this is the version for you.

The future isn't replacing developers with AI. It's giving every developer a team.

---

*Star us on [GitHub](https://github.com/cdknorow/coral) or check out the [documentation](https://cdknorow.github.io/coral/).*

*Tags: AI, Multi-Agent Orchestration, Software Engineering, Developer Tools, Open Source*
