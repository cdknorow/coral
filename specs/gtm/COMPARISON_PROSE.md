# Replacement Prose for the Comparison Section

> ## 🔴 COMPOSITION HAZARD — read before pasting anything from this file
>
> **Only the text inside the two ```markdown fences is inserted into the README.** Everything else —
> the sourcing table, the "why prose not a table" section, and the editing rules — is *about* the
> copy and must never reach the README.
>
> **This matters more than it sounds.** The editing-rules section deliberately quotes banned
> phrasings (`"survives reboots"`, `"cost across vendors"`) so that a future editor can see what not
> to write. Those strings are correct *here* and would be defects *there*. Pasting this file whole
> would inject the exact claims it exists to prevent, and would trip `verify-readme-claims.sh` — or
> worse, wouldn't, if the phrasing sat inside prose the patterns don't match.
>
> **Corollary, and the reason this box exists:** running the verifier against *this file* is a
> misuse. It checks for the **absence** of banned phrases; this file contains them **on purpose**.
> A FAIL here means nothing. Run it on the **composed README**, which is the only artifact whose
> result is meaningful.
>
> **Fence 1** (line ~14) is the required replacement for `README.md:123-138`.
> **Fence 2** (line ~45) is the optional closing paragraph — recommended, see below.

**Owner:** GTM Strategist · Delivers Orchestrator rulings **D2** (delete the table) and **D3** (drop
AutoGen/CrewAI as the named contrast)
**For:** Content and Launch Producer · **Replaces:** README.md:123-138 (heading, the framing line at
:125, and the entire ✓/— grid)

Every factual claim below is sourced. No claim asserts a competitor *lacks* something.

---

## The copy — drop in as-is

```markdown
## How Coral compares

Most tools that run coding agents in parallel run **one vendor's** agents. Claude Code has
built-in git worktrees and experimental agent teams, where a lead session spawns teammates
that share a task list and message each other — but those teammates are Claude Code
instances. Cursor's Agents Window runs Cursor's agents. Each vendor orchestrates its own
processes, which is a reasonable thing for a vendor to do.

Coral is agent-agnostic. Claude Code, Codex, Gemini CLI and Pi.dev run side by side on one
message board, against one repo, in one dashboard.

The second difference is durability. Coral keeps teams in SQLite rather than in a process,
so you can sleep an agent team, quit Coral, restart it later, and wake the team with its
conversation context intact. Anthropic's own documentation lists this among agent teams'
limitations: "`/resume` and `/rewind` do not restore in-process teammates," the team
directory is removed when the session ends, and it's one team per session.

The third is where you watch from. Coral's dashboard is a web page, so every agent's live
terminal is in one browser tab — from your editor, another machine, or your phone. Claude
Code's split-pane view needs tmux or iTerm2 and, per its docs, doesn't work in VS Code's
integrated terminal, Windows Terminal, or Ghostty.

If you only run one vendor's agent, that vendor's own tooling may be all you need, and
that's a fine answer. Coral is for the case where you're running several.
```

**Optional closing paragraph** — include it if we want to name the open-source field. It costs a
little conversion and buys a lot of credibility, and it makes us much harder to dunk on for
pretending to be alone in the category:

```markdown
Coral isn't alone in this space — Claude Squad, Conductor, Crystal and vibe-kanban all
tackle parallel coding agents, and several use the same tmux-and-worktrees approach under
the hood. What's specific to Coral is the combination: multiple vendors on one board,
teams you can sleep and wake after restarting Coral, a web dashboard, and multi-step
workflows that chain tasks across agents.
```

✅ **#42 resolved this. Workflows restored.** Verified by execution — a three-step chain passed data
between steps and an agent step fed a shell step, which is the across-agents half the claim names.

⚠️ **CORRECTION to my own earlier note here, and it was an overcorrection.** This previously read
*"Never restore 'cron-scheduled jobs' here: they are broken in the documented default."* **That
banned a true claim.** The Developer Advocate's row 5 splits in two and only one half failed:

| Half | Status |
|---|---|
| **Cron scheduling** — jobs fire on schedule, `trigger_type` `cron`, `validate-cron` returns correct next-fire times | ✅ **verified — they watched it fire** |
| **"in isolated worktrees"** | ⛔ fails 100% on the documented default (#43) |

⛔ **SUPERSEDED — and the correction is the important part.** I wrote here that *"run agents or
workflows on a cron schedule"* was supportable today. **It is not, and the row stays cut.**

The Orchestrator reversed the cron-only restore, and the reason is in the code: **the agent launch
is gated behind worktree creation.** `scheduler.go:568-577` — on `git worktree add` failure the run
is marked `failed` and the function **returns**, before anything launches. I verified that `return`
myself rather than accept the reasoning.

So on shipped v1.0.8 with documented defaults, **no agent ever runs.** The cron half genuinely
evaluates a schedule and writes a run record — which is accurate about the *scheduler* and false
about the *feature*, because a features table is read as *what the product does for me*. The
Developer Advocate's observation on the shipped artifact was `status: failed, worktree: None`.

⚖️ **I wrote a never-restore rule that would have kept a verified capability out permanently** — and
a deleted claim is invisible in a way a qualified one is not, because nobody greps for a feature that
is not there. That is the overcorrection reflex with a rule attached, which is worse than the reflex
alone. *"We are as obliged to claim what is true as to cut what is not"* — I have been repeating that
line all day and enforcing its opposite in this file.

⛔ **Scheduled jobs stay out of the closing paragraph entirely** — not the cron half, not the
isolation clause. Both become available when the fix ships **in a release**, not when it merges.
⛔ Webhooks stay out — unverifiable without an external endpoint, and configuring a *local* one is
refused at send time.

**My recommendation: include it.** In a field this crowded, "we're the only one" is disprovable in
one search, and the person who does that search will be in the HN thread.

---

## Sourcing

| Claim in the copy | Source |
|---|---|
| Claude Code has built-in git worktrees | [code.claude.com/docs/en/worktrees](https://code.claude.com/docs/en/worktrees) — the `--worktree` flag |
| Agent teams: lead spawns teammates, shared task list, teammates message each other | [code.claude.com/docs/en/agent-teams](https://code.claude.com/docs/en/agent-teams) |
| Teammates are Claude Code instances | Same page: "Coordinate multiple Claude Code instances"; teammates are "separate Claude Code instances" |
| "`/resume` and `/rewind` do not restore in-process teammates" | Same page, **Limitations** — quoted verbatim |
| Team directory removed at session end; one team per session | Same page, Architecture and Limitations |
| Split panes need tmux or iTerm2; unsupported in VS Code terminal, Windows Terminal, Ghostty | Same page, Limitations |
| Cursor's Agents Window runs parallel agents | [Cursor 3 Agents Window](https://www.agentpatterns.ai/tools/cursor/agents-window/), [Cursor changelog 2.0](https://cursor.com/changelog/2-0) |
| Claude Squad / Conductor / Crystal / vibe-kanban exist and use worktrees | [Open-source orchestrator survey](https://www.augmentcode.com/tools/open-source-agent-orchestrators) |
| Coral: four agents | `internal/agent/` — claude.go, codex.go, gemini.go, pi.go |
| Coral: sleep/wake per agent and per team | `/api/sessions/live/{id}/sleep`, `/wake`; `/api/sessions/live/team/{board}/sleep`, `/wake` |
| ⛔ *cost across vendors — CUT* | Not demonstrable: Claude reported $0.97, Codex nothing (#41). Cost tracking works per agent; the cross-vendor total does not |
| ⛔ *full-text search — CUT* | Never worked. `FTSBody` never assigned; zero FTS rows in production (#40) |

---

## Why prose and not a repaired table

A table is a promise to defend **every cell, forever**. Five of the ten rows in the old grid were
false or stale within months of being written, one was unsourceable, and the framing line at
README.md:125 anchored us to AutoGen — which Microsoft moved to maintenance mode, shipping Microsoft
Agent Framework 1.0 on 2026-04-03 as its successor. ([microsoft/autogen](https://github.com/microsoft/autogen))

Prose lets us make **narrow, dated, sourced** claims and concede the overlap openly. The concession
in the last line — "that vendor's own tooling may be all you need" — is doing real work: it is what
makes the rest believable to a skeptical reader, and it pre-empts the strongest objection in any
launch thread.

---

## Rules for anyone editing this later

1. **Never re-add a ✓/— grid.** That was the ruling and the reasoning still holds.
2. **Never write that a competitor "can't" do something.** Describe what Coral does and quote their
   documentation for the contrast. Their words age into their problem, not ours.
3. **Never claim to be first or only.** Four alternatives are named above; a search finds more.
4. ⛔ **Sleep/wake — a SERVER restart is verified; a machine reboot is not.** Write "restarting
   Coral", never the bare "survives a restart": unqualified, it invites the machine-reboot reading.
   **This rule was violated in this very file** — the main paragraph was corrected and the optional
   closing paragraph kept the ambiguous form for hours. Check both. Verified by
   execution: the Coral process was killed and a fresh one restored sleep state from disk. Still
   never write "survives reboots", "come back tomorrow", long intervals, large multi-agent teams,
   or any claim about how much scrollback survives. Approved wording is in the paragraph above; do
   not upgrade it past "quit Coral, restart it later".
5. **Date-check the Claude Code quotes before any launch.** They ship fast, agent teams are marked
   experimental, and a limitation quoted here could be fixed by the time we post. If a quoted
   limitation is gone, **cut that paragraph** — do not soften it.
