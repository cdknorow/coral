package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// A network path as a task's repo_path is refused before anything stats it (on Windows the stat alone connects to the host as the user), and
// through Coral Hub the refusal and the not-found answer never echo the path (#126 F1).
func TestTasksRunRefusesANetworkRepoPath(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "t.db"))
	require.NoError(t, err)
	defer db.Close()
	h := NewTasksHandler(db, &config.Config{})
	run := func(hub bool, repo string) (int, string) {
		r := httptest.NewRequest(http.MethodPost, "/api/tasks/run", strings.NewReader(`{"prompt":"p","repo_path":`+jsonString(repo)+`}`))
		if hub {
			r.Header.Set(tunnel.ViaHeader, "1")
		}
		rec := httptest.NewRecorder()
		h.SubmitTask(rec, r)
		return rec.Code, rec.Body.String()
	}
	for _, repo := range []string{`\\127.0.0.1\x\y`, `//127.0.0.1/x/y`, `\\?\UNC\127.0.0.1\x\y`, `\\.\pipe\x`} {
		for _, hub := range []bool{false, true} {
			code, body := run(hub, repo)
			assert.Equal(t, http.StatusBadRequest, code, "%q hub=%v: %s", repo, hub, body)
			assert.Contains(t, body, "network path", "%q hub=%v", repo, hub)
			assert.NotContains(t, body, "127.0.0.1", "%q hub=%v: the path is not echoed", repo, hub)
		}
	}

	missing := filepath.Join(t.TempDir(), "no-such-folder")
	code, body := run(false, missing)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "no-such-folder", "a local caller is told which folder")
	code, body = run(true, missing)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.NotContains(t, body, "no-such-folder", "through the Hub the caller already has the path")
}

// The home directory's 8.3 form (C:\Users\JOHNSM~1, as os.TempDir() often spells it when the user name is over 8 characters) shows as "~"
// through Coral Hub like the long form (#126 F5).
func TestHubDisplaysTheShortFormOfTheHome(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("8.3 names are a Windows thing")
	}
	home := filepath.Join(t.TempDir(), "a-user-name-longer-than-eight")
	require.NoError(t, os.MkdirAll(home, 0o755))
	short := shortPathOf(home)
	if short == "" || strings.EqualFold(short, home) {
		t.Skip("this volume makes no 8.3 names")
	}
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	assert.Equal(t, `~\proj`, hubDisplayPath(home+`\proj`), "the long form")
	assert.Equal(t, `~\proj`, hubDisplayPath(short+`\proj`), "the short form: %s", short)
	assert.Equal(t, `failed in ~\Temp\x`, hubDisplayText(`failed in `+short+`\Temp\x`))
}
