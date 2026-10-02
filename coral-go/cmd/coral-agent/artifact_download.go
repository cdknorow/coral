package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func artifactDigest(base, ref string) (string, error) {
	id := strings.TrimPrefix(ref, "coral://artifacts/")
	if strings.HasPrefix(ref, "/api/artifacts/") {
		id = strings.TrimPrefix(ref, "/api/artifacts/")
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		u, err := url.Parse(ref)
		b, baseErr := url.Parse(base)
		if err != nil || baseErr != nil || u.Scheme != b.Scheme || u.Host != b.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("artifact URL must belong to the configured Coral server")
		}
		id = strings.TrimPrefix(u.Path, "/api/artifacts/")
	}
	if len(id) != 64 {
		return "", fmt.Errorf("expected coral://artifacts/<SHA-256>, /api/artifacts/<SHA-256>, or a SHA-256 digest")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", fmt.Errorf("invalid artifact SHA-256")
	}
	return strings.ToLower(id), nil
}

func downloadArtifact(base, ref, output string) (string, error) {
	id, err := artifactDigest(base, ref)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("artifact redirects are not supported") }}
	resp, err := client.Get(strings.TrimRight(base, "/") + "/api/artifacts/" + id)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("artifact download failed: HTTP %d", resp.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	body := bufio.NewReader(resp.Body)
	if mediaType == "" || mediaType == "application/octet-stream" {
		prefix, _ := body.Peek(512)
		mediaType, _, _ = mime.ParseMediaType(http.DetectContentType(prefix))
	}
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif", "image/svg+xml": ".svg", "text/plain": ".txt", "text/markdown": ".md", "application/json": ".json", "application/pdf": ".pdf"}[mediaType]
	if ext == "" {
		ext = ".bin"
	}
	dir := ""
	if output != "" {
		output, err = filepath.Abs(output)
		if err != nil {
			return "", err
		}
		dir = filepath.Dir(output)
	}
	f, err := os.CreateTemp(dir, "coral-artifact-*"+ext)
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		f.Close()
		if !keep {
			os.Remove(f.Name())
		}
	}()
	hash := sha256.New()
	const maxSize = 64 << 20
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(body, maxSize+1))
	if err != nil {
		return "", err
	}
	if n > maxSize {
		return "", fmt.Errorf("artifact exceeds 64 MiB")
	}
	if hex.EncodeToString(hash.Sum(nil)) != id {
		return "", fmt.Errorf("artifact SHA-256 mismatch")
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if output != "" {
		// Publish without overwriting an existing file, including concurrent writers.
		if err := os.Link(f.Name(), output); err != nil {
			return "", err
		}
		return output, nil
	}
	keep = true
	return filepath.Abs(f.Name())
}
