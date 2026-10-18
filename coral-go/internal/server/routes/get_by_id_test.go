package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4627: GetView, GetWorkflow and GetWorkflowRun share one get-by-id step. Each keeps its own
// messages: 400 for a name that is not a number, 404 for a row that is not there, the row as it is.
func TestGetByIDRoutesKeepTheirAnswers(t *testing.T) {
	db, err := storetest.Open(t, t.TempDir()+"/getbyid.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	views, workflows := NewViewsHandler(db), NewWorkflowHandler(db, &config.Config{})
	viewID, err := store.NewViewStore(db).CreateView(ctx, &store.CustomView{Name: "Board", Prompt: "p", HTML: "<p>x</p>", Scope: "global"})
	require.NoError(t, err)
	wf, err := workflows.ws.CreateWorkflow(ctx, &store.Workflow{Name: "wf", StepsJSON: `[{"name":"one","type":"shell","command":"true"}]`})
	require.NoError(t, err)
	run, err := workflows.ws.CreateWorkflowRun(ctx, wf.ID, "manual", nil)
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Get("/api/views/{id}", views.GetView)
	r.Get("/api/workflows/{workflowID}", workflows.GetWorkflow)
	r.Get("/api/workflows/runs/{runID}", workflows.GetWorkflowRun)
	get := func(path string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
		return rec.Code, out
	}

	for _, tc := range []struct{ base, id, bad, missing string }{
		{"/api/views/", strconv.FormatInt(viewID, 10), "invalid id", "view not found"},
		{"/api/workflows/", strconv.FormatInt(wf.ID, 10), "invalid workflow ID", "workflow not found"},
		{"/api/workflows/runs/", strconv.FormatInt(run.ID, 10), "invalid run ID", "run not found"},
	} {
		code, body := get(tc.base + tc.id)
		assert.Equal(t, http.StatusOK, code, tc.base)
		assert.EqualValues(t, mustInt(t, tc.id), body["id"], tc.base)
		for _, notNumber := range []string{"abc", "1.5", "99999999999999999999"} {
			code, body = get(tc.base + notNumber)
			assert.Equal(t, http.StatusBadRequest, code, tc.base+notNumber)
			assert.Equal(t, tc.bad, body["error"], tc.base+notNumber)
		}
		code, body = get(tc.base + "424242")
		assert.Equal(t, http.StatusNotFound, code, tc.base)
		assert.Equal(t, tc.missing, body["error"], tc.base)
	}
	code, body := get("/api/views/" + strconv.FormatInt(viewID, 10))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Board", body["name"])
	assert.Equal(t, "<p>x</p>", body["html"])
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	require.NoError(t, err)
	return n
}

// A store failure is a 500 with the error, not a 404.
func TestGetByIDStoreFailureIs500(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/x/{id}", func(w http.ResponseWriter, r *http.Request) {
		getByID(w, r, "id", "bad", "missing", func(context.Context, int64) (*struct{ N int }, error) { return nil, errors.New("disk on fire") })
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x/1", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "disk on fire")
}
