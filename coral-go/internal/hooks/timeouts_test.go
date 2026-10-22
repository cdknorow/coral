package hooks

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A silent Coral (it takes the connection and never answers) costs a fire-and-forget post FastTimeout, not the full wait: the agent pays it on every tool call (#244).
func TestCoralPostGivesUpFast(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	err := CoralPost(srv.URL, "/api/x", map[string]any{"a": 1})
	took := time.Since(start)
	require.Error(t, err)
	assert.Less(t, took, APITimeout-500*time.Millisecond, "it did not wait the full %s: %s", APITimeout, took)
	assert.GreaterOrEqual(t, took, FastTimeout-100*time.Millisecond)

	start = time.Now()
	_, err = CoralAPITimeout(srv.URL, "GET", "/api/x", nil, 300*time.Millisecond)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second, "an explicit timeout is honoured")
}

func TestTimeoutsAreWhatTheHooksSay(t *testing.T) {
	assert.Equal(t, 3*time.Second, APITimeout)
	assert.Less(t, FastTimeout, time.Second)
}

// The bound is a few times the largest payload a real CLI sends (674 KB seen in 110,000 Claude transcript lines; Codex and agy not measured), not tens of megabytes.
func TestStdinBoundIsSmall(t *testing.T) {
	assert.LessOrEqual(t, MaxHookStdin, 8<<20)
	assert.GreaterOrEqual(t, MaxHookStdin, 2<<20, "well above the largest payload seen")
}
