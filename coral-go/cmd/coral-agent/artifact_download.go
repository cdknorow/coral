package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/secfile"
)

// linkVersion is the query of a versioned artifact link (/api/artifacts/<digest>?v=2).
var linkVersion = regexp.MustCompile(`^v=[0-9]+$`)

func artifactDigest(base, ref string) (string, error) {
	id := strings.TrimPrefix(ref, "coral://artifacts/")
	if strings.HasPrefix(ref, "/api/artifacts/") {
		id = strings.TrimPrefix(ref, "/api/artifacts/")
		if i := strings.IndexByte(id, '?'); i >= 0 && linkVersion.MatchString(id[i+1:]) {
			id = id[:i] // the ?v=N link form (v1.4.12)
		}
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		u, err := url.Parse(ref)
		b, baseErr := url.Parse(base)
		if err != nil || baseErr != nil || u.Scheme != b.Scheme || u.Host != b.Host || u.User != nil || (u.RawQuery != "" && !linkVersion.MatchString(u.RawQuery)) || u.Fragment != "" {
			return "", usageError{"artifact URL must belong to the configured Coral server"}
		}
		id = strings.TrimPrefix(u.Path, "/api/artifacts/")
	}
	if len(id) != 64 {
		return "", usageError{"expected coral://artifacts/<SHA-256>, /api/artifacts/<SHA-256>, or a SHA-256 digest"}
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", usageError{"invalid artifact SHA-256"}
	}
	return strings.ToLower(id), nil
}

func downloadArtifact(base, ref, output string) (string, error) {
	return downloadArtifactOpts(base, ref, output, false)
}

// downloadArtifactOpts is downloadArtifact; with force an existing output file is replaced, otherwise it is refused.
func downloadArtifactOpts(base, ref, output string, force bool) (string, error) {
	id, err := artifactDigest(base, ref)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: clierr.Timeout(30 * time.Second), CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("artifact redirects are not supported") }}
	var src io.Reader
	var mediaType, localObject string
	resp, err := client.Get(strings.TrimRight(base, "/") + "/api/artifacts/" + id)
	if err != nil {
		// Coral could not be reached: some agent sandboxes (Codex's default one) block connections to localhost. The artifact is a file in this user's Coral data folder, which such a
		// sandbox lets the agent read: read it there; the SHA-256 check below proves it is the artifact the link names (#5496). A server that answered (any status) is never second-guessed.
		f, mt, lerr := openLocalArtifact(id)
		if lerr != nil {
			return "", fmt.Errorf("%w (and the artifact's file could not be read in Coral's data folder either: %v; if this agent runs in a sandbox that blocks localhost, approve the command to run outside it)", err, lerr)
		}
		defer f.Close()
		src, mediaType, localObject = f, mt, f.Name()
	} else {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("Coral says: this artifact is not on this computer. It may live on another Coral (a link from a message sent by another team or computer is only a name here): ask the sender to attach the file with coral-board send --attach coral://artifacts/<id> (HTTP 404)")
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%s", clierr.Describe(nil, resp.StatusCode, nil))
		}
		mediaType, _, _ = mime.ParseMediaType(resp.Header.Get("Content-Type"))
		src = resp.Body
	}
	body := bufio.NewReader(src)
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
		if fi, serr := os.Stat(output); serr == nil && fi.IsDir() {
			return "", fmt.Errorf("%s is a folder; give a file name, for example --output %s", output, filepath.Join(output, "artifact.bin"))
		}
		if fi, serr := os.Stat(dir); serr != nil || !fi.IsDir() {
			return "", fmt.Errorf("the folder %s does not exist; create it or choose another --output", dir)
		}
	}
	f, err := os.CreateTemp(dir, "coral-artifact-*"+ext)
	if err != nil {
		// A read-only sandbox lets the agent read the stored file but not write a copy: after the same SHA-256 proof, the stored file's own path is the answer (it has no extension).
		if localObject != "" && output == "" && errors.Is(err, fs.ErrPermission) {
			if verr := verifyObject(localObject, id); verr != nil {
				return "", verr
			}
			fmt.Fprintln(os.Stderr, "note: this sandbox cannot write a copy; the path printed is Coral's own stored file (checked against the link's SHA-256, no file extension): read it, do not write to it")
			return filepath.Abs(localObject)
		}
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
		if force {
			// An explicit --force: the file the person named is replaced (a rename over it; on Windows it fails if another program has it open).
			if err := os.Rename(f.Name(), output); err != nil {
				return "", fmt.Errorf("cannot replace %s: %w", output, err)
			}
			keep = true
			return output, nil
		}
		// Publish without overwriting an existing file, including concurrent writers.
		if err := publishNew(f.Name(), output, id); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return "", fmt.Errorf("the file %s already exists; choose another --output or add --force to replace it", output)
			}
			return "", err
		}
		return output, nil
	}
	keep = true
	return filepath.Abs(f.Name())
}

// openLocalArtifact opens the stored copy of artifact id in this user's Coral data folder (CORAL_DATA_DIR, else ~/.coral): artifacts/objects/<digest>, a regular file opened without
// following a link, and its recorded media type from the sidecar when there is one. The caller checks the SHA-256 of what it reads.
func openLocalArtifact(id string) (*os.File, string, error) {
	object := filepath.Join(config.ResolveCoralDir(""), "artifacts", "objects", id)
	f, err := secfile.OpenNoFollow(object, os.O_RDONLY, 0)
	if err != nil {
		return nil, "", err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, "", fmt.Errorf("not a regular file")
	}
	mediaType := ""
	if side, err := os.ReadFile(object + ".json"); err == nil && len(side) < 64<<10 {
		var m struct {
			MediaType string `json:"media_type"`
		}
		if json.Unmarshal(side, &m) == nil {
			mediaType, _, _ = mime.ParseMediaType(m.MediaType)
		}
	}
	return f, mediaType, nil
}

// linkFile makes dst a hard link to src (a variable so a test can stand in for a file system that has none).
var linkFile = os.Link

// publishNew puts the verified file src at dst, which must not exist (ErrExist if it does, also when another writer got there first). A hard link does it in one atomic
// step; where the file system has none (FAT and exFAT on a USB stick or an SD card, some network shares) the bytes are copied into a file created exclusively and checked
// against the SHA-256 again, and the temporary file is left for the caller to remove.
func publishNew(src, dst, id string) error {
	err := linkFile(src, dst)
	if err == nil || errors.Is(err, fs.ErrExist) || !linkUnsupported(err) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // exclusive: never over a file that is there
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	if err := verifyObject(dst, id); err != nil { // read back from where it now is
		os.Remove(dst)
		return err
	}
	return nil
}

// verifyObject reports whether the file at path holds exactly the bytes whose SHA-256 is id.
func verifyObject(path, id string) error {
	f, err := secfile.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if n, err := io.Copy(h, io.LimitReader(f, 64<<20+1)); err != nil || n > 64<<20 {
		return fmt.Errorf("unreadable or too large")
	}
	if hex.EncodeToString(h.Sum(nil)) != id {
		return fmt.Errorf("artifact SHA-256 mismatch")
	}
	return nil
}
