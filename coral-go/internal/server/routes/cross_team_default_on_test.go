package routes

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A refused relayed message is the uniform 404 "no such team" (every cause alike); a refused file is 403.
// #5916 (the Operator's decision): the cross-team messaging and file switches are kill switches that default to ON for a board that never set them, and an owner's explicit off stays
// off. The permission is the explicit Coral Hub team link (#5862): with no link, and no list on a hub that never advertised links, nothing is admitted, so the default opens nothing wider.

// newDefaultsDoorEnv is newDoorEnv without its switches turned on: the boards have never set them.
func newDefaultsDoorEnv(t *testing.T) *doorEnv {
	t.Helper()
	e := &doorEnv{linksEnv: newLinksEnv(t)}
	e.wire(e.h)
	_, err := e.bs.Subscribe(t.Context(), e.target, "Orchestrator", "Orchestrator", "tmux-door", nil, "mentions")
	require.NoError(t, err)
	_, err = e.bs.Subscribe(t.Context(), e.target2, "Orchestrator", "Orchestrator", "tmux-door2", nil, "mentions")
	require.NoError(t, err)
	return e
}

func TestBoardsThatNeverSetTheSwitchesTakeAMessageFromALinkedTeamAndNobodyElse(t *testing.T) {
	e := newDefaultsDoorEnv(t)
	for _, b := range []string{e.target, e.target2} {
		s, err := e.bs.GetBoardSettings(t.Context(), b)
		require.NoError(t, err)
		require.True(t, s.CrossTeamMessaging && s.CrossTeamArtifacts, "on by default")
		require.False(t, s.MessagingStored || s.ArtifactsStored, "never set")
	}
	// the hub advertises links but there is no snapshot, or no link for this team: refused, switch on or not
	code, _ := e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 404, code, "the switch defaulting on admits nobody: the link is the permission")
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	code, body := e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 200, code, body)
	code, _ = e.relay("lab2", srcTeam2, e.target, true, true)
	assert.Equal(t, 404, code, "another team of the same Coral is not linked")
	code, _ = e.relay("lab2", srcTeam1, e.target2, true, true)
	assert.Equal(t, 404, code, "nor is this team linked to the other board")
	code, _ = e.relay("lab3", srcTeam1, e.target, true, true)
	assert.Equal(t, 404, code, "nor another Coral")
}

func TestAnExplicitOffStaysOffWhateverTheLink(t *testing.T) {
	e := newDefaultsDoorEnv(t)
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	e.switchOn(e.target, false)
	code, _ := e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 404, code, "the owner turned messaging off: the kill switch holds against a link")
	e.switchOn(e.target, true)
	code, body := e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 200, code, body)
}

func TestTheOldServerListDoorIsNoWiderForTheDefault(t *testing.T) {
	e := newDefaultsDoorEnv(t)
	// a hub that never advertised links, a board that never set its switches, no list: nothing is admitted
	code, _ := e.relay("lab2", srcTeam1, e.target, false, true)
	assert.Equal(t, 404, code, "no list, no links: refused")
	code, _ = e.relay("lab3", srcTeam1, e.target, false, true)
	assert.Equal(t, 404, code)
	assert.False(t, e.seen, "this hub never advertised links")
	// a server list on a board that never stored "on" stays refused, as before the default: the old door needs the owner's own switch
	e.servers(e.target, "lab2")
	code, _ = e.relay("lab2", srcTeam1, e.target, false, true)
	assert.Equal(t, 404, code, "a list is not a switch: a board that never stored its switch does not take the listed server")
	// the owner's explicit on makes it the rule it always was: a listed server is admitted, another is not
	e.switchOn(e.target, true)
	code, body := e.relay("lab2", srcTeam1, e.target, false, true)
	assert.Equal(t, 200, code, body)
	code, _ = e.relay("lab3", srcTeam1, e.target, false, true)
	assert.Equal(t, 404, code, "a server that is not listed")
	// and an explicit off still decides
	e.switchOn(e.target, false)
	code, _ = e.relay("lab2", srcTeam1, e.target, false, true)
	assert.Equal(t, 404, code, "listed but switched off")
}

// The Messaging tab's two reaches are told apart: teams on other computers follow the default (on, with a Coral Hub link), teams on this computer need the explicit on.
func TestTheStateTellsTheTwoReachesOfTheSwitchApart(t *testing.T) {
	e := newDefaultsDoorEnv(t)
	get := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		e.r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/board/"+e.target+"/cross-team-state", nil))
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	st := get()
	assert.Equal(t, false, st["was_open"], "a board that never stored its switch was never open: no legacy notice (#5916)")
	assert.Equal(t, true, st["messaging_elsewhere"], "other computers: on by default (needs a link)")
	assert.Equal(t, false, st["messaging_here"], "this computer: off until turned on")
	e.switchOn(e.target, true)
	st = get()
	assert.Equal(t, true, st["was_open"], "an explicit on with no list: open, as the notice always said")
	assert.Equal(t, true, st["messaging_elsewhere"])
	assert.Equal(t, true, st["messaging_here"], "an explicit on")
	e.switchOn(e.target, false)
	st = get()
	assert.Equal(t, false, st["was_open"])
	assert.Equal(t, false, st["messaging_elsewhere"])
	assert.Equal(t, false, st["messaging_here"])
}

func TestTheSameComputerDoorStillNeedsAnExplicitOptIn(t *testing.T) {
	e := newCrossTeamEnv(t)
	code, out := e.send("alpha", "Orchestrator", "", "hi")
	require.Equal(t, 403, code, "two teams of one computer are not opened to each other by a default: %v", out)
	e.enable("alpha", true)
	e.enable("beta", true)
	code, _ = e.send("alpha", "Orchestrator", "", "hi")
	assert.Equal(t, 200, code, "an explicit opt-in on both boards")
}

func TestAFileFromALinkedTeamIsTakenByDefaultAndAnExplicitOffRefusesIt(t *testing.T) {
	e := &fileDoorEnv{doorEnv: newDefaultsDoorEnv(t), dir: t.TempDir()}
	e.h.coralDir = e.dir
	t.Cleanup(e.h.WaitBackground)
	e.r.Post("/api/board/{project}/external-artifacts", e.h.PostExternalArtifact)

	code, _ := e.file("lab2", srcTeam1, e.target, true)
	assert.Equal(t, 403, code, "no link yet: the default admits nothing")
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	code, body := e.file("lab2", srcTeam1, e.target, true)
	assert.Equal(t, 200, code, body)
	assert.Positive(t, e.storedCount())
	before := e.storedCount()
	e.filesOn(e.target, false)
	code, _ = e.file("lab2", srcTeam1, e.target, true)
	assert.Equal(t, 403, code, "the files switch was turned off explicitly")
	e.filesOn(e.target, true)
	e.switchOn(e.target, false)
	code, _ = e.file("lab2", srcTeam1, e.target, true)
	assert.Equal(t, 403, code, "and so was messaging")
	assert.Equal(t, before, e.storedCount(), "nothing refused was stored")
	// the old rule on a hub that never advertised links: no list, nothing
	f2 := &fileDoorEnv{doorEnv: newDefaultsDoorEnv(t), dir: t.TempDir()}
	f2.h.coralDir = f2.dir
	t.Cleanup(f2.h.WaitBackground)
	f2.r.Post("/api/board/{project}/external-artifacts", f2.h.PostExternalArtifact)
	code, _ = f2.file("lab2", srcTeam1, f2.target, false)
	assert.Equal(t, 403, code, "no list, no links: refused")
	f2.servers(f2.target, "lab2")
	code, _ = f2.file("lab2", srcTeam1, f2.target, false)
	assert.Equal(t, 403, code, "a list is not a switch: the old door needs the owner's own ons")
	f2.switchOn(f2.target, true)
	code, _ = f2.file("lab2", srcTeam1, f2.target, false)
	assert.Equal(t, 403, code, "messaging on but the files switch never stored")
	f2.filesOn(f2.target, true)
	code, body = f2.file("lab2", srcTeam1, f2.target, false)
	assert.Equal(t, 200, code, body)
	assert.Positive(t, f2.storedCount())
}
