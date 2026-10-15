package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3561 (c), the write side of TestHubSweepNoHomeInAnyViewRoute: every route Coral Hub may call
// with a body (Interact and Launch, from the route census) gets "~/nope-3561" in every path
// field, through the real stamp at launch mode. HubPaths expands it to this computer's home, so
// an answer that quotes it (an error that names the folder, in any field) would carry the home
// path; no answer, whatever its status, may.
func TestHubSweepNoHomeInAnyWriteAnswer(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	r := newE2ERemote(t, "hub-sweep-write", nil)
	t.Setenv("HOME", r.dir)
	// A checkout with a change, so stopping the agent stores a diff and says where (QA's kill finding).
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
	js := map[string]string{"Content-Type": "application/json"}
	require.Equal(t, 200, call(h, "POST", "/api/board/team/subscribe", `{"subscriber_id":"Dev","job_title":"Dev"}`, js).Code)
	rec := call(h, "POST", "/api/board/team/tasks", `{"subscriber_id":"Operator","created_by":"Operator","title":"a task","assigned_to":"Dev"}`, js)
	require.Equal(t, 201, rec.Code, rec.Body.String())
	var task struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &task))
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, js).Code)
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), "Content-Type": "application/json", "X-Coral-Hub-User": "hub-user-1234abcd"}

	const nope = "~/nope-3561"
	body := map[string]any{"session_id": sid, "subscriber_id": "Dev", "agent_type": "terminal", "name": "x", "title": "x", "content": "x"}
	for _, f := range []string{"working_dir", "working_directory", "default_working_dir", "repo_path", "worktree_path", "git_root",
		"cwd", "root", "dir", "directory", "folder", "path", "filepath", "file"} {
		body[f] = nope
	}
	body["agents"] = []map[string]any{{"name": "x", "working_dir": nope, "prompt": "x"}}
	payload, _ := json.Marshal(body)

	// Not called: these start a process on this computer (an editor, a terminal window, an agent
	// CLI) even when they fail; their path handling is the same HubPaths expansion.
	skip := map[string]string{
		"/api/sessions/live/{name}/attach":                  "opens a terminal window on the host",
		"/api/teams/detail/{name}/distill-knowledge":        "runs an agent CLI",
		"/api/teams/generate":                               "runs an agent CLI",
		"/api/sessions/live/{sessionID}/wake":               "starts the agent's CLI",
		"/api/sessions/live/team/{boardName}/wake":          "starts agents' CLIs",
		"/api/sessions/live/wake-all":                       "starts agents' CLIs",
		"/api/sessions/live/{name}/restart":                 "starts the agent's CLI",
		"/api/sessions/live/{name}/resume":                  "starts the agent's CLI",
		"/api/teams/detail/{name}/resurrect":                "starts agents' CLIs",
		"/api/sessions/live/team/{boardName}/reset":         "starts agents' CLIs",
		"/api/tasks/run":                                    "runs a command on the host",
		"/api/workflows/{workflowID}/trigger":               "runs a workflow's commands",
		"/api/workflows/by-name/{name}/trigger":             "runs a workflow's commands",
		"/api/sessions/live/{name}/auto-accept/enable":      "needs a live agent CLI's prompt",
		"/api/sessions/live/{name}/keys":                    "types into the agent",
		"/api/sessions/live/{name}/send":                    "types into the agent",
		"/api/sessions/live/{name}/answer-prompt":           "types into the agent",
		"/api/board/{project}/tasks/{taskID}/nudge":         "types into the agent",
		"/api/sessions/live/{name}/resize":                  "resizes the terminal",
		"/api/agent/ui-request":                             "asks the agent for a panel",
		"/api/board/{project}/external-messages":            "a relay from another Coral, signed by the hub",
		"/api/board/{project}/hub-notice":                   "set by the hub itself",
		"/api/sessions/live/{name}/files/refresh":           "runs git in the checkout (read-only, but slow under -race)",
		"/api/sessions/live/{name}/goal":                    "types into the agent",
		"/api/upload":                                       "a multipart upload, not JSON",
		"/api/sessions/live/{sessionID}/unarchive":          "nothing archived",
		"/api/board/{project}/tasks/{taskID}/cancel":        "ends the task the others need",
		"/api/board/{project}/tasks/{taskID}/complete":      "ends the task the others need",
		"/api/board/{project}/tasks/{taskID}/reassign":      "changes the task the others need",
		"/api/board/{project}/tasks/{taskID}/submit-review": "changes the task the others need",
	}

	golden, err := os.ReadFile("testdata/cloud_routes.golden")
	require.NoError(t, err)
	repl := strings.NewReplacer("{name}", name, "{sessionID}", sid, "{project}", "team", "{boardName}", "team",
		"{taskID}", strconv.FormatInt(task.ID, 10), "{runID}", "1", "{jobID}", "1", "{workflowID}", "1",
		"{groupID}", "g", "{id}", "1", "{noteID}", "1", "{presetID}", "x", "{messageID}", "1", "{agentName}", "x")
	homes := []string{r.dir}
	if real, err := filepath.EvalSymlinks(r.dir); err == nil && real != r.dir {
		homes = append(homes, real)
	}
	// These end what the others need, so they run last, in this order (covered, not skipped).
	last := []string{
		"/api/sessions/live/{sessionID}/archive", "/api/sessions/live/{sessionID}/sleep",
		"/api/sessions/live/{name}/kill", "/api/sessions/live/team/{boardName}/sleep",
		"/api/sessions/live/team/{boardName}/kill", "/api/sessions/live/sleep-all", "/api/board/{project}",
	}
	lastOK := map[string]bool{}
	for _, p := range last {
		lastOK[p] = true
	}
	var leaks, tried, deferred []string
	for _, line := range strings.Split(string(golden), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] == "GET" || (f[2] != "interact" && f[2] != "launch") {
			continue
		}
		if _, ok := skip[f[1]]; ok {
			continue
		}
		if lastOK[f[1]] {
			deferred = append(deferred, line)
			continue
		}
		rec := call(h, f[0], repl.Replace(f[1]), string(payload), hub)
		tried = append(tried, f[0]+" "+f[1]+" "+strconv.Itoa(rec.Code))
		if rec.Code == 403 && os.Getenv("SWEEP_DEBUG") == "1" {
			t.Logf("403 %s %s: %.200s", f[0], f[1], rec.Body.String())
		}
		for _, home := range homes {
			if strings.Contains(rec.Body.String(), home) {
				leaks = append(leaks, f[0]+" "+f[1]+": "+rec.Body.String())
				break
			}
		}
	}
	// The deferred ones, in the order of last.
	for _, p := range last {
		for _, line := range deferred {
			f := strings.Fields(line)
			if f[1] != p {
				continue
			}
			rec := call(h, f[0], repl.Replace(f[1]), string(payload), hub)
			tried = append(tried, f[0]+" "+f[1]+" "+strconv.Itoa(rec.Code))
			for _, home := range homes {
				if strings.Contains(rec.Body.String(), home) {
					leaks = append(leaks, f[0]+" "+f[1]+": "+rec.Body.String())
					break
				}
			}
		}
	}
	sort.Strings(tried)
	t.Logf("tried %d write routes through the tunnel: %s", len(tried), strings.Join(tried, ", "))
	require.Empty(t, leaks, "the home path came back through the tunnel")
	require.GreaterOrEqual(t, len(tried), 40, "write routes called (a broken rig would call none)")
	for path, why := range skip {
		require.NotEmpty(t, why, path)
	}
}
