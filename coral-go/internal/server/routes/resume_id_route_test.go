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
	// Every type rejects these: they carry a path separator, "." or ".." (charset floor).
	bad := []string{"../x", "a/../../b", "/abs", "..", "x/../../../etc/passwd", "a.b"}

	// A traversal id ending in a path separator, "." or NUL is rejected for every agent type,
	// including codex (whose non-UUID ids are otherwise allowed).
	for _, agentType := range []string{"claude", "codex"} {
		for _, id := range bad {
			code, body := postRaw(t, server.URL+"/api/sessions/live/victim/resume", map[string]any{"session_id": id, "agent_type": agentType})
			assert.Equal(t, http.StatusBadRequest, code, "%s resume session_id %q", agentType, id)
			assert.Contains(t, body, "resume id", "%s resume session_id %q: %s", agentType, id, body)

			code, body = postRaw(t, server.URL+"/api/sessions/launch", map[string]any{"working_dir": t.TempDir(), "agent_type": agentType, "resume_session_id": id})
			assert.Equal(t, http.StatusBadRequest, code, "%s launch resume_session_id %q", agentType, id)
			assert.Contains(t, body, "resume_session_id", "%s launch resume_session_id %q: %s", agentType, id, body)
		}
	}

	notAUUID := func(code int, body string) bool {
		return code == http.StatusBadRequest && strings.Contains(body, "resume_session_id is not a valid resume id")
	}
	// A canonical UUID passes the path check for claude (it may fail later for other reasons).
	code, body := postRaw(t, server.URL+"/api/sessions/launch",
		map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "resume_session_id": "11111111-2222-4333-8444-555555555555"})
	assert.False(t, notAUUID(code, body), "a valid UUID must pass the claude path check: %d %s", code, body)

	// A non-UUID but safe-charset id passes the path check for codex (its native thread ids),
	// but the SAME id is rejected for claude, which requires a UUID.
	codexID := "codex_thread-01HZX8Y3QK7abcDEF"
	code, body = postRaw(t, server.URL+"/api/sessions/launch",
		map[string]any{"working_dir": t.TempDir(), "agent_type": "codex", "resume_session_id": codexID})
	assert.False(t, notAUUID(code, body), "a codex native id must pass the path check: %d %s", code, body)
	code, _ = postRaw(t, server.URL+"/api/sessions/launch",
		map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "resume_session_id": codexID})
	assert.Equal(t, http.StatusBadRequest, code, "claude must reject a non-UUID resume id")
}
