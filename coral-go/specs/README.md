# Coral Specs

Design specifications for Coral features. Each subdirectory contains a `README.md` with the full spec.

## Status Legend

| Status | Meaning |
|--------|---------|
| **Shipped** | Implemented and released |
| **In Progress** | Actively being built |
| **Planned** | Spec complete, not yet started |
| **Research** | Exploratory / needs validation |

## Feature Specs

### Core Platform

| Spec | Status | Summary |
|------|--------|---------|
| [Agent Config](AGENT_CONFIG/) | Shipped | How Coral configures and launches agents; brings Codex and Gemini to feature parity with Claude |
| [Agent Support](AGENT_SUPPORT/) | Shipped | Agent abstraction layer and guide for adding support for new AI agents |
| [Team Config](TEAM_CONFIG/) | Shipped | JSON format for defining heterogeneous agent teams with global defaults and per-agent overrides |
| [Launch Modes](LAUNCH_MODES/) | Shipped | Standalone server vs tray app modes and their architectural differences |
| [Licensing & Auth](LICENSING_AUTH/) | Shipped | Tiered approach to auth and licensing across three Coral deployment models |
| [Auth](AUTH/) | Shipped | Two-layer auth with auto-generated API key and optional PIN |
| [Public API](PUBLIC_API/) | Shipped | REST + WebSocket API for third-party UIs, CLI tools, and automations |
| [Update System](UPDATE_SYSTEM/) | Shipped | Notifications when new Coral versions are available |
| [Setup Wizard](SETUP_WIZARD/) | Shipped | First-run wizard guiding users through detecting and configuring agent CLIs |

### Automation & Orchestration

| Spec | Status | Summary |
|------|--------|---------|
| [Workflows](WORKFLOWS/) | In Progress | Multi-step automations chaining shell commands and AI agent prompts |
| [Board Tasks](BOARD_TASKS/) | Shipped | Atomic task management system for multi-agent coordination with server-side locking |
| [Connected Apps](CONNECTED_APPS/) | In Progress | Generic OAuth2 credential store for external services (Google, GitHub, Slack) |

### UI & Frontend

| Spec | Status | Summary |
|------|--------|---------|
| [Top Nav Redesign](TOP_NAV_REDESIGN/) | Shipped | Restructuring sidebar by moving components to top navigation |
| [Custom Views](CUSTOM_VIEWS/) | Planned | User-generated agentic sidebar tabs created from natural language descriptions |
| [Git Diff Tree View](GIT_DIFF_TREE_VIEW/) | Shipped | File preview pane showing changed files with diffs and inline editing |
| [Git Diff Toggle](GIT_DIFF_TOGGLE/) | Shipped | Choosing what to compare against in file diff view |
| [File Search Mode](FILE_SEARCH_MODE/) | Shipped | Progressive directory-based file browsing replacing fuzzy matching |
| [LSP Code Intelligence](LSP_CODE_INTELLIGENCE/) | In Progress | Implemented Go-first semantic hover, definition navigation, and references; real-gopls automation complete, live-editor DOM validation pending |
| [Team Directory Grouping](TEAM_DIRECTORY_GROUPING/) | Shipped | Better UI distinction for multiple teams by working directory and worktree |
| [Native Titlebar Drag](NATIVE_TITLEBAR_DRAG/) | Shipped | Restoring native macOS window dragging in transparent titlebar |

### Terminal & Performance

| Spec | Status | Summary |
|------|--------|---------|
| [Terminal Unified Stream](TERMINAL_UNIFIED_STREAM/) | Shipped | Single raw-byte streaming protocol for both PTY and tmux backends (replaces polling + capture-pane with pipe-pane tail and binary WebSocket frames) |
| [Tmux Polling Optimization](TMUX_POLLING_SPEC/) | Superseded | Superseded by TERMINAL_UNIFIED_STREAM |
| [Tmux Native Scroll](TMUX_NATIVE_SCROLL/) | Shipped | Replacing xterm.js scrollback with tmux copy-mode to eliminate flicker |
| [Xterm Flicker Fix](XTERM_FLICKER/) | Superseded | Flicker root cause eliminated by TERMINAL_UNIFIED_STREAM; retained for historical cursor-positioning notes |

### Mobile

| Spec | Status | Summary |
|------|--------|---------|
| [Mobile App](MOBILE_APP/) | Shipped | Responsive design with tablet/mobile breakpoints and touch-friendly targets |
| [Mobile Simplification](MOBILE_APP_SIMPLIFICATION/) | Shipped | Refocusing mobile app on live agents, group chat, and individual conversations |

### Research

| Spec | Status | Summary |
|------|--------|---------|
| [Consumer Research](CONSUMER_RESEARCH/) | Research | Qualitative research on Coral positioning and pricing (N=10, March 2026) |

## Writing a New Spec

1. Create a new directory: `specs/YOUR_FEATURE/`
2. Add a `README.md` following the structure of existing specs:
   - **Overview** — What and why
   - **Problem** — What's broken or missing
   - **Design** — Data model, API, UI
   - **Implementation Plan** — Phased with file paths
3. Update this index with the new spec
