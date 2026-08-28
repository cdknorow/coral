# gh-pages replacement — deployment notes

**Status: DRAFT. NOT DEPLOYED. Operator approval required.**
I have not pushed to `gh-pages` and will not. `gh-pages` is a live public website; that
push is outward-facing and is the operator's call.

## What this replaces

GitHub Pages currently serves `cdknorow.github.io/coral` from `branch: gh-pages, path: /`,
`build_type: legacy` (verified via `gh api repos/cdknorow/coral/pages`). The branch's last
commit is **2026-03-19**, `"Deployed 53611cf with MkDocs version: 1.6.1"` — five months
stale, 934 commits behind main, and built from a root `docs/` directory that no longer
exists on `main`. It cannot be regenerated from current source.

That site instructs visitors to run `pip install agent-coral`, which succeeds and installs
the retired Python product (PyPI 4.4.1). It never mentions the DMG, Go, or GitHub Releases.

## Files

| File | Purpose |
|---|---|
| `index.html` | Replacement root. Self-contained, no external CSS/JS/fonts — nothing to rot or get blocked. |
| `404.html` | Catches the ~15 old MkDocs deep links (`/webhooks/`, `/live-sessions/`, `/multi-agent-orchestration/`, …) that are indexed and linked from elsewhere. Without this, replacing the branch turns every one of them into a bare GitHub 404 with no route to the real product. Pages already has `custom_404: true`. |

## Every claim on the page, and where it came from

| Claim | Source |
|---|---|
| Asset names `Coral.v<version>.dmg`, `coral-linux-amd64-<version>.tar.gz` | `gh release view v1.0.8` — the actual published assets |
| Current version v1.0.8 | `gh release list` |
| PyPI `agent-coral` is 4.4.1 | `pypi.org/pypi/agent-coral/json`, uploaded 2026-03-21 |
| Build from source: `cd coral-go && make build` | `README.md:67-70`, `CLAUDE.md` |
| Runs on `http://localhost:8420` | `README.md:78` |
| tmux is the default backend on macOS/Linux | `cmd/coral/main.go:60-63` |
| Agents: Claude Code, Codex, Gemini CLI, Pi.dev | `internal/agent/{claude,codex,gemini,pi}.go` |
| Free, no features gated | `internal/license/middleware.go:18-19` |
| Apache 2.0 | `LICENSE` |
| $49.99 one-time, optional | `README.md:49`, checkout URL returns 200 |
| "no API keys of its own, never calls a model on your behalf" | 📖 **READ ONLY** — `internal/proxy/` uses user credentials only; `/api/teams/generate` shells out to the user's Claude CLI (`routes/system.go:704-706`). **Nobody has watched what the proxy actually sends**, and this is a universal negative, so reading cannot settle it. Highest-priority open verification |

## ⛔ Ship-blocking pairing rule

**The privacy paragraph and the telemetry paragraph must ship together.** The page says
*"nothing is sent to us"* — true of the agents' API traffic, and a **false impression** about
the product if it stands alone, because the shipped binary posts to PostHog by default with
no opt-out. Every sentence true, the impression false: the same completeness failure the D5
rewrite was written to fix, in the sentence that fixed it.

Do not remove, shorten, or relocate either paragraph independently. If the page is trimmed,
they are trimmed together or not at all.

## Deliberately absent

- **Homebrew** — blocked on task #21. `Casks/coral.rb` has three independent defects.
- **Windows** — blocked on #22. `coral.exe` does not currently compile.
- **Any metric** — no download counts, user counts, benchmarks, or testimonials.
- **A comparison table** — nothing here we would have to defend cell by cell.
- **Campaign parameters on the supporter link** — awaiting the #19 attribution scheme.
  Add them before deploy if #19 has landed by then.

## Deployment, when approved

`gh-pages` is an orphan branch serving static files from its root. Replacing its contents
with these two files is the whole change. **Recommended: tag the current branch head first**
(`git tag gh-pages-mkdocs-archive a1d279c`) so the old site is recoverable — the MkDocs
source that produced it is not on `main` and could not otherwise be reconstructed.

This is a stopgap that stops the bleeding. Task #28 (Growth Engineer) is the real fix: a
docs pipeline built from `coral-go/agent_docs/` by a workflow, so it cannot rot by hand again.
