package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5572: one table of words naming the screen, one refusal toward a sender on another Coral, and the owner's status line.

func TestTheTextsNameTheScreenAndTheSwitchInTheSameWords(t *testing.T) {
	assert.Equal(t, `Cross-team messaging is off on "alpha". Turn it on in Team settings > Messaging.`, textCrossTeamOff([]string{`"alpha"`}))
	assert.Equal(t, `Cross-team messaging is off on "alpha" and "beta". Turn it on in Team settings > Messaging.`, textCrossTeamOff([]string{`"alpha"`, `"beta"`}))
	assert.Equal(t, `Sharing files with other teams is off on "alpha". Turn on Share files with other teams in Team settings > Messaging.`, textFilesOff([]string{`"alpha"`}))
	assert.Contains(t, textFilesTurnedOffMidSend(`"alpha"`, "attachment 1 of 2"), "Team settings > Messaging")
	assert.Equal(t, "This Coral is not connected to Coral Hub. Connect it in Settings > Coral Hub.", textNotLinked)
	for name, text := range map[string]string{
		"off": textCrossTeamOff([]string{"x"}), "files": textFilesOff([]string{"x"}), "mid": textFilesTurnedOffMidSend("x", "y"), "unlinked": textNotLinked,
		"notfound": textNotFoundExplained("homelab/qa"),
	} {
		for _, old := range []string{"cross_team_artifacts", "cross_team_messaging", "the Operator can", "in Team settings on this computer"} {
			assert.NotContains(t, text, old, name+": a setting key or an old phrase is not the screen's name")
		}
	}
	// the screen is named wherever a person can fix it, and the sender-side text names both ends without saying which it was
	nf := textNotFoundExplained("homelab/qa")
	assert.Contains(t, nf, "homelab/qa")
	assert.Contains(t, nf, "Team links in Coral Hub")
	assert.Contains(t, nf, "Team settings > Messaging")
}

func TestTheSenderIsToldTheSameForEveryRefusalTheHubGives(t *testing.T) {
	want := textNotFoundExplained("homelab/qa")
	assert.Equal(t, want, textForHubRefusal("not_found", "homelab/qa"))
	for _, code := range []string{"target_refused", "target_refused:source_not_allowed", "target_refused:cross_team_disabled", "target_refused:anything"} {
		assert.Equal(t, want, textForHubRefusal(code, "homelab/qa"), code+": a reason from the hub is never passed on")
	}
	assert.Equal(t, textTargetOffline, textForHubRefusal("target_offline", "homelab/qa"))
	assert.Equal(t, textHubRefused, textForHubRefusal("hub_error", "homelab/qa"))
	assert.Equal(t, textHubRefused, textForHubRefusal("rate_limited:target_busy", "homelab/qa"))
}

func TestTheOwnersStatusLineByState(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		linkable, on, stale  bool
		linked               int
		heard, state, within string
	}{
		{"an old team, whatever else", false, true, false, 3, "", statusOld, "made before Coral Hub links existed"},
		{"an old team that is off", false, false, false, 0, "", statusOld, "cannot use the relay"},
		{"off", true, false, false, 2, "", statusOff, "Turn it on in Team settings > Messaging."},
		{"off wins over stale", true, false, true, 2, "", statusOff, "off for this team"},
		{"stale, when it last heard", true, true, true, 2, "today 14:02", statusStale, "Waiting for Coral Hub: messages from other teams are refused until this computer reconnects. Last heard from Coral Hub today 14:02. Check the connection in Settings > Coral Hub."},
		{"stale, never heard", true, true, true, 2, "", statusStale, "reconnects. Check the connection in Settings > Coral Hub."},
		{"waiting", true, true, false, 0, "", statusWaiting, "No team on another Coral is linked to this team yet."},
		{"ready, one", true, true, false, 1, "", statusReady, "1 team on another Coral can message this team."},
		{"ready, two", true, true, false, 2, "", statusReady, "2 teams on other Corals can message this team."},
	} {
		got := statusFor(tc.linkable, tc.on, tc.linked, tc.stale, tc.heard)
		assert.Equal(t, tc.state, got.State, tc.name)
		assert.Contains(t, got.Text, tc.within, tc.name)
	}
}

func TestCrossTeamStateCarriesTheStatusTheLinksAndWhetherTheTeamCanBeLinked(t *testing.T) {
	e := newAttachEnv(t, false)
	code, st := e.get("/api/board/beta/cross-team-state", opHdr)
	require.Equal(t, http.StatusOK, code)
	status, _ := st["status"].(map[string]any)
	require.NotNil(t, status)
	assert.Equal(t, statusOld, status["state"], "the test boards are named, not b- ids: they cannot use the relay")
	assert.Equal(t, false, st["linkable"])
	assert.NotNil(t, st["links"])
}

// ---- toward a sender on another Coral: one answer, the same steps, the same limiter ----

func relayedRec(e *artifactEnv, src, target, id, subscriber, to string) *httptest.ResponseRecorder {
	e.t.Helper()
	body := map[string]any{"from_board": "alpha", "subscriber_id": subscriber, "content": "hello", "relay": map[string]any{"message_id": id, "hops": 0}}
	if to != "" {
		body["to"] = to
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/board/"+target+"/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.HubServerHeader, src)
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	return rec
}

func TestEveryCauseOfARelayedRefusalIsTheSameBytesHeadersAndSteps(t *testing.T) {
	e := newArtifactEnv(t)
	var steps []string
	doorTrace = func(s string) { steps = append(steps, s) }
	defer func() { doorTrace = nil }()
	asked := func(target, to string) (*httptest.ResponseRecorder, []string) {
		steps = nil
		rec := relayedRec(e, relaySrc, target, "id-"+strings.Repeat("z", len(steps)+1), "Orchestrator", to)
		return rec, append([]string(nil), steps...)
	}
	e.h.crossTeam.now = func() time.Time { return fixedNow }

	// 1 no such team, 2 a team with its switch off, 3 a team that does not take this server, 4 a team that takes it but has no such Orchestrator
	var recs []*httptest.ResponseRecorder
	var trace [][]string
	add := func(rec *httptest.ResponseRecorder, s []string) { recs, trace = append(recs, rec), append(trace, s) }
	add(asked("nowhere", ""))
	add(asked("beta", ""))
	code, out := e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_messaging": true}, false)
	require.Equal(t, http.StatusOK, code, out)
	e.h.crossTeam.now = func() time.Time { return fixedNow.Add(time.Minute) }
	add(asked("beta", ""))
	code, out = e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_servers": []string{relaySrc}}, false)
	require.Equal(t, http.StatusOK, code, out)
	e.h.crossTeam.now = func() time.Time { return fixedNow.Add(2 * time.Minute) }
	add(asked("beta", "QA"))

	for i, rec := range recs {
		assert.Equal(t, http.StatusNotFound, rec.Code, "case %d", i)
		assert.Equal(t, recs[0].Body.String(), rec.Body.String(), "case %d: the same bytes", i)
		assert.Equal(t, recs[0].Header(), rec.Header(), "case %d: the same headers", i)
		assert.Equal(t, []string{"exists", "settings", "list", "recipients"}, trace[i], "case %d: the same four reads in the same order", i)
	}
	assert.JSONEq(t, `{"ok":false,"error":"not_found","message":"no such team"}`, recs[0].Body.String())
	assert.NotContains(t, recs[0].Body.String(), "beta")
	assert.NotContains(t, recs[0].Body.String(), "nowhere")
}

// The sender's rate token is taken before anything about the target is looked up: a missing team and a team that is switched off trip the limiter after the same number of sends.
func TestTheRateLimiterTripsAtTheSameCountForAMissingTeamAndAnExistingOne(t *testing.T) {
	count := func(target string) int {
		e := newArtifactEnv(t)
		e.h.crossTeam.now = func() time.Time { return fixedNow }
		n := 0
		for i := 0; i < 40; i++ {
			rec := relayedRec(e, relaySrc, target, "c"+strings.Repeat("y", i+1), "Orchestrator", "")
			if rec.Code == http.StatusTooManyRequests {
				return n
			}
			require.Equal(t, http.StatusNotFound, rec.Code)
			n++
		}
		return -1
	}
	missing, off := count("nowhere"), count("beta")
	require.Positive(t, missing, "the limiter trips")
	assert.Equal(t, missing, off, "a 429 that only an existing team could earn would be the probe the single word removes")
}

// The switch is a cause of its own: a team that takes this server but has its cross-team switch OFF refuses like every other cause, and the same team with the switch on admits the
// message. (The four-cause test cannot tell the switch from the list: the first two causes there are also not on the list.)
func TestARelayedMessageToAListedTeamNeedsItsSwitchOn(t *testing.T) {
	e := newArtifactEnv(t)
	e.h.crossTeam.now = func() time.Time { return fixedNow }
	code, out := e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_servers": []string{relaySrc}}, false)
	require.Equal(t, http.StatusOK, code, out)
	off := relayedRec(e, relaySrc, "beta", "sw-"+strings.Repeat("a", 3), "Orchestrator", "")
	assert.Equal(t, http.StatusNotFound, off.Code, "listed but switched off: refused like any other cause")
	assert.JSONEq(t, `{"ok":false,"error":"not_found","message":"no such team"}`, off.Body.String())
	e.h.crossTeam.now = func() time.Time { return fixedNow.Add(time.Minute) }
	code, out = e.put("/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_messaging": true}, false)
	require.Equal(t, http.StatusOK, code, out)
	on := relayedRec(e, relaySrc, "beta", "sw-"+strings.Repeat("b", 3), "Orchestrator", "")
	assert.NotEqual(t, http.StatusNotFound, on.Code, "listed and switched on: admitted (%d %s)", on.Code, on.Body.String())
}

func TestHeardWordsGivesTheDateOnlyWhenItIsNotToday(t *testing.T) {
	now := time.Date(2026, 10, 22, 16, 0, 0, 0, time.Local)
	assert.Equal(t, "", heardWords(time.Time{}, now))
	assert.Equal(t, "today 09:05", heardWords(time.Date(2026, 10, 22, 9, 5, 0, 0, time.Local), now))
	assert.Equal(t, "21 Oct 23:59", heardWords(time.Date(2026, 10, 21, 23, 59, 0, 0, time.Local), now))
	assert.Equal(t, "22 Oct 2025 09:05", heardWords(time.Date(2025, 10, 22, 9, 5, 0, 0, time.Local), now), "another year says the year")
}
