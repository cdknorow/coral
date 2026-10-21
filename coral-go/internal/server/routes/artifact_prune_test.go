package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
	_ "modernc.org/sqlite"
)

// #3554: retention and pruning. What is never pruned (references in every place, kept, open
// tasks, running sessions, a young upload), what goes, shrink, the shared-diff sweep, the
// race with a new link, a failed scan, and who may run it.

type pruneRig struct {
	t        *testing.T
	h        *SessionsHandler
	bs       *board.Store
	boardDB  *sqlx.DB // a writer on the board database, to set task fields directly
	coralDir string
	srv      *httptest.Server
	now      time.Time
}

func newPruneRig(t *testing.T) *pruneRig {
	t.Helper()
	cfg := config.Load(t.TempDir())
	cfg.LogDir = t.TempDir()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	boardPath := filepath.Join(t.TempDir(), "board.db")
	bs, err := boardtest.Open(boardPath)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	raw, err := sqlx.Open("sqlite", boardPath+"?_pragma=busy_timeout(10000)")
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })
	h := NewSessionsHandler(db, cfg, nil, newMockTerminal(), bs)
	r := chi.NewRouter()
	ident := testIdentity(db)
	r.Use(func(next http.Handler) http.Handler {
		withIdent := ident(next)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get(testDashboardHeader) != "" {
				next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie)))
				return
			}
			withIdent.ServeHTTP(w, req)
		})
	})
	h.RegisterAgentArtifacts(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &pruneRig{t: t, h: h, bs: bs, boardDB: raw, coralDir: cfg.CoralDir(), srv: srv, now: time.Now()}
}

// object stores data as an artifact uploaded by session at the given age; it returns the id.
func (rig *pruneRig) object(data, session string, age time.Duration) string {
	id, _, err := storeArtifact(rig.coralDir, session, "f.txt", "text/plain", []byte(data))
	require.NoError(rig.t, err)
	rig.age(id, session, age)
	return id
}

// age back-dates an object's file and its upload record.
func (rig *pruneRig) age(id, session string, age time.Duration) {
	at := rig.now.Add(-age)
	obj := filepath.Join(rig.coralDir, "artifacts", "objects", id)
	require.NoError(rig.t, os.Chtimes(obj, at, at))
	rec := filepath.Join(rig.coralDir, "artifacts", "uploads", session, id+".json")
	b, err := os.ReadFile(rec)
	require.NoError(rig.t, err)
	var m map[string]any
	require.NoError(rig.t, json.Unmarshal(b, &m))
	m["created_at"] = at.UTC().Format(time.RFC3339)
	b, _ = json.Marshal(m)
	require.NoError(rig.t, os.WriteFile(rec, b, 0o600))
}

func (rig *pruneRig) exists(id string) bool {
	_, err := os.Stat(filepath.Join(rig.coralDir, "artifacts", "objects", id))
	return err == nil
}

func (rig *pruneRig) post(content string) {
	_, err := rig.bs.PostMessage(context.Background(), "team", "Lead", content, nil)
	require.NoError(rig.t, err)
}

// task makes a board task with status, completed this long ago, and stores diff as its changes.
func (rig *pruneRig) task(status string, completedAgo time.Duration, diff []byte) int64 {
	tk, err := rig.bs.CreateTask(context.Background(), "team", "t", "", "medium", "Operator")
	require.NoError(rig.t, err)
	at := rig.now.Add(-completedAgo).UTC().Format(time.RFC3339)
	_, err = rig.boardDB.Exec("UPDATE board_tasks SET status = ?, completed_at = ? WHERE id = ?", status, at, tk.ID)
	require.NoError(rig.t, err)
	if diff != nil {
		require.NoError(rig.t, writeChangesArtifact(rig.taskDiff(tk.ID), diff))
	}
	return tk.ID
}

func (rig *pruneRig) taskDiff(id int64) string {
	return filepath.Join(rig.coralDir, "artifacts", "tasks", fmt.Sprint(id), "changes.diff")
}

func (rig *pruneRig) session(id, status string, sleeping bool, stoppedAgo time.Duration, diff []byte) string {
	ss := store.NewSessionStore(rig.h.db)
	require.NoError(rig.t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: id, AgentName: id, AgentType: "claude", WorkingDir: rig.t.TempDir()}))
	sl := 0
	if sleeping {
		sl = 1
	}
	_, err := rig.h.db.Exec("UPDATE live_sessions SET status = ?, is_sleeping = ?, stopped_at = ? WHERE session_id = ?", status, sl, rig.now.Add(-stoppedAgo).UTC().Format(time.RFC3339), id)
	require.NoError(rig.t, err)
	p := filepath.Join(rig.coralDir, "artifacts", "sessions", id, "changes.diff")
	require.NoError(rig.t, writeChangesArtifact(p, diff))
	return p
}

func (rig *pruneRig) run(o pruneOptions) *pruneReport {
	o.Now = rig.now
	rep, err := rig.h.runPrune(context.Background(), o)
	require.NoError(rig.t, err)
	return rep
}

func kinds(k ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range k {
		m[x] = true
	}
	return m
}

func ageAllDiffObjects(t *testing.T, coralDir string, age time.Duration) {
	dir := filepath.Join(coralDir, "artifacts", "diff-objects")
	ents, _ := os.ReadDir(dir)
	at := time.Now().Add(-age)
	for _, e := range ents {
		require.NoError(t, os.Chtimes(filepath.Join(dir, e.Name()), at, at))
	}
}

func TestPruneObjectsNeverTakesAReferencedKeptOrYoungOne(t *testing.T) {
	rig := newPruneRig(t)
	old := 30 * 24 * time.Hour
	inPost := rig.object("linked from a post", "s1", old)
	inTask := rig.object("linked from a task result", "s1", old)
	inNote := rig.object("linked from agent notes", "s1", old)
	inKnowledge := rig.object("linked from knowledge", "s1", old)
	kept := rig.object("kept by the Operator", "s1", old)
	young := rig.object("uploaded again recently", "s1", old)
	unused := rig.object("nothing links this", "s1", old)
	// young: a second agent uploaded the same bytes yesterday.
	_, _, err := storeArtifact(rig.coralDir, "s2", "again.txt", "text/plain", []byte("uploaded again recently"))
	require.NoError(t, err)
	rig.age(young, "s2", 24*time.Hour)

	rig.post("see [the report](coral://artifacts/" + strings.ToUpper(inPost) + ")") // case-insensitive
	tk, err := rig.bs.CreateTask(context.Background(), "team", "x", "", "medium", "Operator")
	require.NoError(t, err)
	_, err = rig.boardDB.Exec("UPDATE board_tasks SET completion_message = ? WHERE id = ?", "result: </api/artifacts/"+inTask+"?v=2>", tk.ID)
	require.NoError(t, err)
	_, err = rig.h.db.Exec("INSERT INTO agent_notes (agent_name, session_id, content, created_at, updated_at) VALUES ('a9', 's9', ?, 'x', 'x')", "`coral://artifacts/"+inNote+"`")
	require.NoError(t, err)
	kdir := filepath.Join(rig.coralDir, "agent_knowledge", "team")
	require.NoError(t, os.MkdirAll(kdir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(kdir, "facts.md"), []byte("- ![shot](coral://artifacts/"+inKnowledge+")\n"), 0o600))
	_, err = rig.h.db.Exec("INSERT INTO artifact_keep (hex, kept_at) VALUES (?, 'x')", kept)
	require.NoError(t, err)

	dry := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindObjects), DryRun: true})
	require.Equal(t, 1, dry.Kinds[pruneKindObjects].Count, "only the unused, old object: %+v", dry.Largest)
	assert.Equal(t, unused, dry.Largest[0].ID)
	assert.True(t, rig.exists(unused), "a dry run removes nothing")

	rep := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindObjects)})
	assert.Equal(t, 1, rep.Removed)
	assert.False(t, rig.exists(unused), "the unused object went")
	_, err = os.Stat(filepath.Join(rig.coralDir, "artifacts", "uploads", "s1", unused+".json"))
	assert.True(t, os.IsNotExist(err), "with its upload record")
	for name, id := range map[string]string{"post": inPost, "task": inTask, "note": inNote, "knowledge": inKnowledge, "kept": kept, "young": young} {
		assert.True(t, rig.exists(id), "kept: %s", name)
	}
}

func TestPruneReferenceForms(t *testing.T) {
	id := strings.Repeat("ab", 32)
	for _, text := range []string{
		"coral://artifacts/" + id, "/api/artifacts/" + id, "/api/artifacts/" + id + "?v=2",
		"[x](coral://artifacts/" + id + ")", "`coral://artifacts/" + id + "`", "<coral://artifacts/" + id + ">",
		"![x](/api/artifacts/" + strings.ToUpper(id) + "?v=2)", `{"uri":"coral://artifacts/` + id + `"}`,
	} {
		refs := map[string]bool{}
		addArtifactRefs([]byte(text), refs)
		assert.True(t, refs[id], text)
	}
}

func TestPruneRescanKeepsAnObjectLinkedMidRun(t *testing.T) {
	rig := newPruneRig(t)
	id := rig.object("linked while the prune runs", "s1", 30*24*time.Hour)
	pruneAfterCandidatesHook = func() { rig.post("now it's linked: coral://artifacts/" + id) }
	t.Cleanup(func() { pruneAfterCandidatesHook = nil })
	rep := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindObjects)})
	assert.True(t, rig.exists(id), "the re-scan under the run saw the new link")
	assert.Equal(t, 0, rep.Removed)
	assert.Equal(t, 1, rep.Skipped)
}

func TestPruneFailedScanRemovesNothing(t *testing.T) {
	rig := newPruneRig(t)
	id := rig.object("nothing links this", "s1", 30*24*time.Hour)
	require.NoError(t, rig.bs.Close()) // the board database can't be read
	// A dry run fails too: it must never list "what it could verify".
	_, err := rig.h.runPrune(context.Background(), pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindObjects), DryRun: true, Now: rig.now})
	require.Error(t, err, "a failed scan fails the dry run as well")
	_, err = rig.h.runPrune(context.Background(), pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindObjects), Now: rig.now})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing removed")
	assert.True(t, rig.exists(id))
}

func TestPruneDiffsOnlyClosedAndOld(t *testing.T) {
	rig := newPruneRig(t)
	old, recent := 20*24*time.Hour, 2*24*time.Hour
	done := rig.task("completed", old, []byte("diff --git a/a b/a\n+done\n"))
	skipped := rig.task("skipped", old, []byte("diff --git a/a b/a\n+skipped\n"))
	open := rig.task("in_progress", old, []byte("diff --git a/a b/a\n+open\n"))
	review := rig.task("review_pending", old, []byte("diff --git a/a b/a\n+review\n"))
	fresh := rig.task("completed", recent, []byte("diff --git a/a b/a\n+fresh\n"))
	stopped := rig.session("sess-stopped", "inactive", false, old, []byte("diff --git a/a b/a\n+stopped\n"))
	running := rig.session("sess-running", "active", false, old, []byte("diff --git a/a b/a\n+running\n"))
	asleep := rig.session("sess-asleep", "inactive", true, old, []byte("diff --git a/a b/a\n+asleep\n"))

	// The dry run lists exactly the closed, old ones (both layers: the listing and the re-check).
	dry := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindDiffs), DryRun: true})
	var listed []string
	for _, it := range dry.Largest {
		listed = append(listed, it.ID)
	}
	assert.ElementsMatch(t, []string{fmt.Sprintf("task %d", done), fmt.Sprintf("task %d", skipped), "session sess-stopped"}, listed,
		"never an open or in-review task, a running or sleeping session, or a recent one")

	rep := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindDiffs)})
	gone := func(p string) bool { _, err := os.Stat(p); return os.IsNotExist(err) }
	assert.True(t, gone(rig.taskDiff(done)))
	assert.True(t, gone(rig.taskDiff(skipped)))
	assert.True(t, gone(stopped))
	assert.False(t, gone(rig.taskDiff(open)), "an open task keeps its diff")
	assert.False(t, gone(rig.taskDiff(review)), "a task in review keeps its diff")
	assert.False(t, gone(rig.taskDiff(fresh)), "completed too recently")
	assert.False(t, gone(running), "a running session keeps its diff")
	assert.False(t, gone(asleep), "a sleeping session can be resumed")
	assert.Equal(t, 3, rep.Removed)
}

func TestPruneSharedDiffSweep(t *testing.T) {
	rig := newPruneRig(t)
	same := []byte("diff --git a/a b/a\n+the same change\n")
	a := rig.task("completed", 20*24*time.Hour, same)
	b := rig.task("completed", 2*24*time.Hour, same) // completed recently: stays
	objects := func() int {
		e, _ := os.ReadDir(filepath.Join(rig.coralDir, "artifacts", "diff-objects"))
		return len(e)
	}
	require.Equal(t, 1, objects(), "one shared object")
	ageAllDiffObjects(t, rig.coralDir, 30*24*time.Hour)

	rep := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindDiffs)})
	assert.Equal(t, 1, rep.Removed, "task a's link")
	assert.EqualValues(t, 0, rep.Freed, "b still links the bytes")
	assert.Equal(t, 1, objects(), "the object stays while b links it")
	got, err := os.ReadFile(rig.taskDiff(b))
	require.NoError(t, err)
	assert.Equal(t, same, got)
	_, err = os.Stat(rig.taskDiff(a))
	assert.True(t, os.IsNotExist(err))

	rig.now = rig.now.Add(10 * 24 * time.Hour) // b is now old too
	rep = rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindDiffs)})
	assert.Equal(t, 2, rep.Removed, "b's link, then the object at the sweep")
	assert.EqualValues(t, len(same), rep.Freed, "freed once")
	assert.Equal(t, 0, objects())
}

func TestShrinkRewritesClosedDiffsOnly(t *testing.T) {
	rig := newPruneRig(t)
	secret := "OPENAI_API_KEY=qa-secret"
	old := []byte("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/.env.local b/.env.local\nnew file mode 100644\n--- /dev/null\n+++ b/.env.local\n@@ -0,0 +1 @@\n+" + secret + "\n" +
		"diff --git a/shot.png b/shot.png\nnew file mode 100644\nGIT binary patch\nliteral 3\nKcmZ?wbN~PV\n\n")
	closed := rig.task("completed", 0, old)
	open := rig.task("in_progress", 0, old) // shares the object; must keep its bytes

	dry := rig.run(pruneOptions{Shrink: true, DryRun: true})
	require.Equal(t, 1, dry.Kinds["shrink"].Count)
	got, _ := os.ReadFile(rig.taskDiff(closed))
	require.Equal(t, old, got, "a dry run rewrites nothing")

	rep := rig.run(pruneOptions{Shrink: true})
	assert.Equal(t, 1, rep.Removed)
	got, err := os.ReadFile(rig.taskDiff(closed))
	require.NoError(t, err)
	assert.Contains(t, string(got), "diff --git a/main.go b/main.go", "the text change stays")
	assert.NotContains(t, string(got), secret)
	assert.NotContains(t, string(got), "GIT binary patch")
	assert.Less(t, len(got), len(old))
	still, err := os.ReadFile(rig.taskDiff(open))
	require.NoError(t, err)
	assert.Equal(t, old, still, "the open task's diff, which shared the object, is untouched")
	fi, err := os.Stat(rig.taskDiff(closed))
	require.NoError(t, err)
	if runtime.GOOS != "windows" { // NTFS has no Unix permission bits (Go reports 0666)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}

	rep = rig.run(pruneOptions{Shrink: true})
	assert.Equal(t, 0, rep.Removed, "already shrunk: nothing to do")
}

func TestPruneRouteIsTheOperators(t *testing.T) {
	rig := newPruneRig(t)
	call := func(body string, hdr map[string]string) (int, map[string]any) {
		req, err := http.NewRequest(http.MethodPost, rig.srv.URL+"/api/artifacts/prune", bytes.NewReader([]byte(body)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	body := `{"older_than_seconds": 604800, "kinds": ["all"]}`
	for name, hdr := range map[string]map[string]string{
		"an agent's token":       {testAgentHeader: "s1"},
		"a token-less script":    {},
		"Coral Hub (the tunnel)": {tunnel.ViaHeader: "x"},
	} {
		code, out := call(body, hdr)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Equal(t, "operator_only", out["code"], name)
	}
	dash := map[string]string{testDashboardHeader: "1"}
	code, out := call(body, dash)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, true, out["dry_run"], "dry_run defaults to true")
	code, _ = call(`{"older_than_seconds": 3600}`, dash)
	assert.Equal(t, http.StatusBadRequest, code, "a prune of anything younger than a day is refused")
	code, _ = call(`{"older_than_seconds": 604800, "kinds": ["everything"]}`, dash)
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = call(`{"shrink": true}`, dash)
	assert.Equal(t, http.StatusOK, code, "shrink needs no age")
}

func TestKeepRouteAndDeleteClearsIt(t *testing.T) {
	rig := newPruneRig(t)
	id := rig.object("keep me", "s1", time.Hour)
	do := func(method, path string, hdr map[string]string) int {
		req, err := http.NewRequest(method, rig.srv.URL+path, nil)
		require.NoError(t, err)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	dash := map[string]string{testDashboardHeader: "1"}
	assert.Equal(t, http.StatusForbidden, do(http.MethodPut, "/api/artifacts/"+id+"/keep", map[string]string{testAgentHeader: "s1"}))
	assert.Equal(t, http.StatusNotFound, do(http.MethodPut, "/api/artifacts/"+strings.Repeat("0", 64)+"/keep", dash))
	require.Equal(t, http.StatusOK, do(http.MethodPut, "/api/artifacts/"+id+"/keep", dash))
	kept, err := rig.h.isKept(context.Background(), id)
	require.NoError(t, err)
	require.True(t, kept)
	require.Equal(t, http.StatusOK, do(http.MethodDelete, "/api/artifacts/"+id, dash))
	kept, err = rig.h.isKept(context.Background(), id)
	require.NoError(t, err)
	assert.False(t, kept, "deleting a kept artifact drops its keep mark")
}

func TestPruneAndShrinkSkipATaskReopenedMidRun(t *testing.T) {
	rig := newPruneRig(t)
	// The .env section is longer than the note that replaces it, so a shrink has something to drop.
	diff := []byte("diff --git a/a b/a\n+x\n" + "diff --git a/.env b/.env\n" + strings.Repeat("+SECRET_LINE=qa-fake-value\n", 10))
	for _, shrink := range []bool{false, true} {
		id := rig.task("completed", 20*24*time.Hour, diff)
		pruneBeforeDiffHook = func(diffCandidate) {
			_, err := rig.boardDB.Exec("UPDATE board_tasks SET status = 'in_progress' WHERE id = ?", id)
			require.NoError(t, err)
		}
		o := pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindDiffs), Shrink: shrink}
		rep := rig.run(o)
		pruneBeforeDiffHook = nil
		assert.Equal(t, 0, rep.Removed, "shrink=%v: a reopened task keeps its diff", shrink)
		assert.Equal(t, 1, rep.Skipped, "shrink=%v", shrink)
		got, err := os.ReadFile(rig.taskDiff(id))
		require.NoError(t, err)
		assert.Equal(t, diff, got, "shrink=%v: untouched", shrink)
	}
}

func TestRetentionIsOffUntilTheOperatorOptsIn(t *testing.T) {
	rig := newPruneRig(t)
	ctx := context.Background()
	unused := rig.object("nothing links this", "s1", 30*24*time.Hour)
	set := func(k, v string) { require.NoError(t, rig.h.ss.SetSetting(ctx, k, v)) }
	tick := func(at time.Time) string { _, state := rig.h.retentionTick(ctx, at); return state }

	assert.Equal(t, "off", tick(rig.now), "default: off")
	set(RetentionDaysSetting, "7")
	set(RetentionKindsSetting, "objects")
	assert.Equal(t, "waiting", tick(rig.now), "turned on: waits for an acknowledged dry run")
	assert.Equal(t, "waiting", tick(rig.now.Add(23*time.Hour)), "still within the 24 hours")
	assert.True(t, rig.exists(unused), "nothing removed while waiting")

	set(RetentionAckAtKey, rig.now.Add(time.Hour).UTC().Format(time.RFC3339)) // acknowledged
	assert.Equal(t, "ran", tick(rig.now.Add(2*time.Hour)))
	assert.False(t, rig.exists(unused), "the run removed the unused object")
	s, _ := rig.h.ss.GetSettings(ctx)
	assert.Contains(t, s[RetentionLastKey], `"removed":1`)

	// A changed value restarts the wait, even after an earlier acknowledgement.
	other := rig.object("another unused one", "s1", 40*24*time.Hour)
	set(RetentionDaysSetting, "14")
	assert.Equal(t, "waiting", tick(rig.now.Add(3*time.Hour)))
	assert.True(t, rig.exists(other))
	assert.Equal(t, "ran", tick(rig.now.Add(28*time.Hour)), "24 hours after the change, it runs without an acknowledgement")
	assert.False(t, rig.exists(other))

	set(RetentionDaysSetting, "0")
	assert.Equal(t, "off", tick(rig.now.Add(30*time.Hour)))
}

func TestPrunePerRunCap(t *testing.T) {
	rig := newPruneRig(t)
	a := rig.object(strings.Repeat("a", 100), "s1", 30*24*time.Hour)
	b := rig.object(strings.Repeat("b", 100), "s1", 30*24*time.Hour)
	rep := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindObjects), MaxBytes: 50})
	assert.True(t, rep.Capped)
	assert.Equal(t, 1, rep.Removed, "it stops once the cap is reached")
	assert.True(t, rig.exists(a) != rig.exists(b), "exactly one of the two is gone")
}

func TestRetentionSettingsAndAck(t *testing.T) {
	for in, ok := range map[any]bool{float64(0): true, float64(30): true, "7": true, float64(3650): true, float64(3651): false, float64(-1): false, 1.5: false, "x": false, true: false} {
		_, got := validRetentionDays(in)
		assert.Equal(t, ok, got, "%v", in)
	}
	for in, ok := range map[string]bool{"diffs": true, "diffs,objects": true, "all": true, "everything": false, "": false} {
		_, got := validRetentionKinds(in)
		assert.Equal(t, ok, got, in)
	}
	rig := newPruneRig(t)
	post := func(hdr map[string]string) int {
		req, _ := http.NewRequest(http.MethodPost, rig.srv.URL+"/api/artifacts/retention/ack", nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusForbidden, post(map[string]string{testAgentHeader: "s1"}))
	assert.Equal(t, http.StatusForbidden, post(map[string]string{tunnel.ViaHeader: "x"}))
	assert.Equal(t, http.StatusBadRequest, post(map[string]string{testDashboardHeader: "1"}), "retention is off: nothing to acknowledge")
	require.NoError(t, rig.h.ss.SetSetting(context.Background(), RetentionDaysSetting, "7"))
	assert.Equal(t, http.StatusOK, post(map[string]string{testDashboardHeader: "1"}))
}

// The reference scan is deny by default (Lead's #3554 review): every text column of every table
// is read, and a table is skipped only when referenceExclusions names it with a reason. A stale
// exclusion (a table that no longer exists) fails here, and links in columns no list ever named
// keep their objects.
func TestPruneScanCensus(t *testing.T) {
	rig := newPruneRig(t)
	ctx := context.Background()
	tables := map[string]bool{}
	for _, db := range []*sqlx.DB{rig.bs.Reader(), rig.h.db.Reader()} {
		var names []string
		require.NoError(t, db.Select(&names, "SELECT name FROM sqlite_master WHERE type = 'table'"))
		for _, n := range names {
			tables[n] = true
		}
	}
	for table, why := range referenceExclusions {
		assert.True(t, tables[table], "excluded table %q doesn't exist: remove it from referenceExclusions", table)
		assert.NotEmpty(t, strings.TrimSpace(why), "excluded table %q needs a reason", table)
	}
	scanned, err := scannedColumns(ctx, rig.h.db.Reader())
	require.NoError(t, err)
	assert.Contains(t, scanned["session_fts"], "body", "chat transcripts are scanned")
	assert.Contains(t, scanned["live_sessions"], "prompt")
	assert.Contains(t, scanned["scheduled_jobs"], "prompt")

	old := 30 * 24 * time.Hour
	inTitle := rig.object("linked from a task title", "s1", old)
	inLaunch := rig.object("linked from a launch prompt", "s1", old)
	inChat := rig.object("linked from a chat transcript", "s1", old)
	inMetrics := rig.object("only fetched, never linked", "s1", old)
	_, err = rig.bs.CreateTask(ctx, "team", "review coral://artifacts/"+inTitle, "", "medium", "Operator")
	require.NoError(t, err)
	ss := store.NewSessionStore(rig.h.db)
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "s-launch", AgentName: "a", AgentType: "claude", WorkingDir: t.TempDir()}))
	_, err = rig.h.db.Exec("UPDATE live_sessions SET prompt = ? WHERE session_id = 's-launch'", "start from coral://artifacts/"+inLaunch)
	require.NoError(t, err)
	_, err = rig.h.db.Exec("INSERT INTO session_fts (session_id, body) VALUES ('s-chat', ?)", "assistant: the report is at coral://artifacts/"+inChat)
	require.NoError(t, err)
	// Declared types don't limit what SQLite stores: a link in an INTEGER column still counts.
	inInteger := rig.object("linked from an INTEGER column", "s1", old)
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "s-int", AgentName: "b", AgentType: "claude", WorkingDir: t.TempDir()}))
	_, err = rig.h.db.Exec("UPDATE live_sessions SET context_window = ? WHERE session_id = 's-int'", "coral://artifacts/"+inInteger)
	require.NoError(t, err)
	var metricCols []string
	require.NoError(t, rig.h.db.Select(&metricCols, "SELECT name FROM pragma_table_info('call_metrics') WHERE type LIKE '%TEXT%'"))
	require.NotEmpty(t, metricCols)
	rep := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindObjects), DryRun: true})
	var listed []string
	for _, it := range rep.Largest {
		listed = append(listed, it.ID)
	}
	assert.NotContains(t, listed, inTitle, "a task title keeps its object")
	assert.NotContains(t, listed, inLaunch, "a launch prompt keeps its object")
	assert.NotContains(t, listed, inChat, "a chat transcript keeps its object")
	assert.NotContains(t, listed, inInteger, "a link in an INTEGER-declared column keeps its object")
	assert.Contains(t, listed, inMetrics, "an artifact nothing links is listed")
}

func TestPruneScansABigKnowledgeFile(t *testing.T) {
	rig := newPruneRig(t)
	atEnd := rig.object("linked at the end of a big file", "s1", 30*24*time.Hour)
	straddling := rig.object("linked across a chunk boundary", "s1", 30*24*time.Hour)
	unused := rig.object("nothing links this", "s1", 30*24*time.Hour)
	dir := filepath.Join(rig.coralDir, "agent_knowledge", "team")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	link := "coral://artifacts/" + straddling
	pad := strings.Repeat("x", (1<<20)-len(link)/2) // the link straddles the first 1 MiB boundary
	body := pad + " " + link + " " + strings.Repeat("filler line\n", 500_000) + "see coral://artifacts/" + atEnd + "\n"
	require.Greater(t, len(body), 5<<20, "bigger than the old 4 MiB cap")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big.md"), []byte(body), 0o600))
	rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindObjects)})
	assert.True(t, rig.exists(atEnd), "a link at the end of a 5+ MiB file protects its object")
	assert.True(t, rig.exists(straddling), "a link split across two read chunks still counts")
	assert.False(t, rig.exists(unused))
}

func TestPruneDiffWithoutACloseTimeStays(t *testing.T) {
	rig := newPruneRig(t)
	id := rig.task("completed", 20*24*time.Hour, []byte("diff --git a/a b/a\n+x\n"))
	_, err := rig.boardDB.Exec("UPDATE board_tasks SET completed_at = NULL WHERE id = ?", id)
	require.NoError(t, err)
	old := rig.now.Add(-30 * 24 * time.Hour) // an old file: falling back to its time would prune it
	require.NoError(t, os.Chtimes(rig.taskDiff(id), old, old))
	dry := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindDiffs), DryRun: true})
	assert.Empty(t, dry.Largest, "no completed_at: too young to judge, so it stays")
}

func TestPruneSkipsATaskClosedAgainMidRun(t *testing.T) {
	rig := newPruneRig(t)
	id := rig.task("completed", 20*24*time.Hour, []byte("diff --git a/a b/a\n+x\n"))
	pruneBeforeDiffHook = func(diffCandidate) {
		// Reopened and completed again just now: its diff is new, not due yet.
		_, err := rig.boardDB.Exec("UPDATE board_tasks SET status = 'completed', completed_at = ? WHERE id = ?", rig.now.UTC().Format(time.RFC3339), id)
		require.NoError(t, err)
	}
	t.Cleanup(func() { pruneBeforeDiffHook = nil })
	rep := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindDiffs)})
	assert.Equal(t, 0, rep.Removed)
	_, err := os.Stat(rig.taskDiff(id))
	assert.NoError(t, err, "the regenerated diff stays")
}

func TestPruneRescansDuringALongRun(t *testing.T) {
	rig := newPruneRig(t)
	var ids []string
	for i := 0; i < 6; i++ {
		ids = append(ids, rig.object(fmt.Sprintf("old unused %d", i), "s1", 30*24*time.Hour))
	}
	old := pruneRescanEvery
	pruneRescanEvery = 2
	t.Cleanup(func() { pruneRescanEvery = old; pruneAfterDeleteHook = nil })
	posted := false
	pruneAfterDeleteHook = func() {
		if posted {
			return
		}
		posted = true
		for _, id := range ids { // link every object still there
			if rig.exists(id) {
				rig.post("now linked: coral://artifacts/" + id)
			}
		}
	}
	rep := rig.run(pruneOptions{OlderThan: 7 * 24 * time.Hour, Kinds: kinds(pruneKindObjects)})
	left := 0
	for _, id := range ids {
		if rig.exists(id) {
			left++
		}
	}
	// The first deletion posts links to the rest; the re-scan after 2 deletions sees them, so at most
	// one more (the one already under the lock's re-check before the re-scan) can go.
	assert.LessOrEqual(t, rep.Removed, 2, "the re-scan stopped the run deleting newly linked objects")
	assert.GreaterOrEqual(t, left, 4)
}
