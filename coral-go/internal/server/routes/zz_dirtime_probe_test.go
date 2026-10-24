package routes

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// trial probe (not for landing): does a directory's time move when writeChangesArtifact puts a file in it, a second identical (hard-linked) diff, and on this file system?
func TestProbeDirTimeMovesWhenADiffIsWritten(t *testing.T) {
	root := t.TempDir()
	diff := []byte("diff --git a/a b/a\n+x\n")
	older := filepath.Join(root, "artifacts", "tasks", "b0123", "1", "changes.diff")
	if err := writeChangesArtifact(older, diff); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(older, old, old)
	os.Chtimes(filepath.Dir(older), old, old)
	di, _ := os.Stat(filepath.Dir(older))
	t.Logf("older folder time set to %v, reads %v", old.Format(time.RFC3339), di.ModTime().Format(time.RFC3339))
	before := time.Now()
	time.Sleep(1100 * time.Millisecond)
	own := filepath.Join(root, "artifacts", "tasks", "b0456", "2", "changes.diff")
	if err := writeChangesArtifact(own, diff); err != nil { // the same bytes: a hard link to the shared object
		t.Fatal(err)
	}
	a, _ := os.Stat(older)
	b, _ := os.Stat(own)
	dn, _ := os.Stat(filepath.Dir(own))
	t.Logf("same file: %v; own file time %v; own folder time %v; before %v", os.SameFile(a, b), b.ModTime().Format(time.RFC3339Nano), dn.ModTime().Format(time.RFC3339Nano), before.Format(time.RFC3339Nano))
	if !os.SameFile(a, b) {
		t.Log("(not shared on this file system)")
	}
	if dn.ModTime().Before(before) {
		t.Errorf("the folder's time did not move when the diff was written into it")
	}
	// a second write into the same folder (the task finishing again) moves it again
	time.Sleep(1100 * time.Millisecond)
	before2 := time.Now()
	if err := writeChangesArtifact(own, append(diff, '+', 'y', '\n')); err != nil {
		t.Fatal(err)
	}
	dn2, _ := os.Stat(filepath.Dir(own))
	t.Logf("after a rewrite: folder time %v (before %v)", dn2.ModTime().Format(time.RFC3339Nano), before2.Format(time.RFC3339Nano))
	if dn2.ModTime().Before(before2) {
		t.Errorf("the folder's time did not move on a rewrite")
	}
}
