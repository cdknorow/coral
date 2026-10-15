package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// QA's sweep (#3559 review), kept as the route-level guard a fixed field list needs: every View
// GET in the route census, through the tunnel with the real stamp, at view mode, with the
// parameters its query allow-list permits. No answer, whatever its status (an error that quotes
// a path included, Security's review), may carry the home directory as given or resolved. A new
// path field fails here even if hubPathFields doesn't list it.
func TestHubSweepNoHomeInAnyViewRoute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	r := newE2ERemote(t, "hub-sweep", nil)
	t.Setenv("HOME", r.dir)
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
	sid := r.launchDirectTerminal(t, r.work)
	name := r.sessionNameFor(sid)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	// A board with a message and a task, so the board and task routes answer too.
	require.Equal(t, 200, call(h, "POST", "/api/board/team/subscribe", `{"subscriber_id":"Dev","job_title":"Dev"}`, js).Code)
	require.Equal(t, 200, call(h, "POST", "/api/board/team/messages", `{"subscriber_id":"Dev","content":"working on it"}`, js).Code)
	rec := call(h, "POST", "/api/board/team/tasks", `{"subscriber_id":"Operator","created_by":"Operator","title":"a task","assigned_to":"Dev"}`, js)
	require.Equal(t, 201, rec.Code, rec.Body.String())
	var task struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &task))
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"view"}`, js).Code)
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}

	golden, err := os.ReadFile("testdata/cloud_routes.golden")
	require.NoError(t, err)
	homes := []string{r.dir}
	if real, err := filepath.EvalSymlinks(r.dir); err == nil && real != r.dir {
		homes = append(homes, real)
	}
	repl := strings.NewReplacer("{name}", name, "{sessionID}", sid, "{project}", "team", "{boardName}", "team",
		"{taskID}", strconv.FormatInt(task.ID, 10), "{runID}", "1", "{jobID}", "1", "{workflowID}", "1", "{groupID}", "g", "{id}", "1",
		"{category}", "x", "{agentName}", "x", "{subagentID}", "x", "{folderName}", "x", "{name}", name)
	var leaks, tried []string
	answered := 0
	for _, line := range strings.Split(string(golden), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "GET" || f[2] != "view" || strings.HasPrefix(f[1], "/ws/") {
			continue
		}
		path := repl.Replace(f[1])
		q := hubSweepQuery(f[1], sid)
		rec := call(h, "GET", path+q, "", hub)
		tried = append(tried, f[1]+" "+strconv.Itoa(rec.Code))
		if rec.Code == 200 {
			answered++
		}
		for _, home := range homes {
			if strings.Contains(rec.Body.String(), home) {
				leaks = append(leaks, f[1])
				break
			}
		}
	}
	sort.Strings(tried)
	t.Logf("tried %d View GETs: %s", len(tried), strings.Join(tried, ", "))
	if len(leaks) > 0 {
		t.Errorf("home path through the tunnel on: %s", strings.Join(leaks, ", "))
	}
	// The sweep only guards what answers: if the rig or the stamp broke, every route would be
	// refused and nothing checked.
	require.GreaterOrEqual(t, answered, 95, "View routes that answered 200 through the tunnel")
}

var hubSweepQueries = func() map[string]string {
	src, _ := os.ReadFile("../../pkg/tunnel/routes_query.go")
	out := map[string]string{}
	for _, m := range regexp.MustCompile(`"GET ([^"]+)":\s*"([^"]*)"`).FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = m[2]
	}
	return out
}()

func hubSweepQuery(route, sid string) string {
	key := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(route, "{}")
	allowed, ok := hubSweepQueries[key]
	if !ok {
		return ""
	}
	vals := map[string]string{"session_id": sid, "filepath": "README.md", "agent_type": "terminal", "q": "hi", "query": "hi",
		"dir": ".", "message_index": "0", "window": "5", "pids": "1", "page": "1", "limit": "5", "subscriber_id": "x", "timeout": "1"}
	var parts []string
	for _, k := range strings.Split(allowed, ",") {
		k = strings.TrimSuffix(k, "*")
		if v, ok := vals[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "?" + strings.Join(parts, "&")
}
