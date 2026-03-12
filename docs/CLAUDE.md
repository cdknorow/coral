# Docs

Documentation uses [MkDocs](https://www.mkdocs.org/) with the [Material](https://squidfunk.github.io/mkdocs-material/) theme. Config is in `mkdocs.yml`, content lives in `docs/`.

## Build and deploy

```bash
# From the docs/ directory
cd docs

# Local preview
mkdocs serve

# Build static site to site/
mkdocs build

# Deploy to GitHub Pages (gh-pages branch)
mkdocs gh-deploy
```

The site is published at https://cdknorow.github.io/corral/.

## Current pages

- **Home** (`index.md`)
- **Multi-Agent Orchestration** (`multi-agent-orchestration.md`)
- **Live Sessions** (`live-sessions.md`)
- **Button Macros** (`button-macros.md`)
- **Agent Protocol (PULSE)** (`protocol.md`)
- **Session History & Search** (`session-history-search.md`)
- **Metrics & Observability** (`metrics-observability.md`)
- **Scheduled Jobs** (`scheduled-jobs.md`)
- **One-Time Jobs** (`one-time-jobs.md`)
- **Webhook Notifications** (`webhooks.md`)
- **API Reference:** Jobs (`api/jobs.md`), Webhooks (`api/webhooks.md`)

Screenshots are stored in `docs/images/`.

## Adding pages

1. Create a markdown file in `docs/` (e.g. `docs/guide.md`)
2. Add it to the `nav` section in `mkdocs.yml`
3. Run `mkdocs gh-deploy` to publish
