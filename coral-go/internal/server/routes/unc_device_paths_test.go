package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3671 (#3669 finding 1): a network path from a request is refused before anything stats it
// (on Windows the stat alone connects to the host as the user), at each site that stat'ed one:
// a launch's working folder, a team import, the folder browser and is-git. Each answer is the
// network refusal, not "not found", so the check ran first.
var uncForms = []string{`\\host\share`, `//host/share`, `\/host\share`, `\\?\UNC\host\share`, `\\.\pipe\x`, `\??\UNC\host\share`}

func TestUNCRefusedBeforeAnyStat(t *testing.T) {
	h := newSystemHandlerForTest(t)
	for _, p := range uncForms {
		assert.Contains(t, launchDirError(p), "a network path can't be a working folder", p)

		_, err := importTeamRoot(p)
		assert.ErrorContains(t, err, "a network path can't be imported from", p)
		code, out := importTeam(t, h, p, asOperator)
		assert.Equal(t, http.StatusBadRequest, code, p)
		assert.Contains(t, out["error"], "network path", p)

		// The local dashboard (relaxed bounds: it stat'ed the path unchecked before), and an agent.
		for _, as := range []func(*http.Request) *http.Request{dashboardRequest, asAgent} {
			_, ok := browsable(as(httptest.NewRequest(http.MethodGet, "/api/filesystem/list", nil)), p)
			assert.False(t, ok, p)
			rec := httptest.NewRecorder()
			h.IsGitDir(rec, as(httptest.NewRequest(http.MethodGet, "/api/filesystem/is-git?path="+p, nil)))
			var out map[string]bool
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			assert.False(t, out["is_git"], p)
		}
	}
	// A local folder still works.
	dir := t.TempDir()
	assert.Empty(t, launchDirError(dir))
	_, ok := browsable(dashboardRequest(httptest.NewRequest(http.MethodGet, "/api/filesystem/list", nil)), dir)
	assert.True(t, ok)
}

// #3671 (#3669 finding 4): a theme or a workflow can't be named after a Windows device ("nul.json"
// is the NUL device there), or end in a dot or a space.
func TestDeviceNamesRefusedForThemesAndWorkflows(t *testing.T) {
	cfg := &config.Config{}
	cfg.UseCoralDir(t.TempDir())
	th := NewThemesHandler(cfg)
	for _, n := range []string{"CON", "nul", "Com1", "LPT9", "aux "} {
		assert.Empty(t, th.safePath(n), n)
	}
	assert.NotEmpty(t, th.safePath("console"))
	assert.NotEmpty(t, th.safePath("my theme"))

	db, err := storetest.Open(t, t.TempDir()+"/wf.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	wf := NewWorkflowHandler(db, &config.Config{})
	create := func(name string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		wf.CreateWorkflow(rec, httptest.NewRequest(http.MethodPost, "/api/workflows", strings.NewReader(`{"name":"`+name+`","steps":[{"name":"one","type":"shell","command":"true"}]}`)))
		return rec
	}
	for _, n := range []string{"CON", "nul", "com1", "LPT3"} {
		rec := create(n)
		assert.Equal(t, http.StatusBadRequest, rec.Code, n)
		assert.Contains(t, rec.Body.String(), "invalid characters", n)
	}
	assert.Less(t, create("console").Code, 300, "a name that only starts like a device is fine")
}

// A local drive in the extended-length form is named for what it is, not called a network path (#278 row 14); a real device or UNC form still is one.
func TestExtendedDrivePathIsNotCalledANetworkPath(t *testing.T) {
	ext := string([]byte{92, 92, 63, 92}) + `D:\project\x`
	msg := launchDirError(ext)
	assert.Contains(t, msg, "extended-length path")
	assert.NotContains(t, msg, "network")
	assert.Contains(t, msg, `such as D:\project\x`)
	for _, p := range uncForms {
		assert.Contains(t, launchDirError(p), "a network path can't be a working folder", p)
	}
}
