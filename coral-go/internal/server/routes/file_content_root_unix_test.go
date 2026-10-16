//go:build !windows

package routes

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The file preview reads through a held-open root (#3672, path audit #3669 finding 2): a FIFO in
// the work tree (or one a local dashboard names by absolute path) answers at once instead of
// hanging the request, and a hard link to a credential file is refused, for both callers.
func TestFileContentReadsThroughTheRoot(t *testing.T) {
	e := newBoundsEnv(t)
	require.NoError(t, syscall.Mkfifo(filepath.Join(e.repo, "app", "pipe"), 0o600))
	outsideFifo := filepath.Join(filepath.Dir(e.outside), "outside-pipe")
	require.NoError(t, syscall.Mkfifo(outsideFifo, 0o600))
	require.NoError(t, os.Link(filepath.Join(e.home, ".ssh", "id_rsa"), filepath.Join(e.repo, "app", "notes.txt")))
	require.NoError(t, os.Link(e.outside, filepath.Join(e.repo, "app", "readme-copy.txt")))

	type res struct {
		code int
		body string
	}
	get := func(via bool, fp string) res {
		done := make(chan res, 1)
		go func() { c, b := e.get(fc, via, "filepath", fp); done <- res{c, b} }()
		select {
		case r := <-done:
			return r
		case <-time.After(5 * time.Second):
			t.Fatalf("reading %s blocked", fp)
			return res{}
		}
	}
	_, missing := e.get(fc, true, "filepath", "no/such/file")
	for _, via := range []bool{false, true} {
		r := get(via, "app/pipe")
		assert.Equal(t, 404, r.code, "a FIFO in the work tree, tunnel=%v", via)
		for _, fp := range []string{"app/notes.txt", "app/readme-copy.txt"} {
			r := get(via, fp)
			assert.Equal(t, 404, r.code, "%s (a hard link out), tunnel=%v", fp, via)
			assert.NotContains(t, r.body, "PRIVATE", fp)
			assert.NotContains(t, r.body, "outside-secret", fp)
			if via {
				assert.Equal(t, missing, r.body, "same body as a missing file: "+fp)
			}
		}
		// A plain file still reads.
		r = get(via, "app/main.go")
		assert.Equal(t, 200, r.code)
		assert.Contains(t, r.body, "package main")
	}
	// The local dashboard's absolute read outside the tree goes through the file's own folder.
	assert.Equal(t, 404, get(false, outsideFifo).code, "a FIFO named by absolute path")
}
