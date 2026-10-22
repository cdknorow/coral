package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClaude puts a "claude" on PATH that prints a version and answers "auth status --json" with the given JSON and exit code.
func fakeClaude(t *testing.T, authJSON string, exit int) (authRuns string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = auth ]; then echo ran >> '" + filepath.Join(dir, "auth-runs") + "'; echo '" + authJSON + "'; exit " + string(rune('0'+exit)) + "; fi\necho 9.9.9 '(Claude Code)'\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755))
	t.Setenv("HOME", t.TempDir())
	for _, k := range credentialEnvForTest {
		t.Setenv(k, "")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	signInAskMu.Lock()
	delete(signInAskedAt, "claude")
	signInAskMu.Unlock()
	return filepath.Join(dir, "auth-runs")
}

var credentialEnvForTest = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}

func cliCheckBody(t *testing.T, query string) string {
	t.Helper()
	h := newSystemHandlerForTest(t)
	w := httptest.NewRecorder()
	h.CLICheck(w, httptest.NewRequest(http.MethodGet, "/api/system/cli-check?"+query, nil))
	require.Equal(t, http.StatusOK, w.Code)
	return w.Body.String()
}

// #6048: with signin=1 the CLI check also says whether a CLI that runs is signed in, asked fresh; without it the check does not run the sign-in command at all.
func TestCLICheckSaysWhetherAFoundCLIIsSignedIn(t *testing.T) {
	t.Run("signed out: signed_in false, and the CLI's own status command is what said it", func(t *testing.T) {
		fakeClaude(t, `{"loggedIn": false}`, 1)
		body := cliCheckBody(t, "type=claude&signin=1")
		assert.Contains(t, body, `"found":true`)
		assert.Contains(t, body, `"signed_in":false`)
	})
	t.Run("signed in: signed_in true", func(t *testing.T) {
		fakeClaude(t, `{"loggedIn": true}`, 0)
		assert.Contains(t, cliCheckBody(t, "type=claude&signin=1"), `"signed_in":true`)
	})
	t.Run("not asked for: no sign-in field and the status command is not run", func(t *testing.T) {
		fakeClaude(t, `{"loggedIn": false}`, 1)
		body := cliCheckBody(t, "type=claude")
		assert.NotContains(t, body, "signed_in")
		assert.NotContains(t, body, "signin_timeout")
	})
	t.Run("an answer that cannot be read says nothing", func(t *testing.T) {
		fakeClaude(t, `garbage`, 0)
		body := cliCheckBody(t, "type=claude&signin=1")
		assert.NotContains(t, strings.ToLower(body), `"signed_in":false`, "an unreadable answer is never 'not signed in'")
	})
}

// The route runs a process, so it is not a loop: requests at the same moment, and requests one after another inside the gap, run the CLI's status command once and share its answer.
func TestCLICheckSignInRunsTheStatusCommandOnceForABurst(t *testing.T) {
	runs := fakeClaude(t, `{"loggedIn": false}`, 1)
	h := newSystemHandlerForTest(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ { // twenty requests at once: the claim is atomic
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w := httptest.NewRecorder()
			h.CLICheck(w, httptest.NewRequest(http.MethodGet, "/api/system/cli-check?type=claude&signin=1", nil))
		}()
	}
	close(start)
	wg.Wait()
	for i := 0; i < 20; i++ { // and twenty more one after another inside the gap
		assert.Contains(t, cliCheckBody(t, "type=claude&signin=1"), `"signed_in":false`, "the answer already recorded is shared")
	}
	b, _ := os.ReadFile(runs)
	assert.Equal(t, 1, strings.Count(string(b), "ran"), "one run of the CLI's status command for forty requests")
}
