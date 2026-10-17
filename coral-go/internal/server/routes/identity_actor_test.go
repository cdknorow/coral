package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cliauth"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/tunnel"
)

func TestClaimOutcome(t *testing.T) {
	token := Actor{Kind: ActorToken, Name: "Dev"}
	cases := []struct {
		name  string
		actor Actor
		given []string
		want  string
	}{
		{"nothing given", token, nil, claimNone},
		{"blanks are nothing", token, []string{"", "  "}, claimNone},
		{"its own name", token, []string{"Dev"}, claimMatch},
		{"its own name twice", token, []string{"Dev", "Dev"}, claimMatch},
		{"another capitalisation", Actor{Kind: ActorOperator, Name: "Operator"}, []string{"operator"}, claimSpelling},
		{"someone else", token, []string{"Lead"}, claimDiffers},
		{"own and someone else", token, []string{"Dev", "Lead"}, claimDiffers},
		{"a session not on the board yet", Actor{Kind: ActorToken}, []string{"Dev"}, claimUnbound},
		{"the Hub decides", Actor{Kind: ActorHub}, []string{"Operator"}, claimUnbound},
		{"no credential: only a claim", Actor{Kind: ActorClaimed}, []string{"Dev"}, claimUnchecked},
		{"no credential, no claim", Actor{Kind: ActorClaimed}, nil, claimNone},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, claimOutcome(c.actor, c.given), c.name)
	}
}

// The identity layer decides every request's actor (#4244): a token's member name on the board, the
// Operator for the dashboard, the signed command line and an API key, the Hub for the tunnel, and
// "claimed" for a caller with no credential. A client can't bring its own, and the claim is
// compared with it per route and caller kind. Nothing about the request changes.
func TestIdentityLayerDecidesTheActor(t *testing.T) {
	_, bh := setupBoardTestServer(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	_, err = bh.bs.Subscribe(ctx, "p", "Dev", "Developer", "claude-dev", nil, "")
	require.NoError(t, err)
	token, err := db.MintSessionToken(ctx, "sid-dev", "claude-dev")
	require.NoError(t, err)
	loose, err := db.MintSessionToken(ctx, "sid-loose", "claude-loose") // a session on no board
	require.NoError(t, err)

	cookie := false
	o := NewIdentityObserver(db, bh.bs, func(*http.Request) bool { return cookie })
	o.SetVerifiers(func(*http.Request) bool { return false }, func(r *http.Request) (*http.Request, bool) {
		if r.Header.Get(cliauth.CLIAuthHeader) == "good" {
			return cliauth.WithVerified(r), true
		}
		return r, false
	})
	var seen Actor
	var had bool
	record := func(w http.ResponseWriter, r *http.Request) { seen, had = ActorOf(r.Context()) }
	h := chi.NewRouter()
	h.Use(o.Middleware)
	h.Post("/api/board/{project}/messages", record)
	h.Post("/api/board/{project}/subscribe", record)
	h.Get("/api/sessions/live", record)
	do := func(method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
		seen, had = Actor{}, false
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	tok := map[string]string{store.SessionTokenHeader: token, "User-Agent": "coral-board/dev"}

	// A token: the member name its session holds on the board, whatever the request says it is.
	do("POST", "/api/board/p/messages", `{"subscriber_id":"Dev","content":"x"}`, tok)
	require.True(t, had)
	assert.Equal(t, Actor{Kind: ActorToken, Name: "Dev", Session: "claude-dev"}, seen)
	do("POST", "/api/board/p/messages", `{"content":"no claim at all"}`, tok)
	assert.Equal(t, "Dev", seen.Name, "an empty claim means the caller")
	// A token on a route that is not a board's has the session, not a board name.
	do("GET", "/api/sessions/live", ``, tok)
	assert.Equal(t, ActorToken, seen.Kind)
	assert.Equal(t, "claude-dev", seen.Session)
	// A session on no board, joining: a token, with no member name yet.
	do("POST", "/api/board/p/subscribe", `{"subscriber_id":"New One"}`, map[string]string{store.SessionTokenHeader: loose, "User-Agent": "coral-board/dev"})
	assert.Equal(t, Actor{Kind: ActorToken, Session: "claude-loose"}, seen)

	// A client can't bring its own: nothing a request carries (headers, query, body) is an actor.
	do("POST", "/api/board/p/messages", `{"actor":"Operator","kind":"operator","subscriber_id":"Dev"}`, map[string]string{
		store.SessionTokenHeader: token, "User-Agent": "coral-board/dev", "X-Coral-Actor": "Operator", "X-Actor-Kind": "operator", "Actor": "Operator"})
	assert.Equal(t, Actor{Kind: ActorToken, Name: "Dev", Session: "claude-dev"}, seen)

	// The Operator: the dashboard's cookie, and a command-line signature the server checked.
	cookie = true
	do("POST", "/api/board/p/messages", `{"subscriber_id":"Operator"}`, nil)
	assert.Equal(t, Actor{Kind: ActorOperator, Name: "Operator"}, seen)
	cookie = false
	do("POST", "/api/board/p/messages", `{"subscriber_id":"operator"}`, map[string]string{cliauth.CLIAuthHeader: "good", "User-Agent": "coral-board/dev"})
	assert.Equal(t, Actor{Kind: ActorOperator, Name: "Operator"}, seen)
	// A signature that did not verify is refused before any handler (it is not "claimed" either).
	rec := do("POST", "/api/board/p/messages", `{"subscriber_id":"operator"}`, map[string]string{cliauth.CLIAuthHeader: "forged", "User-Agent": "coral-board/dev"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, had)

	// Coral Hub: the tunnel's guard decides the name.
	do("POST", "/api/board/p/messages", `{"subscriber_id":"Operator"}`, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()})
	assert.Equal(t, Actor{Kind: ActorHub}, seen)

	// No credential at all (outside strict mode): claimed; an agent tool without its token as well.
	do("POST", "/api/board/p/messages", `{"subscriber_id":"Dev"}`, nil)
	assert.Equal(t, Actor{Kind: ActorClaimed}, seen)
	do("POST", "/api/board/p/messages", `{"subscriber_id":"Dev"}`, map[string]string{"User-Agent": "coral-board/dev"})
	assert.Equal(t, Actor{Kind: ActorClaimed}, seen)
	// An unknown token is not a token's actor.
	do("POST", "/api/board/p/messages", `{"subscriber_id":"Dev"}`, map[string]string{store.SessionTokenHeader: "made-up", "User-Agent": "coral-board/dev"})
	assert.Equal(t, Actor{Kind: ActorClaimed}, seen)

	// The claims, compared per kind and route, in the report.
	rep := o.Report()
	counts := rep["actor_claims"].(map[string]int64)
	assert.EqualValues(t, 2, counts["token match POST /api/board/{project}/messages"], "own name: once with a claim, once as part of the client-actor request")
	assert.EqualValues(t, 1, counts["token no_claim POST /api/board/{project}/messages"])
	assert.EqualValues(t, 1, counts["token unbound POST /api/board/{project}/subscribe"], "a session joining has no member name yet")
	assert.EqualValues(t, 1, counts["operator match POST /api/board/{project}/messages"])
	assert.EqualValues(t, 1, counts["operator match_spelling POST /api/board/{project}/messages"], "the signed command line sends lower-case operator")
	assert.EqualValues(t, 1, counts["hub unbound POST /api/board/{project}/messages"])
	assert.EqualValues(t, 3, counts["claimed unverified POST /api/board/{project}/messages"])
	for k := range counts {
		assert.NotContains(t, k, "GET /api/sessions/live", "only board requests are compared")
	}
	b, _ := json.Marshal(rep)
	assert.NotContains(t, string(b), token, "the report never shows a token")
}

// Through Coral Hub the actor is what the tunnel decided (#4244 follow-up (b)): the Hub user for a
// chat post or a read, the Operator (for that Hub user) when TunnelIdentityGuard admitted the write
// as the Hub operator, nothing for a malformed Hub user, and no actor at all for a forged stamp or a
// write the guard refused. The real guard runs in front of the identity layer, as in the router.
func TestHubActorIsWhatTheTunnelDecided(t *testing.T) {
	_, bh := setupBoardTestServer(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = bh.bs.Subscribe(context.Background(), "p", "Dev", "Developer", "claude-dev", nil, "")
	require.NoError(t, err)

	operatorAllowed := true
	o := NewIdentityObserver(db, bh.bs, func(*http.Request) bool { return false })
	var seen Actor
	var reached bool
	record := func(w http.ResponseWriter, r *http.Request) { seen, reached = ActorOf(r.Context()); reached = true }
	h := chi.NewRouter()
	h.Use(TunnelIdentityGuard(bh.bs, func(*http.Request) bool { return operatorAllowed }))
	h.Use(o.Middleware)
	h.Post("/api/board/{project}/messages", record)
	h.Get("/api/board/{project}/messages", record)
	h.Post("/api/board/{project}/tasks", record)
	do := func(method, target, body string, hdr map[string]string, extra ...[2]string) *httptest.ResponseRecorder {
		seen, reached = Actor{}, false
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		for _, kv := range extra {
			r.Header.Add(kv[0], kv[1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	const user = "hub-user-1a2b3c4d"
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: user}

	// A chat post and a read: the Hub user, whatever the post claims.
	do("POST", "/api/board/p/messages", `{"content":"hi"}`, hub)
	assert.Equal(t, Actor{Kind: ActorHub, Name: user, HubUser: user}, seen)
	do("POST", "/api/board/p/messages", `{"subscriber_id":"Operator","content":"hi"}`, hub)
	assert.Equal(t, Actor{Kind: ActorHub, Name: user, HubUser: user}, seen, "a claim of the Operator in a chat post does not make the Hub user the Operator here")
	do("GET", "/api/board/p/messages", ``, hub)
	assert.Equal(t, Actor{Kind: ActorHub, Name: user, HubUser: user}, seen)

	// A write the guard admitted as the Hub operator: the Operator, for that Hub user.
	rec := do("POST", "/api/board/p/tasks", `{"created_by":"Operator","title":"x"}`, hub)
	require.True(t, reached, "%d %s", rec.Code, rec.Body.String())
	assert.Equal(t, Actor{Kind: ActorHub, Name: "Operator", HubUser: user}, seen)
	// ...and when the guard refuses it (the owner turned the Hub operator off), nothing is served.
	operatorAllowed = false
	rec = do("POST", "/api/board/p/tasks", `{"created_by":"Operator","title":"x"}`, hub)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, reached)
	operatorAllowed = true
	// Another member's name through the Hub is the guard's 409, never an actor.
	rec = do("POST", "/api/board/p/tasks", `{"created_by":"Dev","title":"x"}`, hub)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.False(t, reached)

	// A Hub user that is not exactly one well-formed value names nobody.
	for _, bad := range []string{"HUB-USER-1A2B3C4D", "hub-user-1a2b3c4", "hub-user-1a2b3c4d5", "hub-user-1a2b3c4d\n", "someone", strings.Repeat("a", 40)} {
		do("GET", "/api/board/p/messages", ``, map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubUserHeader: bad})
		assert.Equal(t, Actor{Kind: ActorHub}, seen, "Hub user %q", bad)
	}
	do("GET", "/api/board/p/messages", ``, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}, [2]string{tunnel.HubUserHeader, user}, [2]string{tunnel.HubUserHeader, "hub-user-ffffffff"})
	assert.Equal(t, Actor{Kind: ActorHub}, seen, "two Hub users are none")
	do("GET", "/api/board/p/messages", ``, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()})
	assert.Equal(t, Actor{Kind: ActorHub}, seen, "no Hub user header")

	// A forged tunnel stamp is refused before any handler: it is not a Hub actor, however well formed its user.
	rec = do("GET", "/api/board/p/messages", ``, map[string]string{tunnel.ViaHeader: "not-the-connectors-secret", tunnel.HubUserHeader: user})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, reached)
}
