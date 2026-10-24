package background

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #7053: the poll of a remote board server is sent to the address that was checked, and only there. A remote that answers with a redirect to an address the check would have refused is not
// followed: nothing reaches the target, the redirect is not read as an answer, and the agent is not nudged from it.
func TestRemoteBoardPollerDoesNotFollowARedirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte(`{"unread":5}`))
	}))
	defer target.Close()
	var checkedHits atomic.Int32
	checked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkedHits.Add(1)
		http.Redirect(w, r, target.URL+r.URL.Path+"?"+r.URL.RawQuery, http.StatusFound)
	}))
	defer checked.Close()

	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	defer db.Close()
	rb := store.NewRemoteBoardStore(db)
	ctx := context.Background()
	_, err = rb.AddRemoteSub(ctx, "sess-1", checked.URL, "team", "Dev")
	require.NoError(t, err)

	rt := &mockRuntime{}
	p := NewRemoteBoardPoller(rb, rt, time.Hour)
	// The check accepts the server the subscription names and nothing else: as it would a public host and refuse an internal address.
	p.validateAddr = func(string) (string, error) { return "127.0.0.1", nil } // the dial-time check is its own test below; the test servers are on the loopback
	p.validateURL = func(raw string) (string, error) {
		if strings.HasPrefix(raw, checked.URL) {
			return "203.0.113.1", nil
		}
		return "", assert.AnError
	}
	p.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "claude", AgentType: "claude", SessionID: "sess-1"}}, nil
	})
	require.NoError(t, p.RunOnce(ctx))

	assert.EqualValues(t, 1, checkedHits.Load(), "the checked address was asked once")
	assert.EqualValues(t, 0, targetHits.Load(), "the redirect target was never reached")
	subs, err := rb.ListAllRemoteSubs(ctx)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Zero(t, subs[0].LastNotifiedUnread, "nothing from the redirect counted as unread")
	rt.mu.Lock()
	defer rt.mu.Unlock()
	assert.Empty(t, rt.sent, "the agent was not nudged")
}

// An answer that is not a success is not read as one, whatever its body says.
func TestRemoteBoardPollerIgnoresANonSuccessAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"unread":3}`))
	}))
	defer srv.Close()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	defer db.Close()
	rb := store.NewRemoteBoardStore(db)
	ctx := context.Background()
	_, err = rb.AddRemoteSub(ctx, "sess-1", srv.URL, "team", "Dev")
	require.NoError(t, err)
	rt := &mockRuntime{}
	p := NewRemoteBoardPoller(rb, rt, time.Hour)
	p.validateURL = func(string) (string, error) { return "203.0.113.1", nil }
	p.validateAddr = func(string) (string, error) { return "127.0.0.1", nil }
	p.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "claude", AgentType: "claude", SessionID: "sess-1"}}, nil
	})
	require.NoError(t, p.RunOnce(ctx))
	subs, _ := rb.ListAllRemoteSubs(ctx)
	require.Len(t, subs, 1)
	assert.Zero(t, subs[0].LastNotifiedUnread)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	assert.Empty(t, rt.sent)
}

// #7109 (Security's LOW 1 on #7053): a remote the URL check refuses is never asked.
func TestRemoteBoardPollerNeverAsksARemoteTheCheckRefuses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"unread":4}`))
	}))
	defer srv.Close()
	p, rb, rt := remotePollerFor(t, srv.URL)
	p.validateURL = func(string) (string, error) { return "", assert.AnError }
	p.validateAddr = func(string) (string, error) { return "127.0.0.1", nil } // only the URL check stands between the poll and the remote
	require.NoError(t, p.RunOnce(context.Background()))
	assert.Zero(t, hits.Load(), "the remote was never asked")
	assertNotNudged(t, rb, rt)
}

// #7109 (Security's LOW 2): the address is checked again where the connection is made. A name the URL check let through but that resolves, at connect time, to an address the check refuses is not dialled;
// the remote is never reached, and the dial goes to the IP that passed the check.
func TestRemoteBoardPollerChecksTheAddressAtDialTime(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"unread":4}`))
	}))
	defer srv.Close()
	p, rb, rt := remotePollerFor(t, srv.URL)
	p.validateURL = func(string) (string, error) { return "203.0.113.1", nil } // the first look says fine
	var dialed []string
	p.validateAddr = func(addr string) (string, error) { dialed = append(dialed, addr); return "", assert.AnError } // the answer changed by the time of the connect
	require.NoError(t, p.RunOnce(context.Background()))
	assert.Zero(t, hits.Load(), "the connection was never made")
	require.Len(t, dialed, 1, "the dial-time check ran on the address being dialled")
	assertNotNudged(t, rb, rt)

	// And when it passes, the connection goes to the IP it returned, not to a second resolution of the name.
	p.validateAddr = func(addr string) (string, error) { return "127.0.0.1", nil }
	require.NoError(t, p.RunOnce(context.Background()))
	assert.EqualValues(t, 1, hits.Load())
}

// remotePollerFor is a poller with one subscription to server whose agent is found.
func remotePollerFor(t *testing.T, server string) (*RemoteBoardPoller, *store.RemoteBoardStore, *mockRuntime) {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	rb := store.NewRemoteBoardStore(db)
	_, err = rb.AddRemoteSub(context.Background(), "sess-1", server, "team", "Dev")
	require.NoError(t, err)
	rt := &mockRuntime{}
	p := NewRemoteBoardPoller(rb, rt, time.Hour)
	p.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "claude", AgentType: "claude", SessionID: "sess-1"}}, nil
	})
	return p, rb, rt
}

func assertNotNudged(t *testing.T, rb *store.RemoteBoardStore, rt *mockRuntime) {
	t.Helper()
	subs, err := rb.ListAllRemoteSubs(context.Background())
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Zero(t, subs[0].LastNotifiedUnread)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	assert.Empty(t, rt.sent)
}

// #7170 (Security's LOW on #7109): the connection is made to the IP the dial-time check returned, not to the name the subscription holds. The remote is registered under a name that does not resolve
// (rebind.invalid); the check returns the loopback, and one poll reaches the server. Dialling the name instead would fail to resolve.
func TestRemoteBoardPollerDialsTheValidatedIPNotTheName(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"unread":0}`))
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	p, _, _ := remotePollerFor(t, "http://rebind.invalid:"+port)
	p.validateURL = func(string) (string, error) { return "203.0.113.1", nil }
	var asked []string
	p.validateAddr = func(addr string) (string, error) { asked = append(asked, addr); return "127.0.0.1", nil }
	require.NoError(t, p.RunOnce(context.Background()))
	assert.EqualValues(t, 1, hits.Load(), "the poll reached the server through the validated IP")
	assert.Equal(t, []string{"rebind.invalid:" + port}, asked, "the check was asked about the name being dialled")
}
