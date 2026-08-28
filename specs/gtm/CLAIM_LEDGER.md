# Claim Ledger — what was actually executed

Every row below was produced by running the shipped v1.0.8 build. This is the reference
for the post-#42 README pass and for verifying the composed result.

**Rule for using this file:** a claim may appear in shipped copy only if its row is ✅.
The "Supportable wording" column is the strongest form the evidence actually carries —
not a softening, and not to be rounded up.

| # | README | Claim | Verdict | Evidence | Supportable wording |
|---|---|---|---|---|---|
| 1 | :110 | "any CLI-based tool" | ⛔ | `GetAgent` (`agent/agent.go:157-168`) is a hardcoded 4-case switch; no registry/plugin/config anywhere in `internal/`. `default:` silently starts Claude for an unknown type — observed. | "Works with Claude Code, Codex, Gemini CLI, and Pi.dev." |
| 2 | :89, :41 | per-agent "isolated worktrees" | ⛔ | One worktree **per team**, shared; both agents returned an identical `worktree_path`. Two agents produced `Truncate redeclared in this block`. Worktree is **off by default** — a default team runs in the user's real checkout on their real branch. | "Each team can run in its own git worktree on its own branch, isolated from your main checkout." |
| 3 | :115 | Workflows "chain tasks across agents with dependencies" | ✅ | 3-step shell pipeline produced `CHAIN-A-CHAIN-B-CHAIN-C` in the final file (proves ordering **and** data-passing in one artifact). Agent→shell chain produced `AGENTOUT-5573` downstream. Runs 6s / 10s. | "Multi-step pipelines that chain output between shell and agent steps." **Never call them isolated — workflow runs create no worktree (`worktree_path` null).** |
| 4 | :114 | Templates "save and share; generate from plain English" | ✅ | Generation returned 4 agents in ~30s, asserted 4/4 with `name`+`agent_type`+`prompt` and valid types. Folder import parsed `SKILL.md`→orchestrator + `agents/*.md`→workers, 3/3. Catalog: 97 templates across 6 of 28 categories; one pulled at 15,972 chars. | "Generate a team from a plain-English description; share teams as a folder." **Import RETURNS a config, it does not persist one — `teams/all` stayed 0. Avoid "save", which implies a library that does not exist.** |
| 5 | :116 | Scheduled jobs "on a cron schedule in isolated worktrees" | **⛔ for a downloader** (fix verified in code only) | On v1.0.8 every run fails: `fatal: 'main' is already used by worktree`. **The cron half is NOT independently shippable** — agent launch is gated behind worktree creation (`scheduler.go`: on worktree failure the run is marked `failed` and the function `return`s *before* `launchFn`). Run records confirm it: `status=failed`, `worktree=None`, **`session_id=null`** — no agent ever existed. So "launch agents on a cron schedule" describes an outcome that does not occur. Fixed in `6ec04fc` (`worktree add -b coral/job-run-<runID>`), re-verified by me: `error: none`, run on its own branch, user checkout still on `main` — but that is an **unpushed branch**. | **Claim nothing.** The row stays cut until the fix ships in a release. Do not restore a "cron only" variant — that was my recommendation and it was wrong; the halves are not independent. |
| 6 | :118 | Token tracking "cost and consumption in real time" | ⛔ (Claude-only) | Real figures per agent/session/team with input/output/cache broken out. But 8 usage records, **all Claude, zero Codex** — in a same-team controlled comparison Claude reported $2.97 and Codex reported nothing, despite `~/.codex/sessions` holding token counts. | "Token and cost tracking per agent, per session, and per team." **Nothing cross-vendor; a mixed-team total looks complete and is not.** |
| 7 | :119 | "Full-text search across all past sessions" | ⛔ | `FTSBody` is declared (`agent/agent.go:66`) and read (`indexer.go:114-115`) but **never assigned anywhere in the repo**. `session_fts` = 0 rows in a fresh instance (42 indexed) **and in production (56 indexed)**. A term visible on screen returns 0 hits. | "Every past session kept, with auto-summaries, tags, and notes." **Search is dead — cut it.** |
| 8 | :121 | Webhooks "Slack, Discord, or any HTTP endpoint" | 📖 | **Structurally unverifiable here.** SSRF protection (`httputil/ssrf.go`) blocks loopback/private on both `/test` and the dispatch path (`background/webhook.go:76`), no override — so the only test is a public third-party endpoint. CRUD works; 0 delivery records. Defect found: URL is validated at **send** time, not create time, so a localhost webhook saves as enabled and can never fire. | Say nothing stronger than "webhook notifications to an HTTP endpoint", and mark it unverified in the header. |
| 10 | :115 | Task management "create, assign, and track tasks on the message board; agents mark tasks complete as they finish" | **✅ CLI / 📖 dashboard** | **Not verified by me and not by any test** — verified by *continuous use*: **27 tasks, 23 completions, four assignees** over one working day, through `coral-board task add/list/claim/complete`. (The Orchestrator created and assigned every task and was assigned none, so five agents exercised the board while the assignee column holds four names.) *Counts re-run against `coral-board task list` on 2026-08-28; an earlier version of this row said 28 tasks / five agents — the 28 counted the table header row, and the five counted the team rather than the column.* Every clause exercised repeatedly, with results visible to all five agents throughout. Evidence is the working day itself; there is no fixture and no report. | "Create, assign, and track tasks on the message board." ✅ for the CLI mechanism. **📖 for the dashboard** — nobody has created, assigned or completed a task through the browser UI, the same standing gap as every other UI surface. |

## Artifact provenance

Every verdict in this file states **which artifact it was run against**, because a claim
verified on an unreleased branch is not a claim a downloading reader can rely on.

| Reading | Meaning |
|---|---|
| **Downloader** | Run against the binary inside the published `Coral.v1.0.8.dmg` — signed `Developer ID Application: Chris Knorowski (33UR27T84L)`, `spctl: accepted, source=Notarized Developer ID`, `version="1.0.8"`, `build tier=prod`. A locally compiled binary carries none of that, so provenance is checkable rather than asserted. |
| **Repository** | Run against a binary built from an unpushed branch. True of the code, **not** of anything a user can download. |
| **Unknown** | Not run. Distinct from disproven — see *Not verifiable from this machine*. |

**Downloader** covers every row in this file except two.

**Verified by use, not by test — row 10 only.** Task management was exercised continuously by five
agents all day rather than run in a fixture. That is a *stronger* provenance than most rows here —
more execution than anything we tested deliberately — but it is **not reproducible by a reader**,
which is why it is named rather than folded in with the rest. A row exercised constantly by everyone
generates no artifact that looks like verification, which is why it read as unexamined until someone
noticed we were standing on it.

> **A restored claim is a new claim.** The both-directions rule ("as obliged to claim what is
> true as to cut what is not") is a reason to *re-examine* a cut, not a reason to *restore* one.
> I invoked it to argue for restoring row 5's cron half and skipped the re-examination — the
> rule made restoring feel like rigour. The burden of proof does not drop for a restoration.

**Repository only — exactly one:** row 5's scheduled-jobs *fix* (built from `6ec04fc`). The
**defect** was found on the shipped artifact; the fix exists only on a local branch. No copy may
claim the isolated-worktree clause until it ships in a release.

> **Evidence notes go stale too.** Row 3's note that the launch response's `backend` field cannot
> be trusted describes v1.0.8. `#33` has since changed that response to report
> `backend (launch path)` and `terminal (what runs)` separately, so the note is already stale
> *for the repository* while remaining true *for the artifact*. No verdict changes; the reading does.

| 9 | :118 | Git integration "tracks commits, branches, and changed files per agent session" | **✅ all three** | Commits: snapshot recorded `7250728` with subject and timestamp. Branches: `feature/git-probe`, matching ground truth. Changed files: `/files` returned `a.go` (+2) and `b.go` (+1), `status M`, `source: git`, `diff_mode branch_point` — exactly the two files changed against the base branch, written to `git_changed_files` by `git_poller.go:116,161`. All keyed to the session. Poller interval **120 s** (`config.go:93`), so nothing is instant. **Note for anyone re-testing:** `changed_file_count` in the sessions list is a *different* metric — `tasks.go:517-524` counts agent Write/Edit tool events, not git state, and it reads 0 on a non-default port because the hook posts to `localhost:8420`. Test `/files`, not that field. | "Tracks commits, branches, and changed files per agent session." Verified in full. |

## Verified and safe to claim

| Claim | Evidence |
|---|---|
| macOS DMG is signed **and notarized** | `spctl -a -vvv`: `accepted`, `source=Notarized Developer ID`. Universal x86_64+arm64. Gatekeeper does not block. |
| Sleep/wake keeps context **across restarting Coral** | Process killed (tmux server gone), fresh process reported `sleeping:true` from disk, woke under the same `session_id`, agent recalled a pre-sleep fact. **Machine reboot untested.** |
| Board delivery is cursor-tracked | `last_read_id` unchanged across a full process kill; read messages not re-delivered, unread not skipped. |
| Agent-to-agent review over the board | 27s from handoff to a substantive posted verdict; the reviewer independently noticed the new function shipped without tests while its neighbours had them. |
| Heterogeneous agents on one repo | Claude Code and Codex ran simultaneously under one dashboard, each producing a working implementation. |
| No provider API keys in the product | Zero real-key-shape hits across **all nine executables** in the DMG; keys read only from `ANTHROPIC_API_KEY`/`OPENAI_API_KEY`/`GOOGLE_API_KEY` (`proxy/providers.go:24-33`); `proxy.go:166-172` passes the caller's own credentials through. |
| Nothing is license-gated | On an unlicensed prod build (`license=true`, status `valid:false`): 8/8 feature endpoints returned 200 and a write returned 201. `license/middleware.go:20-26` is a deliberate no-op. |
| Time to first result | 90s from server start to a committed, passing change. **Excludes install and agent-CLI authentication, and required answering the CLI's trust prompt.** |

## Retracted by me

| Claim | Where I asserted it | Why it is wrong |
|---|---|---|
| "The native PTY backend genuinely works on macOS — an agent launched on it in 0.58s" | board #196, #231, used as evidence the architecture is Windows-ready | The launch response's `backend` field reports `pty` whenever a backend exists, regardless of what actually runs — a defect **I later proved myself**. Running the shipped binary with `--backend pty` explicitly fails at spawn: `pty spawn failed: startPTYProcess: fork/exec /bin/zsh: operation not permitted`. No agent I ran was ever PTY-backed. I corrected the field and left the conclusion that depended on it. |

**Current honest state of the PTY backend:** unexercised, not known-broken and not known-working.
It fails with EPERM on this host, which is consistent with a sandbox restriction rather than a
code defect — my own shell forks `/bin/zsh` fine and the same binary spawns agents on the tmux
backend all day — but those two causes cannot be told apart from inside the sandbox. Resolving it
needs one launch on an unrestricted machine.

**Consequence for Windows:** three unknowns stacked, not one scoped fix — no artifact has ever
been built, the server does not compile for Windows, and the terminal backend it would default to
has never been seen working anywhere.

## Pair-or-omit

**No-keys and telemetry ship together or not at all.** "Coral holds no API keys of its own" is true and sits one sentence from the false impression that Coral sends nothing. The shipped binary carries a PostHog project key and posts to `us.i.posthog.com` by default with no opt-out.

## Revisit on the next release — this file expires

Every ⛔ here is ⛔ **because the shipped product fails**, judged against **v1.0.8**. Fixes now
exist on unpushed branches. **When a release ships, rows change correctness without a single
line of this file being edited** — there is no diff, grep, or verifier that detects it, because
the file holds still while the truth moves underneath it.

So this is the reminder nothing else provides. **Whoever cuts the next release must re-verify
these rows before any copy derived from them ships:**

| Row / claim | Pending fix | What changes when it ships |
|---|---|---|
| 5 — scheduled jobs | **#43, `6ec04fc`** (verified by me in code) | The isolated-worktree half becomes true, and with it the gated agent launch. The row can be **restored in full** — it is currently cut entirely, so nobody will grep for it. **Highest risk of being forgotten.** |
| 7 — full-text search | **#40** (`FTSBody` never assigned) | If assigned, search starts working and "Every past session kept…" can regain the search clause. |
| 6 — token/cost tracking | **#41** (Codex usage never ingested) | The "Codex usage is not currently ingested" caveat can drop, and a cross-vendor total becomes claimable. |
| 1 — agent types | **#32** (unknown type silently starts Claude) | Does **not** make "any CLI agent" true — it only stops the silent fallback. The four-agent claim is unaffected. |
| 8 — webhooks | none | Unchanged. Structurally unverifiable without a public endpoint, release or not. |
| 2 — worktrees | none | Unchanged. The code was always right; only the README overclaimed. |

Quickstart and troubleshooting also expire: **#26** (first-launch nag), **#30** (`install-cli.sh`
on Apple Silicon), **#33** (unsurfaced trust prompt), **#34** (`CORAL_PORT`/`CORAL_URL`), **#31**
(tmux socket merge) all change documented behaviour in `coral-go/agent_docs/`.

**Re-run the harnesses rather than re-reading this file:**

```bash
bash specs/gtm/verify-readme-claims.sh README.md          # 24 checks
bash specs/gtm/verify-scheduled-jobs.sh /path/to/coral    # row 5, calibrated both ways
```

## Not verifiable from this machine

Linux runtime (no machine/VM/container — binary is statically linked x86-64, **never executed**, and is amd64-only with no arm64 build and no tray/desktop binary), Windows anything (never built; does not compile), webhook delivery, the Finder drag-to-Applications flow, and the first-run EULA.

**The EULA now has a known cause, not just an unknown.** Per `#34`'s audit, `eula.go:46`
hardcodes `~/.coral/.eula-accepted` and ignores the configured data dir — so *no* isolated
instance on this machine can ever show a first run, regardless of `--home`. That marker was
deliberately not deleted (it gates a legal acceptance). This is the same root shape as the tmux
socket merge and the board-state path: **the code asks the machine for its data dir rather than
being told by the caller.** Observing it needs a clean user account or that hardcode fixed.
