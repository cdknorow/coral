// Package agentsignin answers one question before Coral runs an agent CLI in the background: is it signed in? A background caller (a cron or workflow step, the
// summarizer, the goal and knowledge generators, team and theme generation, the model list) that starts "claude --print" while Claude is signed out would start Claude's
// own sign-in flow, which holds the login and blocks the person who is trying to sign in. So every such caller asks here first, and a signed-out CLI is never started:
// the job fails at once with "Claude is not signed in" (#5469, the operator's rule). Nothing here tries to sign in, and nothing waits for a person.
//
// The check is the CLI's own non-interactive status command: "claude auth status --json" (signed out: loggedIn false and exit 1) and "codex login status" (exit 1 when
// signed out). It runs with no stdin and no terminal, from the temp folder, under a deadline. Antigravity (agy), Gemini and Pi have no such command, so they are not
// checked (Supports reports it). An answer that cannot be read (an old CLI without the command, output with no loggedIn) is Unknown and the caller goes on as before, because a
// CLI that cannot be asked must not be locked out for good. A check that does not answer within its deadline is TimedOut: the job is still skipped (a second claude must not start
// behind whatever the first is waiting on), but with its own sentence and code, "Claude did not answer the sign-in check" and signin_check_timeout, never "not signed in":
// a slow start is not a signed-out user.
package agentsignin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/executil"
)

// State is what a status check found.
type State int

const (
	// Unknown: the CLI could not be asked, or its answer was not understood. Callers go on.
	Unknown State = iota
	SignedIn
	SignedOut
	// TimedOut: the status command did not answer within checkTimeout. Jobs are skipped, but the CLI is not said to be signed out.
	TimedOut
)

func (s State) String() string {
	switch s {
	case SignedIn:
		return "signed in"
	case SignedOut:
		return "signed out"
	case TimedOut:
		return "did not answer"
	}
	return "unknown"
}

// NotSignedInError is why a background job did not start: the CLI is signed out, or (TimedOut) did not answer the sign-in check in time.
type NotSignedInError struct {
	Agent    string
	TimedOut bool
}

// Error is the sentence the job's history shows: "Claude is not signed in", or "Claude did not answer the sign-in check".
func (e *NotSignedInError) Error() string {
	if e.TimedOut {
		return displayName(e.Agent) + " did not answer the sign-in check"
	}
	return displayName(e.Agent) + " is not signed in"
}

// Error codes a refused run carries (the run record's error_code).
const (
	CodeNotSignedIn        = "not_signed_in"
	CodeSignInCheckTimeout = "signin_check_timeout"
)

// Code is the fixed word for the refusal: not_signed_in, or signin_check_timeout.
func (e *NotSignedInError) Code() string {
	if e.TimedOut {
		return CodeSignInCheckTimeout
	}
	return CodeNotSignedIn
}

// IsNotSignedIn reports whether err is (or wraps) a refusal of this package: the CLI is signed out or did not answer the check (see TimedOut and Code).
func IsNotSignedIn(err error) bool {
	var e *NotSignedInError
	return errors.As(err, &e)
}

func displayName(agentType string) string {
	switch agentType {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	}
	return agentType
}

// spec is how one CLI is asked.
type spec struct {
	args  []string
	state func(out, errOut []byte, runErr error) State
}

var specs = map[string]spec{
	"claude": {args: []string{"auth", "status", "--json"}, state: claudeState},
	"codex":  {args: []string{"login", "status"}, state: codexState},
}

// Supports reports whether agentType has a non-interactive way to tell whether it is signed in. agy, gemini and pi do not, and are never checked.
func Supports(agentType string) bool {
	_, ok := specs[agentType]
	return ok
}

var noLoggedInOnce sync.Once

// claudeState reads "claude auth status --json": {"loggedIn": true|false, ...}. Signed out exits 1 with loggedIn false. Without readable JSON, a failing run that says it is not
// logged in (the CLI's own words) is signed out too; any other answer (an old CLI with no such command, garbage) is Unknown.
func claudeState(out, errOut []byte, runErr error) State {
	var v struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if json.Unmarshal(bytes.TrimSpace(out), &v) == nil {
		if v.LoggedIn != nil {
			if *v.LoggedIn {
				return SignedIn
			}
			return SignedOut
		}
		// Readable JSON with no loggedIn: the CLI renamed its field, and this check can no longer tell. Said once, so that it is not a silent off switch.
		noLoggedInOnce.Do(func() {
			slog.Warn("[signin] claude auth status answered without a loggedIn field: the sign-in check cannot tell whether Claude is signed in and lets jobs start")
		})
	}
	if runErr != nil {
		said := strings.ToLower(string(out) + " " + string(errOut))
		if strings.Contains(said, "not logged in") || strings.Contains(said, "not signed in") {
			return SignedOut
		}
	}
	return Unknown
}

// codexState reads "codex login status": exit 0 is signed in, exit status 1 is signed out; any other failure (not found, a timeout, a signal) is Unknown.
func codexState(_, _ []byte, runErr error) State {
	if runErr == nil {
		return SignedIn
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) && exit.ExitCode() == 1 {
		return SignedOut
	}
	return Unknown
}

// Variables the tests change.
var (
	// checkTimeout is how long the status command may take. It normally answers in a second or two (claude is a Node program: a cold start, an antivirus scan or a loaded machine
	// can take longer), and one that does not answer is waiting on something, such as the sign-in flow another claude holds, so the job is skipped as TimedOut and asked again
	// after timeoutBackoff.
	checkTimeout   = 6 * time.Second
	timeoutBackoff = 30 * time.Second
	now            = time.Now
	// positiveTTL is how long a "signed in" (or Unknown) answer is reused by Require: background jobs run often and the check starts a process.
	positiveTTL = time.Minute
	// backoffMin and backoffMax bound how long a "signed out" answer is reused by Require: it doubles with each signed-out answer in a row, so a signed-out Claude is asked
	// about a few times an hour, not by every job.
	backoffMin = 2 * time.Second
	backoffMax = 5 * time.Minute
)

// checkTimeoutOverride is a test's shorter limit (SetCheckTimeoutForTests), 0 for none. It is atomic because checks run in background goroutines of earlier tests and of the server under
// test, which read the limit while another test sets it; the plain checkTimeout above is only ever written by this package's own tests.
var checkTimeoutOverride atomic.Int64

// SetCheckTimeoutForTests shortens how long the status command may take, for a test in another package that makes the stand-in hang on purpose (claudestub.HangingAuth): what such a test
// checks (the run is skipped as "did not answer", never as signed out) does not need the production 6 s. It returns what puts the limit back.
func SetCheckTimeoutForTests(d time.Duration) (restore func()) {
	prev := checkTimeoutOverride.Swap(int64(d))
	return func() { checkTimeoutOverride.Store(prev) }
}

// effectiveCheckTimeout is the limit a check runs under: a test's override, else checkTimeout.
func effectiveCheckTimeout() time.Duration {
	if o := checkTimeoutOverride.Load(); o > 0 {
		return time.Duration(o)
	}
	return checkTimeout
}

const maxStatusOutput = 64 << 10

// Check asks the CLI at bin now, whatever was answered before. It never signs in: stdin is empty, there is no terminal, and the call ends at checkTimeout.
func Check(ctx context.Context, agentType, bin string) State { return CheckIn(ctx, agentType, bin, "") }

// CheckIn is Check run from dir, the folder the job's own CLI will run in ("" or a folder that is not there: the temp folder). The CLI reads the project settings of its working
// folder (an apiKeyHelper or a credential in <dir>/.claude/settings.json or settings.local.json) and says "signed in" for what it finds, so asking from the temp folder would
// call a user who is credentialed there signed out and refuse every job in that folder (#5476).
func CheckIn(ctx context.Context, agentType, bin, dir string) State {
	sp, ok := specs[agentType]
	if !ok || bin == "" {
		return Unknown
	}
	if agentType == "claude" && credentialedOutsideLogin() {
		return Unknown // signed in by an API key, a token, a key helper or a cloud provider: no login to ask about, and none to refuse
	}
	cctx, cancel := context.WithTimeout(ctx, effectiveCheckTimeout())
	defer cancel()
	cmd := executil.Command(cctx, bin, sp.args...)
	cmd.Stdin = nil // the null device: a prompt gets end of input
	cmd.Dir = os.TempDir()
	if dir != "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			cmd.Dir = dir
		}
	}
	cmd.Env = agent.CLIEnv(agentenv.Filter(os.Environ()), bin) // an npm-installed CLI runs with its own node (#5899)
	cmd.WaitDelay = 2 * time.Second                            // a child that holds the pipes open cannot hold this call
	var out, errOut limited
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	executil.NoCwdSearch(cmd) // Windows: a claude.cmd shim looks for node in the current folder first, so a repository's node.exe would run
	err := cmd.Run()
	if cctx.Err() != nil {
		if ctx.Err() != nil {
			return Unknown // the caller gave up, not the CLI
		}
		return TimedOut
	}
	return sp.state(out.b.Bytes(), errOut.b.Bytes(), err)
}

// limited keeps the first maxStatusOutput bytes and drops the rest.
type limited struct{ b bytes.Buffer }

func (l *limited) Write(p []byte) (int, error) {
	if room := maxStatusOutput - l.b.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		l.b.Write(p[:room])
	}
	return len(p), nil
}

type entry struct {
	mu        sync.Mutex // held while the CLI is asked: callers that arrive meanwhile share the answer
	snap      sync.Mutex // guards state, checkedAt and valid for Snapshot, which must never wait for a check that is running (the status handler reads it, #5476 review)
	state     State
	until     time.Time
	signedOut int // answers of "signed out" in a row
	checkedAt time.Time
	valid     bool
}

var (
	cacheMu sync.Mutex
	cache   = map[string]*entry{}
)

func entryFor(agentType, bin, dir string) *entry {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	k := agentType + "\x00" + bin + "\x00" + dir
	e := cache[k]
	if e == nil {
		e = &entry{}
		cache[k] = e
	}
	return e
}

// Require is the check every background caller makes before it starts agentType's CLI at bin: nil to go on (signed in, Unknown, or an agent with no check), a
// *NotSignedInError to skip. A recent answer is reused (a minute for signed in, a backoff that starts at two seconds and doubles to five minutes for signed out), so a signed-out CLI is asked about a dozen times an hour once the backoff has grown, not
// by every job. It logs once per backoff window that jobs are being skipped.
func Require(ctx context.Context, agentType, bin string) error {
	return ask(ctx, agentType, bin, "", false)
}

// RequireIn is Require for a job that runs in dir (see CheckIn): the answer is kept per folder.
func RequireIn(ctx context.Context, agentType, bin, dir string) error {
	return ask(ctx, agentType, bin, dir, false)
}

// RequireFresh is Require without the reused answer: a workflow run asks once, before its first step, so a person who has just signed in is not refused on a stale answer.
func RequireFresh(ctx context.Context, agentType, bin string) error {
	return ask(ctx, agentType, bin, "", true)
}

// RequireFreshIn is RequireFresh for a job that runs in dir (see CheckIn).
func RequireFreshIn(ctx context.Context, agentType, bin, dir string) error {
	return ask(ctx, agentType, bin, dir, true)
}

func ask(ctx context.Context, agentType, bin, dir string, fresh bool) error {
	if !Supports(agentType) || bin == "" {
		return nil
	}
	e := entryFor(agentType, bin, dir)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !fresh && e.valid && now().Before(e.until) {
		return e.verdict(agentType)
	}
	st := CheckIn(ctx, agentType, bin, dir)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	e.snap.Lock()
	e.valid = true
	e.state = st
	e.checkedAt = now()
	e.snap.Unlock()
	switch st {
	case SignedOut:
		e.signedOut++
		wait := backoffMin << min(e.signedOut-1, 10)
		if wait > backoffMax || wait <= 0 {
			wait = backoffMax
		}
		e.until = now().Add(wait)
		slog.Warn("[signin] "+displayName(agentType)+" is not signed in: its background jobs are skipped and it is checked again later", "agent", agentType, "next_check_in", wait.String())
	case TimedOut:
		e.until = now().Add(timeoutBackoff)
		slog.Warn("[signin] "+displayName(agentType)+" did not answer the sign-in check: its background jobs are skipped and it is checked again soon", "agent", agentType, "next_check_in", timeoutBackoff.String())
	default:
		e.signedOut = 0
		e.until = now().Add(positiveTTL)
	}
	return e.verdict(agentType)
}

func (e *entry) verdict(agentType string) error {
	switch e.state {
	case SignedOut:
		return &NotSignedInError{Agent: agentType}
	case TimedOut:
		return &NotSignedInError{Agent: agentType, TimedOut: true}
	}
	return nil
}

// Forget drops what was learned about bin, for a caller that knows the sign-in just changed.
func Forget(agentType, bin string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	prefix := agentType + "\x00" + bin + "\x00"
	for k := range cache {
		if strings.HasPrefix(k, prefix) {
			delete(cache, k)
		}
	}
}

// Describe is "Claude is not signed in" for an agent type, for messages that name the state without an error value.
func Describe(agentType string) string { return fmt.Sprint(&NotSignedInError{Agent: agentType}) }

// Status is what the last check of an agent CLI found, for the dashboard's one quiet line.
type Status struct {
	SignedIn  bool
	CheckedAt time.Time
}

// Snapshot is the newest answer for agentType over every CLI path asked about: ok is false when no check has been made (nothing to say); a check that timed out says nothing about the sign-in and is not reported. A signed-out answer stands until a
// later check finds the CLI signed in, so the line clears itself within the backoff (five minutes at most) of the person signing in, or at once when a workflow run, a distill
// or a theme or team generation asks.
func Snapshot(agentType string) (st Status, ok bool) {
	cacheMu.Lock()
	entries := make([]*entry, 0, len(cache))
	for k, e := range cache {
		if strings.HasPrefix(k, agentType+"\x00") {
			entries = append(entries, e)
		}
	}
	cacheMu.Unlock()
	for _, e := range entries {
		e.snap.Lock()
		if e.valid && (e.state == SignedIn || e.state == SignedOut) && (!ok || e.checkedAt.After(st.CheckedAt)) {
			st, ok = Status{SignedIn: e.state == SignedIn, CheckedAt: e.checkedAt}, true
		}
		e.snap.Unlock()
	}
	return st, ok
}

// credentialEnv are the variables that sign Claude in without an OAuth login: an API key, a token, or a cloud provider.
var credentialEnv = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}

// credentialedOutsideLogin reports whether Claude gets its credentials some way other than its own sign-in: one of credentialEnv set in the environment, or an apiKeyHelper or one of
// those variables in the user settings file. Such a user is never refused (and never asked about): "auth status" speaks for the OAuth login only.
func credentialedOutsideLogin() bool {
	for _, k := range credentialEnv {
		if os.Getenv(k) != "" {
			return true
		}
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".claude")
	}
	f, err := os.Open(filepath.Join(dir, "settings.json"))
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1)) // a bigger file is read no further than its limit
	if err != nil || len(data) > 1<<20 {
		return false
	}
	// Only whether a value is a non-empty string is asked, so one setting of another type (a number in env, say) cannot hide an apiKeyHelper that is there (#5476 review).
	var settings struct {
		APIKeyHelper any            `json:"apiKeyHelper"`
		Env          map[string]any `json:"env"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return false
	}
	if s, ok := settings.APIKeyHelper.(string); ok && s != "" {
		return true
	}
	for _, k := range credentialEnv {
		if s, ok := settings.Env[k].(string); ok && s != "" {
			return true
		}
	}
	return false
}
