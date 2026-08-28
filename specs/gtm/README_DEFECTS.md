# README Defect List

**Task:** #25 · **Owner:** Content & Launch Producer · **Date:** 2026-08-28
**Method:** every claim checked against the code, the release workflow, published release
assets, or a live HTTP request. Nothing here is inferred from other documentation.

Severity: **P0** visitor-facing and wrong today · **P1** blocks launch copy · **P2** cleanup.

### ⚠️ Evidence tier — added after the fact, and the omission was the point

The method line above says findings were checked against "code, release.yml, published
assets, or a live HTTP request." **That sentence conflates two different kinds of evidence**
and reads as one undifferentiated claim of rigour. A `file:line` citation has *the texture of
proof* — and three retractions today came from correct citations of correct mechanisms.

| Tier | Meaning | Track record today |
|:--:|---|---|
| ✅ | **Executed** — a command was run, a request made, a binary launched | held |
| 📖 | **Read** — code, config or docs inspected; nothing run | **failed three times** |

Per-defect tiers:

- **✅ Executed:** D1 (published asset names via `gh release view`), D2 (live fetch of the docs
  site + the PyPI JSON API), D3 (403 reproduced with two user-agents), D4 (shields.io returned
  `Discord: invalid`), D14/D15 corroborated against the real published tarball, D17/D18
  (my reading, confirmed by Dev Advocate execution).
- **📖 Read only:** **D5**, D9–D13, D16.

### D5 is the one that matters, and it is 📖

D5 rests entirely on reading — `proxy.go:1-2`, `providers.go:24-32`, `system.go:704-706`.
**Nobody has watched what the proxy actually sends.** Yet the wording it produced is now
canonical and shipped verbatim in four artifacts (this list, `TELEMETRY_DISCLOSURE.md`,
`README_ABOVE_THE_FOLD.md`, the gh-pages draft) plus the Strategist's FAQ.

So the *finding* is marked 📖 on the suspect list while its *conclusion* travels unmarked as
settled. That asymmetry is the thing to watch: **a claim can be downgraded in one document
while the sentence derived from it keeps propagating at full confidence.**

And note the claim's shape. *"Never calls a model on your behalf"* is a **universal negative**
— the claim type least suited to verification by reading, because reading confirms what is
present, not what is absent. Our strongest privacy claim is exactly that shape. It is closeable
on hardware we have: watch what the proxy sends.

---

## P0 — wrong or broken on the front page right now

### D1. Download filenames have never existed — **FIXED, awaiting operator approval**
`README.md:61-62` told users to download `Coral.dmg` and `coral-linux-amd64.tar.gz`.
Actual published assets on v1.0.8: `Coral.v1.0.8.dmg`, `coral-linux-amd64-1.0.8.tar.gz`.
`release.yml:320` sets `DMG_NAME="Coral.v${VERSION}.dmg"`; `release.yml:55` produces
`coral-linux-amd64-${VERSION}.tar.gz`. Neither README name has ever been produced.

Fix applied to the working tree (filenames only, uncommitted):
```
- **macOS**: `Coral.v<version>.dmg` (universal binary) — e.g. `Coral.v1.0.8.dmg`
- **Linux**: `coral-linux-amd64-<version>.tar.gz` — e.g. `coral-linux-amd64-1.0.8.tar.gz`
```
Placeholder + example so it stays true across releases.

### D2. Four links send visitors to an install funnel for the abandoned Python product
`README.md:12` (badge), `:20` (nav), `:142` (Documentation), `:158` (footer) all point to
`cdknorow.github.io/coral`, which instructs `pip install agent-coral` — a command that
succeeds and installs the legacy Python product (PyPI 4.4.1, 2026-03-21). Full analysis in
board msg #176. Redirect prepared as a separate patch (see `patches/`), and replacement
page drafted under `drafts/gh-pages/` — **task #27, operator approves the push**.

### D3. The hero video thumbnail is a broken image
`README.md:30` embeds `cdn.loom.com/sessions/thumbnails/7dce...-with-play.gif`.
That URL returns **HTTP 403** — with a plain client *and* with a browser User-Agent
(`content_type: application/xml`, 111 bytes). GitHub's camo proxy fetches it the same way,
so the largest visual element on the page renders broken. The adjacent
`<!-- TODO: Replace with hosted mp4 once uploaded to GitHub -->` at `:28` is still open.
The Loom *page* itself is fine (200) — only the thumbnail is blocked.

### D4. The Discord badge renders the literal word "invalid"
`README.md:14` uses `img.shields.io/discord/placeholder`. `placeholder` is not a server ID.
shields.io returns HTTP 200 with `<title>Discord: invalid</title>` — the badge visibly reads
**"Discord | invalid"**. The Discord *invite* (`discord.gg/qhfgY57AZn`) is live (200); only
the badge is broken. Fix needs the numeric guild ID, which I don't have — **need from operator.**

---

## P1 — blocks launch copy

### D5. "Coral doesn't call any AI APIs itself" is imprecise, and the imprecision is the kind HN finds
`README.md:47`. Two things complicate it:
- `internal/proxy/` is *"an HTTP proxy for LLM API calls with SSE streaming support and
  per-request cost tracking"* (`proxy.go:1-2`), with upstreams `api.anthropic.com`,
  `api.openai.com`, `generativelanguage.googleapis.com` (`providers.go:24-32`). This is how
  the README's "Token tracking" feature works — Coral sits in the request path.
- `/api/teams/generate` *"kicks off an async Claude CLI call"* (`routes/system.go:704-706`).

Neither uses a Coral-owned key, so the *spirit* of the claim holds. But "doesn't call any AI
APIs itself" invites a reader who finds the proxy to conclude we were hiding it.
**Recommend making it stronger by making it precise**, e.g.: *"Coral has no API keys of its
own and never calls a model on your behalf. It runs the CLI agents you've already installed,
and can proxy their traffic with your credentials to count tokens locally."* That is both
accurate and a better privacy story. **Needs Strategist sign-off on wording.**

### D6. Comparison table — **resolved by Orchestrator, tracked here for closure**
`README.md:127-138`. The "Open source" row is defensible: Apache 2.0, and
`internal/license/middleware.go:18-19` states all requests pass through regardless of license
status — nothing is gated. Remaining nine rows are with the Strategist: verify with citations
or cut, default cut. Note `CLAUDE.md`'s tier table claiming prod requires a license is
**stale documentation, not product behavior** — must not enter copy.

### D7. No campaign attribution on any supporter link
Five bare links to the checkout URL (`:13`, `:19`, `:50`, `:52`, `:155`). Growth Engineer
task #19 defines the scheme; I will not hardcode UTMs until it is posted, or seven assets
ship inconsistent parameters.

### D8. Windows is correctly absent — **do not "fix" this**
`README.md:59-63` lists macOS and Linux only, which is **accurate**. Dev Advocate confirmed
`coral.exe` does not currently compile (`internal/background/workflow_runner.go`, six
Unix-only syscall sites), so no MSI can exist. Logged so nobody "helpfully" adds Windows.

---

## P2 — cleanup, not launch-blocking

| # | Defect | Location |
|---|---|---|
| D9 | `assets/icons/` referenced as `icons/` in `CLAUDE.md` and `release.yml:239`; build survives via root `Coral.icns` fallback | docs only |
| D10 | Requirements line names tmux without noting it is the **default** backend on macOS/Linux (`cmd/coral/main.go:60-63`). **Corrected 2026-08-28:** this entry previously offered the `--backend pty` path as a mitigation implying the tmux requirement is softer than stated. That was reasoning built on a premise since retracted — the Dev Advocate disproved their own "PTY backend works" claim (it cannot spawn on their host; EPERM, and they cannot distinguish sandbox from code from inside the sandbox). The flag and code path exist; **the backend is unexercised**. Do not present it as an alternative. | `README.md:81` |
| D11 | Logo masters are 2048x2048 / ~6.7MB — unusable in a README or landing page without web exports | `assets/icons/` |
| D12 | No Open Graph / social card image exists; every shared link renders bare | repo-wide |
| D13 | `.claude/skills/release.md` is a PyPI publish runbook for `agent-coral` (twine, lines 55-89) | escalated to operator |

### D14. "Linux" oversells what the Linux tarball contains — **verified at source**
Flagged by the Developer Advocate from the artifact; I confirmed it in the build definition.
`release.yml:42-49` builds the Linux tarball from exactly six commands — `coral`,
`launch-coral`, `coral-board` and three hooks. **`coral-tray` and `coral-app` are absent**,
while the macOS bundle ships both (`release.yml:226`). Linux is therefore **CLI/headless
only**, with no tray and no desktop window.

Any copy describing Coral as a "native desktop app" on Linux is unsupported. I could not
locate that exact string in `coral-go/internal/server/frontend/` — it is most likely on the
external Lemon Squeezy store page. **Need the exact location from the Dev Advocate before I
can draft the correction.**

### D15. Linux is amd64-only, and "Linux" doesn't say that
`release.yml:44` hardcodes `GOARCH=amd64`. No arm64 build exists — nothing to install on
Raspberry Pi, Ampere, Graviton, or most ARM cloud VMs. `README.md:63` naming the file
`coral-linux-amd64...` is not strictly wrong, but a reader scanning for "Linux" will not
infer the exclusion. Worth an explicit "(x86-64)" in copy.

*Upside worth using once someone actually runs it:* `CGO_ENABLED=0` yields a statically
linked binary — no glibc dependency, so it should run on musl/Alpine and old distros. Dev
Advocate confirmed `file` reports "statically linked". **Not yet run by a human on Linux —
do not put it in copy until it is.**

### D16. First terminal output is a 900-character debug diagnostic
Dev Advocate observed startup emitting a ~900-char line beginning
`DEV-TMUX-DIAGNOSTIC-2026-06-03-02` on every invocation, including `--help`. Cosmetic, but
it is the literal first impression of the product. Growth Engineer's surface, logged here
because it affects the quickstart screenshots and any terminal capture we publish.

### D17. "Any CLI agent" is unsupportable — **RULED: cut, patch ready**
`README.md:37`, `:81`, and the features-table row at `:110` all imply a user can add their
own CLI agent. `internal/agent/agent.go:157-168` is a hardcoded switch over four types with a
`default:` arm; `:177-182` hardcodes those same four CLIs and their install commands. A grep
across `internal/` for `custom_agent|customagent|user_defined|plugin|registerAgent` returns
nothing — no registry, plugin interface, config file, UI form, or env var. Adding an agent
means writing Go and recompiling.

Compounding it: the `default:` arm means an unknown `agent_type` returns HTTP 200 `ok:true`
and **silently starts Claude** — so a user testing the advertised extensibility gets a real
Claude session and may not notice (Growth Engineer task #32).

Patch: `patches/README-remove-any-cli-agent-claim.patch`. The features row is retitled to
carry the real differentiator — mixing different vendors' agents in one team — rather than
leaving a deflated cell behind.

### D18. Per-agent worktree isolation does not exist — **P0, the differentiator claim**
Found by the Developer Advocate via `git worktree list`; I verified it in the code before
changing any copy.

`sessions.go:2428-2450` (the team-launch path) creates **one** worktree:
```go
worktreePath = filepath.Join(worktreeDir, body.BoardName)          // keyed on the BOARD
worktreeBranch := fmt.Sprintf("coral-team/%s", body.BoardName)
workingDir = worktreePath                                          // then used for every agent
```
The path is derived from the **board name**, not the agent name, the block runs **once**
before the agent loop, and `workingDir` is assigned to all agents. It is one worktree per
team, on one shared branch, shared by every agent on it.

Three README claims say otherwise:
- `:41` "Each agent runs in its own tmux session with its own git worktree, so agents can
  write code in parallel **without merge conflicts**"
- `:97` "Coral creates a git worktree for each agent... Each agent has its own copy of the
  repo and can read, write, and run commands **without interfering with others**"
- `:108` features row "each in its own git worktree and tmux session"

**It is also OFF BY DEFAULT.** `sessions.go:2431` gates the whole block on
`body.Worktree && body.WorkingDir != ""`, and `templates/includes/modals.html:240` has **no
`checked` attribute** — the option is additionally `display:none` until the directory is
detected as a git repo. So the median user clicking through the normal team-creation flow
gets **no worktree at all**: the team runs directly in their working directory.

**The in-product copy is already correct.** `modals.html:243` reads *"Create an isolated git
worktree so the team works on a separate branch"* — team-level, accurate. The code and the UI
agree; only the README overclaims.

**Priority within this defect:** the phrase *"without merge conflicts"* on `:41` is a
**safety** claim, not a feature claim. Agents sharing one directory can absolutely clobber
each other, and we currently tell users they cannot. Someone who believes it launches a
four-agent team on real work and trusts it. Cut that clause even if the other two wait.

**Be precise about what is and isn't wrong.** Each agent *does* get its own tmux session —
that half is true. The worktree half is not. Isolation exists **from your main checkout**,
not **between agents**.

Supportable: *"Each team works in its own git worktree on its own branch, isolated from your
main checkout. Every agent gets its own terminal session."*

Why this outranks every other defect in this file: it is the product's stated reason to exist
versus running terminals by hand, and `git worktree list` disproves it in **one command** —
faster than the ninety-second `agent.go` check. Per-team worktrees are a defensible design
(teammates usually *should* share a branch), but it is a different story from the one we
tell, and ours is the one that gets us caught.

---

## What checked out clean

Worth stating, since the audit found real problems — the product claims themselves hold up.

- **Features table (`:107-121`) is accurate.** Verified routes exist for workflows
  (`server.go:440`), scheduled jobs (`:429`), webhooks (`:466`), sleep/wake (`:331-340`),
  and templates (`:484`).
- **Agent support is accurate — four agents.** `internal/agent/` implements exactly
  `claude.go`, `codex.go`, `gemini.go`, `pi.go`. (`shell.go` is *not* a fifth agent: it is
  `detectShell()`, `classifyShell()`, PATH helpers and `SanitizeShellValue()` — no agent type
  in it. `agenttypes/types.go:8-12`'s fifth constant, `terminal`, is a raw shell session and
  is absent from `GetAgent` entirely.) But see D17 — the README overstates extensibility.
- **"Free and fully unlocked" is true and verifiable** — no route gating.
- Supporter checkout, Discord invite, GitHub Releases, and the hero dashboard screenshot all
  return 200.
- The README contains **no** pip/PyPI/legacy references. The contamination is entirely on the
  docs domain, not in this file.
