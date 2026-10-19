package routes

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #5021: Diff and GetFileOriginal refuse a filepath that leaves the working directory (the isPathWithinDir branches no test reached).
// A file one level up exists and holds a marker: it must never show. Mutation: drop either isPathWithinDir check and the
// file above the repository is served.
func TestDiffAndFileOriginalRefuseAPathOutsideTheWorkingDirectory(t *testing.T) {
	e := newBoundsEnv(t)
	q := func(fp string) url.Values { return url.Values{"filepath": {fp}, "session_id": {"fb-1"}} }
	// the git root is <repo>: ../outside.txt is the file beside the repository. (A path outside that does not exist is not refused: the
	// check lets a missing file through for its handler, which answers empty, so only paths to a file that is there are asserted.)
	for _, fp := range []string{"../outside.txt", "app/../../outside.txt", "./../outside.txt", "app/../../outdir/inner.txt"} {
		for _, k := range []kind{kindDashboard, kindTunnel, kindCurl, kindBearer} {
			code, body := e.doAs("GET", "/api/sessions/live/claude-fb/file-original", q(fp), "", k)
			assert.Equal(t, 403, code, "file-original, kind %d, %q: %s", k, fp, body)
			assert.NotContains(t, body, "outside-secret", "file-original, kind %d, %q", k, fp)
			assert.NotContains(t, body, "inner", "file-original, kind %d, %q", k, fp)

			code, body = e.doAs("GET", "/api/sessions/live/claude-fb/diff", q(fp), "", k)
			want := 404 // anything but the local dashboard is told the file is not there
			if k == kindDashboard {
				want = 403
			}
			assert.Equal(t, want, code, "diff, kind %d, %q: %s", k, fp, body)
			assert.NotContains(t, body, "outside-secret", "diff, kind %d, %q", k, fp)
		}
	}
}
