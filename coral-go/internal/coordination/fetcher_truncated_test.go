package coordination

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeCoral(t *testing.T, tasksBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/tasks"):
			_, _ = w.Write([]byte(tasksBody))
		case strings.HasSuffix(r.URL.Path, "/messages/all"):
			_, _ = w.Write([]byte(`[]`))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// #4622: the server cuts the full task list at 500 tasks and says truncated; the report says so too
// instead of presenting a cut list as the whole board. The tasks it did get are all used.
func TestFetchFromAPISaysWhenTheServerCutTheTaskList(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := fakeCoral(t, `{"tasks":[{"id":7,"board_id":"b","title":"open one","status":"pending","priority":"low","created_by":"x","created_at":"2026-10-18T00:00:00Z"}],"truncated":true}`)
	tasks, _, _, err := FetchFromAPI(srv.URL, "b")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.EqualValues(t, 7, tasks[0].ID)
	assert.Contains(t, logs.String(), "only the open tasks and the newest done ones")

	// A whole list says nothing; so does an older server's bare array.
	logs.Reset()
	srv = fakeCoral(t, `{"tasks":[{"id":1,"board_id":"b","title":"t","status":"pending","priority":"low","created_by":"x","created_at":"2026-10-18T00:00:00Z"}]}`)
	tasks, _, _, err = FetchFromAPI(srv.URL, "b")
	require.NoError(t, err)
	assert.Len(t, tasks, 1)
	srv = fakeCoral(t, `[{"id":2,"board_id":"b","title":"t","status":"pending","priority":"low","created_by":"x","created_at":"2026-10-18T00:00:00Z"}]`)
	tasks, _, _, err = FetchFromAPI(srv.URL, "b")
	require.NoError(t, err)
	assert.Len(t, tasks, 1)
	assert.Empty(t, logs.String())
}
