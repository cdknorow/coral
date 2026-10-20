package agentsignin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	oldNow, oldPos, oldMin, oldMax, oldTO := now, positiveTTL, backoffMin, backoffMax, checkTimeout
	t.Cleanup(func() { now, positiveTTL, backoffMin, backoffMax, checkTimeout = oldNow, oldPos, oldMin, oldMax, oldTO })
}

func TestClaudeStatusIsReadFromTheCLIsOwnAnswer(t *testing.T) {
	reset(t)
	for mode, want := range map[string]State{
		"claude-in":      SignedIn,
		"claude-out":     SignedOut,
		"claude-text":    SignedOut, // the CLI's own words, no JSON
		"claude-old":     Unknown,   // cannot be asked: not treated as signed out
		"claude-garbage": Unknown,
		"hang":           SignedOut, // waiting on something (another claude's sign-in): a second claude must not start behind it
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
