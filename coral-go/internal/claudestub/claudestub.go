// Package claudestub is a stand-in for the claude CLI with four sign-in states, for tests of everything Coral runs in the background (#5469, #5472). It is test
// support: no program that is released imports it (a test here holds that), and it never starts, finds or talks to the real CLI; Isolate makes the real one
// unreachable from a test.
//
// The stub is a small /bin/sh script in a temp folder; the folder's "state" file says how it behaves and the test can change it between calls:
//
//	signed-in         `auth status` prints {"loggedIn":true} and exits 0; a print-mode run (-p or --print) reads its stdin and answers.
//	signed-out        `auth status` says "Not logged in" and exits 1. A print-mode run does what the bug is: it STARTS THE LOGIN FLOW (a GRAB is logged and login.lock
//	                  is made), says "Not logged in" and holds for 2 s before it exits 1, so a run that should never have started is slow as well as logged.
//	login-in-progress the user's own `claude auth login` is open (login.lock exists, made by New): `auth status` says not logged in; a print-mode run waits behind
//	                  that login for 20 s, the way a second claude does.
//	hanging-auth      `auth status` and a print-mode run both hang for 20 s: a caller that asks first must give up on its own within its own timeout.
//
// Every run of the stub is appended to the call log (its pid, whether stdin was a terminal, the state it saw, its arguments cut to 200 characters each), so a test can say
// what was started and how: Calls, PrintRuns, AuthChecks, Grabs. `--version` answers at once in every state, as a real one does.
package claudestub

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/agent"
)

// State is how the stub behaves.
type State string

const (
	SignedIn        State = "signed-in"
	SignedOut       State = "signed-out"
	LoginInProgress State = "login-in-progress"
	HangingAuth     State = "hanging-auth"
)

// script is the stub. A hold is 20 s (a test that waits for it is the finding); `exec sleep` makes the stub's own pid the sleeper, so killing what a caller started ends it.
const script = `#!/bin/sh
D=$(dirname "$0")
state=$(cat "$D/state" 2>/dev/null || echo signed-out)
tty=0; [ -t 0 ] && tty=1
argv=""
for a in "$@"; do argv="$argv$(printf '%.200s' "$a" | tr '\n\t' '  ')$(printf '\037')"; done
log() { printf '%s\t%s\t%s\t%s\t%s\n' "$$" "$tty" "$state" "$1" "$argv" >> "$D/calls.log"; }
case "$1" in --version|-v|-V) log VERSION; echo "2.1.999 (Claude Code)"; exit 0;; esac
if [ "$1" = auth ]; then
  case "$2" in
    status)
      log AUTHSTATUS
      case "$state" in
        signed-in) echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty"}'; exit 0;;
        hanging-auth) exec sleep 20;;
        *) echo "Not logged in. Run claude auth login to authenticate." >&2; echo '{"loggedIn":false}'; exit 1;;
      esac;;
    login) log GRAB; touch "$D/login.lock"; exec sleep 20;;
  esac
fi
print=0
for a in "$@"; do case "$a" in -p|--print) print=1;; esac; done
if [ "$print" = 1 ]; then
  case "$state" in
    signed-in)
      log PRINT
      if [ "$tty" = 0 ]; then
        # the model list is asked as an initialize control request on stream-json (agentmodels.fetchClaude): the real claude answers it at once and goes on reading, so the stub does too
        case " $* " in *" --input-format stream-json "*)
          IFS= read -r _req
          echo '{"type":"control_response","response":{"subtype":"success","request_id":"coral-models","response":{"models":[{"value":"stub-model","displayName":"Stub model","description":"the stand-in model","supportedEffortLevels":["low","high"]}]}}}';;
        esac
        cat > /dev/null
      fi
      case " $* " in
        *" json "*) echo '{"type":"result","subtype":"success","is_error":false,"result":"stub reply","session_id":"stub","total_cost_usd":0}';;
        *) echo "stub reply";;
      esac
      exit 0;;
    signed-out)
      log PRINT; log GRAB; touch "$D/login.lock"
      echo "Not logged in · Please run /login"; sleep 2; exit 1;;
    login-in-progress) log PRINT; echo "waiting for the login in progress" >&2; exec sleep 20;;
    hanging-auth) log PRINT; exec sleep 20;;
  esac
fi
log OTHER
exit 0
`

// Stub is one stand-in CLI.
type Stub struct {
	t   testing.TB
	dir string
}

// New writes a stub in the given state into a temp folder of its own (removed with the test). It skips the test on Windows, where a shell script is not a program.
func New(t testing.TB, state State) *Stub {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the claude stand-in is a shell script")
	}
	dir := t.TempDir()
	s := &Stub{t: t, dir: dir}
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "calls.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.Set(state)
	return s
}

// NoSlower fails the test when subject takes a second or more longer than baseline: how a hold shows (a wait on a lock or on a login, a sleep put into the path), told apart from a
// busy machine without an absolute number of seconds. baseline and subject are timed five times, interleaved so both meet the machine's load of the moment, and the FASTEST run of each
// is compared: a spike from load is in some runs only and does not count; a delay in the subject is in every run. The slack (a second) is under the two-second hold the tests guard against.
func NoSlower(t testing.TB, what string, baseline, subject func(), slack time.Duration) {
	t.Helper()
	const n = 5
	var bestBase, bestSubj time.Duration
	for i := 0; i < n; i++ {
		start := time.Now()
		baseline()
		if d := time.Since(start); i == 0 || d < bestBase {
			bestBase = d
		}
		start = time.Now()
		subject()
		if d := time.Since(start); i == 0 || d < bestSubj {
			bestSubj = d
		}
	}
	if bestSubj > bestBase+slack {
		t.Errorf("%s: the fastest of %d runs took %v against %v for the control: held for %v more than a plain run takes", what, n, bestSubj, bestBase, bestSubj-bestBase)
	}
}

// Path is the stub's program: what a test gives as the CLI path.
func (s *Stub) Path() string { return filepath.Join(s.dir, "claude") }

// Dir is the stub's folder (state, call log and login.lock are in it).
func (s *Stub) Dir() string { return s.dir }

// Set changes the state for the calls that follow. LoginInProgress also makes login.lock (the user's own login); any other state removes it.
func (s *Stub) Set(state State) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, "state"), []byte(string(state)), 0o600); err != nil {
		s.t.Fatal(err)
	}
	lock := filepath.Join(s.dir, "login.lock")
	if state == LoginInProgress {
		_ = os.WriteFile(lock, []byte("the user's own login"), 0o600)
	} else {
		_ = os.Remove(lock)
	}
}

// Call is one run of the stub.
type Call struct {
	PID   int
	TTY   bool // stdin was a terminal
	State State
	Event string // VERSION, AUTHSTATUS, PRINT, GRAB or OTHER
	Args  []string
}

// Calls is every run so far, in order.
func (s *Stub) Calls() []Call {
	b, err := os.ReadFile(filepath.Join(s.dir, "calls.log"))
	if err != nil {
		s.t.Fatal(err)
	}
	var out []Call
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		f := strings.SplitN(line, "\t", 5)
		if len(f) < 5 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		out = append(out, Call{PID: pid, TTY: f[1] == "1", State: State(f[2]), Event: f[3], Args: strings.Split(strings.TrimSuffix(f[4], "\x1f"), "\x1f")})
	}
	return out
}

func (s *Stub) events(event string) []Call {
	var out []Call
	for _, c := range s.Calls() {
		if c.Event == event {
			out = append(out, c)
		}
	}
	return out
}

// PrintRuns are the print-mode runs (-p or --print): what a background caller must not start while Claude is not signed in.
func (s *Stub) PrintRuns() []Call { return s.events("PRINT") }

// AuthChecks are the `auth status` runs.
func (s *Stub) AuthChecks() []Call { return s.events("AUTHSTATUS") }

// Grabs is how many times a run started the login flow (a print-mode run while signed out, or `auth login`): the bug, counted. Always 0 for a correct caller.
func (s *Stub) Grabs() int { return len(s.events("GRAB")) }

// Isolate makes the real claude unreachable from the test: PATH is the system's /usr/bin and /bin only, HOME and the data folder are temp, and every variable the
// real CLI reads for credentials or behaviour is removed. It returns the temp HOME. Call it before anything resolves a CLI.
func Isolate(t testing.TB) string {
	t.Helper()
	defer func() {
		// Last, with HOME and PATH already the isolated ones (#5475 review): the PATH above cannot hide a CLI the resolver finds by an absolute path.
		if p := RealCLIReachable(); p != "" {
			t.Skipf("a real claude is installed at %s, outside the isolated PATH and HOME: Coral's CLI search (agent.FindCLI, FindCLIInCommonPaths) would reach it if a caller did not use the stand-in's configured path; this matrix runs where none is installed (CI, epoch)", p)
		}
	}()
	home := t.TempDir()
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CORAL_DATA_DIR", filepath.Join(home, ".coral"))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_") || strings.HasPrefix(name, "CORAL_SESSION") {
			t.Setenv(name, "")
			_ = os.Unsetenv(name)
		}
	}
	return home
}

// findCLIInCommonPaths is Coral's own search of the places a CLI is installed (/usr/local/bin, /opt/homebrew/bin, /snap/bin, the app bundle, and under HOME); a variable so a test
// can stand in for a machine that has one.
var findCLIInCommonPaths = agent.FindCLIInCommonPaths

// RealCLIReachable is where a real claude would be found by the search a caller that does not use the configured path falls back to ("" when there is none): PATH first, then the
// common install locations, as agent.FindCLI does. The stand-in's folder is not on either, so anything returned here is not the stand-in. Call it with the isolated PATH and HOME.
func RealCLIReachable() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return findCLIInCommonPaths("claude")
}
