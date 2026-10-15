# Team Directory Grouping

## Problem

Coral can show multiple teams at once, and users often distinguish those teams
by working directory or worktree rather than by team name alone.

The current UI treats directory information as weak footer metadata. That works
only when:

- one team is visible
- all agents in that team share one directory
- users already remember which team belongs to which repo

Those assumptions do not hold in real use. Users launch multiple teams in
parallel across different worktrees, and some agents within a team may override
the team default directory.

This creates two UX failures:

1. Teams are harder to identify quickly in the sidebar.
2. Per-agent directory overrides are invisible or misleading.

## Goals

- Make working directory a first-class part of team identity.
- Preserve fast scanning when multiple teams are visible.
- Support mixed-directory teams without repeating long paths on every row.
- Keep the sidebar compact enough for daily use.
- Avoid introducing a heavy tree UI for the common single-directory case.

## Non-Goals

- Changing the underlying team config format
- Reworking launch flows in this spec
- Building a full repo/worktree browser inside the sidebar

## Design Principles

1. Team identity is `name + worktree`, not just `name`.
2. Shared metadata belongs at the team level.
3. Agent-level directory UI should appear only when it adds information.
4. Exceptions should be highlighted, not duplicated everywhere.
5. The default case should stay visually simple.

## Proposed Information Hierarchy

### Sidebar Team Card

Each team card should show:

1. Team name
2. Primary worktree / working directory
3. Team metadata summary
4. Agent list

Recommended header structure:

```text
coral-team
~/Software/coralai/coral-go
Agent Team · 8 agents
```

### Agent Rows

Each agent row should show:

- agent name
- status / activity signal
- optional model badge if it adds distinction
- optional prompt preview for the selected row only

Directory information should not be repeated on every row when the agent uses
the team default directory.

Agent type should not appear in the default sidebar row. It is lower-priority
metadata and belongs in hover details, session info, and configuration UI.

## Directory Display Rules

### Case A: All agents use the same directory

Use the team header to show the directory. Do not repeat it in agent rows.

```text
Team: coral-team
Dir:  ~/.../coral-go
Rows: Orchestrator, Frontend Dev, QA Engineer...
```

### Case B: Some agents override the team directory

Keep the team default directory in the header. For only the agents that differ,
show a compact exception indicator on the row.

Recommended row treatment:

- muted directory chip, for example `docs-site`
- or a secondary text line, for example `Different worktree: ~/.../docs-site`

Preferred default:

- use a short directory chip in collapsed lists
- show the full absolute path on hover, tooltip, or expanded detail

### Case C: Many agents use different directories

When directory overrides become common, switch the expanded team view to
directory subgroups.

Structure:

```text
coral-team
~/.../coral-go

coral-go · 5 agents
  Orchestrator
  Frontend Dev
  QA Engineer

docs-site · 2 agents
  Technical Writer
  Content Writer

infra-scripts · 1 agent
  DevOps Engineer
```

This grouping should appear only when it materially improves readability.

## Recommended Behavior Model

Use a progressive disclosure approach:

### Collapsed team card

- Show team name
- Show primary directory directly below the team name
- Show agent count and status summary
- Show flat agent list
- Show per-agent directory chips only for agents that differ from team default

### Expanded or focused team view

- If all agents share one directory, keep a flat list
- If a minority of agents differ, still keep a flat list with exception chips
- If multiple clusters exist, group agents by directory

## Visual Design Guidance

### Promote directory from footer to identity line

The current footer-style path is too faint and too detached from the team name.
Move it into the header block directly beneath the team title.

### Reduce redundant chrome

Because directory becomes more important, reduce low-value visual noise:

- de-emphasize footer metadata
- avoid multiple competing selection markers
- keep model pills smaller and quieter
- reserve preview text for the selected row
- remove agent type from the default row layout

### Directory formatting

Use a shortened path by default:

- prefer last segment or repo/worktree name for chips
- prefer a compact, tilde-relative path in headers
- expose full path on hover or detail view

Examples:

- header: `~/Software/coralai/coral-go`
- chip: `docs-site`
- tooltip: `/Users/me/Software/docs/docs-site`

## Interaction Rules

### Team-level directory

- Always visible in the team header
- Click behavior should continue to select/open the team, not navigate paths
- Full path can be copied from a context menu or tooltip affordance later

### Agent-level directory exception

- Only shown when agent directory differs from team default
- Should not overpower the agent name
- Must remain visible even when the row is not selected

### Agent metadata in hover and detail surfaces

Detailed metadata that does not help rapid list scanning should live in the
existing hover card, session info modal, or full configuration views.

This includes:

- agent type
- full model identifier
- branch
- board
- full goal / status text
- full absolute working directory

### Directory subgroup headers

If grouping is enabled, subgroup headers should:

- show short directory label
- show agent count
- be visually subordinate to the team header
- be collapsible only if needed later; do not require collapse in v1

## Sorting and Grouping Rules

### Team list

Do not sort teams by directory in the main sidebar by default. Preserve the
existing team ordering unless there is a separate sorting feature.

### Agent list within team

Default behavior:

- preserve explicit agent order set by the user

When directory subgrouping is active:

- group by directory first
- preserve manual order within each directory group where possible

## Edge Cases

### Team name collision across worktrees

Two teams may have similar names but different worktrees. The directory line
must make those distinct at a glance.

### Long paths

Long paths should truncate gracefully in the header with ellipsis, while hover
or expanded detail reveals the full value.

### Detached agents

If an agent has no known working directory, show no chip and fall back to team
context. Avoid placeholder noise such as `unknown`.

### All agents overridden

If nearly every agent uses a different directory, the team header should still
show the configured default directory, but the expanded view should likely group
by effective directory to avoid misleading sameness.

## Proposed Implementation Phases

### Phase 1: Team identity fix

- Move working directory into the team header
- Remove or de-emphasize the footer path
- Keep flat agent list
- Add per-agent exception chip for non-default directories

This phase solves the biggest recognition issue with minimal layout churn.

### Phase 2: Exception-aware agent rows

- Add row-level detection for `agent.working_dir != team.working_dir`
- Render compact directory chip for overrides
- Add full path tooltip or title attribute

### Phase 3: Adaptive directory subgrouping

- Add expanded-team grouping mode when multiple effective directories exist
- Group by directory only when override count crosses a threshold
- Preserve manual order semantics as much as possible

## Suggested Threshold for Grouping

Do not introduce directory subgrouping for one-off exceptions.

Initial heuristic:

- flat list if 0 or 1 agents differ from the team default
- flat list with exception chips if 2 agents differ but remain a minority
- grouped view if 3+ agents differ or if there are 2+ meaningful directory
  clusters

This threshold can be tuned after real usage.

## Success Criteria

- Users can identify the correct team by scanning `name + directory`
- Users can notice when an agent is operating outside the team default worktree
- Multiple teams can coexist in the sidebar without requiring path recall
- The common single-directory team remains compact and easy to scan
- Lower-priority metadata is available on hover without competing in the list

## Open Questions

- Should the header show full compact path or only repo/worktree name by
  default?
- Should grouped-by-directory view appear automatically or only when the team is
  expanded?
- Should directory chips use neutral styling or inherit repo/worktree color
  coding later?

## Decision

Adopt a layered directory model:

- team header shows the primary worktree
- agent rows show directory only when they differ from the team default
- directory subgrouping appears only when mixed-directory teams become dense
- agent type is removed from the primary row and deferred to hover/detail UI

This preserves fast recognition across multiple teams while still supporting
heterogeneous teams cleanly.
