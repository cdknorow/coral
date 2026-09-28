# Recover a task awaiting completion review

A completion candidate is evidence, not an accepted task result. If completion
needs review, preserve the candidate explicitly:

```sh
coral-board task submit-review TASK_ID --reason "Completion needs review" \
  --message "Candidate built; see verification evidence" --outcome success \
  --artifacts candidate-manifest.json
```

The current owner or an active registered orchestrator can submit this once for
an in-progress task. Use named artifacts with inline content or durable URIs and
exact revisions/digests. The candidate is immutable and appears in task detail
and the task list as **Review requested**. The worker slot remains occupied.

An active registered orchestrator can then release capacity independently:

```sh
coral-board task release-review TASK_ID --reason "Independent work can continue while completion is reviewed"
```

The task becomes **Review pending**, keeps its owner, claim history, candidate,
and release audit, and stops occupying the worker slot. Independent tasks are
claimable. Success, failure, and termination dependencies remain unsatisfied;
release does not fire completion waits or record an outcome. Queued work can
notify the worker again.

After actual review, the orchestrator uses the ordinary `task complete` command
with the accepted outcome, message, and artifacts. Required outputs and
prerequisites still apply. A failed or cancelled result does not satisfy success
dependencies. The original candidate remains visible alongside the final result.
There is no automatic acceptance, inferred QA approval, or retry of rejected
commands through another endpoint.

If an external safety review prevents a command from executing, Coral cannot
know that rejection occurred or recover evidence it never received. These
commands must themselves be permitted and reach Coral. A reported rejection is
recorded as the submitter's reason, not as a verified external review decision.
An independently passed QA result can be included as evidence; it never silently
completes the implementation task.

The recovery commands apply to team board tasks. Personal tasks use the same
lifecycle engine and preserve review state in their dashboard projection; they
do not currently expose these board recovery commands through `coral-agent`.
Reviewer checks follow the local board API's subscriber identity model: an active
registered Orchestrator role or `can_peek` privilege is required. This is not a
new network authentication mechanism.
