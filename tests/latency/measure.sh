#!/usr/bin/env bash
# measure.sh  BASEURL  TARGETS_JSON  LABEL  N  OUTDIR
# Sends a short warmup + N sequential requests per hot endpoint, records
# per-request time_total (seconds), and writes p50/p95/p99/max to OUTDIR/<label>.csv.
# Requests carry the same-origin dashboard headers the real frontend sends.
set -u

BASE="$1"; TARGETS="$2"; LABEL="$3"; N="${4:-100}"; OUT="${5:-.}"
WARM=5
mkdir -p "$OUT"
CSV="$OUT/$LABEL.csv"
echo "endpoint,n,p50_ms,p95_ms,p99_ms,max_ms,errors" > "$CSV"

j() { python3 -c "import json,sys;print(json.load(open('$TARGETS'))$1)"; }

BOARD_ID=$(j "['board_id']")
CHAT_NAME=$(j "['chat']['name']"); CHAT_ID=$(j "['chat']['id']"); CHAT_WD=$(j "['chat']['working_dir']")
POLL_NAME=$(j "['poll']['name']"); POLL_ID=$(j "['poll']['id']")
FILES_PATH=$(j "['files_path']")

HOST=$(echo "$BASE" | sed -E 's#^https?://##')
HDR=(-H "Origin: $BASE" -H "Referer: $BASE/" -H "Sec-Fetch-Site: same-origin")

# run_ep NAME  curl-args...
run_ep() {
  local name="$1"; shift
  local times=() codes=() errors=0
  # warmup
  for ((i=0;i<WARM;i++)); do curl -s -o /dev/null "${HDR[@]}" "$@" >/dev/null 2>&1; done
  for ((i=0;i<N;i++)); do
    read -r tt code < <(curl -s -o /dev/null -w '%{time_total} %{http_code}' "${HDR[@]}" "$@")
    times+=("$tt"); codes+=("$code")
    [[ "$code" =~ ^2|^3 ]] || errors=$((errors+1))
  done
  # percentiles via python (ms)
  python3 - "$name" "$errors" "$CSV" "${times[@]}" <<'PY'
import sys
name=sys.argv[1]; errors=sys.argv[2]; csv=sys.argv[3]
xs=sorted(float(x)*1000.0 for x in sys.argv[4:])
n=len(xs)
def pct(p):
    if n==0: return 0.0
    import math
    k=min(n-1, max(0, math.ceil(p/100.0*n)-1))
    return xs[k]
row=f"{name},{n},{pct(50):.1f},{pct(95):.1f},{pct(99):.1f},{xs[-1]:.1f},{errors}"
print(row)
open(csv,'a').write(row+"\n")
PY
}

run_ep team_availability -G "$BASE/api/teams/detail/$BOARD_ID/availability"
run_ep board_status      -G "$BASE/api/board/$BOARD_ID/status"
run_ep sleep_status      -G "$BASE/api/sessions/live/team/$BOARD_ID/sleep-status"
run_ep chat              -G "$BASE/api/sessions/live/$CHAT_NAME/chat" \
                            --data-urlencode "agent_type=claude" \
                            --data-urlencode "session_id=$CHAT_ID" \
                            --data-urlencode "working_directory=$CHAT_WD" \
                            --data-urlencode "after=0" --data-urlencode "limit=100"
run_ep poll              -G "$BASE/api/sessions/live/$POLL_NAME/poll" \
                            --data-urlencode "agent_type=claude" \
                            --data-urlencode "session_id=$POLL_ID"
run_ep files             -G "$BASE/api/filesystem/list" --data-urlencode "path=$FILES_PATH"

echo "[measure] $LABEL done -> $CSV"
column -t -s, "$CSV"
