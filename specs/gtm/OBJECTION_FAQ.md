# Coral Objection-Handling FAQ

**Owner:** GTM Strategist · **Task:** #24
**Use:** answers here are written to be **quoted verbatim** in HN comments, Reddit replies, Discord
and the README. They are written to survive someone reading the source afterwards.

**Three rules for everyone answering:**
1. **Concede the true part first.** Every hard objection below contains something true. Leading
   with the concession is what makes the rest credible.
2. **Never claim a capability nobody has verified.** If asked something we haven't checked, say
   "I don't know, let me check" — that answer costs nothing and buys everything.
3. **Do not spin the telemetry answer.** It is the one where a detected evasion does permanent
   damage.

---

## Q1. "Why not just use Claude Code subagents, or tmux and a shell script?"

**The hardest objection, because for a lot of people the honest answer is: you should.**

> Honestly? If you're running Claude Code and only Claude Code, you may not need Coral. Claude Code
> ships `--worktree` for isolated parallel sessions and Agent Teams for a lead that spawns
> teammates with a shared task list and a mailbox. That covers a lot of ground and it's free and
> first-party.
>
> Coral is for the case that doesn't cover: **agents from different vendors on the same repo.**
> Claude Code's teammates are Claude Code instances. If you're running Claude Code *and* Codex
> *and* Gemini CLI, there is no first-party thing that puts them on one board — each vendor
> orchestrates its own processes. That's what Coral does, across four agents: Claude Code, Codex,
> Gemini CLI and Pi.dev.
>
> The second one is durability. Claude Code's docs list this under limitations: "`/resume` and
> `/rewind` do not restore in-process teammates," the team config directory is removed when the
> session ends, and it's one team per session. Coral keeps teams in SQLite — you can sleep an agent
> team, quit Coral, restart it later, and wake the team with its conversation context intact.
>
> And on the shell-script version: yes, you can absolutely wire up tmux and worktrees yourself —
> that's literally what Coral does under the hood, and several other tools do the same. What you
> get here is that it's already wired, plus one browser tab showing every agent's live terminal,
> plus token usage and cost tracked per agent and per session.

**Sources if pressed:** [worktrees](https://code.claude.com/docs/en/worktrees),
[agent teams](https://code.claude.com/docs/en/agent-teams) — the limitations are quoted from
Anthropic's own docs, which is why this answer works. Let them go read it.

**Never say:** "Claude Code can't do parallel agents." It can, natively, and saying otherwise ends
your credibility for the whole thread.

---

## Q2. "Another wrapper around agents I already have."

> That's exactly what it is, and that's the point — it's the reason Coral doesn't call any model
> API of its own and doesn't want your keys.
>
> The question worth asking is whether the wrapper does something you'd otherwise do by hand.
> Concretely, Coral gives you: a tmux session per agent, and optionally a git worktree and branch
> per team, isolated from your main checkout — that one's a checkbox at launch, off by default; a
> shared message board with cursor-tracked delivery so a handoff survives an agent restart; one
> browser tab with every agent's live terminal; and token and cost tracking per agent and per
> session.
>
> If you've already built that, you don't need this. Plenty of people have built parts of it —
> Claude Squad, Conductor, Crystal and vibe-kanban all sit in this space and some use the same
> tmux-plus-worktrees approach. What's different here is that it's cross-vendor, the state is
> durable, and the view is a web dashboard rather than a terminal multiplexer.

⚠️ **"Off by default" is not optional here, and this answer failed a set test without it.** Q10c
tells the reader agents are *not* isolated from each other. Q2 previously said Coral "gives you" a
team worktree with no qualifier. Read together those are consistent; **read alone — which is how
answers get quoted — Q2 implies you get a worktree, and by default you don't.** The checkbox at
`modals.html:240` has no `checked` attribute.

**Do not** get defensive about the word "wrapper," and **do not** claim to be more than an
operational layer. Owning it is more persuasive than resisting it. **Do not** claim to be first or
only — in this field someone will name three alternatives, correctly.

---

## Q3. Telemetry. "It's on by default and there's no opt-out?"

**The operator's decision is settled: on by default in release builds, clear disclosure, no opt-out
toggle. My job is the honest answer, not a better-sounding one.** State both halves. Do not imply a
runtime opt-out exists, because none does — I checked for a `DO_NOT_TRACK` / `CORAL_NO_TELEMETRY`
env var and there is no such thing in the code.

> Yes — release builds send anonymous usage events by default, and there's no in-app toggle to turn
> them off. I'd rather say that plainly than bury it.
>
> Here's exactly what happens. The PostHog project key is injected at build time via ldflags
> (`internal/config/config.go:11-12`), and every tracking call returns immediately if that key is
> empty (`internal/tracking/posthog.go:47` and `:61`). So a **binary you build from source sends
> nothing at all** — there's no key in it. A **release build you download from us does send
> events**, and that's the trade for a downloaded binary.
>
> What's in an event: a random UUID generated on first run and stored at `~/.coral/.install_id`,
> the Coral version, build edition, OS and architecture, and the event name. That's the whole
> payload — you can read it at `internal/tracking/posthog.go`.
>
> What is never sent: your prompts, your code, repository or file names, agent output, your license
> key, and anything identifying you personally. Coral holds no API keys of its own and never calls
> a model on your behalf.

*(That last sentence is now ✅ — see Q6, closed by D5. It belongs here precisely because the two
claims must travel together: no-keys is about the agents, telemetry is about Coral itself, and
either one alone leaves a reader with a wrong impression of the other.)*
>
> If you want zero telemetry, build from source: `cd coral-go && make build`. That's a supported
> path, not a workaround — it's the same source tree the release is built from.

**Canonical source for every fact in this answer:** `specs/gtm/TELEMETRY_DISCLOSURE.md`, which
carries a claim-by-claim sourcing table maintained against the post-#18 code. **Cite that document,
not the line numbers below** — #18 moved them once already and they will move again.

**Stable anchors (function-level, so they don't rot):**
- `config.PostHogKey` is ldflags-injected — `internal/config/config.go:11-12`. Injection sites:
  `.github/workflows/release.yml` and `installers/build-macos.sh`.
- Both `TrackInstallAsync()` and `TrackEvent()` early-return on an empty key — `internal/tracking/posthog.go`.
- Property set is `version`, `edition`, `os`, `arch` plus event name and distinct ID.
- `distinct_id` is a `uuid.New()` written to `~/.coral/.install_id`. **It is not derived from
  hostname, hardware, username or email**, and deleting that file makes you a new, unlinked install.
- Delivery failures are logged to `~/.coral/tracking-failures.log`, so a user can see what we tried
  to send.
- `POST /api/tracking/event` is a **strict allowlist** — one permitted event name, four property
  keys — not a general event pipe.
- Events after #18 (eleven): `install`, `upgrade`, `app_opened`, `session_launched`,
  `first_agent_launched`, `team_launched`, `first_team_launched`, `first_task_completed`,
  `returned_24h`, `supporter_checkout_clicked`, `license_activated`.
- **No opt-out env var or setting exists.** Verified by grep across Go and frontend sources.

**One more line worth using, because a skeptic can confirm it in one command:** source builds have
no key, **send nothing, and don't burn their first-run milestones** — so building from source costs
you nothing later.

⚠️ **Do not claim events are arriving until B11 is resolved.** Nobody has confirmed
`POSTHOG_PROJECT_KEY` is set. Say what Coral sends *when a build has a key*; never say "your
download is sending data right now."

**Never say:** "you can opt out" (you cannot), "it's just anonymous analytics" (dismissive of a
legitimate objection), or "everyone does this."

**If someone is angry:** "That's a fair objection and I'm not going to talk you out of it. Build
from source and you get zero telemetry — that's the honest answer." Then stop. Do not keep selling.

⚠️ **Consistency requirement:** this answer and the Q6 proxy answer get read together. Any gap
between them is what gets quoted. Growth Engineer #20's disclosure text must match both.

---

## Q4. "It's Apache 2.0 and free. What am I paying $49.99 for?"

> Nothing that's gated — because nothing is gated. I want to be blunt about that: the license
> middleware in Coral tracks license state and explicitly does not gate any route
> (`internal/server/server.go:245-246`), and the prod build sets no team or agent limits
> (`internal/config/tier_prod.go:13-14`). Every feature works forever without paying.
>
> What $49.99 buys is: it retires the periodic support reminder, you get priority support, and your
> feature requests get priority consideration. Mostly it funds continued development. It's a
> supporter license, one time, no subscription.
>
> If that's not worth $49.99 to you, use Coral free — that's genuinely fine and it's the default.

**Verified:** `server.go:245-246` (comment: "License middleware — tracks license state but does not
gate any routes"), `tier_prod.go:13-14` (`TierMaxTeams = 0`, `TierMaxAgents = 0`).

**Never say:** "support the project" as the whole answer — it sounds like deflection. Name the three
concrete benefits, then say it funds development.

⚠️ **This answer is currently contradicted by our own activation page**, which lists "Agent team
templates & sharing" and "Search chat history" under the paid column when both are free and
ungated. Growth Engineer #29 is cutting them; replacement copy is in `ACTIVATION_PAGE_COPY.md`.
**Until that ships, expect to be caught on this**, and if you are: "You're right, that's a mistake
in our own copy — those features are free and we're fixing the page." Concede immediately.

---

## Q5. "Isn't this just AutoGen / CrewAI / LangGraph?"

> Different job. Those are frameworks for *building* agent systems in code — you write Python and
> define the pipeline. Coral doesn't ask you to write anything: it runs the agent CLIs you've
> already installed, as separate processes, and gives you a place to watch and steer them.
>
> Worth knowing, if you're comparing: Microsoft moved AutoGen to maintenance mode and shipped
> Microsoft Agent Framework 1.0 in April 2026 as the successor.

**Source:** [microsoft/autogen](https://github.com/microsoft/autogen).
**Note:** we are deliberately no longer *leading* with this contrast (Orchestrator D3). Use it only
when someone else raises it.

---

## Q6. "You say Coral doesn't call AI APIs, but there's a proxy in the source."

Anticipate this — someone will find `internal/proxy/`. Answering it before we are asked is
strictly better.

> Fair catch, and our README wording was imprecise — we're fixing it. Here's what's actually true:
> Coral has no API keys of its own and never calls a model on your behalf. It runs the CLI agents
> you already installed, using your credentials.
>
> The proxy is how token counting works: Coral can proxy the agents' own API traffic locally so it
> can count tokens and cost. It reads your existing environment variables —
> `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GOOGLE_API_KEY` (`internal/proxy/providers.go:24-32`) —
> the traffic goes to the same provider it always did, and none of it comes to us.

✅ **EVIDENCE TIER: VERIFIED (D5, Developer Advocate). Cleared to publish.**

Previously 📖 — read by two people, never run. Now closed by execution: **all nine executables in
the shipped DMG** scanned with structured key patterns, zero provider-key hits, with an assertion
proving the scan ran; keys come only from `os.Getenv` with no fallback (`providers.go:24-33`); and `proxy.go:166-172` prefers the
caller's own credentials, so an agent's key passes through rather than being replaced.

⚠️ **Publish it alongside the telemetry answer, never alone.** "No API keys of its own" is true and
invites the false reading "Coral sends nothing." It does send — PostHog, by default, no opt-out.
Every sentence true and the impression false is the completeness failure we cut five times today.

⚠️ **"Never calls a model on your behalf" is a universal negative** — the claim type least suited to
verification by reading, because reading confirms what *is* present, not what is absent. To
establish it you must have looked everywhere; to break it, one path suffices. **Our strongest
privacy claim has the shape most vulnerable to someone who has read the source**, and this answer is
written to be quoted verbatim in public. (Diagnosis: Content Producer.)

**Never** downplay the proxy or answer "there's no proxy." There is one; it is benign; say so first.

---

## Q7. "Is this abandoned? PyPI says 4.4.1 and your release says 1.0.8."

> Coral was originally a Python package (`agent-coral`). It was rewritten in Go and is now a native
> app you download from GitHub Releases — no Python, no pip. Version numbers restarted with the
> rewrite, so **v1.0.8 is newer than 4.4.1** despite the lower number. The Python package is
> retired and unmaintained.

**Never** disparage the Python version. **Never** call it a "migration" — there is no migration
path and no shared state, and implying one creates support we cannot honor.
**Open:** whether a stale `agent-coral` install interferes with the Go app — Dev Advocate is
checking. Until answered, do not assert that it doesn't.

---

## Q8. "Does it work on Windows?"

> Not today. macOS and Linux. We're not going to claim Windows support for a build nobody on the
> team has run on a real Windows machine.

**Settled** (Dev Advocate + Orchestrator). Do not soften this and do not say "coming soon."

---

## Q9. "Why tmux? I don't use tmux."

> Coral uses tmux to give each agent a real terminal it can own, which is also what makes sleep and
> wake work — the session keeps running when you close the browser. You don't have to *use* tmux;
> you need it installed. Everything you do goes through the web dashboard.

**Note:** worth knowing that Claude Code's own split-pane mode also requires tmux or iTerm2, so this
is a normal dependency in this category rather than an oddity.

---

## Q10. "How is this different from Claude Squad / Conductor / Crystal / vibe-kanban?"

> They're real and some are good — I'd rather point at them than pretend they don't exist. Claude
> Squad uses tmux and worktrees like we do and supports several agents. Conductor and Crystal are
> desktop apps for parallel Claude Code sessions.
>
> Where Coral differs: agents from **different vendors** on one shared message board; teams that
> **sleep and wake** with state in a database rather than dying with the process; a **web**
> dashboard rather than a desktop app or TUI, so you can watch from another machine; **cost
> tracking per agent**; and multi-step workflows that chain tasks across agents.
>
> ✅ *Drafting note: workflows restored — verified by execution in #42. ⛔ Do not add cron-scheduled
> jobs (broken in the documented default, #43) or webhooks (unverifiable without an external
> endpoint).*
>
> If you're happy with what you're using, stay with it.

**Never** claim to be the first or only tool in this space. **Never** disparage a competitor.
"Here's the specific difference, pick what fits" is both more honest and more persuasive.

---

## Q10b. "Can I add my own agent?"

> Not today. Coral supports four agents — Claude Code, Codex, Gemini CLI and Pi.dev — and each one
> has a real implementation in the source. Adding a fifth means writing Go and opening a PR; there's
> no plugin system or config file for it yet. If there's an agent you want, open an issue — that's
> genuinely useful signal for us.

⛔ **This replaces the old "any CLI-based agent can be added" claim, which was wrong.** If someone
quotes that line back at you from an older README or a cached page: "That was an overstatement in
our README and we've removed it — it's four named agents." Concede it immediately and completely.

---

## Q10c. "So your agents can overwrite each other's work?"

**Answer this one straight. It is true, we told people otherwise for months, and it has been
demonstrated — two agents on one team both wrote the same function into the same file and broke the
build.**

> Yes, and our README used to claim otherwise — that was wrong and we've corrected it. Agents on a
> team share one working directory and one branch. They're not isolated from each other. Two agents
> given overlapping work can and will overwrite or duplicate each other's code.
>
> The mitigation is scoping: give each agent its own set of files. That's the thing that actually
> prevents it.
>
> The message board helps agents coordinate and hand off — but it's a communication channel, not a
> lock. It doesn't stop two agents writing to the same file.
>
> Worth knowing this isn't specific to Coral: Anthropic's own agent-teams guidance says the same
> thing — "Two teammates editing the same file leads to overwrites. Break the work so each teammate
> owns a different set of files." That's the state of the art for multi-agent coding right now, not
> a Coral limitation.

**Source for the quote:** [Claude Code agent teams, Best practices → Avoid file
conflicts](https://code.claude.com/docs/en/agent-teams).

⛔ **Do not present the message board as the safeguard.** This is the original error reproduced one
level down, and I made it in the first draft of this answer. The Developer Advocate's demo settles
it empirically: those two agents **had** a board, and both still wrote `Truncate` into the same file
and broke the build. The board is a communication channel, not a lock. **Mitigation first (scope the
files), board second, and described as what it is.**

**Why this matters for positioning:** shared-branch collision risk is **inherent to multi-agent
coding today**, and the first-party tooling gives the same advice we do. That reframes the
correction from "Coral has a flaw" to "here is the real constraint everyone is working under."

**Ordering is binding:** concede first, contextualise second, never the reverse. Lead with "yes, and
we said otherwise." The Anthropic quote comes after. Reached for first it reads as deflection and
burns the credibility the concession buys.

**Never say:** "worktree isolation prevents this," "the message board prevents this," "agents can't
conflict," or anything containing "without merge conflicts."

---

## Q11. "Will you train on my code?"

> No. Coral never sees your code — it runs agents locally on your machine and holds no API keys of
> its own. The only thing that leaves your machine is the anonymous usage events in Q3, and those
> contain no prompts, code, file names, repository names, or agent output.

✅ **Upgraded 📖 → VERIFIED (Developer Advocate, D5, rescanned at corrected scope).** Zero
provider-key hits across **all nine executables in the shipped DMG** — the first scan covered only
`coral`, and the word "binary" concealed that the DMG is a bundle. Structured patterns
(`sk-ant-api`, `sk-proj-`, `AIzaSy`, `xoxb-`), with a positive-quantity assertion proving the scan
actually ran (`ANTHROPIC_API_KEY` appears 6 times). Keys come only from the user's
environment (`providers.go:24-33`, `os.Getenv`, no fallback), and `proxy.go:166-172` passes the
caller's own credentials straight through. **Safe to publish.**

⚠️ **Never separate the two sentences above.** "Coral holds no API keys of its own" is true and sits
one sentence from a false reading: *"Coral sends nothing."* It does — the shipped binary posts to
PostHog by default with no opt-out. The telemetry clause is what makes this answer complete rather
than merely true.

---

## Q12. "Why should I trust a 31-star repo with my codebase?"

> You shouldn't take my word for it — it's Apache 2.0, so read it. The parts worth reading are
> `internal/tracking/posthog.go` (everything that leaves your machine, about 150 lines) and
> `internal/agent/` (exactly how each agent is launched).
>
> Coral is early. It's free and fully unlocked partly for that reason.

**Do not** inflate adoption numbers. **Do not** cite stars as social proof at 31 stars — pointing at
the code is the stronger move at our size.
