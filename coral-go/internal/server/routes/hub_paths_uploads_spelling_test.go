package routes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #5400 on Windows, through the same test seams as the other hub path tests: the dashboard types the path as the OS spells it, with backslashes and
// in the case the OS gave, so the uploads folder must be found in "C:\Users\alice\.coral\uploads\x.png", in "C:/Users/alice/...", in a mixed
// spelling and in another letter case, and shown as "~/.coral/uploads/x.png". On Linux the match is exact and a backslash is just a character.
func TestAPastedImagesPathIsFoundWhateverWaysItIsSpelledOnWindows(t *testing.T) {
	fold, slashes, old := hubFold, hubSlashes, uploadDir
	t.Cleanup(func() { hubFold, hubSlashes, uploadDir = fold, slashes, old })
	id := strings.Repeat("ab", 32)
	link := " coral://artifacts/" + id
	t.Setenv("HOME", `C:\Users\alice`)
	t.Setenv("USERPROFILE", `C:\Users\alice`) // os.UserHomeDir on Windows
	uploadDir = `C:\Users\alice\.coral\uploads`

	hubFold, hubSlashes = true, true // Windows
	for name, in := range map[string]string{
		"backslashes":   `C:\Users\alice\.coral\uploads\shot.png`,
		"slashes":       `C:/Users/alice/.coral/uploads/shot.png`,
		"mixed":         `C:\Users\alice/.coral/uploads\shot.png`,
		"another case":  `c:\users\ALICE\.CORAL\uploads\shot.png`,
		"inside a line": "look at " + `C:\Users\alice\.coral\uploads\shot.png`,
	} {
		out := hubUploadText(in + link)
		assert.NotContains(t, strings.ToLower(out), "alice", name+": %s", out)
		assert.Contains(t, out, "~/.coral/uploads", name)
		assert.True(t, strings.HasSuffix(out, link), name)
	}
	// text that is not about the uploads folder is left alone, even in the home
	other := `cd C:\Users\alice\proj && make`
	assert.Equal(t, other, hubUploadText(other))

	hubFold, hubSlashes = false, false // Linux: a backslash is a character, and case matters
	uploadDir = "/home/alice/.coral/uploads"
	t.Setenv("HOME", "/home/alice")
	t.Setenv("USERPROFILE", "/home/alice")
	assert.Equal(t, "~/.coral/uploads/shot.png"+link, hubUploadText("/home/alice/.coral/uploads/shot.png"+link))
	assert.Equal(t, "/HOME/alice/.coral/uploads/shot.png", hubUploadText("/HOME/alice/.coral/uploads/shot.png"))
}
