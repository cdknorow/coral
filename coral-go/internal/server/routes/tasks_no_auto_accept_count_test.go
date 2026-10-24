package routes

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store/storetest"
)

// Auto-accept is the agent's CLI permission flag and has no count (#6917): a caller who still sends max_auto_accepts is told so with 400, as webhook_url is, instead of getting unbounded auto-approval and no error (#6937,
// QA's #6924 LOW and Security). The key is checked, not its value, so 0 is refused too; a body without it is not.
func TestTasksRunRefusesMaxAutoAccepts(t *testing.T) {
	db, err := storetest.Open(t, t.TempDir()+"/tasks.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	tasks := NewTasksHandler(db, &config.Config{})
	r := chi.NewRouter()
	r.Post("/api/tasks/run", tasks.SubmitTask)
	for _, v := range []any{3, 0, nil, "3"} {
		w := send(t, r, "POST", "/api/tasks/run", map[string]any{"prompt": "p", "repo_path": t.TempDir(), "auto_accept": true, "max_auto_accepts": v})
		assert.Equal(t, http.StatusBadRequest, w.Code, "%v", v)
		assert.Contains(t, w.Body.String(), errAutoAcceptHasNoCount, "%v", v)
	}
	w := send(t, r, "POST", "/api/tasks/run", map[string]any{"prompt": "p", "repo_path": t.TempDir(), "auto_accept": true})
	assert.NotContains(t, w.Body.String(), errAutoAcceptHasNoCount, "a body without the field is not refused for it")
}
