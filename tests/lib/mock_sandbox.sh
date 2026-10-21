# Mock-agent run safety (#4354). Sourced by agent_env.sh (so every bash runner has it) and, for a
# runner that does not source agent_env.sh, directly.
#
# Two leaks today: a perf harness started real Claude CLIs on the operator's account (a launch
# client hang-up plus the cli_path PATH-fallback), and a mock run wrote transcripts into the real
# ~/.claude/projects. A harness that starts a test Coral with mock agents calls coral_test_sandbox
# before starting the server, runs the server (and thus its mock agents) with
# HOME="$CORAL_TEST_HOME" (and USERPROFILE, which Go reads for the home folder on Windows) and PATH="$CORAL_TEST_STUB_BIN:$PATH", and calls coral_test_sandbox_check
# at the end (coral_test_sandbox_cleanup in its trap). This makes two failures impossible to miss:
#  - a stub for every CLI-backed agent type (claude, codex, gemini, agy, pi, antigravity; see
#    CORAL_TEST_STUB_CLIS) is first on PATH, so if a launch ever falls back to the PATH CLI (the
#    hang-up bug) it hits the stub, which records a marker and exits 1 -- the real CLI on the
#    operator's account never runs;
#  - the server's HOME is an isolated temp dir, so mock transcripts land there, not in the real
#    ~/.claude/projects.
# The check then fails the run loudly if the stub was ever called or anything new appeared under the
# real ~/.claude/projects. Keep in step with the harness lint in coral-go/internal/testguard
# (TestHarnessScriptsUseTheMockAgentSandbox).

# Every CLI-backed agent type's launch binary gets a stub, not just claude/codex/gemini (#4354 INFO 2):
# the binaries internal/agent's agentCLIs maps every agenttypes type (except 'terminal', which is a
# shell, not a CLI) to -- claude, codex, agy (agy/antigravity/gemini), pi -- plus the type names
# themselves, so a path that execs the type rather than the resolved binary is covered too. A Go test
# (TestMockSandboxStubsEveryAgentCLI in coral-go/internal/testguard) fails if a new agent type or
# binary is not in this list, so it stays in step automatically.
# The built-in defaults are ALWAYS stubbed; an inherited CORAL_TEST_STUB_CLIS may ADD names, never
# remove them, so a stray value in the environment can't shorten the list for a run (Lead's INFO on #4354).
CORAL_TEST_STUB_CLIS="claude codex gemini agy pi antigravity ${CORAL_TEST_STUB_CLIS:-}"

coral_test_sandbox() {
    CORAL_TEST_HOME="$(mktemp -d "${TMPDIR:-/tmp}/coral-test-home.XXXXXX")"
    mkdir -p "$CORAL_TEST_HOME/.claude/projects"
    CORAL_TEST_STUB_BIN="$(mktemp -d "${TMPDIR:-/tmp}/coral-test-stub.XXXXXX")"
    CORAL_TEST_STUB_MARKER="$CORAL_TEST_STUB_BIN/REAL_CLI_WAS_CALLED"
    CORAL_TEST_SANDBOX_CHECKED=""   # reset: the end check (or the EXIT trap) sets it
    local _cli
    for _cli in $CORAL_TEST_STUB_CLIS; do
        # The stub answers nothing: any call means a test tried the PATH CLI instead of its mock.
        printf '#!/bin/sh\nprintf "%%s %%s\\n" "%s" "$*" >> "%s"\nexit 1\n' \
            "$_cli" "$CORAL_TEST_STUB_MARKER" > "$CORAL_TEST_STUB_BIN/$_cli"
        chmod +x "$CORAL_TEST_STUB_BIN/$_cli"
        # Windows finds a command on PATH only with an extension (PATHEXT), never the
        # extensionless script above, so a server on Windows gets a .cmd stub as well.
        case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*)
            printf '@echo %s %%* >> "%s"\r\n@exit /b 1\r\n' "$_cli" "$(cygpath -w "$CORAL_TEST_STUB_MARKER")" \
                > "$CORAL_TEST_STUB_BIN/$_cli.cmd" ;;
        esac
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
    CORAL_TEST_SANDBOX_CHECKED=1   # so the EXIT trap does not run the check a second time
    return $_fail
}

coral_test_sandbox_cleanup() {
    # INFO 3 (#4354): a crash skips the harness's explicit end check. Run it here (from the EXIT trap)
    # if it has not run, so a leak still prints loudly. It only prints -- it does not change the exit
    # status, so an already-failing run keeps its code and a clean run's failure is surfaced by the
    # explicit end check, which every wired harness calls.
    if [[ -z "${CORAL_TEST_SANDBOX_CHECKED:-}" && -n "${CORAL_TEST_STUB_BIN:-}" ]]; then
        coral_test_sandbox_check || true
    fi
    coral_test_rm_tree "${CORAL_TEST_HOME:-}"
    coral_test_rm_tree "${CORAL_TEST_STUB_BIN:-}"
}

# coral_test_rm_tree DIR: rm -rf DIR, never failing the run (it runs in EXIT traps, under set -e). On
# Windows the system makes AppData\Local\Microsoft\Windows\INetCache\Content.IE5 in the isolated home,
# a hidden system reparse point Git Bash's rm can't remove ("Directory not empty"), which made a clean
# run exit 1: cmd's rmdir removes it. A leftover is only reported.
# Standing rule (#5762): only a folder whose name starts with coral-test is ever removed (the harness makes its own with mktemp ... coral-test-*.XXXXXX); a
# variable that is empty, was never set to a harness folder, or points at a real one (/, a home, a project) is refused and said, never removed.
coral_test_rm_tree() {
    local d="$1"
    [[ -n "$d" && -e "$d" ]] || return 0
    case "$(basename "$d")" in
        coral-test*) ;;
        *) echo "[#5762] refusing to remove $d: the name does not start with coral-test (the harness removes only its own folders)" >&2; return 0 ;;
    esac
    rm -rf "$d" 2>/dev/null || true
    if [[ -e "$d" ]]; then
        case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*)
            cmd //c rmdir //s //q "$(cygpath -w "$d")" >/dev/null 2>&1 || true ;;
        esac
    fi
    if [[ -e "$d" ]]; then echo "[#4354] note: could not remove $d" >&2; fi
    return 0
}
