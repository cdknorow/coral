#!/usr/bin/env bash
# operator_cli_smoke.sh BIN_DIR [PORT]: the person at a terminal uses coral-board against a real server.
# Starts BIN_DIR/coral on 127.0.0.1:PORT with a fresh data folder and no agent environment, then runs
# BIN_DIR/coral-board join, post and read as the signed Operator (the command-line token the server writes
# in the data folder), and fails unless all three work. v1.4.72 shipped a check that refused the Operator's
# own command line ('subscriber_id "operator" is reserved'); only the release smoke caught it. The gate runs
# this so that can't happen again. The server is stopped by its own PID, the folder removed by its path.
set -euo pipefail
bin=$(cd "${1:?usage: operator_cli_smoke.sh BIN_DIR [PORT]}" && pwd)
port=${2:-8467}
[ -x "$bin/coral" ] && [ -x "$bin/coral-board" ] || { echo "operator_cli_smoke: $bin needs coral and coral-board" >&2; exit 2; }
home=$(mktemp -d "${TMPDIR:-/tmp}/coral-opsmoke.XXXXXX")
pid=""
cleanup() {
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  rm -rf -- "$home"
}
trap cleanup EXIT
# The terms accepted for this throwaway server (the marker the server reads under $HOME/.coral).
mkdir -p "$home/.coral" && echo accepted > "$home/.coral/.eula-accepted"
# No agent variables (CORAL_SESSION_*, CORAL_SUBSCRIBER_ID, TMUX...): a clean environment, as a person's terminal.
clean=(env -i "PATH=/usr/bin:/bin:/usr/sbin:/sbin" "HOME=$home" "CORAL_DATA_DIR=$home" "CORAL_DIR=$home"
       "CORAL_PORT=$port" "CORAL_TELEMETRY_DISABLED=1")
"${clean[@]}" "$bin/coral" --home "$home" --host 127.0.0.1 --port "$port" --backend pty --no-browser \
  --cloud-url off > "$home/server.log" 2>&1 &
pid=$!
for _ in $(seq 1 150); do
  curl -sf "http://127.0.0.1:$port/api/health" >/dev/null 2>&1 && break
  kill -0 "$pid" 2>/dev/null || { echo "operator_cli_smoke: the server exited" >&2; tail -20 "$home/server.log" >&2; exit 1; }
  sleep 0.2
done
curl -sf "http://127.0.0.1:$port/api/health" >/dev/null || { echo "operator_cli_smoke: the server never answered /api/health" >&2; exit 1; }
board() { "${clean[@]}" "$bin/coral-board" "$@"; }
fail() { echo "operator_cli_smoke: FAIL: $*" >&2; tail -20 "$home/server.log" >&2; exit 1; }
board join operator-smoke --as Tester > "$home/join.out" 2>&1 || fail "coral-board join as the signed Operator: $(tail -2 "$home/join.out")"
board post "operator smoke message" > "$home/post.out" 2>&1 || fail "coral-board post as the signed Operator: $(tail -2 "$home/post.out")"
board read --id 1 > "$home/read.out" 2>&1 || fail "coral-board read --id 1: $(tail -2 "$home/read.out")"
grep -q "operator smoke message" "$home/read.out" || fail "the posted message is not in read --id 1: $(head -3 "$home/read.out")"
echo "operator_cli_smoke: ok (join, post and read as the signed Operator on :$port)"
