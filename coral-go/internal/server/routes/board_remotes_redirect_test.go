package routes

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store/storetest"
)

// CodeQL #490 (#7046): the board proxy's request to a registered remote must not follow a redirect to another host: a remote (or anything answering at its address) could send Coral's server to an internal address and have
// the body returned. A 3xx is an error, and the Location is never asked for.
func TestBoardRemoteProxyDoesNotFollowARedirect(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"secret":"an internal answer"}`))
	}))
	t.Cleanup(target.Close)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/board/x", http.StatusFound)
	}))
	t.Cleanup(remote.Close)

	db, err := storetest.Open(t, t.TempDir()+"/remote.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewBoardRemotesHandler(db, &config.Config{})
	h.resolve = func(string) (string, error) { return "127.0.0.1", nil } // the test remotes are on loopback, which the real check refuses
	_, err = h.rbs.AddRemoteSub(t.Context(), "sid", remote.URL, "proj", "Dev")
	require.NoError(t, err)

	body, code, err := h.proxyGet(t.Context(), remote.URL, "/proj/messages/check")
	require.Error(t, err, "a redirect is not an answer")
	assert.Equal(t, http.StatusBadGateway, code)
	assert.Nil(t, body)
	assert.NotContains(t, err.Error(), "internal answer")
	assert.Zero(t, hits.Load(), "the Location was never asked for")
}
