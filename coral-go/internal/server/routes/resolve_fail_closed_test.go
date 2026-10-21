package routes

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/linktest"
)

// #3633: a path that can't be resolved (a symlink loop here; on Windows a junction on the way
// once was) is never taken as inside the working directory. Only a path that doesn't exist yet
// is left to the caller.
func TestUnresolvablePathsAreNeverInside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink loop: Unix")
	}
	wd := realPath(t.TempDir())
	loop := filepath.Join(wd, "loop")
	linktest.Symlink(t, loop, loop)
	for _, p := range []string{loop, filepath.Join(loop, "x.txt")} {
		assert.False(t, isPathWithinDir(wd, p), "isPathWithinDir %s", p)
		assert.False(t, pathInside(p, wd), "pathInside %s", p)
	}
	assert.True(t, isPathWithinDir(wd, filepath.Join(wd, "new.txt")), "a file not made yet is the caller's")
	assert.True(t, pathInside(filepath.Join(wd, "new", "x.txt"), wd))
}
