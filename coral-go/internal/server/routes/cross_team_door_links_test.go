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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5572 patch 3: once a Coral Hub keeps the links, a hub link is the permission. The door admits exactly the linked (server, team) for the team the hub names, and nothing else; never the old list
// of servers; never on a stale table; never without the capability of THIS connection once a hub has advertised it.

const doorSrc = "lab2"

type doorEnv struct {
	*artifactEnv
	n int
}

// newDoorEnv: team beta takes messaging and files, a hub link lab2/srcTeam1 -> beta is applied (fresh), and the connection advertised the capability.
func newDoorEnv(t *testing.T) *doorEnv {
	t.Helper()
	e := &doorEnv{artifactEnv: newArtifactEnv(t)}
	e.h.crossTeam.now = func() time.Time { e.n += 1; return fixedNow.Add(time.Duration(e.n) * time.Hour) } // the rate limit is not what these test
	clock := time.Now()
	e.h.linkClock = func() time.Time { return clock }
	e.h.SetLinksSeen(func(context.Context) (bool, error) { return false, nil }, func(context.Context) error { return nil })
	_, err := e.h.bs.UpdateBoardSettings(context.Background(), "beta", board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(true), CrossTeamArtifacts: ptrBool(true)})
	require.NoError(t, err)
	e.link(srcTeam1)
	return e
}

func (e *doorEnv) link(srcTeam string) {
	e.h.links.apply("0123456789abcdef0123456789abcdef", 1, []teamLink{{doorSrc, srcTeam, "beta"}}, e.h.linkNow())
}

func (e *doorEnv) clockAdvance(d time.Duration) {
	base := e.h.linkNow()
	e.h.linkClock = func() time.Time { return base.Add(d) }
}

// relayed posts a relayed message to target, with the headers hdr on top of the stamp and the server header.
func (e *doorEnv) relayed(target, to string, hdr map[string]string, extra ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	e.n++
	body := map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "content": "hello", "relay": map[string]any{"message_id": fmt.Sprintf("door-%d", e.n), "hops": 0}}
	if to != "" {
		body["to"] = to
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/board/"+target+"/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	req.Header.Set(tunnel.HubServerHeader, doorSrc)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, v := range extra {
		req.Header.Add(tunnel.HubSourceTeamHeader, v)
	}
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	return rec
}

func linkedHdr(team string) map[string]string {
	return map[string]string{tunnel.HubLinksCapHeader: "1", tunnel.HubSourceTeamHeader: team}
}

func (e *doorEnv) refusedLikeEveryCause(rec *httptest.ResponseRecorder, why string) {
	e.t.Helper()
	assert.Equal(e.t, relayedRefusedStatus, rec.Code, why+": "+rec.Body.String())
}

func TestTheDoorAdmitsExactlyTheLinkedTeamTheHubNames(t *testing.T) {
	e := newDoorEnv(t)
	rec := e.relayed("beta", "", linkedHdr(srcTeam1))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// The old list of servers is not what decides any more: the server is listed there, the team is not linked.
	seedServers(t, e.h.bs, "beta", doorSrc)
	for name, hdr := range map[string]map[string]string{
		"another team of the same server":    linkedHdr(srcTeam2),
		"no source team header":              {tunnel.HubLinksCapHeader: "1"},
		"a name instead of an id":            linkedHdr("QA"),
		"a malformed id":                     linkedHdr("b-AAAAAAAAAA"),
		"no capability on this connection":   {tunnel.HubSourceTeamHeader: srcTeam1},
		"an empty source team":               linkedHdr(""),
		"the capability without the stamp's": nil,
	} {
		if hdr == nil {
			continue
		}
		rec := e.relayed("beta", "", hdr)
		e.refusedLikeEveryCause(rec, name)
	}
	// A repeated source-team header is refused, never "the first one".
	rec = e.relayed("beta", "", map[string]string{tunnel.HubLinksCapHeader: "1"}, srcTeam1, srcTeam1)
	e.refusedLikeEveryCause(rec, "a repeated header")
	// Another server, with a team that IS linked for lab2: the pair is what is linked.
	req := httptest.NewRequest("POST", "/api/board/beta/external-messages", strings.NewReader(`{"from_board":"alpha","subscriber_id":"Orchestrator","content":"hi","relay":{"message_id":"m-other","hops":0}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
	req.Header.Set(tunnel.HubServerHeader, "lab3")
	req.Header.Set(tunnel.HubLinksCapHeader, "1")
	req.Header.Set(tunnel.HubSourceTeamHeader, srcTeam1)
	out := httptest.NewRecorder()
	e.r.ServeHTTP(out, req)
	e.refusedLikeEveryCause(out, "another server")
}

func TestTheDoorRefusesWhatIsOffStaleOrNotLinked(t *testing.T) {
	e := newDoorEnv(t)
	hdr := linkedHdr(srcTeam1)
	require.Equal(t, http.StatusOK, e.relayed("beta", "", hdr).Code)
	// The team's own switch off.
	_, err := e.h.bs.UpdateBoardSettings(context.Background(), "beta", board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(false)})
	require.NoError(t, err)
	e.refusedLikeEveryCause(e.relayed("beta", "", hdr), "switch off")
	_, err = e.h.bs.UpdateBoardSettings(context.Background(), "beta", board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(true)})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, e.relayed("beta", "", hdr).Code)
	// A table past its TTL admits nothing.
	e.clockAdvance(linkTTL + time.Second)
	e.refusedLikeEveryCause(e.relayed("beta", "", hdr), "stale table")
	// A link to another team here is not a link to this one.
	e.clockAdvance(0)
	e.h.links.apply("0123456789abcdef0123456789abcdef", 2, []teamLink{{doorSrc, srcTeam1, "gamma"}}, e.h.linkNow())
	e.refusedLikeEveryCause(e.relayed("beta", "", hdr), "linked to another team")
	// Nobody to tell.
	e.link(srcTeam1)
	e.refusedLikeEveryCause(e.relayed("beta", "Nobody", hdr), "no such Orchestrator")
	// A process that has not had a snapshot yet.
	fresh := newDoorEnv(t)
	fresh.h.links = linkTable{}
	e2 := fresh.relayed("beta", "", linkedHdr(srcTeam1))
	fresh.refusedLikeEveryCause(e2, "no snapshot since this process started")
}

// Once a hub has advertised links, a connection without them is refused, never the old path, even though the old list names the server.
func TestNoSilentDowngradeAtTheDoor(t *testing.T) {
	e := newDoorEnv(t)
	e.h.links = linkTable{}
	seen := false
	e.h.SetLinksSeen(func(context.Context) (bool, error) { return seen, nil }, func(context.Context) error { seen = true; return nil })
	seedServers(t, e.h.bs, "beta", doorSrc)
	// A hub that never advertised links: today's path (the list names the server), no capability, no team header.
	require.Equal(t, http.StatusOK, e.relayed("beta", "", nil).Code, "the old path for a hub that never advertised")
	// The hub advertises: the link decides, and the flag is remembered.
	e.link(srcTeam1)
	require.Equal(t, http.StatusOK, e.relayed("beta", "", linkedHdr(srcTeam1)).Code)
	assert.True(t, seen)
	// A reconnect to a hub without the capability: refused, though the old list still names the server.
	e.refusedLikeEveryCause(e.relayed("beta", "", nil), "downgrade")
	e.refusedLikeEveryCause(e.relayed("beta", "", map[string]string{tunnel.HubSourceTeamHeader: srcTeam1}), "downgrade with a team header")
}

// The sending team the hub names is trusted like the server it names: only on a request this connector stamped.
func TestTheSourceTeamHeaderWithoutTheStampIsRefusedNotIgnored(t *testing.T) {
	e := newDoorEnv(t)
	post := func(hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/board/beta/external-messages", strings.NewReader(`{"from_board":"alpha","subscriber_id":"Orchestrator","content":"hi","relay":{"message_id":"m-x","hops":0}}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.r.ServeHTTP(rec, req)
		return rec
	}
	for name, hdr := range map[string]map[string]string{
		"only the team header":          {tunnel.HubSourceTeamHeader: srcTeam1},
		"the team header and a forgery": {tunnel.HubSourceTeamHeader: srcTeam1, tunnel.HubServerHeader: doorSrc, tunnel.ViaHeader: "1"},
		"the team header, no stamp":     {tunnel.HubSourceTeamHeader: srcTeam1, tunnel.HubServerHeader: doorSrc},
	} {
		rec := post(hdr)
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.Contains(t, rec.Body.String(), "hub_server_untrusted", name)
	}
	msgs, err := e.h.bs.ListMessages(context.Background(), "beta", 50, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

// Every cause is the same bytes, headers and steps in links mode too.
func TestEveryLinkRefusalIsTheSameBytesHeadersAndSteps(t *testing.T) {
	e := newDoorEnv(t)
	var steps []string
	doorTrace = func(s string) { steps = append(steps, s) }
	defer func() { doorTrace = nil }()
	ask := func(target, to string, hdr map[string]string) (*httptest.ResponseRecorder, []string) {
		steps = nil
		rec := e.relayed(target, to, hdr)
		return rec, append([]string(nil), steps...)
	}
	var recs []*httptest.ResponseRecorder
	var trace [][]string
	add := func(rec *httptest.ResponseRecorder, s []string) { recs, trace = append(recs, rec), append(trace, s) }
	add(ask("nowhere", "", linkedHdr(srcTeam1)))                           // no such team
	add(ask("beta", "", linkedHdr(srcTeam2)))                              // not linked
	add(ask("beta", "", map[string]string{tunnel.HubLinksCapHeader: "1"})) // no team named
	add(ask("beta", "Nobody", linkedHdr(srcTeam1)))                        // nobody to tell
	_, err := e.h.bs.UpdateBoardSettings(context.Background(), "beta", board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(false)})
	require.NoError(t, err)
	add(ask("beta", "", linkedHdr(srcTeam1))) // switch off
	e.clockAdvance(linkTTL + time.Second)
	add(ask("beta", "", linkedHdr(srcTeam1))) // stale
	for i, rec := range recs {
		assert.Equal(t, recs[0].Code, rec.Code, "cause %d", i)
		assert.Equal(t, recs[0].Body.String(), rec.Body.String(), "cause %d", i)
		assert.Equal(t, recs[0].Header(), rec.Header(), "cause %d", i)
		assert.Equal(t, trace[0], trace[i], "cause %d takes the same steps", i)
	}
	assert.Equal(t, []string{"exists", "settings", "list", "recipients"}, trace[0])
}

// The owner's log keeps the reason of each refusal, one row an hour per reason, for a team that exists; never in the answer.
func TestTheOwnersLogKeepsTheReasonOfEachRefusalAndNothingForAMissingTeam(t *testing.T) {
	e := newDoorEnv(t)
	hdr := linkedHdr(srcTeam1)
	e.relayed("nowhere", "", hdr)              // a team that does not exist: no row
	e.relayed("beta", "", linkedHdr(srcTeam2)) // no_link
	e.relayed("beta", "Nobody", hdr)           // no_orchestrator
	_, err := e.h.bs.UpdateBoardSettings(context.Background(), "beta", board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(false)})
	require.NoError(t, err)
	e.relayed("beta", "", hdr) // off
	_, err = e.h.bs.UpdateBoardSettings(context.Background(), "beta", board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(true)})
	require.NoError(t, err)
	e.clockAdvance(linkTTL + time.Second)
	e.relayed("beta", "", hdr) // stale
	e.h.WaitBackground()
	log, err := e.h.bs.ListLog(context.Background(), "beta")
	require.NoError(t, err)
	kinds := map[string]int{}
	for _, row := range log {
		kinds[row.Kind] += row.Count
	}
	for _, k := range []string{board.LogLinkRefusedNoLink, board.LogLinkRefusedNoOrchestrator, board.LogLinkRefusedOff, board.LogLinkRefusedStale} {
		assert.Equal(t, 1, kinds[k], k)
	}
	none, err := e.h.bs.ListLog(context.Background(), "nowhere")
	require.NoError(t, err)
	assert.Empty(t, none, "no row for a team that does not exist")
}

// Files follow the same rule: only from a linked team, on a connection that advertised links.
func TestARelayedFileNeedsTheSameLink(t *testing.T) {
	e := newDoorEnv(t)
	data := []byte("notes from lab2\n")
	send := func(hdr map[string]string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/board/beta/external-artifacts", bytes.NewReader(frame(meta(data, "n.txt"), data)))
		req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
		req.Header.Set(tunnel.HubServerHeader, doorSrc)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.r.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	for name, hdr := range map[string]map[string]string{
		"no team named": {tunnel.HubLinksCapHeader: "1"},
		"another team":  linkedHdr(srcTeam2),
		"no capability": {tunnel.HubSourceTeamHeader: srcTeam1},
	} {
		code, out := send(hdr)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Equal(t, "artifacts_off", out["error"], name)
	}
	assert.Empty(t, e.stored())
	code, out := send(linkedHdr(srcTeam1))
	require.Equal(t, http.StatusOK, code, out)
	assert.NotEmpty(t, e.stored())
}

// The list of servers has no write path any more: an addition is refused (not accepted and ignored), a removal or a clearing works, and the stored data stays readable.
func TestTheListOfServersCanOnlyGetShorter(t *testing.T) {
	e := newCrossTeamEnv(t)
	seedServers(t, e.h.bs, "beta", relaySrc, "lab9")
	code, out := e.do("PUT", "/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_servers": []string{relaySrc, "lab9", "lab10"}})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "links_in_coral_hub", out["code"])
	assert.Equal(t, textLinksInCoralHub, out["error"])
	code, out = e.do("PUT", "/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_servers": []string{"lab10"}})
	assert.Equal(t, http.StatusBadRequest, code, "swapping one in for one out is an addition")
	s, err := e.h.bs.GetBoardSettings(context.Background(), "beta")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{relaySrc, "lab9"}, s.CrossTeamServers, "nothing changed on a refusal")
	code, out = e.do("PUT", "/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_servers": []string{strings.ToUpper("lab9")}})
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []any{"lab9"}, out["cross_team_servers"], "a removal")
	code, out = e.do("PUT", "/api/board/beta/settings", map[string]any{"subscriber_id": "Operator", "cross_team_servers": []string{}})
	require.Equal(t, http.StatusOK, code, out)
	assert.Empty(t, out["cross_team_servers"])
}
