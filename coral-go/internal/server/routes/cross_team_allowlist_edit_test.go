package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
)

// #5508: who may edit the list, and how an edit is checked, audited and limited.

func (e *attachEnv) put(path string, hdr map[string]string, body any) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", e.url+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (e *attachEnv) get(path string, hdr map[string]string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.url+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

var (
	opHdr    = map[string]string{"X-Test-Operator": "1"}
	agentHdr = func(sid string) map[string]string {
		return map[string]string{"X-Test-Session": sid, "X-Test-Name": "Orchestrator"}
	}
)

func listBody(who string, entries ...board.AllowedSender) map[string]any {
	if entries == nil {
		entries = []board.AllowedSender{} // an empty list is [], never null (null is "no change")
	}
	return map[string]any{"subscriber_id": who, "cross_team_allowed": entries}
}

// Only the verified Operator edits the list: an agent token (an Orchestrator's included, whatever name it gives) and the hub's owner and users are refused AT THE FIELD, and a body that mixes
// an allowed field with it is refused whole, nothing applied.
func TestOnlyTheOperatorEditsTheListAndAMixedBodyIsRefusedWhole(t *testing.T) {
	e := newAttachEnv(t, false)
	alpha := board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")}

	for name, c := range map[string]struct {
		hdr map[string]string
		who string
	}{
		"an agent token":                  {agentHdr(betaOrchSID), "Orchestrator"},
		"an Orchestrator naming Operator": {agentHdr(betaOrchSID), "Operator"},
		"no identity, a team member name": {nil, "QA"},
	} {
		code, _ := e.put("/api/board/beta/settings", c.hdr, listBody(c.who, alpha))
		assert.Equal(t, http.StatusForbidden, code, name)
		code, _ = e.put("/api/board/beta/settings", c.hdr, map[string]any{"subscriber_id": c.who, "cross_team_messaging": true})
		assert.Equal(t, http.StatusForbidden, code, name+": the old switch is the same field class")
	}
	set, _ := e.h.bs.GetBoardSettings(context.Background(), "beta")
	assert.Empty(t, set.CrossTeamAllowed, "nothing was applied")

	// Mixed: a health setting an owner may send, with the list: refused whole.
	code, _ := e.put("/api/board/beta/settings", agentHdr(betaOrchSID), map[string]any{"subscriber_id": "Orchestrator", "cross_team_allowed": []board.AllowedSender{alpha}, "cross_team_artifacts": true})
	assert.Equal(t, http.StatusForbidden, code)
	set, _ = e.h.bs.GetBoardSettings(context.Background(), "beta")
	assert.False(t, set.CrossTeamArtifacts, "the allowed field of a refused mixed body was not applied either")

	code, out := e.put("/api/board/beta/settings", opHdr, listBody("Operator", alpha))
	require.Equal(t, http.StatusOK, code, out)
	set, _ = e.h.bs.GetBoardSettings(context.Background(), "beta")
	assert.Equal(t, []board.AllowedSender{alpha}, set.CrossTeamAllowed)
}

// Through Coral Hub the list is never editable, the owner's session included.
func TestTheListCannotBeEditedThroughCoralHub(t *testing.T) {
	_, h := setupBoardTestServer(t)
	withLabel(t, ownerLabel, nil)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "team", "Dev", "Developer", "dev-session", nil, "all")
	require.NoError(t, err)
	id, ok, _ := h.bs.CrossTeamID(ctx, "team")
	require.True(t, ok)
	other := "x-" + strings.Repeat("q", 16)
	body := `{"subscriber_id":"Operator","cross_team_allowed":[{"server":"local","cross_team_id":"` + other + `"}]}`
	for name, hdr := range map[string]map[string]string{"the hub owner": ownerHeaders(ownerLabel), "a hub user": granteeHeaders()} {
		code, out := boardCall(t, h, http.MethodPut, body, hdr)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.NotEmpty(t, out["code"], name)
	}
	set, _ := h.bs.GetBoardSettings(ctx, "team")
	assert.Empty(t, set.CrossTeamAllowed)
	_ = id
}

func TestTheListIsCheckedAsAWholeAndNothingChangesOnAnError(t *testing.T) {
	e := newAttachEnv(t, false)
	alpha := board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")}
	gamma := board.AllowedSender{Server: "hub-1", CrossTeamID: e.idOf("gamma")}
	code, out := e.put("/api/board/beta/settings", opHdr, listBody("Operator", alpha, gamma))
	require.Equal(t, http.StatusOK, code, out)

	for name, list := range map[string][]board.AllowedSender{
		"a name, not an id":   {alpha, {Server: board.LocalServer, CrossTeamID: "alpha"}},
		"twice":               {alpha, alpha},
		"itself":              {{Server: board.LocalServer, CrossTeamID: e.idOf("beta")}},
		"a bad server":        {{Server: "no way", CrossTeamID: e.idOf("alpha")}},
		"a server alone":      {{Server: "hub-1"}},
		"an id alone":         {{CrossTeamID: e.idOf("alpha")}},
		"more than 200 items": tooMany(201),
	} {
		code, out := e.put("/api/board/beta/settings", opHdr, listBody("Operator", list...))
		assert.Equal(t, http.StatusBadRequest, code, name)
		assert.Equal(t, "bad_allowlist", out["code"], name)
		set, _ := e.h.bs.GetBoardSettings(context.Background(), "beta")
		assert.Equal(t, []board.AllowedSender{alpha, gamma}, set.CrossTeamAllowed, name+": the earlier list stands")
	}
	code, _ = e.put("/api/board/beta/settings", opHdr, listBody("Operator", tooMany(200)...))
	assert.Equal(t, http.StatusOK, code, "200 is the limit")
	code, _ = e.put("/api/board/beta/settings", opHdr, listBody("Operator"))
	assert.Equal(t, http.StatusOK, code, "an empty list clears it")
	set, _ := e.h.bs.GetBoardSettings(context.Background(), "beta")
	assert.Empty(t, set.CrossTeamAllowed)
	// null is no change, never a clear.
	e.setList("beta", alpha)
	code, _ = e.put("/api/board/beta/settings", opHdr, map[string]any{"subscriber_id": "Operator", "cross_team_allowed": nil})
	assert.Equal(t, http.StatusOK, code)
	set, _ = e.h.bs.GetBoardSettings(context.Background(), "beta")
	assert.Equal(t, []board.AllowedSender{alpha}, set.CrossTeamAllowed, "null leaves the list as it is")
}

func tooMany(n int) []board.AllowedSender {
	out := make([]board.AllowedSender, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, board.AllowedSender{Server: "hub-1", CrossTeamID: "x-" + strings.Repeat(string(rune('a'+i%26)), 8) + strings.Repeat(string(rune('a'+(i/26)%26)), 8)})
	}
	return out
}

// Every successful edit is audited by id; a refused edit counts once an hour; no more than 30 edits an hour per owner session.
func TestEditsAreAuditedRefusedEditsCountedOnceAnHourAndEditsAreLimited(t *testing.T) {
	e := newAttachEnv(t, false)
	alpha := board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")}
	code, out := e.put("/api/board/beta/settings", opHdr, listBody("Operator", alpha))
	require.Equal(t, http.StatusOK, code, out)
	code, out = e.put("/api/board/beta/settings", opHdr, listBody("Operator"))
	require.Equal(t, http.StatusOK, code, out)

	for i := 0; i < 20; i++ { // twenty refused edits (an agent token, a bad entry): ONE row
		e.put("/api/board/beta/settings", agentHdr(betaOrchSID), listBody("Orchestrator", alpha))
	}
	e.put("/api/board/beta/settings", opHdr, listBody("Operator", board.AllowedSender{Server: "local", CrossTeamID: "alpha"}))

	e.h.WaitBackground() // the count is written after the answer
	log, err := e.h.bs.ListLog(context.Background(), "beta")
	require.NoError(t, err)
	var edits, refused []board.LogEntry
	for _, l := range log {
		switch l.Kind {
		case board.LogEdit:
			edits = append(edits, l)
		case board.LogEditRefused:
			refused = append(refused, l)
		}
	}
	require.Len(t, edits, 2, "every successful edit")
	assert.Equal(t, "Operator", edits[0].Actor)
	assert.Equal(t, "cross_team_allowed", edits[0].Field)
	assert.Equal(t, "[]", edits[0].New)
	assert.Contains(t, edits[0].Old, alpha.CrossTeamID, "before and after, ids")
	require.Len(t, refused, 1, "refused edits: one row an hour")
	assert.Equal(t, 21, refused[0].Count)
	raw, _ := json.Marshal(log)
	assert.NotContains(t, string(raw), "Orchestrator", "no text of the refused requests")

	// 30 an hour per owner session (an attempt that reaches validation counts, 3 so far): the rest pass, the 31st is refused with a fixed word.
	for i := 0; i < 27; i++ {
		code, _ := e.put("/api/board/beta/settings", opHdr, listBody("Operator"))
		require.Equal(t, http.StatusOK, code, "edit %d", i+3)
	}
	code, out = e.put("/api/board/beta/settings", opHdr, listBody("Operator", alpha))
	assert.Equal(t, http.StatusTooManyRequests, code)
	assert.Equal(t, "edit_rate_limited", out["code"])
	set, _ := e.h.bs.GetBoardSettings(context.Background(), "beta")
	assert.Empty(t, set.CrossTeamAllowed, "the 31st changed nothing")
}

// The owner's reading routes: state (with suggestions, never applied), log and candidates are the Operator's alone.
func TestStateLogAndCandidatesAreTheOperatorsAndSuggestionsAreNeverApplied(t *testing.T) {
	e := newAttachEnv(t, false)
	// alpha messages beta today (flag off: allowed as today): beta now has a stored inbound message from alpha.
	code, _, raw := e.rawSend("alpha", "beta", alphaOrchSID, "hello")
	require.Equal(t, http.StatusOK, code, string(raw))

	for _, path := range []string{"/api/board/beta/cross-team-state", "/api/board/beta/settings/log", "/api/board/beta/cross-team-candidates"} {
		code, _ := e.get(path, agentHdr(betaOrchSID))
		assert.Equal(t, http.StatusForbidden, code, path+": an agent token, an Orchestrator's too")
		code, _ = e.get(path, opHdr)
		assert.Equal(t, http.StatusOK, code, path)
	}
	code, st := e.get("/api/board/beta/cross-team-state", opHdr)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, st["enforced"])
	assert.Equal(t, true, st["was_open"], "the board was open: its old switch is on and the list is empty")
	assert.EqualValues(t, 200, st["max_entries"])
	assert.Equal(t, e.idOf("beta"), st["self_cross_team_id"])
	sugg, _ := st["suggestions"].([]any)
	require.Len(t, sugg, 1)
	s0 := sugg[0].(map[string]any)
	assert.Equal(t, e.idOf("alpha"), s0["cross_team_id"])
	assert.Equal(t, "local", s0["server"])
	assert.Equal(t, "alpha", s0["label"])
	assert.EqualValues(t, 1, s0["count"])
	assert.Equal(t, true, s0["listable"])
	set, _ := e.h.bs.GetBoardSettings(context.Background(), "beta")
	assert.Empty(t, set.CrossTeamAllowed, "looking at the suggestions applied nothing")

	// Once listed, it is no longer a suggestion; the list shows its label.
	e.setList("beta", board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")})
	_, st = e.get("/api/board/beta/cross-team-state", opHdr)
	assert.Empty(t, st["suggestions"])
	allowed, _ := st["allowed"].([]any)
	require.Len(t, allowed, 1)
	assert.Equal(t, "alpha", allowed[0].(map[string]any)["label"])
	assert.Equal(t, false, st["was_open"], "a list with an entry is no longer 'open'")

	_, cand := e.get("/api/board/beta/cross-team-candidates", opHdr)
	cs, _ := cand["candidates"].([]any)
	var labels []string
	for _, c := range cs {
		m := c.(map[string]any)
		labels = append(labels, m["label"].(string))
		assert.Regexp(t, `^x-[a-z2-7]{16}$`, m["cross_team_id"])
		assert.Len(t, m, 3, "id, label and creation time: nothing about subscribers or content")
	}
	assert.ElementsMatch(t, []string{"alpha", "gamma"}, labels, "this Coral's other boards, never the board being edited")
}
