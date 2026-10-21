package routes

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3552 (Security's #3551 finding 1): the first upload's name and type stay; uploading the
// same bytes again can't relabel the artifact for everyone. Each uploader's own name is
// kept in its upload record.
func TestArtifactFirstWriterWins(t *testing.T) {
	dir := t.TempDir()
	data := []byte("the same bytes")
	id, first, err := storeArtifact(dir, "sess-a", "notes.txt", "text/plain", data)
	require.NoError(t, err)
	assert.Equal(t, "notes.txt", first.Name)
	for _, relabel := range [][2]string{{"invoice.html", "text/html"}, {"x.svg", "image/svg+xml"}} {
		id2, got, err := storeArtifact(dir, "sess-b", relabel[0], relabel[1], data)
		require.NoError(t, err)
		assert.Equal(t, id, id2)
		assert.Equal(t, "notes.txt", got.Name, "the stored name is the first writer's")
		assert.Equal(t, "text/plain", got.MediaType)
	}
	side, ok := readSidecar(filepath.Join(dir, "artifacts", "objects", id+".json"))
	require.True(t, ok)
	assert.Equal(t, "notes.txt", side.Name)
	assert.Equal(t, "text/plain", side.MediaType)
	var rec agentUploadRecord
	b, err := os.ReadFile(filepath.Join(dir, "artifacts", "uploads", "sess-b", id+".json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &rec))
	assert.Equal(t, "x.svg", rec.Name, "the second uploader's own record keeps its name")
}

// Finding 6: concurrent uploads of the same bytes never expose a sidecar without the whole
// object, and all agree on one winner.
func TestArtifactConcurrentSameDigest(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	dir := t.TempDir()
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<19) // 8 MiB: a write takes long enough to be seen mid-way
	objDir := filepath.Join(dir, "artifacts", "objects")
	var stop atomic.Bool
	var bad atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // a reader that checks the invariant the whole time
		defer wg.Done()
		for !stop.Load() {
			entries, _ := os.ReadDir(objDir)
			for _, e := range entries {
				if filepath.Ext(e.Name()) != ".json" {
					continue
				}
				fi, err := os.Stat(filepath.Join(objDir, e.Name()[:len(e.Name())-5]))
				if err != nil || fi.Size() != int64(len(data)) {
					bad.Add(1)
				}
			}
		}
	}()
	names := make(chan string, 24)
	var writers sync.WaitGroup
	for i := 0; i < 24; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			_, meta, err := storeArtifact(dir, "s", "file-"+string(rune('a'+i)), "text/plain", data)
			if err == nil {
				names <- meta.Name
			}
		}(i)
	}
	writers.Wait()
	stop.Store(true)
	wg.Wait()
	close(names)
	seen := map[string]bool{}
	for n := range names {
		seen[n] = true
	}
	assert.Len(t, seen, 1, "every upload got the one stored name: %v", seen)
	assert.Zero(t, bad.Load(), "a sidecar was visible before its whole object")
	leftovers, _ := filepath.Glob(filepath.Join(objDir, ".tmp-*"))
	assert.Empty(t, leftovers)
}

// Finding 3: new artifact folders are 0700 and files 0600.
func TestArtifactModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	id, _, err := storeArtifact(dir, "sess", "a.txt", "text/plain", []byte("x"))
	require.NoError(t, err)
	for path, want := range map[string]os.FileMode{
		filepath.Join(dir, "artifacts"):                                0o700,
		filepath.Join(dir, "artifacts", "objects"):                     0o700,
		filepath.Join(dir, "artifacts", "objects", id):                 0o600,
		filepath.Join(dir, "artifacts", "objects", id+".json"):         0o600,
		filepath.Join(dir, "artifacts", "uploads", "sess"):             0o700,
		filepath.Join(dir, "artifacts", "uploads", "sess", id+".json"): 0o600,
	} {
		fi, err := os.Stat(path)
		require.NoError(t, err, path)
		assert.Equal(t, want, fi.Mode().Perm(), path)
	}
}

// Security's #3552 nit (b): on a file system without hard links the sidecar is created with
// O_EXCL and a full write; the first writer still wins.
func TestArtifactSidecarWithoutHardLinks(t *testing.T) {
	sidecarLinkHook = func() error { return &os.LinkError{Op: "link", Err: os.ErrPermission} }
	t.Cleanup(func() { sidecarLinkHook = nil })
	dir := t.TempDir()
	id, first, err := storeArtifact(dir, "a", "notes.txt", "text/plain", []byte("bytes"))
	require.NoError(t, err)
	assert.Equal(t, "notes.txt", first.Name)
	_, again, err := storeArtifact(dir, "b", "invoice.html", "text/html", []byte("bytes"))
	require.NoError(t, err)
	assert.Equal(t, "notes.txt", again.Name, "still the first writer's")
	side, ok := readSidecar(filepath.Join(dir, "artifacts", "objects", id+".json"))
	require.True(t, ok)
	assert.Equal(t, "text/plain", side.MediaType)
}
