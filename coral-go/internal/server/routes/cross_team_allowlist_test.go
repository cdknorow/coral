package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
)

// #5508: who may message a board. With the allowlist in force a board takes a cross-team message only from a sender on its list of {server, cross_team_id}; one refusal for every cause.

// enforced switches the allowlist on for the env and returns the switch.
func (e *attachEnv) enforced(on bool) {
	var v atomic.Bool
	v.Store(on)
	e.h.SetAllowlistFlag(func(context.Context) bool { return v.Load() })
}

func (e *attachEnv) idOf(b string) string {
	e.t.Helper()
	id, ok, err := e.h.bs.CrossTeamID(context.Background(), b)
	require.NoError(e.t, err)
	require.True(e.t, ok, b)
	return id
}

func (e *attachEnv) setList(b string, list ...board.AllowedSender) {
	e.t.Helper()
	if list == nil {
		list = []board.AllowedSender{}
	}
	_, err := e.h.bs.UpdateBoardSettings(context.Background(), b, board.BoardSettingsUpdate{CrossTeamAllowed: &list})
	require.NoError(e.t, err)
}

// rawSend is send with the whole answer kept: status, headers and the exact bytes.
func (e *attachEnv) rawSend(from, to, sid, content string) (int, http.Header, []byte) {
	e.t.Helper()
	sender, sessionName := "Orchestrator", "claude-"+sid
	if sid == "operator" {
		sender, sessionName = "Operator", ""
	}
	b, _ := json.Marshal(map[string]any{"from_board": from, "subscriber_id": sender, "session_name": sessionName, "content": content})
	req, _ := http.NewRequest("POST", e.url+"/api/board/"+to+"/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if sid == "operator" {
		req.Header.Set("X-Test-Operator", "1")
	} else {
		req.Header.Set("X-Test-Session", sid)
		req.Header.Set("X-Test-Name", "Orchestrator")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

func TestAllowlistInForceAdmitsOnlyAListedSenderAndReadsTheListEverySend(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(true)
	alpha := board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")}

	code, _, raw := e.rawSend("alpha", "beta", alphaOrchSID, "hello")
	assert.Equal(t, http.StatusForbidden, code, "an empty list admits nobody")
	assert.Contains(t, string(raw), `"code":"cross_team_refused"`)

	e.setList("beta", alpha)
	code, _, raw = e.rawSend("alpha", "beta", alphaOrchSID, "hello")
	assert.Equal(t, http.StatusOK, code, string(raw))

	// Directional: beta is not on alpha's list.
	code, _, _ = e.rawSend("beta", "alpha", betaOrchSID, "hello back")
	assert.Equal(t, http.StatusForbidden, code, "A may message B says nothing of B messaging A")

	// Removed: the very next send is refused (no positive cache).
	e.setList("beta")
	code, _, _ = e.rawSend("alpha", "beta", alphaOrchSID, "hello again")
	assert.Equal(t, http.StatusForbidden, code, "removed: the next message is refused")
	// Another board's entry does not admit this one.
	e.setList("beta", board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("gamma")})
	code, _, _ = e.rawSend("alpha", "beta", alphaOrchSID, "x")
	assert.Equal(t, http.StatusForbidden, code)
}

// The old board switch means nothing once the list is in force: off or on, only the list admits.
func TestAllowlistInForceIgnoresTheOldReceivingSwitch(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(true)
	e.setList("beta", board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")})
	off := false
	_, err := e.h.bs.UpdateBoardSettings(context.Background(), "beta", board.BoardSettingsUpdate{CrossTeamMessaging: &off})
	require.NoError(t, err)
	code, _, raw := e.rawSend("alpha", "beta", alphaOrchSID, "hi")
	assert.Equal(t, http.StatusOK, code, "a listed sender is admitted: the list is what admits (%s)", raw)
	on := true
	_, err = e.h.bs.UpdateBoardSettings(context.Background(), "gamma", board.BoardSettingsUpdate{CrossTeamMessaging: &on})
	require.NoError(t, err)
	code, _, _ = e.rawSend("alpha", "gamma", alphaOrchSID, "hi")
	assert.Equal(t, http.StatusForbidden, code, "the switch on with an empty list admits nobody")
}

// A deleted and re-created board of the same name is a NEW board: the old entry does not name it.
func TestAListedBoardThatIsRecreatedIsNotListed(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(true)
	e.setList("beta", board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")})
	code, _, _ := e.rawSend("alpha", "beta", alphaOrchSID, "hi")
	require.Equal(t, http.StatusOK, code)
	old := e.idOf("alpha")
	require.NoError(t, e.h.bs.DeleteProject(context.Background(), "alpha"))
	_, err := e.h.bs.Subscribe(context.Background(), "alpha", "Orchestrator", "Orchestrator", "claude-"+alphaOrchSID, nil, "mentions") // the same name again: a new board row
	require.NoError(t, err)
	on := true
	_, err = e.h.bs.UpdateBoardSettings(context.Background(), "alpha", board.BoardSettingsUpdate{CrossTeamMessaging: &on})
	require.NoError(t, err)
	assert.NotEqual(t, old, e.idOf("alpha"), "same name, a new id")
	code, _, _ = e.rawSend("alpha", "beta", alphaOrchSID, "hi again")
	assert.Equal(t, http.StatusForbidden, code, "the re-created board inherits nothing")
}

// One answer for every cause: byte for byte the same status, headers and body, and the same steps taken in the same order.
func TestEveryRefusalIsTheSameAnswerTakingTheSameSteps(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(true)
	e.setList("beta", board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("gamma")}) // alpha is NOT on beta's list
	// A listed sender, but nobody to tell: gamma is listed on delta (a board with no Orchestrator).
	_, err := e.h.bs.Subscribe(context.Background(), "delta", "Dev", "Backend Dev", "claude-dddd", nil, "mentions")
	require.NoError(t, err)
	e.setList("delta", board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")})

	var steps []string
	doorTrace = func(s string) { steps = append(steps, s) }
	defer func() { doorTrace = nil }()

	type answer struct {
		code  int
		hdr   http.Header
		body  string
		steps []string
	}
	ask := func(to string) answer {
		steps = nil
		code, hdr, raw := e.rawSend("alpha", to, alphaOrchSID, "same text")
		return answer{code, hdr, string(raw), append([]string(nil), steps...)}
	}
	cases := map[string]answer{
		"unlisted sender":       ask("beta"),
		"unknown target board":  ask("no-such-board"),
		"a board with nobody":   ask("delta"),
		"an empty list (gamma)": ask("gamma"),
	}
	// A deleted board: its row and its subscribers are gone.
	require.NoError(t, e.h.bs.DeleteProject(context.Background(), "gamma"))
	cases["a deleted board"] = ask("gamma")
	var first answer
	n := 0
	for name, a := range cases {
		assert.Equal(t, http.StatusForbidden, a.code, name)
		assert.Equal(t, []string{"exists", "settings", "list", "recipients"}, a.steps, name+": the same four steps in the same order")
		if n == 0 {
			first = a
		}
		n++
		assert.Equal(t, first.body, a.body, name+": the same bytes")
		assert.Equal(t, first.hdr.Get("Content-Type"), a.hdr.Get("Content-Type"), name)
		assert.Equal(t, first.hdr.Get("Content-Length"), a.hdr.Get("Content-Length"), name+": the same size")
	}
	assert.NotContains(t, first.body, "beta")
	assert.NotContains(t, first.body, "alpha")
	assert.NotContains(t, first.body, "x-")
}

// The verified local Operator is a person, not a team: not limited by the list.
func TestLocalOperatorIsNotLimitedByTheList(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(true)
	code, _, raw := e.rawSend("alpha", "beta", "operator", "from the Operator")
	assert.Equal(t, http.StatusOK, code, string(raw))
}

// Files and the old switch: the sender's own switch off keeps its own specific message (about itself only).
func TestSenderOwnSwitchOffKeepsItsOwnMessage(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(true)
	e.setList("beta", board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")})
	off := false
	_, err := e.h.bs.UpdateBoardSettings(context.Background(), "alpha", board.BoardSettingsUpdate{CrossTeamMessaging: &off})
	require.NoError(t, err)
	code, _, raw := e.rawSend("alpha", "beta", alphaOrchSID, "hi")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, string(raw), `Cross-team messaging is off on \"alpha\"`, "the sender is told about ITS OWN settings")
	assert.NotContains(t, string(raw), "cross_team_refused")
}

var fixedNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// A refused send costs the sender's rate token as an accepted one does (SPEC-6).
func TestARefusedSendCostsTheSameRateTokenAsAnAcceptedOne(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(true)
	e.h.crossTeam.now = func() time.Time { return fixedNow } // a still clock: no token comes back
	e.setList("beta")
	var refused, limited int
	for i := 0; i < 12; i++ {
		code, _, _ := e.rawSend("alpha", "beta", alphaOrchSID, "x")
		switch code {
		case http.StatusForbidden:
			refused++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	assert.Positive(t, refused, "refused while there are tokens")
	assert.Positive(t, limited, "and then rate limited, though nothing was ever accepted: refusals spend tokens")

	e2 := newAttachEnv(t, false)
	e2.enforced(true)
	e2.h.crossTeam.now = func() time.Time { return fixedNow }
	e2.setList("beta", board.AllowedSender{Server: board.LocalServer, CrossTeamID: e2.idOf("alpha")})
	var ok, limited2 int
	for i := 0; i < 12; i++ {
		code, _, _ := e2.rawSend("alpha", "beta", alphaOrchSID, "x")
		switch code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited2++
		}
	}
	assert.Equal(t, refused, ok, "the same number of sends get through when they are refused as when they are accepted")
	assert.Equal(t, limited, limited2)
}

// With the flag off: today's rules, and what the list would have refused is counted as "would refuse" (shadow), once an hour.
func TestFlagOffKeepsTodaysRulesAndCountsWhatWouldBeRefused(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(false)
	for i := 0; i < 3; i++ {
		code, _, raw := e.rawSend("alpha", "beta", alphaOrchSID, "today's rules")
		assert.Equal(t, http.StatusOK, code, string(raw))
	}
	e.h.WaitBackground() // the count is written after the answer
	log, err := e.h.bs.ListLog(context.Background(), "beta")
	require.NoError(t, err)
	require.Len(t, log, 1, "one shadow row for the hour")
	assert.Equal(t, board.LogWouldRefuse, log[0].Kind)
	assert.Equal(t, 3, log[0].Count)
	assert.Equal(t, []board.AllowedSender{{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")}}, log[0].Senders, "ids only")
	// Listed: nothing to count.
	e.setList("gamma", board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")})
	code, _, _ := e.rawSend("alpha", "gamma", alphaOrchSID, "listed")
	assert.Equal(t, http.StatusOK, code)
	e.h.WaitBackground() // the count is written after the answer
	log, _ = e.h.bs.ListLog(context.Background(), "gamma")
	assert.Empty(t, log)
}

// In force, refusals are counted (not content, not labels): one row an hour.
func TestRefusedSendsAreCountedOncePerHourWithIDsOnly(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(true)
	for i := 0; i < 3; i++ {
		code, _, _ := e.rawSend("alpha", "beta", alphaOrchSID, "secret text "+strings.Repeat("x", i))
		assert.Equal(t, http.StatusForbidden, code)
	}
	e.h.WaitBackground() // the count is written after the answer
	log, err := e.h.bs.ListLog(context.Background(), "beta")
	require.NoError(t, err)
	require.Len(t, log, 1)
	assert.Equal(t, board.LogRefused, log[0].Kind)
	assert.Equal(t, 3, log[0].Count)
	raw, _ := json.Marshal(log)
	assert.NotContains(t, string(raw), "secret text")
	assert.NotContains(t, string(raw), `"alpha"`, "no label")
}
