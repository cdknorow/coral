package agentsignin

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests start this test binary as the stand-in CLI: with AGENTSIGNIN_STANDIN set it acts as "claude auth status --json" or "codex login status" and exits, so no
// real CLI (and no real login) is ever touched, on any OS.
func TestMain(m *testing.M) {
	if mode := os.Getenv("AGENTSIGNIN_STANDIN"); mode != "" {
		os.Exit(standIn(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func standIn(mode string, args []string) int {
	if log := os.Getenv("AGENTSIGNIN_LOG"); log != "" { // one line per start, so a test counts how often the CLI was asked
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			f.WriteString(strings.Join(args, " ") + "\n")
			f.Close()
		}
	}
	switch mode {
	case "claude-by-cwd": // the CLI reads the project settings of its working folder: signed in only where a credential is configured (#5476)
		if _, err := os.Stat(filepath.Join(".claude", "settings.json")); err == nil {
			os.Stdout.WriteString(`{"loggedIn":true,"authMethod":"api_key_helper"}`)
			return 0
		}
		os.Stdout.WriteString(`{"loggedIn":false,"authMethod":"none"}`)
		return 1
	case "claude-in":
		os.Stdout.WriteString(`{"loggedIn":true,"authMethod":"claude.ai"}`)
		return 0
	case "claude-out":
		os.Stdout.WriteString(`{"loggedIn":false,"authMethod":"none"}`)
		return 1
	case "claude-old": // a CLI with no "auth" command: no JSON, an error
		os.Stderr.WriteString("error: unknown command 'auth'")
		return 1
	case "claude-text": // signed out in words only: no JSON, the CLI's own sentence on stderr
		os.Stderr.WriteString("Not logged in. Run claude auth login to authenticate.")
		return 1
	case "claude-garbage":
		os.Stdout.WriteString("<html>login</html>")
		return 0
	case "ok":
		return 0
	case "exit1":
		return 1
	case "exit2":
		return 2
	case "hang":
		time.Sleep(time.Minute)
		return 0
	case "stdin-eof": // a prompt for input must see end of input at once, never wait for a person
		b := make([]byte, 1)
		n, _ := os.Stdin.Read(b)
		if n != 0 {
			return 3
		}
		os.Stdout.WriteString(`{"loggedIn":true}`)
		return 0
	}
	return 9
}

func standInBin(t *testing.T, mode string) string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("AGENTSIGNIN_STANDIN", mode)
	return exe
}

func reset(t *testing.T) {
	t.Helper()
	// Nothing of the real machine's Claude setup is read: no credential variables, an empty home with no user settings.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, k := range credentialEnv {
		t.Setenv(k, "")
	}
	cacheMu.Lock()
	cache = map[string]*entry{}
	cacheMu.Unlock()
	oldNow, oldPos, oldMin, oldMax, oldTO, oldTB := now, positiveTTL, backoffMin, backoffMax, checkTimeout, timeoutBackoff
	t.Cleanup(func() {
		now, positiveTTL, backoffMin, backoffMax, checkTimeout, timeoutBackoff = oldNow, oldPos, oldMin, oldMax, oldTO, oldTB
	})
}

func TestClaudeStatusIsReadFromTheCLIsOwnAnswer(t *testing.T) {
	reset(t)
	for mode, want := range map[string]State{
		"claude-in":      SignedIn,
		"claude-out":     SignedOut,
		"claude-text":    SignedOut, // the CLI's own words, no JSON
		"claude-old":     Unknown,   // cannot be asked: not treated as signed out
		"claude-garbage": Unknown,
		"hang":           TimedOut, // waiting on something (another claude's sign-in): jobs are skipped, but it is not said to be signed out
	} {
		checkTimeout = 20 * time.Second
		if mode == "hang" {
			checkTimeout = 300 * time.Millisecond
		}
		start := time.Now()
		defer func(mode string) {
			if mode == "hang" {
				assert.Less(t, time.Since(start), 3*time.Second)
			}
		}(mode)
		bin := standInBin(t, mode)
		assert.Equal(t, want, Check(context.Background(), "claude", bin), mode)
	}
}

func TestCodexStatusIsTheExitCode(t *testing.T) {
	reset(t)
	for mode, want := range map[string]State{"ok": SignedIn, "exit1": SignedOut, "exit2": Unknown} {
		bin := standInBin(t, mode)
		assert.Equal(t, want, Check(context.Background(), "codex", bin), mode)
	}
}

func TestACLIThatCannotBeStartedIsUnknown(t *testing.T) {
	reset(t)
	assert.Equal(t, Unknown, Check(context.Background(), "claude", filepath.Join(t.TempDir(), "nope")))
	assert.NoError(t, Require(context.Background(), "claude", filepath.Join(t.TempDir(), "nope")), "an unknown answer goes on")
}

func TestOnlyCLIsWithAStatusCommandAreChecked(t *testing.T) {
	assert.True(t, Supports("claude"))
	assert.True(t, Supports("codex"))
	for _, a := range []string{"agy", "antigravity", "gemini", "pi", "", "nonsense"} {
		assert.False(t, Supports(a), a)
		assert.NoError(t, Require(context.Background(), a, "/bin/true"), a)
	}
}

// The check runs the status command with no terminal input and the exact arguments.
func TestTheCheckSendsNoInputAndTheStatusArguments(t *testing.T) {
	reset(t)
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("AGENTSIGNIN_LOG", log)
	bin := standInBin(t, "stdin-eof")
	assert.Equal(t, SignedIn, Check(context.Background(), "claude", bin))
	got, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t, "auth status --json\n", string(got))
	os.Remove(log)
	standInBin(t, "ok")
	assert.Equal(t, SignedIn, Check(context.Background(), "codex", bin))
	got, _ = os.ReadFile(log)
	assert.Equal(t, "login status\n", string(got))
}

func TestRequireRefusesASignedOutClaudeWithTheOperatorsSentence(t *testing.T) {
	reset(t)
	bin := standInBin(t, "claude-out")
	err := Require(context.Background(), "claude", bin)
	require.Error(t, err)
	assert.Equal(t, "Claude is not signed in", err.Error())
	assert.True(t, IsNotSignedIn(err))
	assert.Equal(t, "Codex is not signed in", (&NotSignedInError{Agent: "codex"}).Error())
}

// A signed-out CLI is asked about a few times, not by every job: the answer is reused and the wait doubles to a cap; a fresh ask (a workflow run) always asks.
func TestSignedOutIsReusedWithABackoffThatDoublesToACap(t *testing.T) {
	reset(t)
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("AGENTSIGNIN_LOG", log)
	bin := standInBin(t, "claude-out")
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return clock }
	asked := func() int {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "\n")
	}
	for i := 0; i < 20; i++ { // twenty jobs in the same minute: one question
		assert.True(t, IsNotSignedIn(Require(context.Background(), "claude", bin)))
	}
	assert.Equal(t, 1, asked())
	clock = clock.Add(backoffMin + time.Second) // the first wait is over: a second question, and the wait is now twice as long
	assert.True(t, IsNotSignedIn(Require(context.Background(), "claude", bin)))
	assert.Equal(t, 2, asked())
	clock = clock.Add(backoffMin + time.Second)
	assert.True(t, IsNotSignedIn(Require(context.Background(), "claude", bin)))
	assert.Equal(t, 2, asked(), "inside the doubled wait")
	clock = clock.Add(backoffMin)
	assert.True(t, IsNotSignedIn(Require(context.Background(), "claude", bin)))
	assert.Equal(t, 3, asked())
	for i := 0; i < 12; i++ { // the wait never passes the cap
		clock = clock.Add(backoffMax + time.Second)
		Require(context.Background(), "claude", bin)
	}
	assert.Equal(t, 15, asked(), "one question per cap after the doubling")
	assert.True(t, IsNotSignedIn(RequireFresh(context.Background(), "claude", bin)))
	assert.Equal(t, 16, asked(), "a fresh ask always asks")
}

// Signing in ends the refusal at the next question, and signed in is reused for the TTL.
func TestSigningInEndsTheRefusal(t *testing.T) {
	reset(t)
	bin := standInBin(t, "claude-out")
	require.True(t, IsNotSignedIn(Require(context.Background(), "claude", bin)))
	standInBin(t, "claude-in")
	assert.True(t, IsNotSignedIn(Require(context.Background(), "claude", bin)), "inside the wait the old answer stands")
	assert.NoError(t, RequireFresh(context.Background(), "claude", bin), "a fresh ask sees the sign-in")
	standInBin(t, "claude-out")
	assert.NoError(t, Require(context.Background(), "claude", bin), "signed in is reused for the TTL")
	Forget("claude", bin)
	assert.True(t, IsNotSignedIn(Require(context.Background(), "claude", bin)))
}

// A call whose context ended returns the context's error, never a verdict, and records nothing.
func TestACancelledCallRecordsNoAnswer(t *testing.T) {
	reset(t)
	bin := standInBin(t, "claude-out")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, Require(ctx, "claude", bin), context.Canceled)
	cacheMu.Lock()
	n := 0
	for _, e := range cache {
		if e.valid {
			n++
		}
	}
	cacheMu.Unlock()
	assert.Zero(t, n)
}

func TestSnapshotIsTheNewestAnswerAndEmptyBeforeAnyCheck(t *testing.T) {
	reset(t)
	_, ok := Snapshot("claude")
	assert.False(t, ok)
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return clock }
	out := standInBin(t, "claude-out")
	_ = RequireFresh(context.Background(), "claude", out)
	st, ok := Snapshot("claude")
	require.True(t, ok)
	assert.False(t, st.SignedIn)
	assert.Equal(t, clock, st.CheckedAt)
	_, ok = Snapshot("codex")
	assert.False(t, ok, "another CLI has no answer")

	clock = clock.Add(time.Minute) // a later answer for the same CLI replaces it
	standInBin(t, "claude-in")
	require.NoError(t, RequireFresh(context.Background(), "claude", out))
	st, _ = Snapshot("claude")
	assert.True(t, st.SignedIn)
}

// A user who is credentialed without Claude's own login (an API key, a token, a key helper, a cloud provider) is never asked about and never refused.
func TestCredentialedOutsideLoginIsNeverRefused(t *testing.T) {
	for _, k := range credentialEnv {
		t.Run("env "+k, func(t *testing.T) {
			reset(t)
			t.Setenv(k, "x")
			log := filepath.Join(t.TempDir(), "log")
			t.Setenv("AGENTSIGNIN_LOG", log)
			bin := standInBin(t, "claude-out")
			assert.Equal(t, Unknown, Check(context.Background(), "claude", bin))
			assert.NoError(t, Require(context.Background(), "claude", bin))
			_, err := os.Stat(log)
			assert.True(t, os.IsNotExist(err), "the CLI was not even asked")
		})
	}
	for name, body := range map[string]string{
		"apiKeyHelper":     `{"apiKeyHelper":"/usr/local/bin/get-key"}`,
		"settings env key": `{"env":{"ANTHROPIC_AUTH_TOKEN":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			reset(t)
			dir := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", dir)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600))
			bin := standInBin(t, "claude-out")
			assert.Equal(t, Unknown, Check(context.Background(), "claude", bin))
		})
	}
	t.Run("settings with neither", func(t *testing.T) {
		reset(t)
		dir := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"dark"}`), 0o600))
		bin := standInBin(t, "claude-out")
		assert.Equal(t, SignedOut, Check(context.Background(), "claude", bin))
	})
}

// A check that does not answer is its own state: the job is skipped with its own sentence and code, never "not signed in", the dashboard's line is not set, and it is asked again
// after the short timeout backoff, not the signed-out one.
func TestATimedOutCheckIsItsOwnRefusalNotSignedOut(t *testing.T) {
	reset(t)
	// The stand-in is this test binary run again, and it writes its log line only once its TestMain runs. Under a loaded full run
	// its start can take longer than a short deadline, which then kills it before it logs, so no wait finds the line (#158: 0 asks
	// after 15 s). 3 s is long enough for that start and is still a timeout of the hanging stand-in, which is what is checked.
	checkTimeout = 3 * time.Second
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("AGENTSIGNIN_LOG", log)
	bin := standInBin(t, "hang")
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return clock }
	asked := func() int {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "\n")
	}

	// The stand-in writes its log line when it STARTS, which can be after Require has returned, so a count taken right after
	// Require could still read 0 (#83). askedAfter waits for the line it expects
	// (at most 15 s) and then a moment more, so an extra ask would show.
	askedAfter := func(want int) int {
		for deadline := time.Now().Add(15 * time.Second); asked() < want && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		return asked()
	}

	err := Require(context.Background(), "claude", bin)
	var refused *NotSignedInError
	require.ErrorAs(t, err, &refused)
	assert.True(t, refused.TimedOut)
	assert.Equal(t, "Claude did not answer the sign-in check", err.Error())
	assert.NotContains(t, err.Error(), "not signed in")
	assert.Equal(t, "signin_check_timeout", refused.Code())
	assert.Equal(t, "Codex did not answer the sign-in check", (&NotSignedInError{Agent: "codex", TimedOut: true}).Error())
	assert.Equal(t, "not_signed_in", (&NotSignedInError{Agent: "claude"}).Code())
	assert.True(t, IsNotSignedIn(err), "still a refusal for the callers")

	_, reported := Snapshot("claude")
	assert.False(t, reported, "a timeout says nothing about the sign-in: the dashboard line is not set")

	for i := 0; i < 5; i++ { // inside the backoff nothing is asked
		assert.Error(t, Require(context.Background(), "claude", bin))
	}
	assert.Equal(t, 1, askedAfter(1))
	clock = clock.Add(timeoutBackoff - time.Second)
	assert.Error(t, Require(context.Background(), "claude", bin))
	assert.Equal(t, 1, askedAfter(1))
	clock = clock.Add(2 * time.Second) // the short backoff is over: asked again (30 s, not the signed-out doubling)
	assert.Error(t, Require(context.Background(), "claude", bin))
	assert.Equal(t, 2, askedAfter(2))

	// And a later answer replaces it. The short deadline above was for the hanging stand-in: a stand-in that answers must not race it (the race detector makes this binary start slowly).
	checkTimeout = 20 * time.Second
	standInBin(t, "claude-in")
	require.NoError(t, RequireFresh(context.Background(), "claude", bin))
	st, ok := Snapshot("claude")
	require.True(t, ok)
	assert.True(t, st.SignedIn)
}

// The check reads loggedIn: output that lacks it (a renamed field) is Unknown and says so in the log once, so that it is never a silent off switch; the CLI's own shape is read.
func TestStatusOutputWithoutLoggedInIsUnknownAndSaysSo(t *testing.T) {
	assert.Equal(t, SignedIn, claudeState([]byte(`{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty"}`), nil, nil))
	assert.Equal(t, SignedOut, claudeState([]byte(`{"loggedIn":false,"authMethod":"none"}`), nil, errors.New("exit status 1")))

	var logged bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	noLoggedInOnce = sync.Once{}
	assert.Equal(t, Unknown, claudeState([]byte(`{"logged_in":true}`), nil, nil), "a renamed field is not read as signed in")
	assert.Equal(t, Unknown, claudeState([]byte(`{"authMethod":"claude.ai"}`), nil, nil))
	assert.Equal(t, 1, strings.Count(logged.String(), "without a loggedIn field"), "said once")
}
