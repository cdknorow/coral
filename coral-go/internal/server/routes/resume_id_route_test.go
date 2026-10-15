package routes

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func postRaw(t *testing.T, url string, body map[string]any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// A resume session id from the request that isn't a canonical UUID is refused with 400 before
// any filesystem work (#3582): it is joined into history and session-directory paths.
func TestResumeIDsThatEscapePathsAreRejected(t *testing.T) {
	server, _, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	bad := []string{"../x", "a/../../b", "/abs", "..", "x/../../../etc/passwd", "not-a-uuid"}

	for _, id := range bad {
		code, body := postRaw(t, server.URL+"/api/sessions/live/victim/resume", map[string]any{"session_id": id, "agent_type": "claude"})
		assert.Equal(t, http.StatusBadRequest, code, "resume session_id %q", id)
		assert.Contains(t, body, "UUID", "resume session_id %q: %s", id, body)

		code, body = postRaw(t, server.URL+"/api/sessions/launch", map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "resume_session_id": id})
		assert.Equal(t, http.StatusBadRequest, code, "launch resume_session_id %q", id)
		assert.Contains(t, body, "resume_session_id", "launch resume_session_id %q: %s", id, body)
	}

	// A canonical id is not rejected for this reason (it may fail later, but not with the UUID 400).
	code, body := postRaw(t, server.URL+"/api/sessions/launch",
		map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "resume_session_id": "11111111-2222-4333-8444-555555555555"})
	assert.False(t, code == http.StatusBadRequest && strings.Contains(body, "resume_session_id must be"), "a valid UUID must pass the path check: %d %s", code, body)
}
