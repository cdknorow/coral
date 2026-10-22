package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5862: the door for a message relayed from another Coral follows what the connection's hub advertised (DoorMode). cross_team_links/1 admits by the hub's link snapshot and the board's
// switch; a hub that advertised it before and no longer does is refused; a hub that never did keeps the board's server list. Every refusal is the same bytes, and the Messaging tab's
// "linked" teams are the ones the door admits.

type doorEnv struct {
	*linksEnv
	r     chi.Router
	n     int
	trace []string
}

func newDoorEnv(t *testing.T) *doorEnv {
	t.Helper()
	e := &doorEnv{linksEnv: newLinksEnv(t)}
	e.wire(e.h)
	_, err := e.bs.Subscribe(t.Context(), e.target, "Orchestrator", "Orchestrator", "tmux-door", nil, "mentions")
	require.NoError(t, err)
	_, err = e.bs.Subscribe(t.Context(), e.target2, "Orchestrator", "Orchestrator", "tmux-door2", nil, "mentions")
	require.NoError(t, err)
	e.switchOn(e.target, true)
	e.switchOn(e.target2, true)
	doorTrace = func(s string) { e.trace = append(e.trace, s) }
	t.Cleanup(func() { doorTrace = nil })
	return e
}

func (e *doorEnv) wire(h *BoardHandler) {
	clock := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	h.crossTeam.now = func() time.Time { clock = clock.Add(time.Hour); return clock } // the rate limit is not what is checked here
	e.r = chi.NewRouter()
	e.r.Post("/api/board/{project}/external-messages", h.PostExternalMessage)
	e.r.Get("/api/board/{project}/cross-team-state", h.GetCrossTeamState)
}

func (e *doorEnv) switchOn(board_ string, on bool) {
	e.t.Helper()
	_, err := e.bs.UpdateBoardSettings(context.Background(), board_, board.BoardSettingsUpdate{CrossTeamMessaging: &on})
	require.NoError(e.t, err)
}

func (e *doorEnv) servers(board_ string, servers ...string) {
	e.t.Helper()
	_, err := e.bs.UpdateBoardSettings(context.Background(), board_, board.BoardSettingsUpdate{CrossTeamServers: &servers})
	require.NoError(e.t, err)
}

// relay posts a relayed message to target from server src, whose sending team is team as Coral Hub names it (X-Coral-Hub-Source-Team; "" sends none). cap is the connector's capability
// header; stamp is the per-process stamp. The body carries a claim of its own cross-team id that is another team's, which the links door never reads.
func (e *doorEnv) relay(src, team, target string, cap, stamp bool) (int, string) {
	e.t.Helper()
	e.n++
	body := map[string]any{"from_board": "QA", "subscriber_id": "Orchestrator", "content": "hello", "relay": map[string]any{"message_id": fmt.Sprintf("m%d", e.n), "hops": 0},
		"from_cross_team_id": "x-" + strings.Repeat("f", 16)}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/board/"+target+"/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.HubServerHeader, src)
	if team != "" {
		req.Header.Set(tunnel.HubSourceTeamHeader, team)
	}
	if stamp {
		req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	}
	if cap {
		req.Header.Set(tunnel.HubLinksCapHeader, "1")
	}
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (e *doorEnv) snapshotOK(version int64, links ...snapshotLink) {
	e.t.Helper()
	require.Equal(e.t, 200, e.post(e.snapshot(version, links...), stamped(linkSessionA, true)).Code)
}

func TestTheLinksDoorAdmitsAFreshLinkAndRefusesEveryOtherCause(t *testing.T) {
	e := newDoorEnv(t)
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	code, _ := e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 200, code, "a linked team, the switch on, an Orchestrator to tell")

	refusals := map[string][2]any{}
	record := func(name string, code int, body string) { refusals[name] = [2]any{code, body} }
	c, b := e.relay("lab2", srcTeam2, e.target, true, true)
	record("another team", c, b)
	c, b = e.relay("lab2", "", e.target, true, true)
	record("no source team from the hub", c, b)
	c, b = e.relay("lab2", "QA-team", e.target, true, true)
	record("a source team that is not an id", c, b)
	c, b = e.relay("lab3", srcTeam1, e.target, true, true)
	record("another server", c, b)
	c, b = e.relay("lab2", srcTeam1, e.target2, true, true)
	record("a team it is not linked to", c, b)
	e.switchOn(e.target, false)
	c, b = e.relay("lab2", srcTeam1, e.target, true, true)
	record("the board's switch off", c, b)
	e.switchOn(e.target, true)
	c, b = e.relay("lab2", srcTeam1, "b-zzzzzzzzzz", true, true)
	record("no such team", c, b)
	// the board's own server list does not admit under links, and neither does its own list being empty matter
	e.servers(e.target2, "lab9")
	c, b = e.relay("lab9", srcTeam1, e.target2, true, true)
	record("on the server list but not linked", c, b)

	var first [2]any
	for name, r := range refusals {
		assert.Equal(t, 404, r[0], name)
		if first == ([2]any{}) {
			first = r
		}
		assert.Equal(t, first, r, name+": the same status and bytes as every other refusal")
	}
	assert.Len(t, refusals, 8)
}

func TestEveryCauseTakesTheSameStepsInTheSameOrder(t *testing.T) {
	e := newDoorEnv(t)
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	want := []string{"exists", "settings", "list", "recipients"}
	for name, run := range map[string]func(){
		"admitted":     func() { e.relay("lab2", srcTeam1, e.target, true, true) },
		"not linked":   func() { e.relay("lab2", srcTeam2, e.target, true, true) },
		"no such team": func() { e.relay("lab2", srcTeam1, "b-zzzzzzzzzz", true, true) },
		"refuse mode":  func() { e.relay("lab2", srcTeam1, e.target, false, true) },
	} {
		e.trace = nil
		run()
		assert.Equal(t, want, e.trace, name)
	}
}

func TestAStaleOrMissingSnapshotAdmitsNothing(t *testing.T) {
	e := newDoorEnv(t)
	// advertised, but no snapshot has arrived yet in this process
	code, _ := e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 404, code, "no snapshot: nothing is admitted")
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	code, _ = e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 200, code)
	e.advance(linkTTL - time.Second)
	code, _ = e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 200, code, "inside the TTL")
	e.advance(2 * time.Second)
	code, _ = e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 404, code, "a snapshot older than the TTL admits nothing")
	e.snapshotOK(2, e.link("lab2", srcTeam1, e.target))
	code, _ = e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 200, code, "a fresh one does again")
	e.snapshotOK(3) // a revoked link
	code, _ = e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 404, code, "the next snapshot without the link revokes it")
}

func TestADowngradedHubIsRefusedNotSentBackToTheOldDoor(t *testing.T) {
	e := newDoorEnv(t)
	e.servers(e.target, "lab2") // today's list would admit lab2
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	code, _ := e.relay("lab2", srcTeam1, e.target, false, true)
	assert.Equal(t, 404, code, "links were advertised in this process; this connection's hub does not advertise them: refused, though linked and on the server list")
	// and after a restart: the persisted flag, an empty table
	restarted := e.handler(e.bs)
	e.wire(restarted)
	code, _ = e.relay("lab2", srcTeam1, e.target, false, true)
	assert.Equal(t, 404, code, "a restart does not forget that the hub advertised them")
	code, _ = e.relay("lab2", srcTeam1, e.target, true, true)
	assert.Equal(t, 404, code, "advertised again, but no snapshot yet in this process")
}

func TestAHubThatNeverAdvertisedLinksKeepsTheServerList(t *testing.T) {
	e := newDoorEnv(t)
	e.servers(e.target, "lab2")
	code, _ := e.relay("lab2", srcTeam1, e.target, false, true)
	assert.Equal(t, 200, code, "the old door: the server list admits")
	code, _ = e.relay("lab3", srcTeam1, e.target, false, true)
	assert.Equal(t, 404, code, "and a server that is not listed is refused")
	assert.False(t, e.seen, "nothing was advertised")
}

func TestTheCapabilityHeaderWithoutTheStampIsNotTrusted(t *testing.T) {
	e := newDoorEnv(t)
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	code, body := e.relay("lab2", srcTeam1, e.target, true, false)
	assert.Equal(t, 403, code, "a relayed message without the per-process stamp is not Coral Hub's: "+body)
	assert.Equal(t, doorLinks, e.h.DoorMode(t.Context(), func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/x", nil)
		r.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
		r.Header.Set(tunnel.HubLinksCapHeader, "1")
		return r
	}()))
	forged := httptest.NewRequest(http.MethodPost, "/x", nil)
	forged.Header.Set(tunnel.HubLinksCapHeader, "1")
	assert.NotEqual(t, doorLinks, e.h.DoorMode(t.Context(), forged), "the header alone never selects the links door")
}

// The panel and the door read one table: a team the Messaging tab shows as linked is admitted, one it does not show is not.
func TestATeamTheMessagingTabShowsAsLinkedIsAdmitted(t *testing.T) {
	e := newDoorEnv(t)
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target), e.link("lab2", srcTeam2, e.target2))
	state := func(target string) []map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/board/"+target+"/cross-team-state", nil)
		rec := httptest.NewRecorder()
		e.r.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var out struct {
			Links []map[string]any `json:"links"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out.Links
	}
	for target, linkedTeam := range map[string]string{e.target: srcTeam1, e.target2: srcTeam2} {
		links := state(target)
		require.Len(t, links, 1, target)
		assert.Equal(t, linkedTeam, links[0]["team_id"])
		code, _ := e.relay(links[0]["server_id"].(string), links[0]["team_id"].(string), target, true, true)
		assert.Equal(t, 200, code, "shown as linked: admitted")
	}
	code, _ := e.relay("lab2", srcTeam2, e.target, true, true)
	assert.Equal(t, 404, code, "a team the tab does not show for this board is refused")
	assert.NotContains(t, strings.Join(func() []string {
		var ids []string
		for _, l := range state(e.target) {
			ids = append(ids, l["team_id"].(string))
		}
		return ids
	}(), ","), srcTeam2)
}

// The sending team is exactly one value of the hub's header: two (a hub value and one a sender slipped in, or a repeat) name no team.
func TestTwoSourceTeamValuesNameNoTeam(t *testing.T) {
	e := newDoorEnv(t)
	e.snapshotOK(1, e.link("lab2", srcTeam1, e.target))
	req := httptest.NewRequest(http.MethodPost, "/api/board/"+e.target+"/external-messages", strings.NewReader(`{"from_board":"QA","subscriber_id":"Orchestrator","content":"hi","relay":{"message_id":"two1","hops":0}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.HubServerHeader, "lab2")
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	req.Header.Set(tunnel.HubLinksCapHeader, "1")
	req.Header.Add(tunnel.HubSourceTeamHeader, srcTeam1)
	req.Header.Add(tunnel.HubSourceTeamHeader, srcTeam1)
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	assert.Equal(t, 404, rec.Code)
	assert.Equal(t, "", hubSourceTeam(req))
}
