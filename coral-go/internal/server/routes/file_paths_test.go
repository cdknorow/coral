package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #59 (QA #28 C3): the path a file: URL names, on Windows and elsewhere. The percent-decoding
// happens before (resolveFileRef), and the #L line hash after.
func TestFileURLPath(t *testing.T) {
	for _, tc := range []struct {
		in      string
		windows bool
		want    string
	}{
		{"file:///C:/x/y.go", true, "C:/x/y.go"},
		{"file:///c:/x/y.go#L12-L30", true, "c:/x/y.go#L12-L30"},
		{"file://localhost/C:/x/y.go", true, "C:/x/y.go"},
		{"FILE://LOCALHOST/C:/My Dir/x.go", true, "C:/My Dir/x.go"},
		{`file://C:\x\y.go`, true, `C:\x\y.go`}, // a drive right after file:// stays one
		{"file://C:/x/y.go", true, "C:/x/y.go"},
		{"file://server/share/x.go", true, `\\server\share\x.go`}, // UNC: the containment checks decide
		{"file:///x/y.go", true, "/x/y.go"},
		{"C:/x/y.go", true, "C:/x/y.go"},
		{"/C:/x/y.go", true, "/C:/x/y.go"}, // not a file: URL: as it is
		// Elsewhere: as before.
		{"file:///home/me/y.go", false, "/home/me/y.go"},
		{"file://localhost/home/me/y.go#L3", false, "/home/me/y.go#L3"},
		{"file:///C:/x/y.go", false, "/C:/x/y.go"},
		{"file://host/p", false, "host/p"},
		{"app/main.go", false, "app/main.go"},
	} {
		assert.Equal(t, tc.want, fileURLPath(tc.in, tc.windows), "%q windows=%v", tc.in, tc.windows)
	}
}

// #59 (QA #28 C4): a change in a subfolder clears its repository's file list, and the
// repository's clears its subfolders'; on Windows whatever the slashes and case.
func TestFolderKeyMatchesSubfolders(t *testing.T) {
	inside := func(sub, repo string, windows bool) bool {
		s, r := folderKey(sub, windows), folderKey(repo, windows)
		return len(s) >= len(r) && s[:len(r)] == r
	}
	assert.True(t, inside(`C:\Repo\pkg3`, `C:/repo`, true))
	assert.True(t, inside(`C:\repo\pkg3\`, `C:\repo`, true))
	assert.True(t, inside(`c:/repo/pkg3`, `C:\REPO`, true))
	assert.False(t, inside(`C:\repo2\pkg`, `C:\repo`, true), "a sibling with the same prefix")
	assert.True(t, inside("/home/me/repo/pkg3", "/home/me/repo/", false))
	assert.False(t, inside("/home/me/repo2", "/home/me/repo", false))
	assert.False(t, inside("/home/me/Repo/pkg", "/home/me/repo", false), "case counts elsewhere")
}
