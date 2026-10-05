# Coral documentation

Coral brings coding agents into one dashboard. Start individual agents or teams, follow their conversations and tasks, inspect their files, and review the artifacts they produce.

These guides cover the current **Go application**, including the macOS and Linux packages. Start with [installation and your first agent](getting-started.md).

## Get started

1. [Download and install Coral](getting-started.md). The macOS app includes tmux; Linux needs a separate tmux installation for the default backend.
2. Install and sign in to your agent CLI. Coral uses your installed agents and their provider accounts.
3. Open Coral, select a project directory, and launch an agent or [create a team](teams-and-tasks.md).

[Download the latest release](https://github.com/cdknorow/coral/releases/latest) · [Watch the team demo on Loom](https://www.loom.com/share/f2c52e824b1f41a8a18000a2df67adf8)

## Find the right guide

| What you want to do | Guide |
| --- | --- |
| Set up the app and launch an agent | [Getting started](getting-started.md) |
| Delegate work and follow a team's tasks | [Teams and tasks](teams-and-tasks.md) |
| Browse files and preview agent output | [Files and artifacts](files-and-artifacts.md) |
| Ask an agent for a diagram or interactive panel | [Agent UI](agent-ui.md) |
| Connect from a phone or another computer | [Remote access](remote-access.md) |
| Understand analytics, local storage, and encryption | [Privacy](privacy.md) |
| Resolve launch, connection, and preview problems | [Troubleshooting](troubleshooting.md) |
| Use CLI commands, APIs, jobs, or integrations | [Reference](reference.md) |

## Platform and release information

The regular release provides a universal macOS app and a Linux amd64 archive. macOS bundles include tmux starting with **v1.3.23**. Remote access is off by default and requires explicit enablement and a restart.

See [release notes](https://github.com/cdknorow/coral/releases) for changes and package verification. The macOS app targets macOS 13 or later; a deployment target check is not a claim that every older host configuration has been tested.

## Project and support

Coral's source is available under [Apache License 2.0](https://github.com/cdknorow/coral/blob/main/LICENSE). For bugs, include your Coral version, operating system, install method, and steps to reproduce in a [GitHub issue](https://github.com/cdknorow/coral/issues). Remove tokens, private prompts, and sensitive paths before sharing logs.

The Python implementation and its documentation are historical. Do not use the old `pip install agent-coral` instructions to install this app.
