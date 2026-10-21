package routes

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/tunnel"
)

// Browse through Coral Hub (#4527): the home folder is shown as "~", and a click on a folder asks for "~/folder", which the
// connector's classifier now accepts on the two browse routes (pkg/tunnel homeRef) and the machine expands. Walked as the page
// walks it: from the empty path down several levels and back up, every request checked by the classifier first and answered by the
// handler with the Hub's bounds, every answer's path in the "~" form and none naming the home directory.
func TestBrowseThroughTheHubWalksDownAndUp(t *testing.T) {
	e := newBoundsEnv(t)
	// The server's own chain for a tunneled request: HubPaths shows the home as "~" in the answers and expands it in the request.
	r := chi.NewRouter()
	r.Use(HubPaths)
	sys := &SystemHandler{}
	r.Get("/api/filesystem/list", sys.ListFilesystem)
	r.Get("/api/filesystem/is-git", sys.IsGitDir)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	do := func(path string, q url.Values) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+path+"?"+q.Encode(), nil)
		req.Header.Set(tunnel.ViaHeader, "1")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	list := "/api/filesystem/list"
	require.NoError(t, os.MkdirAll(filepath.Join(e.home, "projects", "team", "app", "src"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(e.home, "projects", "team", "app", ".git"), 0o755))
	type listing struct {
		Path    string   `json:"path"`
		Entries []string `json:"entries"`
		Error   string   `json:"error"`
	}
	step := func(path string) (int, listing, string) {
		q := url.Values{"path": {path}}
		require.Equal(t, tunnel.TierLaunch, tunnel.Classify("GET", list, q.Encode()), "the connector lets %q through", path)
		code, body := do(list, q)
		var l listing
		_ = json.Unmarshal([]byte(body), &l)
		return code, l, body
	}

	code, home, body := step("") // Browse's first request on a remote: the empty path
	require.Equal(t, 200, code, body)
	assert.Equal(t, "~", home.Path, "the home is shown as ~, never as an absolute path")
	assert.Contains(t, home.Entries, "projects")
	assert.NotContains(t, body, e.home, "the home directory's path does not go through the Hub")

	path := home.Path
	for _, name := range []string{"projects", "team", "app", "src"} { // click a folder: ask for ~/<folder> and so on down
		next := path + "/" + name
		code, l, body := step(next)
		require.Equal(t, 200, code, next+": "+body)
		assert.Equal(t, next, l.Path, "the listing names its folder in the ~ form")
		assert.NotContains(t, body, e.home)
		path = l.Path
	}
	// ...and back up, as browserNavigateUp does: drop the last segment.
	for path != "~" {
		path = path[:strings.LastIndex(path, "/")]
		code, l, body := step(path)
		require.Equal(t, 200, code, path+": "+body)
		assert.Equal(t, path, l.Path)
	}

	// Nothing outside the allowed roots is reachable by this form: refused by the classifier before the machine sees it, or, where the
	// form is plain, by the machine's bounds (credential folders and links out of the home look like missing folders).
	for _, p := range []string{"~/../x", "~/projects/../../x", "~//etc", "~user", "~/%2e%2e/x"} {
		assert.Equal(t, tunnel.TierNever, tunnel.Classify("GET", list, url.Values{"path": {p}}.Encode()), p)
	}
	for _, p := range []string{"~/.ssh", "~/projects/escape", "~/projects/escape/outdir"} {
		require.Equal(t, tunnel.TierLaunch, tunnel.Classify("GET", list, url.Values{"path": {p}}.Encode()), p)
		code, body := do(list, url.Values{"path": {p}})
		assert.Equal(t, 403, code, p)
		assert.NotContains(t, body, "inner.txt", p)
	}
	// is-git takes the same form, and does not confirm a repository outside the bounds.
	code, body = do("/api/filesystem/is-git", url.Values{"path": {"~/projects/team/app"}})
	assert.Equal(t, 200, code)
	assert.Contains(t, body, `"is_git":true`)
	code, body = do("/api/filesystem/is-git", url.Values{"path": {"~/projects/escape"}})
	assert.Equal(t, 200, code)
	assert.Contains(t, body, `"is_git":false`)
}
