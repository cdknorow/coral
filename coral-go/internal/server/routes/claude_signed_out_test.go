package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/claudestub"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #5469 / #5472, the callers of this package: team generation (the import dialog's "describe your team") and theme generation each run `claude --print` from a
// request. While Claude is not signed in that run takes the CLI's login flow and blocks the user's own login, so neither may start one: each must answer within a few
// seconds, start no print-mode run and no login flow (the stand-in's call log says), and say Claude is not signed in. See internal/background for the other callers.
const failFastRoutes = 3 * time.Second

// hangBoundRoutes is the ceiling when the status command does not answer: its limit (shortened to 1 s by shortHang; production 6 s) passes, then the run is skipped as "did not answer", never as signed out.
const hangBoundRoutes = 4 * time.Second

// shortHang makes the status command's limit one second for this test when the stand-in hangs on purpose (claudestub.HangingAuth): what is tested, a run skipped as "did not answer" and never
// as signed out, does not need the production 6 s (#5604; agentsignin's own test pins the 6 s).
func shortHang(t *testing.T, state claudestub.State) {
	t.Helper()
	if state == claudestub.HangingAuth {
		t.Cleanup(agentsignin.SetCheckTimeoutForTests(time.Second))
	}
}

func boundFor(state claudestub.State) time.Duration {
	if state == claudestub.HangingAuth {
		return hangBoundRoutes
	}
	return failFastRoutes
}

// saysFor is what a refusal must say in a state: not signed in, or (the status command hung) that it did not answer, and then never "not signed in".
func saysFor(t *testing.T, state claudestub.State, text string) {
	t.Helper()
	if state == claudestub.HangingAuth {
		assert.Contains(t, strings.ToLower(text), "did not answer the sign-in check")
		assert.False(t, notSignedInText(text), "a check that did not answer is not 'signed out': %s", text)
		return
	}
	assert.True(t, notSignedInText(text), "says Claude is not signed in; got %s", text)
}

func notSignedInText(s string) bool { return strings.Contains(strings.ToLower(s), "not signed in") }

func TestTeamGenerationNeverStartsAClaudeRunWhileNotSignedIn(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	for _, state := range []claudestub.State{claudestub.SignedOut, claudestub.LoginInProgress, claudestub.HangingAuth} {
		t.Run(string(state), func(t *testing.T) {
			claudestub.Isolate(t)
			shortHang(t, state)
			stub := claudestub.New(t, state)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			start := time.Now()
			result, msg := runTeamGeneration(ctx, stub.Path(), "a team of three", t.TempDir())
			assert.Less(t, time.Since(start), boundFor(state), "comes back fast")
			assert.Nil(t, result)
			assert.Empty(t, stub.PrintRuns(), "no print-mode run was started")
			assert.Zero(t, stub.Grabs(), "no login flow was started or held")
			saysFor(t, state, msg)
		})
	}
}

func TestTeamGenerationStillAsksClaudeWhenSignedIn(t *testing.T) {
	claudestub.Isolate(t)
	stub := claudestub.New(t, claudestub.SignedIn)
	_, msg := runTeamGeneration(context.Background(), stub.Path(), "a team of three", t.TempDir())
	assert.False(t, notSignedInText(msg), "signed in: %q", msg) // the stub's plain answer is not a team: the reply's own parse error is fine
	runs := stub.PrintRuns()
	require.Len(t, runs, 1)
	assert.False(t, runs[0].TTY)
	assert.Zero(t, stub.Grabs())
}

func generateTheme(t *testing.T, path string) (*httptest.ResponseRecorder, time.Duration) {
	t.Helper()
	cfg := &config.Config{}
	cfg.UseCoralDir(t.TempDir())
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), path))
	h := NewThemesHandler(cfg)
	h.SetDB(db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/themes/generate", strings.NewReader(`{"description":"calm blue","agent_type":"claude"}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	start := time.Now()
	h.GenerateTheme(rec, req)
	return rec, time.Since(start)
}

func TestThemeGenerationNeverStartsAClaudeRunWhileNotSignedIn(t *testing.T) {
	for _, state := range []claudestub.State{claudestub.SignedOut, claudestub.LoginInProgress, claudestub.HangingAuth} {
		t.Run(string(state), func(t *testing.T) {
			claudestub.Isolate(t)
			shortHang(t, state)
			stub := claudestub.New(t, state)
			rec, took := generateTheme(t, stub.Path())
			assert.Less(t, took, boundFor(state), "comes back fast")
			assert.Empty(t, stub.PrintRuns(), "no print-mode run was started")
			assert.Zero(t, stub.Grabs(), "no login flow was started or held")
			assert.NotEqual(t, http.StatusOK, rec.Code)
			saysFor(t, state, rec.Body.String())
		})
	}
}

func TestThemeGenerationStillAsksClaudeWhenSignedIn(t *testing.T) {
	claudestub.Isolate(t)
	stub := claudestub.New(t, claudestub.SignedIn)
	rec, _ := generateTheme(t, stub.Path())
	assert.False(t, notSignedInText(rec.Body.String()), rec.Body.String())
	runs := stub.PrintRuns()
	require.Len(t, runs, 1)
	assert.False(t, runs[0].TTY)
	assert.Zero(t, stub.Grabs())
}
