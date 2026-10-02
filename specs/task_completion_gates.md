# Task completion gates

Status: experimental completion-gate feature implemented but disabled by default (October 2026)

Coral tasks may declare evidence requirements chosen by the Operator or a
registered Orchestrator. Gates are optional; a task with no gates keeps the
legacy completion behavior. Coral checks the declaration at completion and
does not impose a universal merge-to-main rule.

## Contract

Gates are stored in `workflow.completion_gates` and are visible in task
detail, claim responses, the dashboard, and downstream task inputs. The
supported first version is deliberately small:

```json
[
  {"type":"report", "name":"QA report", "artifact":"report"},
  {"type":"registered_check", "name":"tests", "check_id":"go_test", "parameters":{"packages":"./..."}},
  {"type":"registered_check", "name":"landing", "check_id":"git_ancestry", "parameters":{"remote":"origin","branch":"release/candidate"}}
]
```

Every enabled listed gate must pass for a successful completion. A report gate checks
that the named artifact exists. Test evidence must identify `kind: "test"`,
the executed `command`, `runner`, RFC3339 `started_at` and `finished_at`,
`exit_code: 0`, and an `output_digest`. It must also carry the exact
`candidate_revision` supplied at completion. Exit code alone is insufficient
evidence that tests ran. A registered `go_test` check executes the fixed `go
test` command in the trusted server check workdir, requiring repository `HEAD`
to exactly match the candidate revision. A registered `git_ancestry` check
resolves the candidate commit and runs `git merge-base --is-ancestor` against
the configured remote-tracking branch. These checks produce server-owned
command, exit code, log digest, observed revision, and timestamps; completion
JSON cannot submit a passing result. The old `landed_revision` schema remains
available only as a declared attestation for compatibility and must not be
described as verified landing.

Completion supplies the candidate identity separately from the task revision:

```json
{
  "candidate_revision": "c98e626",
  "artifacts": [
    {"name":"tests", "kind":"test", "revision":"c98e626",
     "command":"go test ./...", "runner":"ci/linux",
     "exit_code":0, "output_digest":"sha256:...",
     "started_at":"2026-10-02T18:00:00Z",
     "finished_at":"2026-10-02T18:02:00Z", "content":"..."}
  ]
}
```

Failed completion may record diagnostic evidence and the true failed outcome;
it does not fabricate a successful result. A gate failure rejects a successful
completion before the task is made terminal, preserving the work for retry or
correction. Accepted completion stores bounded gate results, candidate
revision, artifacts, and timestamps in the task workflow. Review submission
and final review acceptance use the same completion checks, so a candidate
cannot bypass a gate through the review route.

All completion gates are experimental and dormant by default. New declarations
of any gate type are rejected while disabled with an explicit server-policy
error. Declarations already stored during an opt-in rollout are preserved;
completion and review record a visible `completion gate disabled by server
policy` result without checking artifacts, running commands, or blocking
ordinary completion. Required task outputs remain enforced independently.
The server-only opt-in is `CORAL_ENABLE_COMPLETION_CHECKS=true`; it is not a
caller-controlled request field and must not be enabled in production or
release CI until the remaining isolation work is reviewed.

Requirements are versioned with the existing task amendment revision. An
authorized `task amend` may replace `completion_gates` using the expected
revision; stale amendments are rejected. Changing the gates clears prior gate
results and candidate identity, and completion/review must use the new task
revision. Terminal tasks remain immutable.

## Safety and boundaries

This slice does not execute arbitrary commands from task bodies. Only the
reviewed `go_test` and `git_ancestry` IDs are registered. The local runner uses
a server-configured `CORAL_CHECK_WORKDIR`, a bounded timeout, a minimal
environment with provider secrets removed, `GOPROXY=off`, and bounded output.
Missing workdir/runner, timeout, failed tests, unmerged commits, or unsupported
check IDs fail explicitly. Network isolation and a separate OS sandbox are
deployment requirements for production; the implementation surfaces that
platform limitation rather than pretending a local process is a complete
isolation boundary. Squashed merges do not satisfy commit ancestry; use a
future trusted CI check that records squash mapping rather than asserting
`landed:true`.

One active task per agent remains in force. There is no waiting/suspended
capacity-release state and no automatic preemption or agent spawning in this
feature. Tasks without gates remain compatible with legacy clients.

## CLI examples

```sh
coral-board task add "Verify candidate" --completion-gates \
  '[{"type":"registered_check","check_id":"go_test","parameters":{"packages":"./..."}}]'
coral-board task complete 42 --candidate-revision c98e626 --artifacts evidence.json \
  --message "Tests executed against the submitted candidate"
coral-board task amend 42 --revision 1 --reason "Require landing evidence" \
  --completion-gates '[{"type":"registered_check","check_id":"git_ancestry","parameters":{"remote":"origin","branch":"release/candidate"}}]'
```

`coral-agent task add` and `coral-agent task complete` accept the same gate
and candidate-revision fields for personal tasks, which use the same workflow
engine and preserve the same safety rules. All completion gates remain disabled
unless the server explicitly opts in with the flag above.
