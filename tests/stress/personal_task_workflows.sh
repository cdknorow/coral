#!/usr/bin/env bash
# Sourced by run_agent_tasks.sh after both mock agents have finished solo tasks.
personal() { as_agent "$SESS_A" task "$@"; }
personal_id() { sed -n 's/^Created Task #\([0-9]*\):.*/\1/p'; }
personal_status() { personal detail "$1" | jget "d['status']"; }
personal_reject() { if "$@" >"$TMPDIR_AT/personal-rejection.log" 2>&1; then return 1; fi; }

log "Personal workflows: dependency/artifact parity through coral-agent..."
PB=$(personal add "Personal build" --outputs build --workflow delivery --stage Build --workflow-instructions "Pin the candidate revision." | personal_id)
PT=$(personal add "Personal test" --outputs report --blocked-by "[{\"task_id\":$PB,\"required_artifacts\":[\"build\"]}]" | personal_id)
PR=$(personal add "Personal release" --blocked-by "[$PB,$PT]" | personal_id)
check "personal downstream stages start blocked" '[[ $(personal_status "$PT") == blocked && $(personal_status "$PR") == blocked ]]'
check "blocked personal task cannot be claimed" 'personal_reject personal claim "$PT"'
check "other agent cannot read personal workflow" 'personal_reject as_agent "$SESS_B" task detail "$PB"'
check "other agent cannot depend on personal workflow" 'personal_reject as_agent "$SESS_B" task add "Cross-session dependency" --blocked-by "[$PB]"'
OUT=$(personal claim "$PB")
check "personal claim includes default and custom instructions" 'echo "$OUT" | grep -q "retry_of" && echo "$OUT" | grep -q "Pin the candidate revision"'
check "required personal output enforced" 'personal_reject personal complete "$PB"'
cat >"$TMPDIR_AT/personal-build.json" <<'JSON'
[{"name":"build","content":"Personal candidate","revision":"personal-rev-1"}]
JSON
personal complete "$PB" --artifacts "$TMPDIR_AT/personal-build.json" >/dev/null
check "personal completion immediately unlocks dependent" '[[ $(personal_status "$PT") == pending && $(personal_status "$PR") == blocked ]]'
check "unblocked personal task sends a terminal notification" 'wait_for_input "$NAME_A" "$SID_A" "[mock-agent] input: You have a new task in Coral (#$PT: Personal test)"'
OUT=$(personal claim "$PT")
check "personal task consumes exact upstream revision" 'echo "$OUT" | grep -q "personal-rev-1"'
cat >"$TMPDIR_AT/personal-report.json" <<'JSON'
[{"name":"report","content":"Candidate passed","revision":"personal-rev-1"}]
JSON
personal complete "$PT" --artifacts "$TMPDIR_AT/personal-report.json" >/dev/null
OUT=$(personal claim "$PR")
check "personal release consumes both dependency results" '[[ $(personal detail "$PR" | jget "len(d[\"workflow\"][\"inputs\"])") == 2 ]]'
personal complete "$PR" >/dev/null
check "personal results are immutable" 'personal_reject personal complete "$PB" --outcome failed'

PF=$(personal add "Personal failure" | personal_id)
PC=$(personal add "Personal success consumer" --blocked-by "[$PF]" | personal_id)
PD=$(personal add "Personal diagnosis" --blocked-by "[{\"task_id\":$PF,\"condition\":\"failure\"}]" | personal_id)
personal claim "$PF" >/dev/null
personal complete "$PF" --outcome failed >/dev/null
check "personal failure selects failure branch" '[[ $(personal_status "$PC") == blocked && $(personal_status "$PD") == pending ]]'
PX=$(personal add "Personal retry" --retry-of "$PF" | personal_id)
personal claim "$PX" >/dev/null
personal complete "$PX" >/dev/null
check "personal retry automatically rewires the consumer" '[[ $(personal_status "$PC") == pending && $(personal detail "$PC" | jget "d[\"blocked_by\"][0][\"task_id\"]") == "$PX" ]]'
personal claim "$PC" >/dev/null
personal complete "$PC" >/dev/null
check "personal retry link persists" '[[ $(personal detail "$PX" | jget "d[\"workflow\"][\"retry_of\"]") == "$PF" ]]'
personal complete "$PD" >/dev/null
