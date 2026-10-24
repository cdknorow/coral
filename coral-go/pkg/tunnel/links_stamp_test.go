package tunnel

import (
	"context"
	"net/http"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5572 patch 2: the cross-team link snapshot and the sending team of a relayed message. What the connector forwards, what it adds from the connection itself, and what no hub can send.

func TestScrubForwardsTheSourceTeamHeaderOnlyWhereTheServerHeaderGoes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		kept         bool
	}{
		{http.MethodPost, "/api/board/team/external-messages", true},
		{http.MethodPost, "/api/board/team/external-artifacts", true},
		{http.MethodGet, "/api/board/team/external-messages", false},
		{http.MethodPost, "/api/board/team/messages", false},
		{http.MethodPost, "/api/board/team/hub-notice", false},
		{http.MethodPost, HubLinksPath, false},
		{http.MethodPost, "/api/board/team/external-messages/x", false},
	} {
		for _, key := range []string{HubSourceTeamHeader, "x-coral-hub-source-team"} {
			in := http.Header{}
			in[key] = []string{"b-aaaaaaaaaa"}
			out := scrub(in, tc.method, tc.path)
			if tc.kept {
				assert.Equal(t, "b-aaaaaaaaaa", out.Get(HubSourceTeamHeader), "%s %s (%s)", tc.method, tc.path, key)
			} else {
				assert.Empty(t, out.Values(HubSourceTeamHeader), "%s %s (%s)", tc.method, tc.path, key)
			}
		}
	}
}

func TestScrubForwardsTheLinksMarkerOnlyOnTheSnapshotPathAndNeverTheConnectorsOwnHeaders(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		kept         bool
	}{
		{http.MethodPost, HubLinksPath, true},
		{http.MethodGet, HubLinksPath, false},
		{http.MethodPut, HubLinksPath, false},
		{http.MethodPost, HubLinksPath + "/x", false},
		{http.MethodPost, "/api/board/team/hub-notice", false},
		{http.MethodPost, "/api/board/team/external-messages", false},
	} {
		in := http.Header{}
		in[HubLinksHeader] = []string{HubLinksSnapshot}
		in["x-coral-hub-links"] = []string{HubLinksSnapshot}
		in[HubLinksSessionHeader] = []string{"hub-chosen"}
		in[HubLinksCapHeader] = []string{"1"}
		out := scrub(in, tc.method, tc.path)
		if tc.kept {
			assert.Equal(t, HubLinksSnapshot, out.Get(HubLinksHeader), "%s %s", tc.method, tc.path)
		} else {
			assert.Empty(t, out.Values(HubLinksHeader), "%s %s", tc.method, tc.path)
		}
		assert.Empty(t, out.Values(HubLinksSessionHeader), "%s %s: the hub cannot name the session", tc.method, tc.path)
		assert.Empty(t, out.Values(HubLinksCapHeader), "%s %s: the hub cannot claim the capability on a request", tc.method, tc.path)
	}
}

func TestStampLinksAddsTheConnectionsOwnFactsOnlyWhereTheyAreRead(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		stamped      bool
	}{
		{http.MethodPost, HubLinksPath, true},
		{http.MethodPost, "/api/board/team/external-messages", true},
		{http.MethodPost, "/api/board/team/external-artifacts", true},
		{http.MethodGet, "/api/board/team/external-messages", false},
		{http.MethodPost, "/api/board/team/messages", false},
		{http.MethodGet, "/api/health", false},
	} {
		for _, cap := range []bool{true, false} {
			out := http.Header{}
			out.Set(HubLinksSessionHeader, "left-over")
			out.Set(HubLinksCapHeader, "1")
			stampLinks(out, &Stream{sessionID: "0123456789abcdef0123456789abcdef", linksCap: cap}, tc.method, tc.path)
			switch {
			case tc.stamped:
				assert.Equal(t, "0123456789abcdef0123456789abcdef", out.Get(HubLinksSessionHeader), "%s %s", tc.method, tc.path)
				assert.Equal(t, cap, out.Get(HubLinksCapHeader) == "1", "%s %s cap=%v", tc.method, tc.path, cap)
			default:
				assert.Empty(t, out.Values(HubLinksSessionHeader), "%s %s", tc.method, tc.path)
				assert.Empty(t, out.Values(HubLinksCapHeader), "%s %s", tc.method, tc.path)
			}
		}
	}
	out := http.Header{}
	stampLinks(out, &Stream{linksCap: true}, http.MethodPost, HubLinksPath)
	assert.Empty(t, out.Values(HubLinksSessionHeader), "a stream with no session id stamps nothing")
}

// End to end through a real hub and connector: the session id is the connector's own and differs per connection; the capability header appears only when THIS connection's hub advertised it;
// the hub's own copies of the connector's headers never arrive.
func TestTheConnectorStampsItsSessionAndTheCapabilityItLearnedOnThisConnection(t *testing.T) {
	var mu sync.Mutex
	var got []http.Header
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(200)
	})
	launch := func() Mode { return TierLaunch }
	mod := func(l *LocalHandler) { l.MaxMode = launch; l.AllowPath = nil; l.AllowRequest = AllowRequest(launch) }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	send := func(sess *Session, path string, hdr http.Header) {
		t.Helper()
		req, _ := http.NewRequestWithContext(WithHubTier(ctx, "launch"), http.MethodPost, "http://x"+path, nil)
		for k, vs := range hdr {
			req.Header[k] = vs
		}
		resp, err := sess.RoundTrip(req)
		require.NoError(t, err, path)
		resp.Body.Close()
	}
	forged := http.Header{HubLinksHeader: {HubLinksSnapshot}, HubLinksSessionHeader: {"hub-chosen"}, HubLinksCapHeader: {"1"}}

	withCap := fastCfg()
	withCap.Capabilities = func(any) []string { return []string{CapCrossTeamLinks} }
	hubA := newHub(t, withCap)
	sessA, csA := connect(t, hubA, local, mod)
	require.True(t, csA.advertised(CapCrossTeamLinks), "the hub's Welcome carries the capability")
	send(sessA, HubLinksPath, forged)
	send(sessA, "/api/board/team/external-messages", http.Header{HubSourceTeamHeader: {"b-aaaaaaaaaa"}, HubServerHeader: {"lab2"}})
	send(sessA, "/api/board/team/messages", forged)

	noCap := newHub(t, fastCfg()) // a hub that does not advertise it (a rollback, or another peer)
	sessB, csB := connect(t, noCap, local, mod)
	require.False(t, csB.advertised(CapCrossTeamLinks))
	send(sessB, HubLinksPath, forged)
	send(sessB, "/api/board/team/external-messages", http.Header{HubSourceTeamHeader: {"b-aaaaaaaaaa"}, HubServerHeader: {"lab2"}})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 5)
	sessionRe := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for i := 0; i < 2; i++ {
		assert.Regexp(t, sessionRe, got[i].Get(HubLinksSessionHeader), "request %d", i)
		assert.Equal(t, "1", got[i].Get(HubLinksCapHeader), "request %d: this connection's hub advertised it", i)
	}
	assert.Equal(t, HubLinksSnapshot, got[0].Get(HubLinksHeader))
	assert.Equal(t, got[0].Get(HubLinksSessionHeader), got[1].Get(HubLinksSessionHeader), "one session, one id")
	assert.NotEqual(t, "hub-chosen", got[0].Get(HubLinksSessionHeader))
	assert.Equal(t, "b-aaaaaaaaaa", got[1].Get(HubSourceTeamHeader))
	assert.Empty(t, got[2].Values(HubLinksHeader), "not on another route")
	assert.Empty(t, got[2].Values(HubLinksSessionHeader))
	assert.Empty(t, got[2].Values(HubLinksCapHeader))
	assert.Regexp(t, sessionRe, got[3].Get(HubLinksSessionHeader))
	assert.Empty(t, got[3].Values(HubLinksCapHeader), "a connection whose hub did not advertise it carries no capability, whatever the hub's request claims")
	assert.NotEqual(t, got[0].Get(HubLinksSessionHeader), got[3].Get(HubLinksSessionHeader), "another connection, another session id")
	assert.Empty(t, got[4].Values(HubLinksCapHeader))
}

// A machine that drops back to View stops taking relayed messages, relayed files and link snapshots AT ONCE: each route is Interact tier and the mode is read on every request, so a stored link
// table is inert whatever its freshness (#5572, coral-hub Security's question).
func TestRelayedMessagesFilesAndSnapshotsAreInteractTierAndTheModeIsReadPerRequest(t *testing.T) {
	routes := []string{"/api/board/team/external-messages", "/api/board/team/external-artifacts", HubLinksPath}
	for _, p := range routes {
		assert.Equal(t, TierInteract, Classify(http.MethodPost, p, ""), p)
	}
	mode := TierInteract
	allow := AllowRequest(func() Mode { return mode })
	for _, p := range routes {
		assert.True(t, allow(http.MethodPost, p, ""), "interact: "+p)
	}
	mode = TierView // the owner lowers the cap; the same handler, the next request
	for _, p := range routes {
		assert.False(t, allow(http.MethodPost, p, ""), "view: "+p)
	}
	mode = TierLaunch
	for _, p := range routes {
		assert.True(t, allow(http.MethodPost, p, ""), "launch: "+p)
	}
}

// The hub reads what the connector said in its Hello, and only a well-formed list counts at all.
func TestTheHubReadsThePeersHelloCapabilities(t *testing.T) {
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), nil)
	assert.ElementsMatch(t, []string{CapCrossTeamAllowlist, CapCrossTeamLinks, CapabilityLocalMode, CapGoals}, sess.PeerCapabilities())
	assert.True(t, sess.HasPeerCapability(CapCrossTeamLinks))
	assert.True(t, sess.HasPeerCapability(CapCrossTeamAllowlist))
	assert.False(t, sess.HasPeerCapability("cross_team_links/2"), "another version is another capability")
	assert.False(t, sess.HasPeerCapability("relay/1"))
	got := sess.PeerCapabilities()
	got[0] = "tampered"
	assert.NotContains(t, sess.PeerCapabilities(), "tampered", "a copy")

	tooMany := make([]string, maxCapabilities+1)
	for i := range tooMany {
		tooMany[i] = "cap_x/1"
	}
	for name, list := range map[string][]string{
		"a malformed entry": {CapCrossTeamLinks, "Bad Cap"},
		"too many":          tooMany,
	} {
		assert.Nil(t, wellFormedCapabilities(list), name+": supports nothing, never a partial read")
	}
	assert.Equal(t, []string{CapCrossTeamLinks}, wellFormedCapabilities([]string{CapCrossTeamLinks}))
	assert.Empty(t, (&Session{}).PeerCapabilities())
	assert.False(t, (&Session{}).HasPeerCapability(CapCrossTeamLinks))
}
