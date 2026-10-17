# Mock-agent run safety (#4354). Sourced by agent_env.sh (so every bash runner has it) and, for a
# runner that does not source agent_env.sh, directly.
#
# Two leaks today: a perf harness started real Claude CLIs on the operator's account (a launch
# client hang-up plus the cli_path PATH-fallback), and a mock run wrote transcripts into the real
# ~/.claude/projects. A harness that starts a test Coral with mock agents calls coral_test_sandbox
# before starting the server, runs the server (and thus its mock agents) with
# HOME="$CORAL_TEST_HOME" and PATH="$CORAL_TEST_STUB_BIN:$PATH", and calls coral_test_sandbox_check
# at the end (coral_test_sandbox_cleanup in its trap). This makes two failures impossible to miss:
#  - a stub 'claude'/'codex'/'gemini' is first on PATH, so if a launch ever falls back to the PATH
#    CLI (the hang-up bug) it hits the stub, which records a marker and exits 1 — the real CLI on
#    the operator's account never runs;
#  - the server's HOME is an isolated temp dir, so mock transcripts land there, not in the real
#    ~/.claude/projects.
# The check then fails the run loudly if the stub was ever called or anything new appeared under the
# real ~/.claude/projects. Keep in step with the harness lint in coral-go/internal/testguard
# (TestHarnessScriptsUseTheMockAgentSandbox).

coral_test_sandbox() {
    CORAL_TEST_HOME="$(mktemp -d "${TMPDIR:-/tmp}/coral-test-home.XXXXXX")"
    mkdir -p "$CORAL_TEST_HOME/.claude/projects"
    CORAL_TEST_STUB_BIN="$(mktemp -d "${TMPDIR:-/tmp}/coral-test-stub.XXXXXX")"
    CORAL_TEST_STUB_MARKER="$CORAL_TEST_STUB_BIN/REAL_CLI_WAS_CALLED"
    local _cli
    for _cli in claude codex gemini; do
        # The stub answers nothing: any call means a test tried the PATH CLI instead of its mock.
        printf '#!/bin/sh\nprintf "%%s %%s\\n" "%s" "$*" >> "%s"\nexit 1\n' \
            "$_cli" "$CORAL_TEST_STUB_MARKER" > "$CORAL_TEST_STUB_BIN/$_cli"
        chmod +x "$CORAL_TEST_STUB_BIN/$_cli"
    done
    # Baseline of the real ~/.claude/projects (the leak vector): a test must add nothing here. The
    # project-dir set, not a timestamp, so the operator's own live Claude sessions don't false-positive.
    CORAL_TEST_REAL_CLAUDE="$HOME/.claude"
    CORAL_TEST_CLAUDE_BASELINE="$(ls -1 "$CORAL_TEST_REAL_CLAUDE/projects" 2>/dev/null | sort || true)"
    export CORAL_TEST_HOME CORAL_TEST_STUB_BIN CORAL_TEST_STUB_MARKER
}

# Fails (returns 1, loudly) if the real CLI was attempted or the real ~/.claude/projects grew.
coral_test_sandbox_check() {
    local _fail=0 _now _added
    if [[ -s "${CORAL_TEST_STUB_MARKER:-/nonexistent}" ]]; then
        echo "FAIL (#4354): a real CLI on PATH was invoked during the run (the mock cli_path was bypassed):" >&2
        sed 's/^/    /' "$CORAL_TEST_STUB_MARKER" >&2
        _fail=1
    fi
    _now="$(ls -1 "${CORAL_TEST_REAL_CLAUDE:-$HOME/.claude}/projects" 2>/dev/null | sort || true)"
    _added="$(comm -13 <(printf '%s\n' "${CORAL_TEST_CLAUDE_BASELINE:-}") <(printf '%s\n' "$_now") | sed '/^$/d')"
    if [[ -n "$_added" ]]; then
        echo "FAIL (#4354): new entries under the real ~/.claude/projects (a mock run wrote outside its isolated HOME):" >&2
        printf '    %s\n' $_added >&2
        _fail=1
    fi
    [[ $_fail -eq 0 ]] && echo "[#4354] sandbox check ok: no real CLI called, real ~/.claude/projects unchanged"
    return $_fail
}

coral_test_sandbox_cleanup() {
    [[ -n "${CORAL_TEST_HOME:-}" ]] && rm -rf "$CORAL_TEST_HOME"
    [[ -n "${CORAL_TEST_STUB_BIN:-}" ]] && rm -rf "$CORAL_TEST_STUB_BIN"
}
