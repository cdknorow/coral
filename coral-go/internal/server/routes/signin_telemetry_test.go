package routes

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/agentsignin"
	"github.com/subgentic/coral/internal/tracking"
)

// What the CLI's status command prints about the account: none of it may reach an event (#6062).
const (
	fixtureEmail = "someone.private@example.invalid"
	fixtureOrg   = "Example Private Org Ltd"
	fixtureToken = "sk-ant-oat01-FAKEFAKEFAKEFAKEFAKEFAKE0123456789"
)

func signInFixture(loggedIn string) string {
	return `{"loggedIn": ` + loggedIn + `, "email": "` + fixtureEmail + `", "orgName": "` + fixtureOrg + `", "token": "` + fixtureToken + `"}`
}

func signInEvents(sink *analyticsSink) []map[string]any {
	var out []map[string]any
	for _, e := range prereqEvents(sink) {
		if e["status"] == tracking.PrereqSignedIn || e["status"] == tracking.PrereqSignedOut {
			out = append(out, e)
		}
	}
	return out
}

func propertyKeys(e map[string]any) []string {
	var keys []string
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// A clear answer is counted with its closed status and nothing the CLI printed.
func TestSignInAnswerIsCountedWithOnlyToolStatusAndSource(t *testing.T) {
	for _, tc := range []struct {
		loggedIn, exit, status string
	}{{"false", "1", tracking.PrereqSignedOut}, {"true", "0", tracking.PrereqSignedIn}} {
		t.Run(tc.status, func(t *testing.T) {
			sink := captureAnalytics(t)
			// This checks the clear answer's telemetry, not how quickly a shell starts.
			// Reuse the healthy-probe fixture's isolated lookup and generous guard.
			clis := useFakeCLIs(t)
			authRuns := fakeClaude(t, signInFixture(tc.loggedIn), int(tc.exit[0]-'0'))
			clis.present["claude"] = filepath.Join(filepath.Dir(authRuns), "claude")
			t.Cleanup(agentsignin.SetCheckTimeoutForTests(time.Minute))
			body := cliCheckBody(t, "type=claude&signin=1")
			var reply map[string]any
			require.NoError(t, json.Unmarshal([]byte(body), &reply))
			require.Equal(t, probeOK, reply["probe"], "healthy fixture must answer before checking its telemetry")
			require.Equal(t, tc.loggedIn == "true", reply["signed_in"])
			// The handler enqueued tracking before it answered. Drain that work, rather
			// than polling for an event that a timed-out version probe never emits.
			tracking.WaitForAsyncForTest()
			events := signInEvents(sink)
			require.Len(t, events, 1)
			assert.Equal(t, tracking.ToolClaude, events[0]["tool"])
			assert.Equal(t, tc.status, events[0]["status"])
			assert.Equal(t, tracking.SourceLaunchProbe, events[0]["source"])
			// The event carries the three properties and nothing else of what was read; the transport adds only its own fixed ones, so check the three named ones are the CLI-derived set.
			for _, k := range []string{"email", "orgName", "org", "token", "plan", "path", "output", "message"} {
				assert.NotContains(t, propertyKeys(events[0]), k)
			}
			raw := sink.allRaw()
			for _, secret := range []string{fixtureEmail, fixtureOrg, fixtureToken, "loggedIn"} {
				assert.NotContains(t, raw, secret, "nothing the status command printed is sent")
			}
		})
	}
}

// A CLI that did not give a clear answer sends no status, and nothing is asked when the check was not requested.
func TestSignInIsNotCountedWithoutAClearAnswerOrARequest(t *testing.T) {
	t.Run("unreadable answer", func(t *testing.T) {
		sink := captureAnalytics(t)
		fakeClaude(t, `garbage`, 0)
		cliCheckBody(t, "type=claude&signin=1")
		tracking.WaitForAsyncForTest() // everything the request sent has been delivered; nothing is to be there
		assert.Empty(t, signInEvents(sink))
	})
	t.Run("not asked for", func(t *testing.T) {
		sink := captureAnalytics(t)
		fakeClaude(t, signInFixture("false"), 1)
		cliCheckBody(t, "type=claude")
		tracking.WaitForAsyncForTest() // everything the request sent has been delivered; nothing is to be there
		assert.Empty(t, signInEvents(sink))
	})
}

// Dedupe is per run by (tool, status, source): the same answer again sends nothing, a change of answer sends once.
func TestSignInStatusIsPartOfTheDedupeKey(t *testing.T) {
	sink := captureAnalytics(t)
	for i := 0; i < 3; i++ {
		tracking.TrackPrerequisite(tracking.ToolClaude, tracking.PrereqSignedOut, tracking.SourceLaunchProbe)
	}
	tracking.TrackPrerequisite(tracking.ToolClaude, tracking.PrereqSignedIn, tracking.SourceLaunchProbe)
	require.Eventually(t, func() bool { return len(signInEvents(sink)) >= 2 }, 15*time.Second, 5*time.Millisecond)
	tracking.WaitForAsyncForTest()
	assert.Len(t, signInEvents(sink), 2)
}
