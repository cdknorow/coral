package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// QA's #3561 probe, kept: the multipart upload's and a kill's answers through Coral Hub, with
// the real stamp, carry no home path (kill answered the stored diff's absolute path in
// changes_artifact) and no changes_artifact key.
func TestHubKillAndUploadAnswers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	r := newE2ERemote(t, "kill-upload", nil)
	setHome(t, r.dir)
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...)
		cmd.Dir = r.work
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(r.work, "a.txt"), []byte("a\n"), 0o600))
	git("add", "-A")
	git("commit", "-qm", "x")
	require.NoError(t, os.WriteFile(filepath.Join(r.work, "a.txt"), []byte("a\nb\n"), 0o600))
	sid := r.launchDirectTerminal(t, r.work)
	name := r.sessionNameFor(sid)
	h := r.srv.Router()
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, map[string]string{"Content-Type": "application/json"}).Code)
	homes := homeForms(r.dir) // as given, resolved, and JSON-escaped (#3914)
	send := func(method, path, ctype string, body []byte) (int, string) {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Host = "127.0.0.1:8420"
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Content-Type", ctype)
		req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	check := func(what string, code int, body string) {
		t.Logf("%s: %d %.300s", what, code, body)
		for _, home := range homes {
			if strings.Contains(body, home) {
				t.Errorf("%s: home path through the hub: %s", what, strings.ReplaceAll(body, home, "<HOME>"))
			}
		}
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "shot.png")
	fw.Write([]byte("\x89PNG\r\n\x1a\nfake"))
	mw.WriteField("session_id", sid)
	mw.Close()
	code, body := send("POST", "/api/upload", mw.FormDataContentType(), buf.Bytes())
	check("upload", code, body)
	code, body = send("POST", "/api/sessions/live/"+name+"/kill", "application/json", []byte(`{"agent_type":"claude","session_id":"`+sid+`"}`))
	check("kill", code, body)
	require.Equal(t, 200, code, body)
	var killed map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &killed))
	require.NotContains(t, killed, "changes_artifact", "the hub can't open a local file; it reads the diff by its route")
}
