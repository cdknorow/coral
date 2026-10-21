package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/secrets"
	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The integration review of v1.4.146 (#5647): the pieces were tested one by one (owner-only artifacts #5575, per-user ownership #5577, the cross-team owner path #5572, the owner stamp list), each in
// its own patch, and the release is their merge. This sends the artifact routes and the cross-team routes of the MERGED tree through the real connector (a hub session, tunnel.LocalHandler with
// the production's AllowRequest and ViaValue: stampRole and the route table) to the real router, and through the real router with a real agent token, as the callers that matter:
// the owner, a grantee, another hub user holding the owner role, a request with a forged marker, and agents of three different owners.
type integ struct {
	t     *testing.T
	srv   *Server
	local *httptest.Server
	sess  *tunnel.Session
	ids   map[string]string // board label -> id
	tok   map[string]string // agent name -> token
}

const (
	integOwner   = "hub-user-1234abcd"
	integGrantee = "hub-user-9999ffff"
	integAlice   = "hub-user-aaaaaaaa"
	integBob     = "hub-user-bbbbbbbb"
)

func newInteg(t *testing.T) *integ {
	t.Helper()
	setHome(t, t.TempDir())
	srv, _, _ := strictServer(t, false)
	srv.cloudHandler.SetPeerCheck(func(*http.Request) (bool, bool) { return false, false }) // requests built with httptest carry no local address (as newE2ERemote does)
	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)
	ss := store.NewSessionStore(srv.db)
	ctx := context.Background()
	require.NoError(t, ss.SetSetting(ctx, routes.MaxModeSetting, tunnel.TierLaunch.String()))
	cur := routes.MaxModeFromSettings(ss)
	routes.SetHubOwnerLabel(func(context.Context) (string, error) { return integOwner, nil })
	t.Cleanup(func() { routes.SetHubOwnerLabel(nil) })

	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{}); err == nil {
			sessions <- s
		}
	}))
	t.Cleanup(ths.Close)
	cs, err := tunnel.Dial(ctx, "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler:       &tunnel.LocalHandler{Redactor: secrets.Redactor(), BaseURL: local.URL, AllowRequest: tunnel.AllowRequest(cur), MaxMode: cur, ViaValue: cloud.ViaSecret()},
		AllowInsecure: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	sess := <-sessions
	t.Cleanup(func() { sess.Close() })

	g := &integ{t: t, srv: srv, local: local, sess: sess, ids: map[string]string{}, tok: map[string]string{}}
	// three owners' teams, each with one agent launched onto it
	for _, b := range []struct{ label, creator, agent, sid string }{
		{"alice-team", integAlice, "alice-dev", "aaaaaaaa-0000-0000-0000-00000000000a"},
		{"bob-team", integBob, "bob-dev", "bbbbbbbb-0000-0000-0000-00000000000b"},
		{"ops-team", "Operator", "ops-dev", "cccccccc-0000-0000-0000-00000000000c"},
	} {
		bd, err := srv.boardStore.CreateBoard(ctx, b.label, b.creator)
		require.NoError(t, err)
		g.ids[b.label] = bd.ID
		id := bd.ID
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: b.sid, AgentType: "claude", AgentName: b.agent, WorkingDir: t.TempDir(), BoardName: &id}))
		tok, err := srv.db.MintSessionToken(ctx, b.sid, "claude-"+b.sid)
		require.NoError(t, err)
		g.tok[b.agent] = tok
		_, err = srv.boardStore.Subscribe(ctx, bd.ID, "Dev", "Dev", "claude-"+b.sid, nil, "all")
		require.NoError(t, err)
		_, err = srv.boardStore.Subscribe(ctx, bd.ID, "Orchestrator", "Orchestrator", "", nil, "all")
		require.NoError(t, err)
	}
	return g
}

// agent sends a request to the real router with an agent's token (not through the hub).
func (g *integ) agent(name, method, path, body string, hdr map[string]string) (int, string) {
	g.t.Helper()
	req, err := http.NewRequest(method, g.local.URL+path, strings.NewReader(body))
	require.NoError(g.t, err)
	req.Header.Set(store.SessionTokenHeader, g.tok[name])
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(g.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// hub sends a request through the real connector: role and user are what the hub puts on the stream, header what a browser put on the request.
func (g *integ) hub(role, user, method, path, body string, header map[string]string) (int, string) {
	g.t.Helper()
	ctx := context.Background()
	if user != "" {
		ctx = tunnel.WithHubUser(ctx, user)
	}
	if role != "" {
		ctx = tunnel.WithHubRole(ctx, role)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://ignored"+path, strings.NewReader(body))
	require.NoError(g.t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := g.sess.RoundTrip(req)
	require.NoError(g.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (g *integ) upload(agent, name, text string) string {
	g.t.Helper()
	sid := map[string]string{"alice-dev": "aaaaaaaa-0000-0000-0000-00000000000a", "bob-dev": "bbbbbbbb-0000-0000-0000-00000000000b", "ops-dev": "cccccccc-0000-0000-0000-00000000000c"}[agent]
	code, body := g.agent(agent, "POST", "/api/agent/artifacts?session_id="+sid, text, map[string]string{"X-Artifact-Name": name, "X-Artifact-Media-Type": "text/plain"})
	require.Equal(g.t, http.StatusCreated, code, body)
	var out map[string]any
	require.NoError(g.t, json.Unmarshal([]byte(body), &out))
	return strings.TrimPrefix(out["uri"].(string), "coral://artifacts/")
}

func TestV146ArtifactRoutesThroughTheRealConnectorAndTheRealRouter(t *testing.T) {
	g := newInteg(t)
	alice := g.upload("alice-dev", "alice.txt", "alice's private note")
	bob := g.upload("bob-dev", "bob.txt", "bob's private note")
	ops := g.upload("ops-dev", "ops.txt", "the machine owner's note")
	aliceBoard := g.ids["alice-team"]

	// 1. Through the hub: the owner reads and sees who can read; everyone else is refused on every artifact route, the readers and the delete included
	routesFor := func(id string) []string {
		return []string{"/api/artifacts/" + id + "?v=2", "/api/artifacts/" + id + "/meta", "/api/artifacts/" + id + "/comments", "/api/artifacts/" + id + "/readers"}
	}
	shared := []string{"/api/artifacts", "/api/board/" + aliceBoard + "/artifacts", "/api/board/" + aliceBoard + "/tasks/1/artifact-content"}
	for _, id := range []string{alice, bob, ops} {
		for _, p := range routesFor(id) {
			code, body := g.hub("owner", integOwner, "GET", p, "", nil)
			assert.Equal(t, http.StatusOK, code, "the owner: %s %s", p, body)
		}
	}
	for _, p := range shared[:2] {
		code, body := g.hub("owner", integOwner, "GET", p, "", nil)
		assert.Equal(t, http.StatusOK, code, "the owner: %s %s", p, body)
	}
	code, body := g.hub("owner", integOwner, "GET", "/api/artifacts", "", nil)
	require.Equal(t, http.StatusOK, code)
	for _, id := range []string{alice, bob, ops} {
		assert.Contains(t, body, id, "the machine-wide list shows every owner's artifact to the machine owner")
	}
	code, body = g.hub("owner", integOwner, "GET", "/api/artifacts/"+alice+"/readers", "", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, integAlice, "the readers view names the owner of the record")
	assert.Contains(t, body, "not an operating-system boundary")
	code, _ = g.hub("owner", integOwner, "GET", shared[2], "", nil)
	assert.NotEqual(t, http.StatusForbidden, code, "the owner reaches the task artifact-content route (404: no such task)")

	forged := map[string]string{tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: integOwner, tunnel.ViaHeader: cloud.ViaSecret()}
	callers := map[string][3]any{ // name -> role, user, request header
		"a grantee":                             {"", integGrantee, nil},
		"the owner's user without the role":     {"", integOwner, nil},
		"another hub user holding the role":     {"owner", integGrantee, nil},
		"the owner role and no hub user":        {"owner", "", nil},
		"a forged marker on a grantee":          {"", integGrantee, forged},
		"a forged marker and no hub user":       {"", "", forged},
		"a view role that is not owner":         {"viewer", integOwner, nil},
		"the owner role in capitals":            {"OWNER", integOwner, nil},
		"an owner stream of the wrong hub user": {"owner", integAlice, nil},
	}
	all := append(routesFor(alice), routesFor(bob)...)
	all = append(all, shared...)
	for name, c := range callers {
		var hdr map[string]string
		if c[2] != nil {
			hdr = c[2].(map[string]string)
		}
		for _, p := range all {
			code, body := g.hub(c[0].(string), c[1].(string), "GET", p, "", hdr)
			assert.Equal(t, http.StatusForbidden, code, "%s: %s", name, p)
			assert.NotContains(t, body, "private note", "%s: %s", name, p)
			assert.NotContains(t, body, alice, "%s: %s leaks an id", name, p)
		}
		code, _ := g.hub(c[0].(string), c[1].(string), "DELETE", "/api/artifacts/"+bob, "", hdr)
		assert.Equal(t, http.StatusForbidden, code, "%s: delete", name)
	}
	// nothing was deleted by a refused caller, and the owner's delete works and is recorded
	code, _ = g.agent("bob-dev", "GET", "/api/artifacts/"+bob+"/meta", "", nil)
	require.Equal(t, http.StatusOK, code, "bob's file is still there")
	code, body = g.hub("owner", integOwner, "DELETE", "/api/artifacts/"+ops, "", nil)
	require.Equal(t, http.StatusOK, code, body)

	// 2. Agents of three owners, through the real router with real tokens: each reads what its team's owner owns and nothing else, and the same 404 as for a missing artifact
	missing := strings.Repeat("0", 64)
	type want struct{ alice, bob int }
	for agent, w := range map[string]want{"alice-dev": {200, 404}, "bob-dev": {404, 200}} {
		for _, id := range []struct {
			name string
			id   string
			code int
		}{{"alice's", alice, w.alice}, {"bob's", bob, w.bob}} {
			for _, suffix := range []string{"?v=2", "/meta"} {
				code, body := g.agent(agent, "GET", "/api/artifacts/"+id.id+suffix, "", nil)
				assert.Equal(t, id.code, code, "%s reads %s file %s: %s", agent, id.name, suffix, body)
			}
		}
		code, body := g.agent(agent, "GET", "/api/artifacts/"+missing+"/meta", "", nil)
		assert.Equal(t, http.StatusNotFound, code)
		code2, body2 := g.agent(agent, "GET", "/api/artifacts/"+map[string]string{"alice-dev": bob, "bob-dev": alice}[agent]+"/meta", "", nil)
		assert.Equal(t, code, code2, "not yours looks like missing")
		assert.Equal(t, body, body2, "same bytes")
		// the owner-only routes refuse an agent whatever its owner
		for _, p := range []string{"/api/artifacts", "/api/artifacts/" + alice + "/readers"} {
			code, _ := g.agent(agent, "GET", p, "", nil)
			assert.Equal(t, http.StatusForbidden, code, "%s: %s", agent, p)
		}
		code, _ = g.agent(agent, "DELETE", "/api/artifacts/"+alice, "", nil)
		assert.Equal(t, http.StatusForbidden, code, "%s: delete", agent)
	}
	// the machine owner's team reads the machine owner's, not the hub users'
	code, _ = g.agent("ops-dev", "GET", "/api/artifacts/"+alice+"/meta", "", nil)
	assert.Equal(t, http.StatusNotFound, code)

	// 3. A team list holds only what the agent may read
	code, body = g.agent("alice-dev", "GET", "/api/board/"+aliceBoard+"/artifacts", "", nil)
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, alice)
	assert.NotContains(t, body, bob)

	// 4. A launch is how an agent gets a team: an agent cannot start an agent on another owner's team
	work := t.TempDir()
	code, body = g.agent("alice-dev", "POST", "/api/sessions/launch", `{"working_dir":"`+strings.ReplaceAll(work, `\`, `\\`)+`","agent_type":"claude","board_id":"`+g.ids["bob-team"]+`"}`, map[string]string{"Content-Type": "application/json"})
	assert.Equal(t, http.StatusForbidden, code, "an agent launching onto another owner's team: %s", body)
	assert.Contains(t, body, "forbidden_for_agent")
}

func TestV146CrossTeamRoutesThroughTheRealConnectorAndTheRealRouter(t *testing.T) {
	g := newInteg(t)
	team := g.ids["alice-team"]
	routesFor := func(files bool, verb string) string {
		if files {
			return "/api/board/" + team + "/cross-team/files/" + verb
		}
		return "/api/board/" + team + "/cross-team/" + verb
	}
	// the owner turns each switch on and off through the hub; it is recorded and announced
	for _, files := range []bool{false, true} {
		code, body := g.hub("owner", integOwner, "POST", routesFor(files, "enable"), "{}", nil)
		require.Equal(t, http.StatusOK, code, body)
		st, err := g.srv.boardStore.GetBoardSettings(context.Background(), team)
		require.NoError(t, err)
		if files {
			assert.True(t, st.CrossTeamArtifacts)
		} else {
			assert.True(t, st.CrossTeamMessaging)
		}
	}
	acts, err := g.srv.boardStore.RecentHubOperatorActions(context.Background(), 50)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(acts), 2, "recorded with the hub user")
	// everyone else is refused and nothing changes
	forged := map[string]string{tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: integOwner, tunnel.ViaHeader: cloud.ViaSecret()}
	for name, c := range map[string][3]any{
		"a grantee": {"", integGrantee, nil}, "another hub user holding the role": {"owner", integGrantee, nil}, "the owner's user without the role": {"", integOwner, nil},
		"a forged marker on a grantee": {"", integGrantee, forged}, "no hub user": {"owner", "", nil},
	} {
		var hdr map[string]string
		if c[2] != nil {
			hdr = c[2].(map[string]string)
		}
		for _, files := range []bool{false, true} {
			code, body := g.hub(c[0].(string), c[1].(string), "POST", routesFor(files, "disable"), "{}", hdr)
			assert.Equal(t, http.StatusForbidden, code, "%s: %s", name, body)
		}
	}
	st, err := g.srv.boardStore.GetBoardSettings(context.Background(), team)
	require.NoError(t, err)
	assert.True(t, st.CrossTeamMessaging && st.CrossTeamArtifacts, "no refused caller changed a switch")
	// an agent (its own team's too) and a request from this computer with no marker are refused
	for _, files := range []bool{false, true} {
		code, body := g.agent("alice-dev", "POST", routesFor(files, "disable"), "{}", nil)
		assert.Equal(t, http.StatusForbidden, code, "an agent: %s", body)
		code, body = g.agent("bob-dev", "POST", routesFor(files, "disable"), "{}", nil)
		assert.Equal(t, http.StatusForbidden, code, "another owner's agent: %s", body)
	}
	// the owner cannot set the two switches through the board settings route (local only), nor can anyone else through the hub
	code, _ := g.hub("owner", integOwner, "PUT", "/api/board/"+team+"/settings", `{"subscriber_id":"Operator","cross_team_messaging":false}`, map[string]string{"Content-Type": "application/json"})
	assert.Equal(t, http.StatusForbidden, code, "the settings PUT stays local for the two switches")
	// the local lock: off, the owner is refused in both directions; the lock itself is not writable through the hub
	h := g.srv.Router()
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/hub-cross-team", `{"allow":false}`, map[string]string{"Content-Type": "application/json"}).Code)
	for _, verb := range []string{"enable", "disable"} {
		code, body := g.hub("owner", integOwner, "POST", routesFor(false, verb), "{}", nil)
		assert.Equal(t, http.StatusForbidden, code, "the lock is on: %s", body)
		assert.Contains(t, body, "hub_changes_off")
	}
	lockReq, _ := http.NewRequestWithContext(tunnel.WithHubUser(tunnel.WithHubRole(context.Background(), "owner"), integOwner), "PUT", "http://ignored/api/cloud/hub-cross-team", strings.NewReader(`{"allow":true}`))
	if resp, err := g.sess.RoundTrip(lockReq); err == nil {
		resp.Body.Close()
		assert.NotEqual(t, http.StatusOK, resp.StatusCode, "the lock cannot be changed through the hub, even by its owner")
	} else {
		assert.Contains(t, err.Error(), "path_not_allowed", "the connector refuses the lock route: the owner cannot reach it through the hub")
	}
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/hub-cross-team", `{"allow":true}`, map[string]string{"Content-Type": "application/json"}).Code)
	code, body := g.hub("owner", integOwner, "POST", routesFor(false, "disable"), "{}", nil)
	assert.Equal(t, http.StatusOK, code, body)

	// the relay door through the connector: a message the hub opens carries the sending server; one a hub USER sends with that header gets the uniform refusal (a viewer cannot pose as a server)
	relay := `{"from_board":"alpha","subscriber_id":"Orchestrator","content":"hello","relay":{"message_id":"m-1","hops":0}}`
	code, body = g.hub("", integGrantee, "POST", "/api/board/"+team+"/external-messages", relay, map[string]string{"Content-Type": "application/json", tunnel.HubServerHeader: "lab2"})
	// the connector drops the hub user on this route, so the grantee is just a hub caller naming a server the team does not take: the same uniform answer as anyone
	assert.Equal(t, http.StatusNotFound, code, "a hub user posing as a server: %s", body)
	assert.Contains(t, body, "no such team")
	code, body = g.hub("", "", "POST", "/api/board/"+team+"/external-messages", relay, map[string]string{"Content-Type": "application/json", tunnel.HubServerHeader: "lab2"})
	assert.Equal(t, http.StatusNotFound, code, "a server the team does not take, one answer: %s", body)
	assert.Contains(t, body, "no such team")
}
