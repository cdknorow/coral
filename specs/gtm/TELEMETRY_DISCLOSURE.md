# Telemetry disclosure — copy draft

**Task:** unblocked by #18 (event list frozen) · **Owner:** Content & Launch Producer
**Pairs with:** Growth Engineer #20 (surface), README D5 (proxy wording)
**SHIPPED in #20** as `coral-go/agent_docs/telemetry.md` + the first-run modal, close to
verbatim. This file remains the canonical wording; the doc and modal follow it.

**Status: DRAFT — SHIP-GATED ON B11.** Ruled by the Orchestrator: *the disclosure does not
ship ahead of the thing it discloses.* If `POSTHOG_PROJECT_KEY` is unset, fix the secret; do
not soften this copy. #20 is blocked on B11 resolving, not on implementation.

**Surface — RULED: dashboard modal.** Not the EULA: nobody on this team has observed the
EULA render, and folding a disclosure into a legal-acceptance gate people click through is
the opposite of disclosing. Put it where it will be read.

Operator decision, settled: telemetry is **on by default, with a clear first-run
disclosure, and no opt-out toggle.** This copy does not relitigate that. It does the one
thing that makes such a policy defensible — it says exactly what is sent, in plain terms,
before the user has done anything.

---

## A. First-run disclosure (the screen)

> ### Coral sends anonymous usage data
>
> Coral runs entirely on your machine, which means we cannot see where people get stuck
> unless the app tells us. It reports a small, fixed set of events.
>
> **What Coral sends**
>
> Eleven events, and nothing else: first install, version upgrade, app opened, agent launched,
> team launched, first agent launched, first team launched, first task completed, a return
> visit after 24 hours, a click on the supporter link, and license activation.
>
> Each carries only: Coral's version, the build edition, your operating system, and your CPU
> architecture — attached to a random ID stored in `~/.coral/.install_id`. The two team
> events also carry `agent_count`, the number of agents in the team. That ID is generated
> randomly. It is not derived from your name, email, hostname, or hardware. Delete the file
> and you become a new, unlinked install.
>
> **What Coral never sends**
>
> Your prompts. Your code. Repository, branch, or file names. Agent output. Your name, email
> address, license key, or IP-derived location. None of it leaves your machine, and there is
> no code path that would send it.
>
> **There is no opt-out switch.** Rather than soften that, here is what is true instead, and
> all of it is verifiable:
>
> - Coral is Apache 2.0. The entire tracking implementation is `internal/tracking/` — you can
>   read every event before you trust the list above.
> - **Builds you compile yourself have no analytics key and send nothing at all.** The key is
>   injected at build time; a source build has none, and it does not quietly consume its
>   first-run events either — compile from source and you send nothing, permanently.
> - Failed deliveries are written to `~/.coral/tracking-failures.log`, so you can see what
>   Coral tried to send and could not.
>
> **Separately, about your AI agents:** Coral has no API keys of its own and never calls a
> model on your behalf. It runs the CLI agents you have already installed, using your
> credentials. To count tokens and costs, Coral can proxy those agents' API traffic locally —
> the traffic goes to the same provider it always did, and none of it comes to us.
>
> [ Got it ]

---

## B. README / docs section (longer form)

> ## Telemetry
>
> Coral sends a small set of anonymous usage events. This section describes all of them.
>
> **Events.** `install`, `upgrade`, `app_opened`, `session_launched`, `team_launched`,
> `first_agent_launched`, `first_team_launched`, `first_task_completed`, `returned_24h`,
> `supporter_checkout_clicked`, `license_activated`.
>
> **Properties.** Every event carries Coral's version, build edition, OS, and CPU
> architecture. `team_launched` and `first_team_launched` also carry `agent_count`, the
> number of agents in the team. The supporter-link click may additionally carry campaign
> attribution (`surface`, `campaign`, `source`, `medium`). `license_activated` carries the
> product and variant name only — never the key, your name, or your email.
>
> **Identifier.** A random UUID in `~/.coral/.install_id`, generated on first run. Not
> derived from hardware, hostname, username, or email. Delete it to reset.
>
> **Never collected.** Prompts, source code, repository names, branch names, file paths,
> agent output, personal information, license keys, and IP-derived location.
>
> *On that last one, precisely:* Coral never puts your location in a payload. PostHog does
> geo-resolve source IPs server-side by default, so this is a statement about what we send,
> not a claim about what our analytics provider can infer from the connection itself.
>
> **The browser endpoint.** One event can originate in the dashboard UI
> (`supporter_checkout_clicked`), so Coral exposes `POST /api/tracking/event`. It is a strict
> allowlist, not a general event pipe: one permitted event name, four permitted property
> keys, and values must match `^[A-Za-z0-9_.-]{1,64}$`. Anything else is dropped or rejected.
> Free-form text cannot be smuggled through it.
>
> **No opt-out.** There is no toggle. Coral is Apache 2.0 and the whole implementation is in
> `internal/tracking/` — read it. Builds compiled from source carry no analytics key and send
> nothing. Failed deliveries are logged to `~/.coral/tracking-failures.log`.

---

## C. Notes for the operator and for #20

### Written to survive both B11 outcomes
B11 is unresolved: nobody has confirmed `POSTHOG_PROJECT_KEY` (`release.yml:32`) exists and
is valid. This copy is deliberately phrased so it stays accurate either way — it says what
Coral sends **when a build has a key**, and states plainly that source builds have none. It
never asserts "your download is sending data right now," which is the one sentence that
would become false if the secret is unset.

**The gate is now enforced in code, not by process.** #20 shows the disclosure only when
`tracking.Enabled()` is true, i.e. `config.PostHogKey` is non-empty. A source build shows
nothing, because warning a user about collection that is not happening is its own dishonesty.

**Consequence, stated plainly: the disclosure is therefore NOT a canary for B11.** If the
secret is unset, release builds show no disclosure *and* send no events — the two failure
modes stay consistent, which is right, but it means a silent release build is indistinguishable
from a correctly-configured one by inspection. B11 can only be closed the way the Strategist
specified: **cut a release build and confirm an `install` event arrives.**

**If B11 resolves as "key is set" (expected):** ship as written.
**If B11 resolves as "key is unset":** the copy is still true, but the disclosure would be
warning users about data collection that isn't happening. Fix the secret rather than the
copy — and do not ship a disclosure describing events that never arrive.

### Why "no opt-out" is survivable here
A no-opt-out policy fails when a project is vague about what it collects. It survives when
the list is short, complete, and checkable. The three verifiable facts — Apache 2.0 source,
source builds send nothing, failures logged locally — do more for trust than a toggle would,
because a reader can confirm each one instead of taking our word for it.

I would still expect this to be the most-quoted paragraph if Coral reaches Hacker News. It
should read as a project that thought about it, not one that hoped nobody would ask.

### Consistency with D5 (proxy wording) — must not drift
The telemetry story and the proxy story get read together, and any gap between them is what
gets quoted. Both must say the same thing:

> Coral has no API keys of its own and never calls a model on your behalf. It runs the CLI
> agents you have already installed, using your credentials. To count tokens and costs, Coral
> can proxy those agents' API traffic locally — the traffic goes to the same provider it
> always did, and none of it comes to us.

**Strategist's edit, and why it matters:** my draft explained the *mechanism*; the reader's
actual question is "does anything reach **you**." Answer it in the same breath or they assume
the worst. This is now the canonical wording — README, disclosure, FAQ and landing copy all
use it verbatim.

Verified: `internal/proxy/providers.go:24-32` reads `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
`GOOGLE_API_KEY` from the **user's** environment. Coral holds no key of its own.

### Every factual claim above, and its source

| Claim | Source |
|---|---|
| Event list | `internal/tracking/`, `sessions.go:2367`/`:2553`, `board.go:895`, `milestones.go:171`, `routes/tracking.go:57`, `license/middleware.go:78` |
| Properties version/edition/os/arch | `tracking/posthog.go:84-87` |
| `agent_count` on both team events | `routes/sessions.go:2552-2553`; documented at `tracking/events.go:59,61` |
| Event names are constants; list is generated, not hand-maintained | `tracking/events.go` — `AllEvents` served by `GET /api/system/telemetry`; the UI has no hardcoded list |
| Install ID is a random UUID | `posthog.go:236-238` (`uuid.New()`), stored at `.install_id` (`posthog.go:43-49`) |
| Source builds send nothing | `posthog.go:55`, `:76` — early return on empty `config.PostHogKey`; key injected via ldflags (`config/config.go:11-12`) |
| Source builds don't burn milestones | Growth Engineer #18, verified by test |
| Allowlist: 1 event, 4 props, value regex | `routes/tracking.go:21-40` |
| `license_activated` carries product/variant only | Growth Engineer #18 manual verification |
| Failure log contents and location | `posthog.go:191-217` — timestamp, event, status, detail only; 64KB cap; mode 0600 |
| Coral holds no API keys | 📖 **READ ONLY** — `proxy/providers.go:24-32`. Nobody has watched what the proxy sends. *"Never calls a model on your behalf"* is a **universal negative**, the claim type least suited to verification by reading. Closeable on hardware we have; queued to the Dev Advocate above nothing-is-gated |

### Open questions — resolved
1. ~~Where does the disclosure appear?~~ **RULED: dashboard modal.** Section A is written for
   a modal: short enough to be read, with the verifiable facts above the fold. If #20's
   implementation constrains the height, cut the AI-agents paragraph first — it is the one
   claim that is also stated elsewhere (README D5).
2. ~~Is `upgrade` in scope?~~ **RULED: yes — eleven events, not ten.** Completeness is the
   entire argument of this document; an event we happened to omit is the one a reader finds.
   Both sections list all eleven, `install` and `upgrade` included.

### Still open
3. Campaign properties are listed generically, pending the #19 UTM scheme. Section B names
   `surface`, `campaign`, `source`, `medium` as keys, which is accurate today and does not
   depend on the values #19 chooses.
