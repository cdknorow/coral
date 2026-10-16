package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3559 (Security's #3556 findings 3 and 5), through the real router with the connector's
// real stamp: an agent working under the home directory is shown to Coral Hub as "~/...",
// without its log path or tmux command, and its changed-files list leaves out hidden files;
// /ws/coral's frames to the hub get the same treatment. This computer's answers are unchanged.
func TestHubPathsThroughTheTunnel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	r := newE2ERemote(t, "hub-paths", nil)
	t.Setenv("HOME", r.dir) // the agent's folder, r.work, is under it
	gitIn := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...)
		cmd.Dir = r.work
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	gitIn("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(r.work, "README.md"), []byte("hi\n"), 0o600))
	gitIn("add", "README.md")
	gitIn("commit", "-qm", "init")
	require.NoError(t, os.WriteFile(filepath.Join(r.work, "README.md"), []byte("hi\nchanged\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(r.work, ".env.local"), []byte("SECRET=fake\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(r.work, "keys"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(r.work, "keys", "id_rsa"), []byte("k\n"), 0o600))

	sid := r.launchDirectTerminal(t, r.work)
	name := r.sessionNameFor(sid)
	require.NotEmpty(t, name)

	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"view"}`, js).Code)
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}
	get := func(path string, hdr map[string]string) map[string]any {
		t.Helper()
		rec := call(h, "GET", path, "", hdr)
		require.Equal(t, 200, rec.Code, "%s: %s", path, rec.Body.String())
		var m map[string]any
		if strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "[") {
			var list []any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
			return map[string]any{"list": list}
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
		return m
	}
	noHome := func(where string, v any) {
		t.Helper()
		data, _ := json.Marshal(v)
		assert.NotContains(t, string(data), r.dir, "%s: no path under the home directory", where)
		if real, err := filepath.EvalSymlinks(r.dir); err == nil {
			assert.NotContains(t, string(data), real, "%s: nor its resolved form", where)
		}
	}
	mine := func(list []any) map[string]any {
		for _, s := range list {
			if row, ok := s.(map[string]any); ok && row["session_id"] == sid {
				return row
			}
		}
		t.Fatalf("session %s not listed", sid)
		return nil
	}

	// The live list.
	local := mine(get("/api/sessions/live", nil)["list"].([]any))
	assert.Contains(t, local["working_directory"], r.work[len(filepath.Dir(r.work)):], "locally, the real folder")
	assert.NotEmpty(t, local["log_path"])
	viaHub := get("/api/sessions/live", hub)
	row := mine(viaHub["list"].([]any))
	assert.Equal(t, "~/work/proj", row["working_directory"])
	assert.NotContains(t, row, "log_path")
	noHome("live list", viaHub)

	// Info.
	info := get("/api/sessions/live/"+name+"/info", hub)
	assert.Equal(t, "~/work/proj", info["working_directory"])
	assert.NotContains(t, info, "tmux_command")
	noHome("info", info)
	assert.NotEmpty(t, get("/api/sessions/live/"+name+"/info", nil)["working_directory"], "locally the real folder")

	// Resolve.
	res := get("/api/sessions/"+sid+"/resolve", hub)
	noHome("resolve", res)

	// Changed files: hidden names left out through the hub only.
	names := func(m map[string]any) []string {
		var out []string
		for _, f := range m["files"].([]any) {
			out = append(out, f.(map[string]any)["filepath"].(string))
		}
		return out
	}
	localFiles := names(get("/api/sessions/live/"+name+"/files?session_id="+sid, nil))
	assert.Contains(t, localFiles, ".env.local", "locally every changed file is listed")
	hubFiles := names(get("/api/sessions/live/"+name+"/files?session_id="+sid, hub))
	assert.Contains(t, hubFiles, "README.md")
	assert.NotContains(t, hubFiles, ".env.local")
	assert.NotContains(t, hubFiles, "keys/id_rsa")

	// A "~" path the hub sends in a body is expanded (resume, launch in a folder), and an error
	// that quotes it doesn't echo the real home path (Security's #3559 review). The tunnel's
	// query allow-list refuses "~" in file parameters, so bodies are where it arrives.
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)
	launch := func(dir string) *httptest.ResponseRecorder {
		return call(h, "POST", "/api/sessions/launch", `{"working_dir":"`+dir+`","agent_type":"terminal"}`, mergeHdr(hub, js))
	}
	bad := launch("~/nope")
	assert.NotEqual(t, 200, bad.Code, bad.Body.String())
	noHome("launch in ~/nope", bad.Body.String())
	t.Logf("launch ~/nope -> %d %s", bad.Code, bad.Body.String())
	good := launch("~/work/proj")
	require.Equal(t, 200, good.Code, good.Body.String())
	var started map[string]any
	require.NoError(t, json.Unmarshal(good.Body.Bytes(), &started))
	noHome("launch answer", started)
	ls, err := store.NewSessionStore(r.db).GetLiveSession(context.Background(), started["session_id"].(string))
	require.NoError(t, err)
	require.NotNil(t, ls)
	assert.Equal(t, r.work, ls.WorkingDir, "the hub's ~/work/proj started in the real folder")
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"view"}`, js).Code)

	// /ws/coral through the tunnel.
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set(tunnel.ViaHeader, cloud.ViaSecret())
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws/coral", &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err)
	defer c.CloseNow()
	var frame struct {
		Type     string           `json:"type"`
		Sessions []map[string]any `json:"sessions"`
	}
	for frame.Type != "coral_update" {
		require.NoError(t, wsjson.Read(ctx, c, &frame))
	}
	var wsRow map[string]any
	for _, s := range frame.Sessions {
		if s["session_id"] == sid {
			wsRow = s
		}
	}
	require.NotNil(t, wsRow, "the session is in the hub's feed")
	assert.Equal(t, "~/work/proj", wsRow["working_directory"])
	assert.NotContains(t, wsRow, "log_path")
	noHome("ws/coral", frame)
}
