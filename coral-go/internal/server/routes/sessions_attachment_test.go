package routes

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agent"
)

const (
	attClaudeSID = "00000000-0000-4000-8000-0000000005a1"
	attCodexSID  = "00000000-0000-4000-8000-0000000005a2"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func jsonLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b) + "\n"
}

func claudeImageEntry(t *testing.T, blocks ...map[string]any) string {
	return jsonLine(t, map[string]any{"type": "user", "timestamp": "2026-10-19T00:00:00Z", "message": map[string]any{"role": "user", "content": blocks}})
}

func imageBlock(mediaType string, data []byte) map[string]any {
	return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": b64(data)}}
}

func writeAttClaudeTranscript(t *testing.T, lines string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "proj"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "proj", attClaudeSID+".jsonl"), []byte(lines), 0o644))
}

func writeAttCodexTranscript(t *testing.T, lines string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	d := filepath.Join(home, "sessions", "2026", "10", "19")
	require.NoError(t, os.MkdirAll(d, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(d, "rollout-2026-10-19T00-00-00-"+attCodexSID+".jsonl"), []byte(lines), 0o644))
}

func attGet(t *testing.T, base, agentType, sid, ref string) (int, http.Header, []byte) {
	t.Helper()
	q := url.Values{"agent_type": {agentType}, "session_id": {sid}, "working_directory": {"/proj"}}
	resp, err := http.Get(base + "/api/sessions/live/pasted/attachment/" + ref + "?" + q.Encode())
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

func chatAttachments(t *testing.T, base, agentType, sid string) []map[string]any {
	t.Helper()
	q := url.Values{"agent_type": {agentType}, "session_id": {sid}, "working_directory": {"/proj"}, "after": {"0"}}
	resp, err := http.Get(base + "/api/sessions/live/pasted/chat?" + q.Encode())
	require.NoError(t, err)
	defer resp.Body.Close()
	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	for _, m := range out.Messages {
		if m["type"] == "user" {
			list, _ := m["attachments"].([]any)
			var atts []map[string]any
			for _, a := range list {
				atts = append(atts, a.(map[string]any))
			}
			return atts
		}
	}
	return nil
}

// #5032: a Claude user turn with an image block has a chip whose reference finds the image in the transcript again, and the route serves it hardened.
func TestChatAttachmentServesAClaudePastedImage(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	writeAttClaudeTranscript(t, claudeImageEntry(t, map[string]any{"type": "text", "text": "what is this"}, imageBlock("image/png", testPNG), imageBlock("image/jpeg", testJPEG)))
	atts := chatAttachments(t, server.URL, "claude", attClaudeSID)
	require.Len(t, atts, 2)
	assert.Equal(t, "[Image #1]", atts[0]["name"])
	assert.Equal(t, agent.ImageRef(b64(testPNG)), atts[0]["ref"])
	assert.Equal(t, "[Image #2]", atts[1]["name"])
	for i, want := range []struct {
		body []byte
		ct   string
	}{{testPNG, "image/png"}, {testJPEG, "image/jpeg"}} {
		code, h, body := attGet(t, server.URL, "claude", attClaudeSID, atts[i]["ref"].(string))
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, want.ct, h.Get("Content-Type"))
		assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
		assert.Contains(t, h.Get("Content-Security-Policy"), "default-src 'none'")
		assert.Contains(t, h.Get("Content-Security-Policy"), "sandbox")
		assert.Contains(t, h.Get("Content-Disposition"), "inline")
		assert.Equal(t, "private, no-cache", h.Get("Cache-Control"))
		assert.Equal(t, want.body, body)
	}
}

// An image-only message reads as its labels, and the chat page JSON carries the reference, never the bytes.
func TestChatAttachmentKeepsBytesOutOfTheChatPage(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	writeAttClaudeTranscript(t, claudeImageEntry(t, imageBlock("image/png", testPNG)))
	q := url.Values{"agent_type": {"claude"}, "session_id": {attClaudeSID}, "working_directory": {"/proj"}, "after": {"0"}}
	resp, err := http.Get(server.URL + "/api/sessions/live/pasted/chat?" + q.Encode())
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(raw), `"content":"[Image #1]"`)
	assert.NotContains(t, string(raw), b64(testPNG), "no image bytes in the chat page")
}

// Codex: the data URL in an input_image block, and in the event's images list, give the same reference.
func TestChatAttachmentServesACodexPastedImage(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	dataURL := "data:image/png;base64," + b64(testPNG)
	writeAttCodexTranscript(t, jsonLine(t, map[string]any{"type": "response_item", "timestamp": "2026-10-19T00:00:00Z", "payload": map[string]any{
		"type": "message", "role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "<image name=[Image #1]>"},
			map[string]any{"type": "input_image", "image_url": dataURL},
			map[string]any{"type": "input_text", "text": "</image>"},
			map[string]any{"type": "input_text", "text": "look"}}}}))
	atts := chatAttachments(t, server.URL, "codex", attCodexSID)
	require.Len(t, atts, 1)
	assert.Equal(t, agent.ImageRef(b64(testPNG)), atts[0]["ref"])
	code, h, body := attGet(t, server.URL, "codex", attCodexSID, atts[0]["ref"].(string))
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "image/png", h.Get("Content-Type"))
	assert.Equal(t, testPNG, body)
}

// An SVG (whatever media type the transcript gives it), HTML or plain text is not an image Coral serves.
func TestChatAttachmentRefusesWhatIsNotAnAllowedImage(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	html := []byte(`<html><script>alert(1)</script></html>`)
	writeAttClaudeTranscript(t, claudeImageEntry(t, imageBlock("image/png", svg), imageBlock("image/svg+xml", svg), imageBlock("image/png", html)))
	for _, data := range [][]byte{svg, html} {
		code, h, body := attGet(t, server.URL, "claude", attClaudeSID, agent.ImageRef(b64(data)))
		assert.Equal(t, http.StatusUnsupportedMediaType, code)
		assert.NotContains(t, h.Get("Content-Type"), "svg")
		assert.NotContains(t, string(body), "alert")
	}
}

// A reference that is not one is 400; one that matches nothing in the session's transcript (another session's image, or none) is 404.
func TestChatAttachmentRefusesBadAndUnknownReferences(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	writeAttClaudeTranscript(t, claudeImageEntry(t, imageBlock("image/png", testPNG)))
	for _, ref := range []string{"short", strings.Repeat("g", 32), strings.Repeat("A", 32), "..%2f..%2fetc%2fpasswd", strings.Repeat("a", 33), "%2e%2e"} {
		code, _, _ := attGet(t, server.URL, "claude", attClaudeSID, ref)
		assert.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound}, code, ref)
		assert.NotEqual(t, http.StatusOK, code, ref)
	}
	code, _, _ := attGet(t, server.URL, "claude", attClaudeSID, agent.ImageRef(b64(testJPEG))) // a valid reference to an image this transcript does not hold
	assert.Equal(t, http.StatusNotFound, code)
	other := "00000000-0000-4000-8000-0000000005a9" // a session with no transcript: even a right reference finds nothing
	code, _, _ = attGet(t, server.URL, "claude", other, agent.ImageRef(b64(testPNG)))
	assert.Equal(t, http.StatusNotFound, code)
}
