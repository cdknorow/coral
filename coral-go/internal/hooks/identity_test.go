package hooks

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3387: an agent tool sends its session token and names itself, with no action from the
// agent; without a token, nothing is added but the User-Agent.
func TestInstallIdentity(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer srv.Close()
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })

	t.Setenv("CORAL_SESSION_TOKEN", "tok123")
	InstallIdentity("coral-board")
	InstallIdentity("coral-board") // twice is harmless
	_, err := http.Get(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "tok123", got.Get(SessionTokenHeader))
	assert.Equal(t, "coral-board/"+Version, got.Get("User-Agent"))

	http.DefaultTransport = prev
	t.Setenv("CORAL_SESSION_TOKEN", "")
	InstallIdentity("coral-hook-task-sync")
	_, err = http.Get(srv.URL)
	require.NoError(t, err)
	assert.Empty(t, got.Get(SessionTokenHeader))
	assert.Equal(t, "coral-hook-task-sync/"+Version, got.Get("User-Agent"))
}
