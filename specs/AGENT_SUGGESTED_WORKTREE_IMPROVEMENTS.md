# Agent Suggested Worktree Improvements

Status: partially implemented in the working tree; not committed or deployed. This document records shipped guidance/amendment behavior and deferred proposals. September 30, 2026.

## Purpose and incident boundary

The death-or-trade-ai-auto handoff exposed coordination failures around corrections,
worktrees, stale task instructions, acceptance evidence, and orchestrator bookkeeping.
This document specifies a small first increment and a later harness direction. It
does not change product behavior, board settings, notification policy, or existing
claimed tasks.

Evidence currently available:

- `internal/agent/agent.go:440-489` now has canonical orchestrator-centered
  guidance: ordinary corrections retain owner/worktree, handoffs include evidence,
  authoritative amendments are reread, smaller slices are not completion, and
  success requires current evidence. It still does not define machine-readable
  scope revisions, integration ownership, or status vocabulary.
- `internal/board/task_workflow.go:12-47,121-177` already persists task
  instructions, required outputs, artifacts, outcomes, retry lineage, and dependency
  inputs. `retry_of` rewires unstarted success dependents, but is attempt lineage,
  not scope supersession.
- `internal/board/completion_review.go:41-120` and
  `agent_docs/task-workflows.md:143-173` provide immutable completion candidates
  and `review_pending`; terminal results cannot be reopened.
- `agent_docs/task-workflows.md:203-224` documents Build/Test/Release as an
  example. The incident board is an exception with an explicit local contract:
  `death-or-trade-ai-auto` is `worktrees`, dependency guidance is enabled, and
  its custom instructions require separate dependent Build, Test, and Release
  tasks, named artifacts, exact revisions, scoped checks, queue rewiring for stale
  or canceled dependencies, and truthful failed verification. Preserve those
  settings unless the operator deliberately changes them.
- The current task board `coral-task-workflows` is different: mode `none`,
  dependency guidance disabled, with only a queued-assignment custom rule. Do not
  infer one board's workflow contract from the other.
- `internal/server/routes/system.go:145-157` serves the Go prompt constants.
  `internal/server/frontend/static/modals.js:3490-3503` contains a shorter
  fallback path used if the defaults endpoint fails; it now reports unavailable
  defaults instead of displaying or persisting stale guidance.

Observed incident claims should be treated as evidence to verify, not as automatic
facts: correction tasks and branches multiplied; message updates left stale
instructions; handoffs lacked an actionable owner/decision; mutable worktrees were
used as acceptance assets; cherry-picked corrections omitted prerequisites; test
registration was mistaken for browser execution; rigid numerical gates rejected
intended improvements; and the orchestrator guessed IDs or repeated reminders.
This specification does not claim that each failure is caused by one current code
path.

## Goals

1. Make the orchestrator the clear coordination owner while allowing explicit
   operator or team exceptions.
2. Keep ordinary corrections with the existing owner, branch/worktree, task, and
   prerequisite chain.
3. Make a material scope, owner, prerequisite, or acceptance-input change explicit
   and traceable.
4. Distinguish implemented, verified, merged, and deployed claims with useful
   revision and artifact evidence.
5. Ensure acceptance refers to an immutable runtime and served asset/workdir, not
   merely a binary or mutable checkout.
6. Separate an executed test from a registered test, and evaluate evidence in context.
7. Preserve notification waits, no-poll behavior, task snapshots, immutable terminal
   history, and actual tool/sandbox/secret constraints.

## Non-goals

- Do not force every task into Build/Test/Release. On death-or-trade-ai-auto those
  stages are already required by explicit board instructions; on other boards they
  remain opt-in.
- Do not add universal acknowledgement loops, empty “ack” posts, numerical quality
  gates, or automatic integration heuristics.
- Do not reopen terminal tasks, mutate old claimed snapshots, silently reassign work,
  or infer that a new task supersedes an old one.
- Do not make Coral fetch arbitrary URLs, expose secrets, bypass sandbox/approval
  denials, or claim that a reported artifact was independently verified.
- Do not modify notification-policy edits or unrelated uncommitted activity fixes.

## Canonical prompt contract (implemented in working tree)

The Go constants in `internal/agent/agent.go` are authoritative and are served by
`internal/server/routes/system.go`. The UI has no second abbreviated prompt copy;
if the defaults endpoint fails it reports “defaults unavailable” rather than
showing or saving stale text. Explicit per-team prompt overrides still replace the
defaults, and already claimed tasks retain their frozen instructions.

The orchestrator default now says to reread authoritative task detail and linked
artifacts before correcting, reassigning, or reminding; keep ordinary corrections
with the current owner/worktree; route shared handoffs and integration through the
orchestrator unless an explicit exception is made; create linked work only for a
material scope/owner/prerequisite/acceptance change; and report task ID, owner,
integration owner, status, tested revision, and durable artifact identity. A task
notice, registered test, or mutable checkout is not acceptance evidence.

The shared task guidance now says workers retain ordinary ownership, report shared
handoffs to the orchestrator by default, acknowledge actionable handoffs through
resulting action/evidence rather than empty messages, carry the assigned outcome
across turns, treat smaller slices as progress rather than completion, follow the
latest explicit amendment, reread current evidence, continue the next safe action,
and report limitations/failure honestly. A wait requires a specific current
condition and next action; an observation timeout does not prove work stopped and
does not justify a duplicate restart. No goal engine, token budget, turn threshold,
polling loop, or mandatory per-turn audit was added.

The orchestrator action prompt no longer says to stop after every progress post.
It preserves reactive no-poll reads and waiting for named dependencies. Focused
prompt/composition tests cover these defaults; `agent_docs/task-workflows.md` and
its frontend mirror contain the same outcome/evidence contract.

## Worktree and integration contract

For death-or-trade-ai-auto, the existing `worktrees` preset and mandatory staged
instructions remain authoritative:

- Build owns the isolated branch/worktree and publishes the exact revision plus
  named build artifact.
- Test consumes that exact immutable input and reports executed commands, exit
  status, tested revision, and test artifact. A browser assertion must identify the
  served assets/workdir or runtime identity, not just a binary.
- Release consumes the named Build and Test outputs, checks revision identity, and
  records deployment/release evidence.
- Ordinary corrections stay on the same owner and worktree until accepted. A
  different integration owner is an explicit exception with a linked dependency and
  complete prerequisite inputs.
- A cherry-pick or merge is acceptable only when the correction's prerequisite
  chain and tested revision are represented in the handoff; a commit hash alone is
  insufficient.

Other boards may use shared checkout or no workflow preset. The prompt must describe
the board's effective mode, not silently impose the incident board's stages.

## Versioned amendment implementation (implemented in working tree)

#1668/#1669 implemented and #1670 independently verified a bounded v1. The
implementation adds `board_tasks.revision` and append-only `task_amendments` history
with actor, reason, changes, prior/effective snapshots, and compare-and-swap
`base_revision`. Only body and task-specific workflow instructions are amendable.
Planner authorization is required; unsupported dependency, required-output, owner,
and working-mode fields are rejected. Effective body/instructions and amendment
history appear in claim/current/detail while plain GET remains read-only.

Amendments are allowed for draft/pending/blocked/in-progress tasks and rejected for
review_pending/completed/skipped. Frozen first-claim team mode and original task
instructions remain intact. Completion and submit-review require the expected
revision for amended tasks and reject stale work with HTTP 409. Legacy PATCH body
edits are rejected in favor of `/amend`; UI 409 responses preserve completion drafts.
Claim or another revision-bearing action can record the observed revision; reading
detail/current alone does not claim acknowledgement. No generic ack message or new
lifecycle state exists. Existing `retry_of` and `review_pending` semantics remain.

## Deferred harness concepts

These remain proposals, not shipped behavior, and are deliberately additive rather
than a new lifecycle taxonomy.

1. **Broader assignment supersession.** Extend the v1 amendment history to cover
   explicit dependency/output/owner/acceptance-input changes only after a migration
   and UI/API decision. V1 does not heuristically classify text or require a linked
   task for ordinary body/instruction clarifications.
2. **Authoritative handoff identity.** Add a compact handoff record containing source
   task, owner, integration owner when different, decision needed, source revision,
   and artifact manifest. “Acknowledged” means a state transition or useful evidence
   from the owner, not a required acknowledgement message. Expiration should surface
   stale handoffs rather than create reminder loops.
3. **Evidence status fields.** Keep terminal `success/failed/cancelled` and
   `review_pending`; add optional evidence metadata for implemented, verified,
   merged, and deployed claims. Require revision identity for verified/merged/deployed
   claims instead of multiplying terminal states.
4. **Immutable acceptance runtime.** Extend artifact metadata with runtime/session
   identity, served asset/workdir identity, and command/browser evidence when relevant.
   Store a digest and revision; Coral may validate shape and linkage but must not
   pretend to execute arbitrary external assets.
5. **Executed-test declaration.** Add a small result field distinguishing
   `registered`, `executed_pass`, `executed_fail`, and `not_run`, with command,
   timestamp, revision, and artifact/log reference. Acceptance remains contextual,
   not a rigid count gate.

Compatibility: new fields are optional for old tasks; old task snapshots continue to
render and complete under their stored instructions, including revision 1 tasks.
A migration must never rewrite historical instructions or promote a mutable worktree into an immutable artifact.

## Phased delivery

### Phase 1: prompt consistency and integration ownership (implemented; #1665/#1666)

- Make Go prompts authoritative and remove the verbose frontend fallback. **Done.**
- Add orchestrator/worker outcome, evidence, correction, and integration guidance. **Done.**
- Add prompt-inspection and focused prompt tests. **Done.**
- Document the death-or-trade-ai-auto override and coral-task-workflows distinction. **Done.**
- Acceptance: routine correction stays on one task/worktree; a material correction
  names its linked task and prerequisite; no empty ack loop or mandatory extra stage
  appears on a mode-none board.

### Phase 2: assignment amendments and handoffs (bounded v1 implemented; #1667/#1668/#1669/#1670)

- Add versioned body/instruction amendment history, CAS, effective detail, notices, and stale completion/review guards. **Done.**
- Dependency/output/owner amendments, broad supersession, and automatic dependent rewiring remain deferred.
- Preserve Operator/orchestrator-only planning authorization and frozen snapshots. **Done.**
- Acceptance: old task remains auditable, the new scope has one authoritative owner,
  and dependents consume the replacement only when required inputs are present.

### Phase 3: immutable acceptance evidence (not implemented)

- Add runtime/served-asset identity and executed-test metadata to artifact manifests.
- Add reviewer views that show tested revision, runtime identity, command/result, and
  artifact digest together.
- Acceptance: a mutable checkout, test registration, or binary-only claim cannot
  satisfy a staged release review.

## Acceptance scenarios

1. **Ordinary correction:** Build owner receives a review fix; same task, branch,
   worktree, and dependencies remain. One useful evidence post is enough.
2. **Material scope change:** API contract changes; orchestrator records amendment,
   creates the smallest linked task, rewires only affected dependents, and preserves
   the old candidate.
3. **Stale update:** A worker receives a changed message; task detail and upstream
   artifact reread reveal the authoritative revision before work continues.
4. **Death-or-trade-ai-auto staged handoff:** Build → Test → Release uses named
   artifacts and exact revisions; a canceled/stale prerequisite is rewired rather
   than bypassed.
5. **Acceptance runtime:** Browser assertions identify served assets/workdir and
   runtime/session identity; a binary with no matching served asset is insufficient.
6. **Evidence honesty:** A registered but unexecuted test reports `not_run`; a
   failed scoped command reports failure and remains actionable.
7. **Notification discipline:** A worker posts a useful handoff and waits for a
   named dependency; no polling loop, universal ack, or reminder storm is required.

## Evidence and status boundary

- **Implemented in this working tree:** canonical prompts/docs (#1665), versioned
  body/instruction amendments and UI contract (#1668/#1669), and outcome-fidelity
  guidance (#1684). Focused Go/server/CLI/UI checks passed for those changes.
- **Independently verified:** #1666 passed the first prompt phase and #1670 passed
  amendment behavior. #1684 has focused tests but no separate independent review
  recorded here.
- **Not implemented:** dependency/output/owner amendment semantics, structured
  handoff records, runtime/served-asset manifests, executed-test attestations,
  branch-to-main audits, or new status/lifecycle states.
- **Not committed, merged, or deployed:** all working-tree changes remain subject
  to normal review and release workflow. Existing notification/activity repairs and
  superseded policy edits are outside this specification.

Task evidence references: #1665 first guidance implementation
(`coral://artifacts/14656a0929055d13b8b27c12bbc4d68c833be0f2db4eaa93e8264f0731ca612e`),
#1666 independent prompt verification (PASS), #1667 bounded amendment design
(`coral://artifacts/a04aaa5e2064d6de50cfa6af1ec17d1f88774ad9060be0d8ed739c0eb40be96e`),
#1668 backend implementation
(`coral://artifacts/4f30d88e68bc262d9616aa405a88b001787c4272616d5451c19d85eb789b1cd3`),
#1669 UI implementation
(`coral://artifacts/f8b27cf1e64db0daa9a35ce98452e7e24ef3ab33988f01639031ca408580c607`),
#1670 independent amendment verification (PASS), and #1684 outcome guidance
(`coral://artifacts/43f55574c180c40e0bd3f7c299b82666dad256d04c2a3a56df47a70ad2922d06`).
The independent verification artifact for #1666 is recorded on the board; this
spec does not duplicate a URI that was not returned in the task result.

## Risks and open decisions

- Amendment fields improve lineage but add UI/API complexity and migration surface.
  Decide whether amendments are separate task records or immutable revisions first.
- Runtime identity can be difficult across local browsers and remote sessions. Define
  the minimum stable identity before enforcing it.
- Automated revision checks reduce cherry-pick mistakes but cannot prove semantic
  equivalence. Keep reviewer judgment and operator approval for release decisions.
- The orchestrator-centered contract must not suppress legitimate direct operator,
  reviewer, or emergency integration paths. Define explicit exception roles.
- If a generated build-time prompt copy is considered later, it must be checked
  against the Go source and preserve the current “defaults unavailable” fail-closed
  behavior when freshness cannot be established.
- Existing claimed tasks retain old prompts and team-mode snapshots. Rollout must
  apply new wording to newly created/claimed work only, with a visible compatibility
  note rather than pretending old assignments were repaired.

## Source references

- `coral-go/internal/agent/agent.go:440-570`
- `coral-go/internal/board/amendments.go` and `coral-go/internal/board/task_workflow.go:12-177,377-470`
- `coral-go/internal/board/completion_review.go:41-120`
- `coral-go/internal/board/working_mode.go:12-77`
- `coral-go/internal/server/routes/system.go:145-157`
- `coral-go/internal/server/frontend/static/modals.js:3486-3514`
- `coral-go/internal/server/frontend/static/team_working_mode.js:42-60,120-130`
- `coral-go/agent_docs/task-workflows.md:1-18,80-100,143-173,203-224,315-365`
