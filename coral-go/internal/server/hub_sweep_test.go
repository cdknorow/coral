package server

import (
	"context"
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
	"github.com/cdknorow/coral/internal/store"
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
	setHome(t, r.dir)
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
	// Dev is the launched terminal, so a board peek at Dev has a terminal to read.
	require.Equal(t, 200, call(h, "POST", "/api/board/team/subscribe", `{"subscriber_id":"Dev","job_title":"Dev","session_name":"`+name+`"}`, js).Code)
	require.Equal(t, 200, call(h, "POST", "/api/board/team/messages", `{"subscriber_id":"Dev","content":"working on it"}`, js).Code)
	rec := call(h, "POST", "/api/board/team/tasks", `{"subscriber_id":"Operator","created_by":"Operator","title":"a task","assigned_to":"Dev"}`, js)
	require.Equal(t, 201, rec.Code, rec.Body.String())
	var task struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &task))
	// #3561 (d): one of everything a View route reads, so all of them answer (each created on
	// this computer, the cheapest way: a local call, a store row, or the file Coral would write).
	ctx := context.Background()
	rec = call(h, "PUT", "/api/agent/ui/1?session_id="+sid, `{"title":"p","html":"<p>x</p>"}`, js)
	require.Equal(t, 200, rec.Code, "agent UI panel: %s", rec.Body.String())
	rec = call(h, "POST", "/api/agent/artifacts?session_id="+sid, "an artifact", map[string]string{"X-Artifact-Name": "a.txt"})
	require.Equal(t, 201, rec.Code, "artifact: %s", rec.Body.String())
	var art struct {
		Digest string `json:"digest"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &art))
	artifactHex := strings.TrimPrefix(art.Digest, "sha256:")
	rec = call(h, "POST", "/api/board/team/tasks/"+strconv.FormatInt(task.ID, 10)+"/complete",
		`{"subscriber_id":"Operator","artifacts":[{"name":"notes.txt","content":"done"}]}`, js)
	require.Equal(t, 200, rec.Code, "task result: %s", rec.Body.String())
	diff := []byte("diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1,2 @@\n hi\n+changed\n")
	for _, p := range []string{
		filepath.Join(r.dir, "artifacts", "tasks", strconv.FormatInt(task.ID, 10), "changes.diff"),
		filepath.Join(r.dir, "artifacts", "sessions", sid, "changes.diff"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, diff, 0o600))
	}
	transcript := filepath.Join(r.dir, ".claude", "projects", "x", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(transcript), 0o700))
	require.NoError(t, os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"hi"},"timestamp":"2026-10-15T00:00:00.000Z"}`+"\n"), 0o600))
	require.NoError(t, store.NewSubagentStore(r.db).UpsertSubagent(ctx, &store.Subagent{SessionID: sid, SubagentID: "x"}))
	require.NoError(t, store.NewSessionStore(r.db).UpdateSessionPID(ctx, sid, 1))
	teamConfig, _ := json.Marshal(map[string]any{"working_dir": r.work, "agents": []map[string]any{{"name": "Dev", "working_dir": r.work}}})
	_, err := store.NewTeamStore(r.db).CreateTeam(ctx, &store.Team{Name: name, WorkingDir: r.work, ConfigJSON: string(teamConfig)})
	require.NoError(t, err)
	require.Equal(t, 200, call(h, "PUT", "/api/themes/"+name, `{"description":"d","base":"dark","variables":{}}`, js).Code, "theme")
	require.Equal(t, 201, call(h, "POST", "/api/views", `{"name":"v","html":"<p>x</p>"}`, js).Code, "view")
	// A step whose command names a folder under home (QA's #3561 case): through the hub it reads
	// "cd ~/work/proj && make test".
	steps, _ := json.Marshal([]map[string]any{{"name": "s", "type": "shell", "command": "cd " + r.work + " && make test", "args": []string{"--dir", r.work}}})
	wf, err := store.NewWorkflowStore(r.db).CreateWorkflow(ctx, &store.Workflow{Name: name, StepsJSON: string(steps)})
	require.NoError(t, err)
	_, err = store.NewScheduleStore(r.db).CreateScheduledJob(ctx, &store.ScheduledJob{Name: "nightly", CronExpr: "0 3 * * *",
		Timezone: "UTC", AgentType: "claude", RepoPath: r.work, Prompt: "run the checks", JobType: "workflow", WorkflowID: &wf.ID, Enabled: 1})
	require.NoError(t, err)
	_, err = store.NewWorkflowStore(r.db).CreateWorkflowRun(ctx, wf.ID, "manual", nil)
	require.NoError(t, err)

	// The step's command and args through the hub: the home shown as "~", the rest as written.
	wfHub := call(h, "GET", "/api/workflows/by-name/"+name, "", map[string]string{tunnel.ViaHeader: cloud.ViaSecret()})
	require.Equal(t, 200, wfHub.Code, wfHub.Body.String())
	require.Contains(t, wfHub.Body.String(), "cd "+strings.Trim(jsonString(tildePath("work", "proj")), `"`)+" \u0026\u0026 make test")

	// #3561 (e): a missing task's (or board's) cost is a 404, not a 500.
	require.Equal(t, 404, call(h, "GET", "/api/board/team/tasks/999999/cost", "", nil).Code)
	require.Equal(t, 404, call(h, "GET", "/api/board/b-nosuchboard00/tasks/1/cost", "", nil).Code)
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"view"}`, js).Code)
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}

	golden, err := os.ReadFile("testdata/cloud_routes.golden")
	require.NoError(t, err)
	homes := homeForms(r.dir) // as given, resolved, and JSON-escaped (#3914)
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
		if f[1] == "/api/artifacts/{id}" {
			path = "/api/artifacts/" + artifactHex
		}
		q := hubSweepQuery(f[1], sid)
		rec := call(h, "GET", path+q, "", hub)
		tried = append(tried, f[1]+" "+strconv.Itoa(rec.Code))
		if rec.Code != 200 && os.Getenv("SWEEP_DEBUG") == "1" {
			t.Logf("NON200 %s%s -> %d %.160s", path, q, rec.Code, rec.Body.String())
		}
		// The template routes fetch only names GitHub's listings hold (#3619, Security's SHOULD on
		// H2): the placeholder "x" isn't one, so its answer is a 404 template_not_found, which is
		// the route answering.
		// /api/sessions/resolve names a session only by its live process (#3864); the rig's
		// session has none, so its answer is a 404 "no matching session" (the reply has no path).
		resolveAnswered := f[1] == "/api/sessions/resolve" && rec.Code == 404 && strings.Contains(rec.Body.String(), "no matching session")
		if rec.Code == 200 || resolveAnswered || (strings.HasPrefix(f[1], "/api/templates/") && rec.Code == 404 && strings.Contains(rec.Body.String(), "template_not_found")) {
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
	require.Equal(t, len(tried), answered, "every View route answers 200 through the tunnel (#3561 (d)): %s", strings.Join(tried, ", "))
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
	vals := map[string]string{"target": "Dev", "session_id": sid, "filepath": "README.md", "agent_type": "terminal", "q": "hi", "query": "hi",
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
