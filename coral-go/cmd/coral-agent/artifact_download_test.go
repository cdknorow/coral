package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactDownloadImageAndIntegrity(t *testing.T) {
	data, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII=")
	hash := sha256.Sum256(data)
	id := hex.EncodeToString(hash[:])
	corrupt := false
	mediaType := "image/png"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/artifacts/"+id {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mediaType)
		if corrupt {
			w.Write([]byte("wrong bytes"))
			return
		}
		w.Write(data)
	}))
	defer server.Close()
	path, err := downloadArtifact(server.URL, "coral://artifacts/"+id, "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if filepath.Ext(path) != ".png" {
		t.Fatal(path)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := png.DecodeConfig(f); err != nil {
		t.Fatal(err)
	}
	mediaType = "application/octet-stream"
	sniffed, err := downloadArtifact(server.URL, id, "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(sniffed)
	if filepath.Ext(sniffed) != ".png" {
		t.Fatalf("untyped image has wrong extension: %s", sniffed)
	}
	output := filepath.Join(t.TempDir(), "screen.png")
	if _, err := downloadArtifact(server.URL, server.URL+"/api/artifacts/"+id, output); err != nil {
		t.Fatal(err)
	}
	if _, err := downloadArtifact(server.URL, id, output); err == nil {
		t.Fatal("overwrote destination")
	}
	corrupt = true
	bad := filepath.Join(t.TempDir(), "corrupt.png")
	if _, err := downloadArtifact(server.URL, id, bad); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatal(err)
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatal("published corrupt download")
	}
	if _, err := downloadArtifact(server.URL, strings.Repeat("0", 64), bad); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatal(err)
	}
}

func TestArtifactDownloadRejectsUnrelatedURLs(t *testing.T) {
	for _, ref := range []string{"../secret", "coral://artifacts/xyz", "https://other.example/api/artifacts/" + strings.Repeat("a", 64)} {
		if _, err := artifactDigest("http://localhost:8420", ref); err == nil {
			t.Fatalf("accepted %q", ref)
		}
	}
}
