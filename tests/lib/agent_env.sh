# Sourced by every test runner (bash), right after 'set -e…'.
#
# A runner started from an agent shell would otherwise inherit that agent's identity and
# production links: its session and subscriber, the Cloud Hub URL, hub mode, the tmux
# socket, and the workflow/step context. None of it belongs to a test run, so it is cleared
# here, by name and by prefix (so new step variables can't slip through).
#
# Never run bare 'tmux' from an agent shell: $TMUX there points at production Coral's
# ~/.coral/tmux.sock.
#
# Keep in step with AGENT_SHELL_VARS in tests/frontend/run_cdp_suites.mjs and
# agentShellVars in coral-go/internal/testguard.
unset CORAL_SESSION_NAME CORAL_SESSION_ID CORAL_SESSION_TOKEN CORAL_SESSION_AUTH_FILE CORAL_SUBSCRIBER_ID CORAL_CLOUD_URL CORAL_HUB CORAL_SHELL \
    CORAL_TMUX_SOCKET CORAL_TMUX_FALLBACK CORAL_AGENT_HELPER CORAL_AGENT_ARGS CORAL_BOARD_HELPER CORAL_BOARD_ARGS
# And where the live Coral is: an agent shell's CORAL_PORT/URL/HOST name the team's own server and
# its CORAL_DATA_DIR/DIR the live ~/.coral. A runner sets its own test port and data folder; one
# that forgot would otherwise reach, or start a server on, the live Coral (as the CDP runner's
# cleanEnv also strips them, tests/frontend/run_cdp_suites.mjs).
unset CORAL_PORT CORAL_URL CORAL_HOST CORAL_DATA_DIR CORAL_DIR CORAL_TEST_URL TMUX TMUX_PANE
for _coral_v in $(compgen -e); do
    case "$_coral_v" in
        CORAL_WORKFLOW_*|CORAL_PREV*|CORAL_STEP_*) unset "$_coral_v" ;;
    esac
done
unset _coral_v

# Strict local auth is Coral's default (#3936): every request to the test server needs a
# credential. curl here sends the test server's own API key, read from the runner's
# CORAL_DATA_DIR once the server has written it, and never the production key (~/.coral). A
# request that already names its own Authorization or session token is left as it is.
curl() {
    local _k="" _a
    if [[ -n "${CORAL_DATA_DIR:-}" && "${CORAL_DATA_DIR%/}" != "$HOME/.coral" && -r "$CORAL_DATA_DIR/api_key" ]]; then
        _k="$(tr -d '[:space:]' < "$CORAL_DATA_DIR/api_key")"
    fi
    for _a in "$@"; do
        case "$_a" in [Aa]uthorization:*|X-Coral-Session-Token:*|X-API-Key:*|[Cc]ookie:*) _k="" ;; esac
    done
    if [[ -n "$_k" ]]; then
        command curl -H "Authorization: Bearer $_k" "$@"
    else
        command curl "$@"
    fi
}

# Mock-agent run safety (#4354): coral_test_sandbox / _check / _cleanup. Kept in its own file so a
# runner that does not source this one (e.g. tests/perf/profile_busy.sh) can source just the sandbox.
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/mock_sandbox.sh"
