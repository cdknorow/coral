# Repo-wide sweep: retracted claims surviving outside the README

**Task:** #37 · **Owner:** Content & Launch Producer · 2026-08-28
**Scope:** `coral-go/agent_docs/`, `coral-go/docs/`, `coral-go/internal/server/frontend/`,
the Go-served activation page, `CLAUDE.md`, `Casks/`, `installers/`, `tools/`.
**Excluded:** `coral-python-legacy/` (retired), `specs/gtm/` (my own never-say lists, which
quote the banned phrasings deliberately).
**Method:** grepped each retracted claim family, then verified every hit against the code
before calling it a defect. **Reporting only — nothing fixed.**

---

## S1 — `server.go:837` "Native desktop app (macOS & Linux)" — FALSE for Linux

The activation page's **free** column advertises a Linux desktop app. `release.yml:42`
builds the Linux tarball from six commands — `coral`, `launch-coral`, `coral-board`, three
hooks. **`coral-tray` and `coral-app` are not in that loop**; macOS builds both (`:226`).
Linux is CLI/headless by construction, and the Dev Advocate confirmed neither binary is in
the published tarball.

> **Proposed:** `<li>Native desktop app (macOS)</li>` — or `Native macOS app; Linux CLI`
> if the Linux path should stay visible.

**Owner: Growth Engineer** (pairs with #29, same page). This is the string the Dev Advocate
flagged from the artifact side; the file:line was previously unlocated because the activation
page is inline Go, not a template.

## S2 — `server.go:840` "Claude, Codex & Gemini support" — omits Pi.dev

Undersells by one. `internal/agent/` implements `claude.go`, `codex.go`, `gemini.go`, `pi.go`.

> **Proposed:** `<li>Claude, Codex, Gemini &amp; Pi.dev support</li>`

**Owner: Growth Engineer** (#29). Matches the correction the Strategist already made in
`ACTIVATION_PAGE_COPY.md`.

## S3 — `team-config.md:26` and `jobs.md:31` document only three agent types

Shipped docs, and **read by agents at runtime**:
- `team-config.md:26` — `agent_type` … `CLI to use: "claude", "gemini", or "codex"`
- `jobs.md:31` — `Agent to use (claude, gemini, or codex)`

Both omit `pi`, which `agenttypes/types.go:8-12` defines and `agent.go:157-168` dispatches.

> **Proposed:** add `"pi"` to both enumerations.

**Owner: Developer Advocate** (`agent_docs/` is their area). Note their new
`quickstart.md:29` lists Pi.dev correctly — these two are the stragglers.

This is the failure mode the Orchestrator named: an agent reading `team-config.md` as ground
truth concludes Pi.dev is unsupported and will not offer it.

## S4 — `server.go:696` `<title>Coral — Activate License</title>`

Already raised by the Strategist; recording the file:line. The browser tab is the first thing
a new user sees, and "Activate" reads as a paywall on a product with no gate.

> **Proposed:** `<title>Coral</title>`

**Owner: Growth Engineer** (#29).

## S5 — `CLAUDE.md:122-126` tier table says Prod requires a license

`| Prod | (default) | Required | Required | ... |` — contradicted by
`license/middleware.go:18-19`: all requests pass through regardless of license status.
Internal doc, not shipped to users, so **not launch-blocking** — but the Orchestrator ruled
this must never enter copy, and it is the most likely place a future writer would pick up
the wrong premise.

> **Proposed:** correct the License column for Prod, or footnote that no routes are gated.

**Owner: whoever next edits `CLAUDE.md`** — flagging, not claiming it.

---

## Clean results — recorded deliberately

A clean result is a real result, and these were checked, not assumed.

**`coral-go/agent_docs/` (22 docs) contains none of:**
per-agent worktree or "own copy of the repo" claims · "without merge conflicts" or any
collision-safety claim · the message board framed as a safeguard · "any CLI agent" or
user-facing extensibility · `pip install` / `agent-coral` / PyPI / FastAPI · `brew install` ·
`Coral.dmg` or any wrong asset filename · `cdknorow.github.io/coral` · "doesn't call any AI
APIs" · "free tier" / paid-gate language · "survives reboots" or persistence-across-restart.

**`coral-go/internal/server/frontend/`** — no retracted claims found. The team-creation modal
copy (`modals.html:243`) is already **correct**: *"Create an isolated git worktree so the team
works on a separate branch."*

**No Windows-supported claim anywhere.**

## ⛔ CORRECTED 2026-08-28 — this section was wrong, and it was one of my most-cited findings

**What follows was written before Dev Advocate task #43.** The mechanism analysis below is
still correct; the conclusion drawn from it is not.

`git worktree add <dir> main` — exactly what `scheduler.go:557` runs — **fails whenever the
user's own working copy has `main` checked out**, which is the normal case. Reproduced:

```
$ git worktree add <dir> main
fatal: 'main' is already used by worktree at '<the user's repo>'
```

So **scheduled jobs fail 100% on the documented default configuration** (`create_worktree`
defaults to `true`). The per-run worktree keyed on `runID` is real and correct — **it never
gets to run**, because the git call before it fails. The UI shows the job healthy because the
scheduling half works.

**Therefore:**
- `jobs.md:200` "each job gets an isolated copy of the repo" is **not** accurate in practice.
- My claim that "per-run isolation is real elsewhere in the product" is **false as shipped**.
- The Dev Advocate's user guidance — *"if you need genuine per-unit isolation today, run the
  work as separate scheduled jobs"* — recommends a path that cannot work. They are correcting it.

**The rule I drew from this survives; the reason for it does not.** "Isolated worktree" still
requires a path qualifier every time — but not because one path works and the other doesn't.
Because **neither** delivers per-agent isolation as shipped: teams share one worktree (off by
default), and jobs create a correct per-run worktree that fails to materialise. Keeping a
conclusion while its justification rots is how a document becomes unreviewable, so the reason
is replaced here rather than left standing.

**And the failure is my own catalogued pattern, applied to me.** I verified `scheduler.go:551`
by reading, saw correct per-run keying, and concluded the feature worked. *A correct-looking
mechanism proves a handler is wired, not that the feature works.* Third instance today — FTS,
Codex cost, this — and the first where it produced a false claim in a **remedy we were
recommending** rather than a feature we were selling.

### Original section, retained for the record

## A correction that matters: `jobs.md` is accurate, and it is not the same mechanism

`jobs.md:200` — *"When `create_worktree: true` (the default), each job gets an isolated copy
of the repo"* — reads like the claim we just cut from the README. **It is not, and it is
true.** `scheduler.go:551`:

```go
worktreeDir = fmt.Sprintf("%s_task_run_%d", rc.repoPath, runID)   // keyed on runID
```

Keyed on the **run**, not a board. Jobs are single-agent runs, so one worktree per run really
is one worktree per agent, and `create_worktree` genuinely defaults to `true`.

**So Coral has two different worktree behaviours, and only one was ever described correctly:**

| Path | Worktree | Default | Doc status |
|---|---|---|---|
| **Scheduled jobs / one-time jobs** | one per **run** (`scheduler.go:551`) | **on** | accurate in `jobs.md` |
| **Team launch** | one per **team**, shared by all agents (`sessions.go:2438`) | **off** | was wrong in README |

Worth stating plainly because it explains how the README claim survived so long: **per-run
isolation is real elsewhere in the product.** Anyone spot-checking "does Coral do isolated
worktrees?" against the jobs path would have confirmed it and moved on. The team path is the
anomaly, not the norm.

It is also a live trap for copy: "isolated worktrees" is true of jobs and false of teams, so
the word must never appear without saying which path it describes.

---

## Re-sweep: the three docs added 2026-08-28 — CLEAN

`quickstart.md` (226), `worked-demos.md` (237), `troubleshooting.md` (237) — 700 new lines of
shipped surface area, written after the original sweep. Re-checked against all 11 retracted
claim families.

**Clean on all eleven.** Every grep hit is the *correct* usage, not a defect:

| Hit | Verdict |
|---|---|
| `worked-demos.md:224` "survives reboots" | The **caveat instructing not to claim it** — *'Claim "wake it later with context intact", not "survives reboots"'* |
| `quickstart.md:33,42,217`, `troubleshooting.md:62,69` `agent-coral` | Telling users to **remove** it — `pip uninstall agent-coral`, `which -a coral` |
| `quickstart.md:28` `pip install google-gemini-cli` | The genuine Gemini CLI install command |
| `troubleshooting.md:161` `brew install tmux` | Legitimate; **no `brew install coral` anywhere** |
| `quickstart.md:56` `Coral.dmg` | States filenames carry a version: *"`Coral.v1.0.8.dmg`, not `Coral.dmg`"* |
| `/opt/homebrew` paths | Real tmux discovery paths and the Apple Silicon `install-cli.sh` warning |

Written from execution rather than from the README, and it shows.

**Method note:** the first run of this sweep returned "clean" on four families before the
files were being read at all — zsh does not word-split unquoted variables, so `grep … $FILES`
searched for a single nonexistent filename. Caught because `wc -l` printed nothing. Re-run
with explicit filenames and a line-count assertion. See the meta-rule in the checklist.

---

## Standing process — added to `LAUNCH_CHECKLIST.md`

When a claim is retracted, grep **every** artifact — finished drafts, the product's own
`agent_docs/`, UI strings, and Go-served HTML — not only the files currently being edited.
The do-not-say list is the grep pattern. Two live claims were found this way today: the
per-agent worktree line surviving in a gh-pages draft marked complete, and S1/S2 above.
