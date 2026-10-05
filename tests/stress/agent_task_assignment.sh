#!/usr/bin/env bash
# Sourced by run_agent_tasks.sh after agent_task_workflows.sh: explicit task
# assignment is exclusive. Uses a fresh board, the real coral-board CLI and the
# two live mock agents' terminals. Needs: as_board, api, jget, capture, check,
# log, wait_for_input, task_id and the SESS_/SID_/NAME_ variables of A and B.
#
# Mock agent A's terminal is the Orchestrator's terminal for this board only
# (the shared planner identity has no terminal, so it cannot show a notice). The
# Builder/Tester board state of both mock sessions is restored at the end.

ASG_BOARD="assignment-isolation"
ASG_OTHER_BOARD="assignment-elsewhere"
ASG_RESTORE_BOARD="${ASSIGNMENT_RESTORE_BOARD:-claim-regressions}"

asg_orch()    { as_board "$SESS_A" Orchestrator "$@"; }                       # terminal: mock A
asg_tester()  { as_board "$SESS_B" Tester "$@"; }                             # terminal: mock B
asg_worker()  { local n="$1"; shift; as_board "assign-worker$n-$SID_A" "Worker$n" "$@"; }  # no terminal
asg_lead()    { as_board "assign-lead-$SID_A" Lead "$@"; }                    # second orchestrator

# Run a command, keep its output and exit code, never abort the suite.
asg_run() {
    set +e
    ASG_OUT=$("$@" 2>&1); ASG_CODE=$?
    set -e
}

# Everything an unauthorized attempt must leave alone.
asg_fp() {
    api GET "/api/board/$ASG_BOARD/tasks/$1" | jget "json.dumps({k: d.get(k) for k in ('status','assigned_to','completed_by','completed_at','claimed_at','completion_message','session_id','revision')}, sort_keys=True) + json.dumps((d.get('workflow') or {}).get('artifacts'), sort_keys=True)"
}
asg_status()   { api GET "/api/board/$ASG_BOARD/tasks/$1" | jget "d['status']"; }
asg_assignee() { api GET "/api/board/$ASG_BOARD/tasks/$1" | jget "d.get('assigned_to') or ''"; }
asg_completed_by() { api GET "/api/board/$ASG_BOARD/tasks/$1" | jget "d.get('completed_by') or ''"; }
asg_artifact_count() { api GET "/api/board/$ASG_BOARD/tasks/$1" | jget "len((d.get('workflow') or {}).get('artifacts') or [])"; }
# Number of board messages containing a fixed string.
asg_msgs() { api GET "/api/board/$ASG_BOARD/messages/all?limit=1000" | { grep -oF -- "$1" || true; } | wc -l | tr -d ' '; }
# Wait up to ~10s for a board message containing a fixed string on this board.
asg_wait_msg() {
    local i
    for i in $(seq 1 40); do
        if api GET "/api/board/$ASG_BOARD/messages/all?limit=1000" | grep -Fq -- "$1"; then return 0; fi
        sleep 0.25
    done
    return 1
}
# Number of times a fixed string appears in a mock agent's terminal.
asg_term_count() { capture "$1" "$2" | tr -d '\n' | { grep -oF -- "$3" || true; } | wc -l | tr -d ' '; }
asg_claim_prompt() { echo "[mock-agent] input: [Task #$1 available] Run 'coral-board task claim $1' to claim it when your current task is done."; }
# HTTP status of a completion attempt: subscriber outcome task-id
asg_http_complete() {
    curl -s -m 10 -o /dev/null -w '%{http_code}' -X POST "${BASE_URL}/api/board/$ASG_BOARD/tasks/$3/complete" \
        -H "Content-Type: application/json" -d "{\"subscriber_id\":\"$1\",\"outcome\":\"$2\",\"message\":\"unauthorized attempt\",\"artifacts\":[{\"name\":\"stolen\",\"content\":\"x\",\"revision\":\"r\"}]}"
}

log "Assignment isolation: fresh board, real CLI, owned mock terminals..."
cat >"$TMPDIR_AT/assign-artifacts.json" <<'JSON'
[{"name":"report","content":"unauthorized evidence","revision":"asg-1"}]
JSON
cat >"$TMPDIR_AT/assign-owner.json" <<'JSON'
[{"name":"plan","content":"owner evidence","revision":"asg-owner"}]
JSON

asg_orch join "$ASG_BOARD" --as Orchestrator >/dev/null
asg_tester join "$ASG_BOARD" --as Tester >/dev/null
for n in 1 2 3 4 5 6; do asg_worker "$n" join "$ASG_BOARD" --as Worker >/dev/null; done
asg_lead join "$ASG_BOARD" --as Orchestrator >/dev/null
# Registered orchestrators who must not be able to act: one who left, one on another board.
as_board "assign-retired-$SID_A" Retired join "$ASG_BOARD" --as Orchestrator >/dev/null
as_board "assign-retired-$SID_A" Retired leave >/dev/null
as_board "assign-elsewhere-$SID_A" Elsewhere join "$ASG_OTHER_BOARD" --as Orchestrator >/dev/null

# ── Pending task assigned to the Orchestrator ────────────────────────
OWN=$(asg_orch task add "Orchestrator-owned plan" --priority critical --assignee Orchestrator | task_id)
DECOY=$(asg_orch task add "Open low-priority decoy" --priority low | task_id)
check "owned and decoy tasks were created" '[[ -n "$OWN" && -n "$DECOY" && "$OWN" != "$DECOY" ]]'
check "assignee's terminal got this task's claim notice (exact id)" 'wait_for_input "$NAME_A" "$SID_A" "$(asg_claim_prompt "$OWN")"'
sleep 1.5
check "the other agent's terminal never mentions the owned task" '[[ $(asg_term_count "$NAME_B" "$SID_B" "Task #$OWN") -eq 0 ]]'
BEFORE=$(asg_fp "$OWN")
check "owned task starts pending and assigned to Orchestrator" '[[ "$(asg_status "$OWN")" == pending && "$(asg_assignee "$OWN")" == Orchestrator ]]' "$BEFORE"

asg_run asg_worker 1 task claim
check "worker FIFO claim skips the higher-priority owned task and takes the open one" '[[ $ASG_CODE -eq 0 ]] && echo "$ASG_OUT" | grep -q "Claimed Task #$DECOY" && ! echo "$ASG_OUT" | grep -q "Task #$OWN"' "$ASG_OUT"
asg_worker 1 task complete "$DECOY" >/dev/null
asg_run asg_worker 2 task claim
check "worker FIFO claim finds nothing once only the owned task is left" '[[ "$ASG_OUT" == "No available tasks" ]]' "$ASG_OUT"
asg_run asg_tester task claim "$OWN"
check "named claim by another worker is rejected" '[[ $ASG_CODE -ne 0 ]]' "$ASG_OUT"
asg_run asg_tester task claim
check "FIFO claim by the other live agent is not the owned task" '! echo "$ASG_OUT" | grep -q "Task #$OWN"' "$ASG_OUT"

mkdir -p "$TMPDIR_AT/assign-claims"
rm -f "$TMPDIR_AT"/assign-claims/*
ASG_PIDS=()
for n in 1 2 3 4 5 6; do
    ( set +e
      asg_worker "$n" task claim "$OWN" >"$TMPDIR_AT/assign-claims/$n.named" 2>&1; echo $? >"$TMPDIR_AT/assign-claims/$n.named.code"
      asg_worker "$n" task claim >"$TMPDIR_AT/assign-claims/$n.fifo" 2>&1 ) &
    ASG_PIDS+=($!)
done
wait "${ASG_PIDS[@]}" || true
check "concurrent named and FIFO claims never take the owned task" '! cat "$TMPDIR_AT"/assign-claims/*.named "$TMPDIR_AT"/assign-claims/*.fifo | grep -q "Claimed Task #$OWN" && ! grep -qx 0 "$TMPDIR_AT"/assign-claims/*.named.code'
check "concurrent claims changed nothing" '[[ "$(asg_fp "$OWN")" == "$BEFORE" ]]'

# ── Unauthorized completion of a pending owned task ──────────────────
MSGS_BEFORE=$(asg_msgs "Task #$OWN completed")
for outcome in success failed; do
    asg_run asg_worker 3 task complete "$OWN" --outcome "$outcome" --message "stolen" --artifacts "$TMPDIR_AT/assign-artifacts.json"
    check "pending: worker completion ($outcome) is rejected with the assignee named" '[[ $ASG_CODE -ne 0 ]] && echo "$ASG_OUT" | grep -q "assigned to Orchestrator"' "$ASG_OUT"
    for who in Stranger Retired Elsewhere worker3 orchestrator; do
        CODE=$(asg_http_complete "$who" "$outcome" "$OWN")
        check "pending: $who ($outcome) is refused over the API" '[[ "$CODE" =~ ^4 ]]' "HTTP $CODE"
    done
    check "pending: rejected $outcome attempts left status, assignee, completed_by and artifacts unchanged" '[[ "$(asg_fp "$OWN")" == "$BEFORE" && "$(asg_artifact_count "$OWN")" == 0 ]]'
done
check "pending: no completion notice was posted" '[[ "$(asg_msgs "Task #$OWN completed")" == "$MSGS_BEFORE" ]]'

# ── Unauthorized completion of the same task once it is in progress ──
asg_orch task claim "$OWN" >/dev/null
check "owner claimed it" '[[ "$(asg_status "$OWN")" == in_progress ]]'
INPROGRESS=$(asg_fp "$OWN")
for outcome in success failed; do
    asg_run asg_worker 4 task complete "$OWN" --outcome "$outcome" --artifacts "$TMPDIR_AT/assign-artifacts.json"
    check "in progress: worker completion ($outcome) is rejected" '[[ $ASG_CODE -ne 0 ]] && echo "$ASG_OUT" | grep -q "assigned to Orchestrator"' "$ASG_OUT"
    for who in Stranger Retired Elsewhere Tester; do
        CODE=$(asg_http_complete "$who" "$outcome" "$OWN")
        check "in progress: $who ($outcome) is refused over the API" '[[ "$CODE" =~ ^4 ]]' "HTTP $CODE"
    done
    check "in progress: rejected $outcome attempts changed nothing" '[[ "$(asg_fp "$OWN")" == "$INPROGRESS" && "$(asg_artifact_count "$OWN")" == 0 ]]'
done
check "in progress: no completion notice was posted" '[[ "$(asg_msgs "Task #$OWN completed")" == "$MSGS_BEFORE" ]]'
asg_orch task complete "$OWN" --artifacts "$TMPDIR_AT/assign-owner.json" --message "owner finished" >/dev/null
check "the assignee can complete its own task and keeps ownership" '[[ "$(asg_status "$OWN")" == completed && "$(asg_completed_by "$OWN")" == Orchestrator && "$(asg_assignee "$OWN")" == Orchestrator && "$(asg_artifact_count "$OWN")" == 1 ]]'
check "the legitimate completion posted exactly one notice" 'asg_wait_msg "[Task #$OWN completed by Orchestrator]" && [[ "$(asg_msgs "Task #$OWN completed by")" == "$((MSGS_BEFORE + 1))" ]]'

# ── Permitted completers of someone else's task ──────────────────────
for outcome in success failed; do
    PEND_OP=$(asg_orch task add "Operator closes pending ($outcome)" --assignee Tester | task_id)
    CODE=$(asg_http_complete Operator "$outcome" "$PEND_OP")
    check "Operator may complete a pending task assigned to Tester ($outcome)" '[[ "$CODE" == 200 && "$(asg_completed_by "$PEND_OP")" == Operator && "$(asg_assignee "$PEND_OP")" == Tester ]]' "HTTP $CODE"

    PEND_LEAD=$(asg_orch task add "Second orchestrator closes pending ($outcome)" --assignee Tester | task_id)
    asg_run asg_lead task complete "$PEND_LEAD" --outcome "$outcome" --message "orchestrator closed"
    check "a registered orchestrator may complete a pending task ($outcome)" '[[ $ASG_CODE -eq 0 && "$(asg_completed_by "$PEND_LEAD")" == Lead && "$(asg_assignee "$PEND_LEAD")" == Tester ]]' "$ASG_OUT"

    RUN_LEAD=$(asg_orch task add "Second orchestrator closes running ($outcome)" --assignee Tester | task_id)
    asg_tester task claim "$RUN_LEAD" >/dev/null
    asg_run asg_lead task complete "$RUN_LEAD" --outcome "$outcome" --message "orchestrator closed"
    check "a registered orchestrator may complete an in-progress task ($outcome)" '[[ $ASG_CODE -eq 0 && "$(asg_status "$RUN_LEAD")" == completed && "$(asg_assignee "$RUN_LEAD")" == Tester ]]' "$ASG_OUT"

    OWN_TESTER=$(asg_orch task add "Tester finishes its own ($outcome)" --assignee Tester | task_id)
    asg_tester task claim "$OWN_TESTER" >/dev/null
    asg_run asg_tester task complete "$OWN_TESTER" --outcome "$outcome" --artifacts "$TMPDIR_AT/assign-artifacts.json"
    check "the assignee may complete its own task ($outcome)" '[[ $ASG_CODE -eq 0 && "$(asg_completed_by "$OWN_TESTER")" == Tester && "$(asg_artifact_count "$OWN_TESTER")" == 1 ]]' "$ASG_OUT"
done

# ── CLI reassign: bare name is rejected, explicit forms work ─────────
RE=$(asg_orch task add "Reassignment subject" --assignee Worker1 | task_id)
RE_FP=$(asg_fp "$RE")
RE_MSGS=$(asg_msgs "Task #$RE reassigned")
asg_run asg_orch task reassign "$RE" Tester
check "bare 'reassign <id> <name>' fails and points at --to" '[[ $ASG_CODE -ne 0 ]] && echo "$ASG_OUT" | grep -q -- "--to"' "$ASG_OUT"
asg_run asg_orch task reassign "$RE" --assignee Tester
check "unknown reassign flag fails" '[[ $ASG_CODE -ne 0 ]]' "$ASG_OUT"
sleep 1
check "failed reassigns sent no API mutation (task, notices and terminals unchanged)" '[[ "$(asg_fp "$RE")" == "$RE_FP" && "$(asg_msgs "Task #$RE reassigned")" == "$RE_MSGS" && $(asg_term_count "$NAME_B" "$SID_B" "Task #$RE ") -eq 0 ]]'

asg_run asg_worker 2 task reassign "$RE" --to Worker2
check "a worker cannot reassign shared tasks" '[[ $ASG_CODE -ne 0 && "$(asg_fp "$RE")" == "$RE_FP" ]]' "$ASG_OUT"

asg_run asg_orch task reassign "$RE" --to Tester
check "explicit --to reassigns to Tester" '[[ $ASG_CODE -eq 0 && "$(asg_assignee "$RE")" == Tester && "$(asg_status "$RE")" == pending ]]' "$ASG_OUT"
check "only the new owner's terminal got a notice for this exact task" 'wait_for_input "$NAME_B" "$SID_B" "$(asg_claim_prompt "$RE")"'
sleep 1
check "the notice reached the new owner once and not the old session" '[[ $(asg_term_count "$NAME_B" "$SID_B" "[mock-agent] input: [Task #$RE available]") -eq 1 && $(asg_term_count "$NAME_A" "$SID_A" "Task #$RE ") -eq 0 ]]'
asg_run asg_worker 1 task claim "$RE"
check "the previous owner can no longer claim it" '[[ $ASG_CODE -ne 0 ]]' "$ASG_OUT"

asg_run asg_orch task reassign "$RE"
check "omitting --to unassigns explicitly and says so" '[[ $ASG_CODE -eq 0 && -z "$(asg_assignee "$RE")" ]] && echo "$ASG_OUT" | grep -q "now unassigned"' "$ASG_OUT"
check "unassigning announces the open task to everyone, by id" 'asg_wait_msg "[Task #$RE reassigned — now unassigned]"'

# ── Restore the shared mock sessions' board state ────────────────────
asg_orch leave >/dev/null || true
as_board "$SESS_A" Builder join "$ASG_RESTORE_BOARD" --as Builder >/dev/null || true
asg_tester join "$ASG_RESTORE_BOARD" --as Tester >/dev/null || true
log "Assignment isolation scenarios finished"
