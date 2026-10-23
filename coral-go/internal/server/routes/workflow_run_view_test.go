package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #6530 (#6515 finding 3): the runs list carries each run's duration and, for a failed run, the failing step's name and number (from 1) and a one-line reason.

type runViewRig struct {
	t   *testing.T
	h   *WorkflowHandler
	wf  *store.Workflow
	r   chi.Router
	ctx context.Context
}

func newRunViewRig(t *testing.T) *runViewRig {
	cfg := config.Load(t.TempDir())
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewWorkflowHandler(db, cfg)
	ctx := context.Background()
	wf, err := h.ws.CreateWorkflow(ctx, &store.Workflow{Name: "nightly", StepsJSON: `[]`, DefaultAgentJSON: "{}", BaseBranch: "main", MaxDurationS: 60, Enabled: 1})
	require.NoError(t, err)
	r := chi.NewRouter()
	r.Get("/api/workflows/{workflowID}/runs", h.ListWorkflowRuns)
	r.Get("/api/workflows/runs/recent", h.ListRecentRuns)
	r.Get("/api/workflows/runs/{runID}", h.GetWorkflowRun)
	return &runViewRig{t: t, h: h, wf: wf, r: r, ctx: ctx}
}

// run stores a run with these step results, times and final status.
func (rig *runViewRig) run(status string, steps []map[string]any, started, finished string, errMsg *string) int64 {
	rig.t.Helper()
	run, err := rig.h.ws.CreateWorkflowRun(rig.ctx, rig.wf.ID, "api", nil)
	require.NoError(rig.t, err)
	b, _ := json.Marshal(steps)
	fields := map[string]any{"status": status, "step_results": string(b)}
	if started != "" {
		fields["started_at"] = started
	}
	if finished != "" {
		fields["finished_at"] = finished
	}
	if errMsg != nil {
		fields["error_msg"] = *errMsg
	}
	require.NoError(rig.t, rig.h.ws.UpdateWorkflowRun(rig.ctx, run.ID, fields))
	return run.ID
}

func (rig *runViewRig) get(path string) map[string]any {
	rig.t.Helper()
	rec := httptest.NewRecorder()
	rig.r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(rig.t, http.StatusOK, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(rig.t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func (rig *runViewRig) listed(id int64) map[string]any {
	rig.t.Helper()
	out := rig.get("/api/workflows/" + strconv.FormatInt(rig.wf.ID, 10) + "/runs")
	for _, x := range out["runs"].([]any) {
		if m := x.(map[string]any); m["id"] == float64(id) {
			return m
		}
	}
	rig.t.Fatal("the run is not in the list")
	return nil
}

func TestAFailedRunInTheListNamesItsStepFromOneAndWhy(t *testing.T) {
	rig := newRunViewRig(t)
	steps := []map[string]any{
		{"index": 0, "name": "checkout", "type": "shell", "status": "completed"},
		{"index": 1, "name": "build", "type": "shell", "status": "completed", "output_tail": "ok"},
		{"index": 2, "name": "test", "type": "shell", "status": "failed", "exit_code": 1, "error_tail": "building...\nFAIL TestSomething (0.01s)\n\nexit status 1\n\n"},
		{"index": 3, "name": "deploy", "type": "shell", "status": "skipped"},
	}
	id := rig.run("failed", steps, "2026-10-23T10:00:00.000000+00:00", "2026-10-23T10:02:30.500000+00:00", nil)
	run := rig.listed(id)
	assert.Equal(t, 150.5, run["duration_seconds"])
	assert.Equal(t, map[string]any{"number": float64(3), "name": "test"}, run["failed_step"], "step 3 of 4 (its index is 2)")
	assert.Equal(t, "exit status 1", run["failure_reason"], "the last line with anything on it")
	// the stored fields are as they were: the step's index is still counted from 0, current_step too
	detail := rig.get("/api/workflows/runs/" + strconv.FormatInt(id, 10))
	got := detail["steps"].([]any)
	require.Len(t, got, 4)
	for i, s := range got {
		m := s.(map[string]any)
		assert.Equal(t, float64(i), m["index"], "index stays 0-based: the step-file route takes it")
		assert.Equal(t, float64(i+1), m["number"], "number counts from 1")
	}
	// the recent list (all workflows) carries them too
	recent := rig.get("/api/workflows/runs/recent")["runs"].([]any)
	require.NotEmpty(t, recent)
	assert.Equal(t, "test", recent[0].(map[string]any)["failed_step"].(map[string]any)["name"])
}

func TestTheReasonFallsBackToTheOutputThenTheNoteThenTheRunsError(t *testing.T) {
	rig := newRunViewRig(t)
	failed := func(extra map[string]any) []map[string]any {
		s := map[string]any{"index": 0, "name": "step", "type": "shell", "status": "failed"}
		for k, v := range extra {
			s[k] = v
		}
		return []map[string]any{s}
	}
	runErr := "step 1 timed out after 60s\nmore"
	for name, tc := range map[string]struct {
		steps []map[string]any
		err   *string
		want  string
	}{
		"stderr first":     {failed(map[string]any{"error_tail": "boom", "output_tail": "out"}), nil, "boom"},
		"then the output":  {failed(map[string]any{"output_tail": "line one\nthe last output line\n"}), nil, "the last output line"},
		"then the note":    {failed(map[string]any{"note": "output_artifact refused"}), nil, "output_artifact refused"},
		"then run error":   {failed(nil), &runErr, "step 1 timed out after 60s more"},
		"no step failed":   {[]map[string]any{{"index": 0, "name": "a", "status": "completed"}}, &runErr, "step 1 timed out after 60s more"},
		"nothing at all":   {failed(nil), nil, ""},
		"control and ansi": {failed(map[string]any{"error_tail": "\x1b[31mred\x1b[0m\ttext\x07"}), nil, "red text"},
	} {
		id := rig.run("failed", tc.steps, "2026-10-23T10:00:00+00:00", "2026-10-23T10:00:01+00:00", tc.err)
		run := rig.listed(id)
		reason, _ := run["failure_reason"].(string)
		assert.Equal(t, tc.want, reason, name)
	}
	// a run that failed with no step marked failed has a reason and no failed_step
	id := rig.run("failed", nil, "", "", &runErr)
	run := rig.listed(id)
	assert.Nil(t, run["failed_step"])
	assert.Equal(t, "step 1 timed out after 60s more", run["failure_reason"])
}

func TestTheReasonIsOneShortRedactedLine(t *testing.T) {
	rig := newRunViewRig(t)
	secret := "ghp_" + strings.Repeat("a1B2c3D4e5", 4)
	id := rig.run("failed", []map[string]any{{"index": 0, "name": "push", "status": "failed", "error_tail": "remote: token " + secret + " rejected " + strings.Repeat("x", 500)}}, "", "", nil)
	run := rig.listed(id)
	reason := run["failure_reason"].(string)
	assert.NotContains(t, reason, secret, "a secret a command printed is not shown")
	assert.LessOrEqual(t, len([]rune(reason)), 201, "one short line")
	assert.NotContains(t, reason, "\n")
}

func TestOnlyAFailedRunHasAFailureAndOnlyAFinishedRunADuration(t *testing.T) {
	rig := newRunViewRig(t)
	steps := []map[string]any{{"index": 0, "name": "a", "status": "failed", "error_tail": "x"}}
	for _, status := range []string{"completed", "running", "killed", "pending"} {
		id := rig.run(status, steps, "2026-10-23T10:00:00+00:00", "", nil)
		run := rig.listed(id)
		assert.Nil(t, run["failed_step"], status)
		_, hasReason := run["failure_reason"]
		assert.False(t, hasReason, status)
		_, hasDuration := run["duration_seconds"]
		assert.False(t, hasDuration, "%s: not finished (no finished_at)", status)
	}
	done := rig.run("completed", nil, "2026-10-23T10:00:00+00:00", "2026-10-23T10:00:07+00:00", nil)
	assert.Equal(t, 7.0, rig.listed(done)["duration_seconds"])
	// times that do not make a duration give none, never a negative or a crash
	for _, tc := range [][2]string{{"2026-10-23T10:00:10+00:00", "2026-10-23T10:00:00+00:00"}, {"junk", "2026-10-23T10:00:00+00:00"}} {
		id := rig.run("completed", nil, tc[0], tc[1], nil)
		_, has := rig.listed(id)["duration_seconds"]
		assert.False(t, has, tc[0])
	}
	// the run's own stored fields are unchanged in the answer
	r := rig.listed(done)
	for _, k := range []string{"id", "workflow_id", "status", "trigger_type", "current_step", "started_at", "finished_at", "created_at"} {
		assert.Contains(t, r, k)
	}
}

// Security's #6530 finding: the reason is redacted as a whole before the last line is taken and before it is cut. A token that straddles the 200-character cut, and a private key whose lines
// would each look harmless alone, must not leave any of themselves in the reason.
func TestTheReasonIsRedactedBeforeItIsSplitOrCut(t *testing.T) {
	rig := newRunViewRig(t)
	token := "ghp_" + strings.Repeat("a1B2c3D4e5", 4)
	show := func(tail string) string {
		id := rig.run("failed", []map[string]any{{"index": 0, "name": "step", "status": "failed", "error_tail": tail}}, "", "", nil)
		reason, _ := rig.listed(id)["failure_reason"].(string)
		return reason
	}
	// the token starts a few characters before the cut: cut first, its first characters would be shown
	for _, pad := range []int{180, 190, 195, 198, 199, 200} {
		reason := show("error: " + strings.Repeat("x", pad) + " " + token + " rejected")
		assert.NotContains(t, reason, token[:8], "pad %d: no start of the token", pad)
		assert.NotContains(t, reason, "a1B2c3D4", "pad %d", pad)
	}
	// a key: its body lines are not shown alone, whichever of them is the last line the tail ends on
	key := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAqz0Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZg\nQWxwaGFCZXRhR2FtbWFEZWx0YUVwc2lsb25aZXRhRXRhVGhldGE0NTY3ODk\n-----END RSA PRIVATE KEY-----"
	for _, tail := range []string{"fatal: bad key\n" + key, key, "fatal: bad key\n" + key + "\nexit status 1"} {
		reason := show(tail)
		assert.NotContains(t, reason, "MIIEowIBAAKCAQEA", tail)
		assert.NotContains(t, reason, "QWxwaGFCZXRh", tail)
		assert.NotContains(t, reason, "Zm9vYmFy", tail)
	}
	// the same for the run's error message and an output tail
	id := rig.run("failed", nil, "", "", ptrString("went wrong: "+strings.Repeat("y", 195)+" "+token))
	assert.NotContains(t, rig.listed(id)["failure_reason"], token[:8])
	// an ordinary line is still the reason
	assert.Equal(t, "exit status 1", show("building\nexit status 1\n"))
}

func ptrString(s string) *string { return &s }

// The text a reason is made from is bounded: a huge tail still gives its last line, and a huge sentence its start, without redacting all of it.
func TestAHugeTailStillGivesItsLastLineFromABoundedInput(t *testing.T) {
	rig := newRunViewRig(t)
	huge := strings.Repeat("noise line that goes on and on\n", 100000) + "the last line\n" // about 3 MB
	id := rig.run("failed", []map[string]any{{"index": 0, "name": "s", "status": "failed", "error_tail": huge}}, "", "", nil)
	var most int
	reasonInputSeen = func(n int) { most = max(most, n) }
	t.Cleanup(func() { reasonInputSeen = nil })
	assert.Equal(t, "the last line", rig.listed(id)["failure_reason"])
	assert.LessOrEqual(t, most, maxReasonInput, "no more than the bound is ever sent to the redactor, however much the step wrote")
	assert.Positive(t, most)
	assert.LessOrEqual(t, len(boundedText(huge, true)), maxReasonInput)
	assert.LessOrEqual(t, len(boundedText(huge, false)), maxReasonInput)
	assert.True(t, strings.HasSuffix(boundedText(huge, true), "the last line\n"), "a tail keeps its end")
	assert.True(t, strings.HasPrefix(boundedText(huge, false), "noise line"), "a sentence keeps its start")
	multi := strings.Repeat("é日本", 6000) // multi-byte characters: never cut inside one
	assert.True(t, utf8.ValidString(boundedText(multi, true)) && utf8.ValidString(boundedText(multi, false)))
}
