package background

import (
	"context"
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
