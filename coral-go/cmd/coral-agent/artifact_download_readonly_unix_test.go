//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A read-only sandbox lets the agent read Coral's stored file but not write a copy anywhere: after the SHA-256 proof the stored file's own path is printed; a damaged file is still refused.
func TestDownloadFallbackInAReadOnlySandboxPrintsTheStoredFilesPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only folder")
	}
	dataDir := isolated(t)
	id := storeLocal(t, dataDir, []byte("read-only sandbox bytes"), `{"media_type":"text/plain"}`)
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", ro)

	path, err := downloadArtifact(deadURL(t), id, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dataDir, "artifacts", "objects", id); path != want {
		t.Fatalf("the stored file's own path: %s, want %s", path, want)
	}

	// With an output path the agent asked for, a refused write is an error: it is not told a file exists there.
	if _, err := downloadArtifact(deadURL(t), id, filepath.Join(ro, "x.txt")); err == nil {
		t.Fatal("an unwritable output path is an error")
	}

	// A damaged stored file is never handed out.
	if err := os.WriteFile(filepath.Join(dataDir, "artifacts", "objects", id), []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := downloadArtifact(deadURL(t), id, ""); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("%v", err)
	}
}
