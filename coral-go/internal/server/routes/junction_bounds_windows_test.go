//go:build windows

package routes

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/safefs"
)

// #3633: on Windows filepath.EvalSymlinks doesn't follow junctions, so every containment check
// in routes resolves through safefs.Resolve. A junction an agent makes inside the repository,
// leading to a folder outside it, never lets the file viewer, resolve-path, the diff of an
// untracked file or the explorer read there, through the hub or on the local dashboard.
func TestJunctionOutOfTheRepositoryIsRefusedEverywhere(t *testing.T) {
	e := newBoundsEnvLinks(t, false) // junctions only: no symlink privilege needed
	outdir := filepath.Join(e.base, "outdir")
	require.NoError(t, os.WriteFile(filepath.Join(outdir, "j.txt"), []byte("junction-secret"), 0o644))
	junc := filepath.Join(e.repo, "app", "juncdir")
	out, err := exec.Command("cmd", "/c", "mklink", "/J", junc, outdir).CombinedOutput()
	require.NoError(t, err, "mklink /J: %s", out)

	// The helpers every route uses.
	if _, err := safefs.Resolve(filepath.Join(junc, "j.txt")); err != nil {
		t.Errorf("safefs.Resolve of a file under the junction: %v", err)
	}
	realOut, err := safefs.Resolve(outdir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(realOut, "j.txt"), realPath(filepath.Join(junc, "j.txt")), "realPath follows the junction")
	assert.False(t, isPathWithinDir(e.repo, filepath.Join(junc, "j.txt")))
	assert.False(t, within(realPath(e.repo), realPath(filepath.Join(junc, "j.txt"))))

	for _, fp := range []string{`app\juncdir\j.txt`, "app/juncdir/j.txt", filepath.Join(junc, "j.txt")} {
		for _, via := range []bool{tunneled, dashboard} {
			code, body := e.get(fc, via, "filepath", fp)
			if via == dashboard && filepath.IsAbs(fp) {
				// By design the local dashboard opens any ABSOLUTE path read-only, wherever it
				// is (TestFileContentTunnelBounds reads an outside file the same way); only the
				// relative paths and the hub are bounded to the session root.
				assert.Equal(t, 200, code, "file-content %s on the local dashboard", fp)
				continue
			}
			assert.Contains(t, []int{403, 404}, code, "file-content %s (tunneled %v)", fp, via)
			assert.NotContains(t, body, "junction-secret", "file-content %s", fp)
		}
		code, body := e.get("/api/sessions/live/claude-fb/resolve-path", tunneled, "filepath", fp)
		assert.Equal(t, 404, code, "resolve-path %s", fp)
		assert.NotContains(t, body, realOut)
		for _, via := range []bool{tunneled, dashboard} {
			_, body = e.get("/api/sessions/live/claude-fb/diff", via, "filepath", fp)
			assert.NotContains(t, body, "junction-secret", "diff of an untracked file %s (tunneled %v)", fp, via)
		}
	}

	w := httptest.NewRecorder()
	serveExplorerDirectory(w, httptest.NewRequest("GET", "/", nil), e.repo, `app\juncdir`, false)
	assert.Equal(t, 400, w.Code, "the explorer refuses the junction")
	assert.NotContains(t, w.Body.String(), "j.txt")

	// A plain file inside still reads.
	code, body := e.get(fc, tunneled, "filepath", "app/main.go")
	assert.Equal(t, 200, code)
	assert.Contains(t, body, "package main")
}
