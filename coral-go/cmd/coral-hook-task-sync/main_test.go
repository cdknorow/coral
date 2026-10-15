package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fakeCoral records the hook's requests and keeps the tasks it creates.
type fakeCoral struct {
	mu    sync.Mutex
	calls []string // "METHOD escaped-path"
	tasks []map[string]any
}

func (f *fakeCoral) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.EscapedPath())
	switch r.Method {
	case "POST":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.tasks = append(f.tasks, map[string]any{"id": len(f.tasks) + 1, "title": body["title"], "completed": 0})
		w.Write([]byte(`{}`))
	case "GET":
		json.NewEncoder(w).Encode(f.tasks)
	default:
		w.Write([]byte(`{}`))
	}
}

func (f *fakeCoral) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func rig(t *testing.T) (root, data, secret string, coral *fakeCoral) {
	t.Helper()
	root = t.TempDir()
	data = filepath.Join(root, "data")
	secret = filepath.Join(root, "secret")
	os.WriteFile(secret, []byte("SECRET"), 0o600)
	coral = &fakeCoral{}
	srv := httptest.NewServer(coral)
	t.Cleanup(srv.Close)
	t.Setenv("CORAL_URL", srv.URL)
	t.Setenv("CORAL_DATA_DIR", data)
	t.Setenv("CORAL_DIR", "")
	t.Setenv("TMUX", "")
	t.Setenv("CORAL_SESSION_NAME", "")
	t.Setenv("TMPDIR", filepath.Join(root, "tmp")) // where the old cache was: nothing may appear there
	return
}

func event(t *testing.T, cwd, tool string, input, response map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"cwd": cwd, "session_id": "s-1", "tool_name": tool, "tool_input": input, "tool_response": response})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var cacheFile = regexp.MustCompile(`^(task_[0-9a-f]{64}|debug\.log)$`)

// Both hook paths (TaskCreate caches, TaskUpdate reads the cache) with path-shaped task IDs
// (#3571): nothing is written or read outside <data>/hook_cache, the update still finds its
// task, and the debug log holds no subject.
func TestTaskIDsNeverReachAPath(t *testing.T) {
	for _, id := range []string{"../x", "/abs", "a/../../b", "x/../../..", "../../secret"} {
		t.Run(id, func(t *testing.T) {
			root, data, secret, coral := rig(t)
			subject := "Ship the release " + id
			run(event(t, "/w/agent-x", "TaskCreate", map[string]any{"subject": subject}, map[string]any{"taskId": id}))
			if got := coral.took(); len(got) != 1 || got[0] != "POST /api/sessions/live/agent-x/tasks" {
				t.Fatalf("TaskCreate requests: %v", got)
			}
			// TaskUpdate with no subject: the title comes from the cache.
			run(event(t, "/w/agent-x", "TaskUpdate", map[string]any{"taskId": id, "status": "completed"}, nil))
			want := []string{"GET /api/sessions/live/agent-x/tasks", "PATCH /api/sessions/live/agent-x/tasks/1"}
			if got := coral.took(); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("TaskUpdate requests: %v, want %v", got, want)
			}
			// An update naming another task's path reads nothing.
			run(event(t, "/w/agent-x", "TaskUpdate", map[string]any{"taskId": "../secret", "status": "completed"}, nil))
			if got := coral.took(); len(got) != 0 {
				t.Fatalf("an uncached ID resolved a title: %v", got)
			}

			cache := filepath.Join(data, "hook_cache")
			filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || p == secret {
					return err
				}
				if filepath.Dir(p) != cache || !cacheFile.MatchString(d.Name()) {
					t.Errorf("a file outside the cache, or not named by a hash: %s", p)
				}
				return nil
			})
			if b, _ := os.ReadFile(secret); string(b) != "SECRET" {
				t.Errorf("the file outside the cache changed: %q", b)
			}
			if _, err := os.Stat(filepath.Join(root, "tmp")); err == nil {
				t.Error("something was made in the shared temp folder")
			}
			log, _ := os.ReadFile(filepath.Join(cache, "debug.log"))
			if strings.Contains(string(log), "Ship the release") {
				t.Errorf("the debug log holds a subject:\n%s", log)
			}
			if !strings.Contains(string(log), "TaskCreate: cache_id=") {
				t.Errorf("the debug log lost its event lines:\n%s", log)
			}
		})
	}
}

// The agent's name comes from the payload's cwd: it is escaped in every URL, and a cwd
// that names no folder sends nothing.
func TestAgentNameIsEscaped(t *testing.T) {
	_, _, _, coral := rig(t)
	run(event(t, "/w/a?b#c%d", "TaskCreate", map[string]any{"subject": "s"}, map[string]any{"taskId": "1"}))
	if got := coral.took(); len(got) != 1 || got[0] != "POST /api/sessions/live/a%3Fb%23c%25d/tasks" {
		t.Fatalf("requests: %v", got)
	}
	for _, cwd := range []string{"/w/..", "/w/."} {
		run(event(t, cwd, "TaskCreate", map[string]any{"subject": "s"}, map[string]any{"taskId": "1"}))
		if got := coral.took(); len(got) != 0 {
			t.Errorf("cwd %q sent %v", cwd, got)
		}
	}
}
