#!/usr/bin/env bash
# Sourced by run_agent_tasks.sh: shared isolated server, two live mock agents,
# assertion helpers and cleanup. No model calls; drive their real CLI identities.

as_board() {
    local session="$1" role="$2"; shift 2
    env -u TMUX -u CORAL_PORT CORAL_URL="$BASE_URL" \
        CORAL_SESSION_NAME="$session" CORAL_SUBSCRIBER_ID="$role" \
        "$TMPDIR_AT/coral-board" "$@"
}
builder() { as_board "$SESS_A" Builder "$@"; }
tester() { as_board "$SESS_B" Tester "$@"; }
task_id() { sed -n 's/^Created Task #\([0-9]*\):.*/\1/p'; }
workflow_detail() { builder task detail "$1"; }
workflow_status() { workflow_detail "$1" | jget "d['status']"; }
wait_task_status() {
    local id="$1" wanted="$2" attempt
    for attempt in $(seq 1 40); do
        [[ $(workflow_status "$id") == "$wanted" ]] && return 0
        sleep 0.25
    done
    return 1
}
wait_board_message() {
    local needle="$1" attempt
    for attempt in $(seq 1 40); do
        if api GET "/api/board/workflow-integration/messages/all?limit=500" | grep -Fq "$needle"; then
            return 0
        fi
        sleep 0.25
    done
    return 1
}
reject_board() {
    # Keep expected errors visible in retained test data.
    if "$@" >"$TMPDIR_AT/last-rejection.log" 2>&1; then return 1; fi
}

log "Test 10: Build -> Test -> Release with live mock agents..."
builder join workflow-integration --as Builder >/dev/null
tester join workflow-integration --as Tester >/dev/null
BUILD=$(builder task add "Build candidate" --assignee Builder --workflow delivery --stage Build --outputs build --workflow-instructions "Use the exact candidate revision." | task_id)
TEST=$(builder task add "Test candidate" --assignee Tester --workflow delivery --stage Test --outputs report --blocked-by "[{\"task_id\":$BUILD,\"required_artifacts\":[\"build\"]}]" | task_id)
RELEASE=$(builder task add "Release candidate" --assignee Builder --workflow delivery --stage Release --outputs release --blocked-by "[{\"task_id\":$BUILD,\"required_artifacts\":[\"build\"]},{\"task_id\":$TEST,\"required_artifacts\":[\"report\"]}]" | task_id)
check "test and release start blocked" '[[ $(workflow_status "$TEST") == blocked && $(workflow_status "$RELEASE") == blocked ]]'
check "blocked task cannot be claimed explicitly" 'reject_board tester task claim "$TEST"'
check "another worker cannot claim the assigned build" 'reject_board tester task claim "$BUILD"'
OUT=$(builder task claim "$BUILD")
check "claim includes default and additional workflow instructions" 'echo "$OUT" | grep -q "Use the exact candidate revision" && echo "$OUT" | grep -q "retry_of"'
check "required build artifact enforced on completion" 'reject_board builder task complete "$BUILD"'
check "rejected completion leaves build in progress and test blocked" '[[ $(workflow_status "$BUILD") == in_progress && $(workflow_status "$TEST") == blocked ]]'
cat >"$TMPDIR_AT/build.json" <<'JSON'
[{"name":"build","uri":"artifact://candidate/app.tar.gz","revision":"candidate-abc123","digest":"sha256:mock-build"}]
JSON
builder task complete "$BUILD" --artifacts "$TMPDIR_AT/build.json" --message "Candidate ready" >/dev/null
check "build completion unlocks test but not release" 'wait_task_status "$TEST" pending && [[ $(workflow_status "$RELEASE") == blocked ]]'
check "dependency hand-off reaches tester terminal" 'wait_for_input "$NAME_B" "$SID_B" "[mock-agent] input: You have tasks available."'
OUT=$(tester task claim "$TEST")
check "tester claims exact build revision and artifact URI" 'echo "$OUT" | grep -q "candidate-abc123" && echo "$OUT" | grep -q "artifact://candidate/app.tar.gz"'
OUT=$(tester task current)
check "current retains the claimed input evidence" 'echo "$OUT" | grep -q "sha256:mock-build"'
cat >"$TMPDIR_AT/report.json" <<'JSON'
[{"name":"report","content":"All candidate checks passed","revision":"candidate-abc123"}]
JSON
tester task complete "$TEST" --artifacts "$TMPDIR_AT/report.json" >/dev/null
check "both dependencies unlock release" 'wait_task_status "$RELEASE" pending'
builder task claim "$RELEASE" >/dev/null
DETAIL=$(workflow_detail "$RELEASE")
check "release consumes both upstream results" '[[ $(echo "$DETAIL" | jget "len(d[\"workflow\"][\"inputs\"])") == 2 ]] && echo "$DETAIL" | grep -q "All candidate checks passed"'
cat >"$TMPDIR_AT/release.json" <<'JSON'
[{"name":"release","uri":"artifact://releases/v1","revision":"candidate-abc123"}]
JSON
builder task complete "$RELEASE" --artifacts "$TMPDIR_AT/release.json" >/dev/null
check "pipeline finished successfully" '[[ $(workflow_detail "$RELEASE" | jget "d[\"workflow\"][\"outcome\"]") == success ]]'
check "completed build evidence cannot be overwritten" 'reject_board builder task complete "$BUILD" --outcome failed'
check "original build revision remains intact" '[[ $(workflow_detail "$BUILD" | jget "d[\"workflow\"][\"artifacts\"][0][\"revision\"]") == candidate-abc123 ]]'

log "Test 11: Failure branches, fresh retries and cancellation..."
BROKEN=$(builder task add "Broken candidate" --assignee Builder --outputs build | task_id)
SUCCESSOR=$(builder task add "Consume candidate" --blocked-by "[$BROKEN]" | task_id)
RECOVERY=$(builder task add "Diagnose failure" --assignee Tester --blocked-by "[{\"task_id\":$BROKEN,\"condition\":\"failure\"}]" | task_id)
TERMINAL=$(builder task add "Clean candidate" --blocked-by "[{\"task_id\":$BROKEN,\"condition\":\"termination\"}]" | task_id)
builder task claim "$BROKEN" >/dev/null
builder task complete "$BROKEN" --outcome failed --message "Compiler rejected candidate" >/dev/null
check "failure unlocks recovery and cleanup, not success branch" 'wait_task_status "$RECOVERY" pending && wait_task_status "$TERMINAL" pending && [[ $(workflow_status "$SUCCESSOR") == blocked ]]'
tester task claim "$RECOVERY" >/dev/null
check "recovery receives failed upstream outcome" 'tester task current | grep -q '\''"outcome": "failed"'\'''
tester task complete "$RECOVERY" >/dev/null
RETRY=$(builder task add "Rebuild candidate" --assignee Builder --retry-of "$BROKEN" --outputs build | task_id)
builder task claim "$RETRY" >/dev/null
builder task complete "$RETRY" --artifacts "$TMPDIR_AT/build.json" >/dev/null
check "retry is a new task linked to original failure" '[[ "$RETRY" != "$BROKEN" && $(workflow_detail "$RETRY" | jget "d[\"workflow\"][\"retry_of\"]") == "$BROKEN" ]]'
check "retry success does not silently replace failed input" '[[ $(workflow_status "$SUCCESSOR") == blocked ]]'
api PATCH "/api/board/workflow-integration/tasks/$SUCCESSOR" -d "{\"blocked_by\":[$RETRY]}" >/dev/null
check "explicit dependency rewire unlocks retry consumer" 'wait_task_status "$SUCCESSOR" pending'
CANCELLED_BUILD=$(builder task add "Cancelled candidate" | task_id)
CANCEL_SUCCESS=$(builder task add "Do not ship cancellation" --blocked-by "[$CANCELLED_BUILD]" | task_id)
CANCEL_CLEANUP=$(builder task add "Clean cancelled candidate" --blocked-by "[{\"task_id\":$CANCELLED_BUILD,\"condition\":\"termination\"}]" | task_id)
builder task cancel "$CANCELLED_BUILD" >/dev/null
check "cancellation unlocks cleanup but never success" 'wait_task_status "$CANCEL_CLEANUP" pending && [[ $(workflow_status "$CANCEL_SUCCESS") == blocked ]]'
check "cancellation notifies orchestrator about stalled success consumer" 'wait_board_message "[Task #$CANCEL_SUCCESS stalled]"'

log "Test 12: Concurrent completions preserve one immutable result..."
RACE=$(builder task add "Concurrent artifact submissions" --assignee Builder --outputs build | task_id)
builder task claim "$RACE" >/dev/null
mkdir -p "$TMPDIR_AT/completions"
COMPLETION_PIDS=()
for attempt in $(seq 1 10); do
    echo "[{\"name\":\"build\",\"content\":\"candidate-$attempt\",\"revision\":\"revision-$attempt\"}]" >"$TMPDIR_AT/completions/$attempt.json"
    (
        set +e
        builder task complete "$RACE" --artifacts "$TMPDIR_AT/completions/$attempt.json" >"$TMPDIR_AT/completions/$attempt.out" 2>&1
        echo $? >"$TMPDIR_AT/completions/$attempt.code"
    ) &
    COMPLETION_PIDS+=($!)
done
wait "${COMPLETION_PIDS[@]}"
WINNERS=$(grep -l '^0$' "$TMPDIR_AT"/completions/*.code || true)
check "exactly one concurrent completion succeeds" '[[ $(echo "$WINNERS" | grep -c .) == 1 ]]'
if [[ $(echo "$WINNERS" | grep -c .) == 1 ]]; then
    WINNER=$(basename "$WINNERS" .code)
    DETAIL=$(workflow_detail "$RACE")
    check "stored content and revision belong to the winning submission" '[[ $(echo "$DETAIL" | jget "d[\"workflow\"][\"artifacts\"][0][\"content\"]") == "candidate-$WINNER" && $(echo "$DETAIL" | jget "d[\"workflow\"][\"artifacts\"][0][\"revision\"]") == "revision-$WINNER" ]]'
fi

log "Test 13: Dependency output contracts reject impossible handoffs..."
CONTRACT_PRODUCER=$(builder task add "Contract producer" --assignee Builder --outputs diagnostic,verification | task_id)
check "mismatched dependency contract is rejected" 'reject_board builder task add "Impossible consumer" --blocked-by "[{\"task_id\":'$CONTRACT_PRODUCER',\"required_artifacts\":[\"candidate\",\"verification\"]}]"'
check "contract rejection names the undeclared artifact" 'grep -q "cannot require artifact.*candidate" "$TMPDIR_AT/last-rejection.log"'
CONTRACT_BUILD=$(builder task add "Declared candidate producer" --assignee Builder --outputs candidate,verification | task_id)
CONTRACT_TEST=$(builder task add "Valid contract consumer" --assignee Tester --blocked-by "[{\"task_id\":$CONTRACT_BUILD,\"required_artifacts\":[\"candidate\",\"verification\"]}]" | task_id)
check "declared dependency contract is accepted and starts blocked" '[[ $(workflow_status "$CONTRACT_TEST") == blocked ]]'
builder task claim "$CONTRACT_BUILD" >/dev/null
cat >"$TMPDIR_AT/contract-build.json" <<'JSON'
[{"name":"candidate","uri":"artifact://candidate/contract.tar.gz","revision":"contract-abc123"},{"name":"verification","content":"Contract checks passed","revision":"contract-abc123"}]
JSON
builder task complete "$CONTRACT_BUILD" --artifacts "$TMPDIR_AT/contract-build.json" --message "Candidate and verification handoff" >/dev/null
check "valid declared contract unlocks after matching artifacts" 'wait_task_status "$CONTRACT_TEST" pending'
