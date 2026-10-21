package routes

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// A home directory reached through a symlink (macOS's /tmp and /var are), so both forms count.
func hubTestHome(t *testing.T) (home, real string) {
	t.Helper()
	base := t.TempDir()
	real = filepath.Join(base, "real-home")
	require.NoError(t, os.MkdirAll(real, 0o700))
	home = filepath.Join(base, "home")
	linktest.Symlink(t, real, home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)            // os.UserHomeDir on Windows
	real, err := filepath.EvalSymlinks(real) // fully resolved, as Coral records working directories
	require.NoError(t, err)
	return home, real
}

func TestHubDisplayAndExpandPath(t *testing.T) {
	home, real := hubTestHome(t)
	for in, want := range map[string]string{
		home:                        "~",
		home + "/proj":              "~/proj",
		real + "/proj/a b":          "~/proj/a b",
		home + "x/proj":             home + "x/proj", // a sibling that only starts with the same letters
		"/opt/work":                 "/opt/work",
		"proj/README.md":            "proj/README.md",
		"":                          "",
		filepath.Dir(home) + "/etc": filepath.Dir(home) + "/etc",
	} {
		assert.Equal(t, want, hubDisplayPath(in), in)
	}
	for in, want := range map[string]string{"~": home, "~/proj": home + "/proj", "~proj": "~proj", "/abs": "/abs", "rel/~": "rel/~"} {
		assert.Equal(t, want, hubExpandPath(in), in)
	}
}

func hubTestRouter(h http.HandlerFunc) http.Handler { return HubPaths(h) }

func hubDo(t *testing.T, h http.Handler, method, target, body string, tunneledReq bool, hdr ...string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if tunneledReq {
		req.Header.Set(tunnel.ViaHeader, "stamp")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

// Through the tunnel, a JSON answer's path fields under the home directory read "~/...",
// the log and tmux fields are gone, nested rows included, and the status stays; from this
// computer the answer is unchanged.
func TestHubPathsRewritesTunneledJSON(t *testing.T) {
	home, real := hubTestHome(t)
	h := hubTestRouter(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"working_directory": real + "/proj",
			"log_path":          home + "/.coral/logs/a.log",
			"tmux_command":      "tmux -S " + home + "/.coral/tmux.sock attach -t a",
			"title":             "notes in " + home + "/proj", // not a path field: left as it is
			"count":             json.Number("12345678901234567890"),
			"sessions":          []any{map[string]any{"working_dir": home + "/w", "log_file": "x", "name": "a"}},
		})
	})
	rec, m := hubDo(t, h, "GET", "/api/x", "", true)
	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "~/proj", m["working_directory"])
	assert.NotContains(t, m, "log_path")
	assert.NotContains(t, m, "tmux_command")
	assert.Equal(t, "notes in "+home+"/proj", m["title"], "only the listed fields are rewritten")
	assert.Contains(t, rec.Body.String(), `"count":12345678901234567890`, "numbers keep their digits")
	row := m["sessions"].([]any)[0].(map[string]any)
	assert.Equal(t, "~/w", row["working_dir"])
	assert.NotContains(t, row, "log_file")
	assert.Equal(t, rec.Body.Len(), mustAtoi(t, rec.Header().Get("Content-Length")))

	rec, m = hubDo(t, h, "GET", "/api/x", "", false)
	assert.Equal(t, real+"/proj", m["working_directory"], "this computer's answer is unchanged")
	assert.Equal(t, home+"/.coral/logs/a.log", m["log_path"])
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	require.NoError(t, json.Unmarshal([]byte(s), &n))
	return n
}

// An answer that isn't JSON (a diff, a file, an event stream) goes through untouched and
// unbuffered: each flush reaches the client as it happens.
func TestHubPathsPassesOtherAnswers(t *testing.T) {
	home, _ := hubTestHome(t)
	flushed := make(chan struct{})
	h := hubTestRouter(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+home+"/proj\n\n")
		w.(http.Flusher).Flush()
		close(flushed)
	})
	rec, _ := hubDo(t, h, "GET", "/api/x", "", true)
	<-flushed
	assert.True(t, rec.Flushed, "the stream was flushed through")
	assert.Equal(t, "data: "+home+"/proj\n\n", rec.Body.String())

	h = hubTestRouter(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "diff --git a/x b/x\n")
	})
	rec, _ = hubDo(t, h, "GET", "/api/x", "", true)
	assert.Equal(t, "diff --git a/x b/x\n", rec.Body.String())
}

// A tunneled request's own "~" paths (JSON body fields) are expanded, so a
// display path the hub got from Coral can be sent back; a local request is left alone.
func TestHubPathsExpandsTunneledRequests(t *testing.T) {
	home, _ := hubTestHome(t)
	var gotBody map[string]any
	h := hubTestRouter(func(w http.ResponseWriter, r *http.Request) {
		gotBody = nil
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	body := `{"working_dir":"~/proj","prompt":"~/not a path field","agents":[{"cwd":"~"}]}`
	hubDo(t, h, "POST", "/api/x", body, true, "Content-Type", "application/json")
	assert.Equal(t, home+"/proj", gotBody["working_dir"])
	assert.Equal(t, "~/not a path field", gotBody["prompt"])
	assert.Equal(t, home, gotBody["agents"].([]any)[0].(map[string]any)["cwd"])

	hubDo(t, h, "POST", "/api/x", body, false, "Content-Type", "application/json")
	assert.Equal(t, "~/proj", gotBody["working_dir"], "local requests are unchanged")
}

// /ws/coral's frames: the rows are shared between connections, so a hub's copy is rewritten
// and the original isn't touched.
func TestHubFrameLeavesSharedRowsAlone(t *testing.T) {
	home, _ := hubTestHome(t)
	row := map[string]any{"working_directory": home + "/proj", "log_path": home + "/l.log", "name": "a"}
	msg := map[string]any{"type": "coral_update", "sessions": []map[string]any{row}}
	raw, ok := hubFrame(msg).(json.RawMessage)
	require.True(t, ok)
	var got struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "~/proj", got.Sessions[0]["working_directory"])
	assert.NotContains(t, got.Sessions[0], "log_path")
	assert.Equal(t, home+"/proj", row["working_directory"], "the shared row is unchanged")
	assert.Equal(t, home+"/l.log", row["log_path"])
}

// File lists leave out hidden files' names through the tunnel (Security's #3556 finding 5).
func TestHubVisibleFiles(t *testing.T) {
	files := []store.ChangedFile{{Filepath: "README.md"}, {Filepath: ".env.local"}, {Filepath: "keys/id_rsa"},
		{Filepath: "src/.secret/x"}, {Filepath: "/home/u/.ssh/config"}, {Filepath: "notes.txt"}}
	local := httptest.NewRequest("GET", "/", nil)
	assert.Len(t, hubVisibleFiles(local, files), 6)
	tun := httptest.NewRequest("GET", "/", nil)
	tun.Header.Set(tunnel.ViaHeader, "stamp")
	var names []string
	for _, f := range hubVisibleFiles(tun, files) {
		names = append(names, f.Filepath)
	}
	assert.Equal(t, []string{"README.md", "notes.txt"}, names)
}

// A tunneled JSON request with no "~" in it reaches the handler whole (it is read to look).
func TestHubPathsKeepsOtherRequestBodies(t *testing.T) {
	hubTestHome(t)
	var got string
	h := hubTestRouter(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		got = string(b)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	body := `{"subscriber_id":"Operator","content":"@Dev ship it"}`
	rec, _ := hubDo(t, h, "POST", "/api/board/team/messages", body, true, "Content-Type", "application/json")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, body, got)
	big := `{"content":"` + strings.Repeat("x", 9<<20) + `"}`
	hubDo(t, h, "POST", "/api/x", big, true, "Content-Type", "application/json")
	assert.Equal(t, len(big), len(got), "a body over the 8 MiB look is passed through whole")
}

// Security's #3559 review: an error, detail or message that quotes a path under home shows
// "~" there too, at a path boundary only; other strings are left alone.
func TestHubDisplayTextInMessages(t *testing.T) {
	home, real := hubTestHome(t)
	for in, want := range map[string]string{
		"open " + real + "/nope: no such file":   "open ~/nope: no such file",
		"'" + home + "' is not allowed":          "'~' is not allowed",
		"two: " + home + "/a and " + real + "/b": "two: ~/a and ~/b",
		"sibling " + home + "x/y stays":          "sibling " + home + "x/y stays",
		"nothing here":                           "nothing here",
		// #3561 (b): a file:// URL, and a URL in general, before the home.
		"see file://" + real + "/notes.md":   "see file://~/notes.md",
		"<a href=\"file://" + home + "/x\">": "<a href=\"file://~/x\">",
		"vscode://file" + home + "/a":        "vscode://file" + home + "/a", // not a path start
		home + "/a" + home + "/b":            "~/a" + home + "/b",           // the second follows a path character
	} {
		assert.Equal(t, want, hubDisplayText(in), in)
	}
	h := hubTestRouter(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "open " + real + "/nope: no such file", "detail": home, "hint": home + "/x"})
	})
	rec, m := hubDo(t, h, "GET", "/api/x", "", true)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "open ~/nope: no such file", m["error"])
	assert.Equal(t, "~", m["detail"])
	assert.Equal(t, "~/x", m["hint"], "a value that is itself a home path is shown as ~ in any field (#3561)")
}

// #3561 delta (Security's value-based rule, QA's kill finding): a value that is itself a home
// path is shown as "~/..." in any field (so a new field name can't carry it out), in lists
// too; free text and multi-line values are left alone; changes_artifact is dropped.
func TestHubPathsAnyFieldHoldingAHomePath(t *testing.T) {
	home, real := hubTestHome(t)
	h := hubTestRouter(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"stored_at":        real + "/.coral/artifacts/x",
			"changes_artifact": home + "/.coral/artifacts/sessions/s/changes.diff",
			"dirs":             []any{home + "/a", "/opt/b", "plain"},
			"content":          home + "/notes: written by the agent",
			"note":             home + "/a\nsecond line",
			"label":            "my " + home + "/a", // not a path from its start
			"nested":           map[string]any{"backup_path": home + "/b", "count": 3},
		})
	})
	_, m := hubDo(t, h, "GET", "/api/x", "", true)
	assert.Equal(t, "~/.coral/artifacts/x", m["stored_at"])
	assert.NotContains(t, m, "changes_artifact")
	assert.Equal(t, []any{"~/a", "/opt/b", "plain"}, m["dirs"])
	assert.Equal(t, home+"/notes: written by the agent", m["content"], "free text is left as written")
	assert.Equal(t, home+"/a\nsecond line", m["note"], "a multi-line value isn't a path")
	assert.Equal(t, "my "+home+"/a", m["label"])
	assert.Equal(t, "~/b", m["nested"].(map[string]any)["backup_path"])

	_, m = hubDo(t, h, "GET", "/api/x", "", false)
	assert.Equal(t, real+"/.coral/artifacts/x", m["stored_at"], "this computer's answer is unchanged")
	assert.Contains(t, m, "changes_artifact")
}

// QA's #3561 case: a home folder inside a command (a workflow step, its args, a script) is
// shown as "~" through the hub; a prompt is free text and is shown as written.
func TestHubPathsInCommands(t *testing.T) {
	home, real := hubTestHome(t)
	h := hubTestRouter(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"steps":  []any{map[string]any{"command": "cd " + real + "/proj && make test", "args": []any{"--dir", home + "/x"}}},
			"script": "export P=" + home + "/bin:$PATH",
			"cmd":    "ls " + home + "x/y", // a sibling folder that only starts with the same letters
			"prompt": "check " + home + "/proj",
		})
	})
	_, m := hubDo(t, h, "GET", "/api/x", "", true)
	step := m["steps"].([]any)[0].(map[string]any)
	assert.Equal(t, "cd ~/proj && make test", step["command"])
	assert.Equal(t, []any{"--dir", "~/x"}, step["args"])
	assert.Equal(t, "export P=~/bin:$PATH", m["script"])
	assert.Equal(t, "ls "+home+"x/y", m["cmd"])
	assert.Equal(t, "check "+home+"/proj", m["prompt"], "prompts are shown as written")
}

// #3935: where the file system ignores case (Windows, macOS by default) a home typed in another
// case is the home, and on Windows "/" and "\" are the same separator; elsewhere the match is
// exact. Run on any OS through the seams.
func TestHubHomeMatchFoldsCaseAndSeparators(t *testing.T) {
	home := filepath.Join(t.TempDir(), "Alice-Home")
	require.NoError(t, os.MkdirAll(home, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	fold, slashes := hubFold, hubSlashes
	t.Cleanup(func() { hubFold, hubSlashes = fold, slashes })
	upper := strings.ToUpper(home)
	backslashed := strings.ReplaceAll(home, "/", `\`)

	hubFold, hubSlashes = true, false // macOS
	assert.Equal(t, "~/proj", hubDisplayPath(upper+"/proj"), "another case")
	assert.Equal(t, "~", hubDisplayPath(strings.ToLower(home)))
	assert.Equal(t, upper+"x/proj", hubDisplayPath(upper+"x/proj"), "a sibling stays")
	assert.Equal(t, "open ~/nope: no such file", hubDisplayText("open "+upper+"/nope: no such file"))
	assert.Equal(t, "two: ~/a and ~/b", hubDisplayText("two: "+upper+"/a and "+home+"/b"))

	hubFold, hubSlashes = true, true // Windows
	assert.Equal(t, `~\proj`, hubDisplayPath(strings.ToUpper(backslashed)+`\proj`), "another case and the other separator")
	assert.Equal(t, "~/proj", hubDisplayPath(upper+"/proj"))
	assert.Equal(t, `cd ~\proj && make`, hubDisplayText(`cd `+backslashed+`\proj && make`))

	hubFold, hubSlashes = false, false // Linux
	assert.Equal(t, upper+"/proj", hubDisplayPath(upper+"/proj"), "case matters there")
	assert.Equal(t, "~/proj", hubDisplayPath(home+"/proj"))
}

func TestHubKeyKeepsLengths(t *testing.T) {
	fold, slashes := hubFold, hubSlashes
	t.Cleanup(func() { hubFold, hubSlashes = fold, slashes })
	hubFold, hubSlashes = true, true
	for _, s := range []string{`C:/Users/Zoë/Proj`, "ÄÖÜ/ß/İ", ""} {
		assert.Len(t, hubKey(s), len(s), s)
	}
	assert.Equal(t, `c:\users\zoë\proj`, hubKey(`C:/Users/Zoë/Proj`))
}
