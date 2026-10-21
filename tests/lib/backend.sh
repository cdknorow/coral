# Terminal backend for a test run, and what a runner needs to start its server on Windows.
# Sourced by the stress runners (bash), after agent_env.sh.
#
# CORAL_TEST_BACKEND picks the server's --backend: tmux (the default, so existing runs are
# unchanged) or pty (Windows ConPTY; on Linux and macOS the same PTY backend over a Unix pty).
# Running a suite once per backend and comparing the results finds the places where Coral
# sends input to, or reads output from, an agent's terminal differently on Windows (#18).
#
# On Windows (Git Bash) there is no tmux: CORAL_TEST_BACKEND must be pty there.

CORAL_TEST_BACKEND="${CORAL_TEST_BACKEND:-tmux}"
case "$CORAL_TEST_BACKEND" in
    tmux|pty) ;;
    *) echo "ERROR: CORAL_TEST_BACKEND must be tmux or pty (got '$CORAL_TEST_BACKEND')" >&2; exit 2 ;;
esac

# coral_test_is_windows: true under Git Bash / MSYS / Cygwin.
coral_test_is_windows() {
    case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) return 0 ;; esac
    return 1
}

if coral_test_is_windows && [[ "$CORAL_TEST_BACKEND" == tmux ]]; then
    echo "ERROR: Windows has no tmux backend; run with CORAL_TEST_BACKEND=pty" >&2
    exit 2
fi
export CORAL_TEST_BACKEND

# CORAL_TEST_EXE: the executable suffix of a go build output (.exe on Windows).
CORAL_TEST_EXE=""
coral_test_is_windows && CORAL_TEST_EXE=".exe"

# coral_test_native_path PATH: PATH as the server and agents see it. On Windows a Git Bash path
# (/tmp/x, /c/Users/x) means nothing to a Windows program reading it from JSON or a file, so it
# becomes C:/Users/x/AppData/Local/Temp/x; elsewhere it is unchanged.
coral_test_native_path() {
    if coral_test_is_windows; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

# coral_test_port_in_use PORT: lsof where it exists, else a connection attempt (Git Bash has no lsof).
coral_test_port_in_use() {
    if command -v lsof >/dev/null 2>&1; then
        lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
    else
        command curl -s -m 2 -o /dev/null "http://127.0.0.1:$1/" 2>/dev/null
    fi
}

# coral_test_home_env HOME_DIR: the env assignments that give the server an isolated home. On
# Windows, Go's os.UserHomeDir reads USERPROFILE, not HOME, so both are set: HOME alone left a
# Windows test server reading the real %USERPROFILE%\.claude, .codex and .gemini.
coral_test_home_env() {
    local h; h="$(coral_test_native_path "$1")"
    if coral_test_is_windows; then
        printf 'HOME=%s\nUSERPROFILE=%s\n' "$h" "$h"
    else
        printf 'HOME=%s\n' "$h"
    fi
}

# coral_test_proc_start PID: what identifies the process PID for as long as it lives: its start time. Linux and Git Bash read field 22 of /proc/PID/stat;
# macOS and the BSDs ps -o lstart; where neither answers, Git Bash asks Windows (PowerShell Get-Process StartTime, as UTC ticks, for the Windows pid in
# /proc/PID/winpid). A process that took the number over later has another one. Prints nothing when it can't tell (no such process, nothing above works):
# callers treat that as "not the same", never as "the same".
coral_test_proc_start() {
    local pid="$1" stat rest out winpid
    if [[ -r "/proc/$pid/stat" ]]; then
        stat=$(<"/proc/$pid/stat") 2>/dev/null
        rest=${stat##*) }   # after the command name, which may hold spaces and parentheses: state is the first field (3), start time the 20th (22)
        set -- $rest
        if [[ -n "${20:-}" ]]; then printf '%s\n' "${20}"; return 0; fi
    fi
    out="$(ps -o lstart= -p "$pid" 2>/dev/null | sed -e 's/^ *//' -e 's/ *$//')"
    if [[ -n "$out" ]]; then printf '%s\n' "$out"; return 0; fi
    case "$(uname -s 2>/dev/null)" in
        MINGW*|MSYS*|CYGWIN*) coral_test_proc_start_windows "$pid" ;;
    esac
    return 0
}

# coral_test_proc_start_windows PID: Git Bash's way to ask Windows for the start time of the process whose Git Bash number is PID (UTC ticks); nothing when it can't.
coral_test_proc_start_windows() {
    local pid="$1" winpid
    winpid="$(cat "/proc/$pid/winpid" 2>/dev/null)"
    powershell.exe -NoProfile -NonInteractive -Command "(Get-Process -Id ${winpid:-$pid} -ErrorAction Stop).StartTime.ToUniversalTime().Ticks" 2>/dev/null | tr -d '\r'
}

# coral_test_same_process PID START: true when PID is a live process whose coral_test_proc_start is START (non-empty).
coral_test_same_process() {
    local now
    [[ -n "$2" ]] || return 1
    now="$(coral_test_proc_start "$1")"
    [[ -n "$now" && "$now" == "$2" ]]
}

# coral_test_watchdog [SECONDS]: if the runner is still going after SECONDS (default
# CORAL_TEST_WATCHDOG_S, else 900), it says so and exits 124, so its EXIT trap still stops the server
# and cleans up. A hang then fails one runner instead of blocking a whole suite run (#116: on
# Windows, run_agent_tasks hung on a path the server could not open). Bash acts on the signal
# between commands, and every API call here has a curl -m limit, so it lands within seconds.
# The signal goes only to the process that started the watchdog: its start time is taken now and compared when the time is up, so a runner
# that was killed hard (its EXIT trap never stopped the watchdog) never has its number's later owner signalled (#5776). Where the start time
# can't be read the watchdog is not started, and says so.
# Call coral_test_watchdog_stop first thing in the runner's cleanup.
coral_test_watchdog() {
    local secs="${1:-${CORAL_TEST_WATCHDOG_S:-900}}" me=$$ started
    started="$(coral_test_proc_start "$me")"
    if [[ -z "$started" ]]; then
        echo "[watchdog] cannot tell this process apart from another one here (no /proc/PID/stat, no ps -o lstart): no watchdog for this runner" >&2
        return 0
    fi
    # The limit is expanded now: secs is local, and gone by the time the trap runs.
    trap "echo \"[watchdog] \$(basename \"\$0\") still running after ${secs}s: stopping it\" >&2; exit 124" USR1
    (
        trap 'kill "${sp:-}" 2>/dev/null; exit 0' TERM
        sleep "$secs" & sp=$!
        wait "$sp" && coral_test_same_process "$me" "$started" && kill -USR1 "$me" 2>/dev/null
    ) &
    CORAL_TEST_WATCHDOG_PID=$!
}

coral_test_watchdog_stop() {
    if [[ -n "${CORAL_TEST_WATCHDOG_PID:-}" ]]; then
        kill "$CORAL_TEST_WATCHDOG_PID" 2>/dev/null || true
        CORAL_TEST_WATCHDOG_PID=""
    fi
    return 0
}

# coral_test_require_ok BODY WHAT: a settings write the runner depends on must have taken (the server answers
# {"ok":true}); otherwise the runner stops with the server's answer, and its EXIT trap cleans up. A refused
# cli_path_claude (a Git Bash path on Windows: 'no file at /tmp/...') left the CLI on PATH in place and the
# nudge test passed against it (#121 N2).
coral_test_require_ok() {
    if [[ "$1" != *'"ok":true'* ]]; then
        echo "ERROR: $2 was not accepted by the server: ${1:-no answer}" >&2
        exit 1
    fi
}

# coral_test_backend_only BACKEND REASON: a runner whose checks read that backend's internals
# (tmux sockets, show-environment) runs only on it; on another backend it says why and exits 0.
coral_test_backend_only() {
    if [[ "$CORAL_TEST_BACKEND" != "$1" ]]; then
        echo "[SKIP] $(basename "$0") runs only with CORAL_TEST_BACKEND=$1: $2" >&2
        exit 0
    fi
}
