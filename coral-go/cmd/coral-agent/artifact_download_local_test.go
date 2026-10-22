package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #5496: Codex's default sandbox blocks connections to localhost, so `coral-agent artifact download` cannot reach Coral there. It then reads the artifact's stored file in this user's Coral
// data folder (the sandbox allows reads), proves by SHA-256 that it is what the link names, and copies it out as always. A server that DID answer is never second-guessed.

// isolated points the data folder and home at temp folders: a test never reads the real ~/.coral.
func isolated(t *testing.T) (dataDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dataDir = filepath.Join(home, "data")
	t.Setenv("CORAL_DATA_DIR", dataDir)
	t.Setenv("CORAL_DIR", "")
	return dataDir
}

func storeLocal(t *testing.T, dataDir string, data []byte, sidecar string) string {
	t.Helper()
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	dir := filepath.Join(dataDir, "artifacts", "objects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if sidecar != "" {
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(sidecar), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// deadURL is an address nothing listens on: the connection is refused, as a sandbox that blocks localhost refuses it.
func deadURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

func TestDownloadFallsBackToTheStoredFileWhenCoralCannotBeReached(t *testing.T) {
	dataDir := isolated(t)
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64))
	id := storeLocal(t, dataDir, png, `{"name":"shot.png","media_type":"image/png","size":72}`)

	path, err := downloadArtifact(deadURL(t), "coral://artifacts/"+id, "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if filepath.Ext(path) != ".png" {
		t.Fatalf("extension from the sidecar's media type: %s", path)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(png) {
		t.Fatal("the copy is not the stored bytes")
	}

	// An explicit output path works the same, and never overwrites.
	out := filepath.Join(t.TempDir(), "image.png")
	if p, err := downloadArtifact(deadURL(t), id, out); err != nil || p != out {
		t.Fatalf("%v %s", err, p)
	}
	if _, err := downloadArtifact(deadURL(t), id, out); err == nil {
		t.Fatal("an existing output file is not overwritten")
	}
}

func TestDownloadFallbackProvesTheFileIsTheArtifact(t *testing.T) {
	dataDir := isolated(t)
	id := storeLocal(t, dataDir, []byte("the real bytes"), "")
	// Planted or damaged: the file under that name holds other bytes.
	if err := os.WriteFile(filepath.Join(dataDir, "artifacts", "objects", id), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := downloadArtifact(deadURL(t), id, ""); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("a file whose hash is not the link's is refused: %v", err)
	}
	// Nothing stored: the error says both things.
	_, err := downloadArtifact(deadURL(t), strings.Repeat("a", 64), "")
	if err == nil || !strings.Contains(err.Error(), "could not be read in Coral's data folder") {
		t.Fatalf("%v", err)
	}
}

func TestDownloadFallbackNeverReadsALinkOrADirectory(t *testing.T) {
	dataDir := isolated(t)
	dir := filepath.Join(dataDir, "artifacts", "objects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("not an artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("not an artifact"))
	id := hex.EncodeToString(sum[:])
	if err := os.Symlink(secret, filepath.Join(dir, id)); err == nil {
		if _, err := downloadArtifact(deadURL(t), id, ""); err == nil {
			t.Fatal("a symlink in the objects folder is not followed")
		}
	}
	d := strings.Repeat("b", 64)
	if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := downloadArtifact(deadURL(t), d, ""); err == nil {
		t.Fatal("a directory is not an artifact")
	}
}

// A Coral that answers is final: a 404 (an image the paste switch hides, a deleted artifact) does not send the agent to the stored file.
func TestDownloadDoesNotFallBackWhenCoralAnswers(t *testing.T) {
	dataDir := isolated(t)
	id := storeLocal(t, dataDir, []byte("hidden from the hub"), "")
	for _, code := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		_, err := downloadArtifact(server.URL, id, "")
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "HTTP") {
			t.Fatalf("%d: %v", code, err)
		}
	}
}

// #5499: a link that names an artifact this computer does not have (it lives on another Coral) says so and what to do.
func TestDownloadOfAMissingArtifactSaysItMayLiveOnAnotherCoral(t *testing.T) {
	isolated(t)
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, err := downloadArtifact(server.URL, "coral://artifacts/"+strings.Repeat("d", 64), "")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"Coral says: this artifact is not on this computer", "(HTTP 404)", "not on this computer", "another Coral", "coral-board send --attach"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
