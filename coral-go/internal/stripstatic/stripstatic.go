// Package stripstatic makes the build-time overlay that ships the dashboard's JS and CSS already minified (#6208). The server minifies each file when it serves it (frontend.MinifyAsset), so the
// binary carries the comments and indentation for nothing; a release build passes -overlay to go build, and go:embed then embeds the minified bytes under the same paths. The source tree is not touched.
//
// An overlay can replace any file the compiler reads, so this package writes only what it must and refuses everything else: Validate rejects a key that is not a JS or CSS file MinifyAsset would
// minify under the static directory, and a target outside the overlay's own folder.
package stripstatic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/subgentic/coral/pkg/frontend"
)

// File is the overlay's name inside its folder.
const File = "overlay.json"

// Result says what Generate did.
type Result struct {
	Overlay       string // the overlay.json to pass to go build -overlay
	Files         int    // files replaced
	SourceBytes   int64  // their size before
	StrippedBytes int64  // and after
	Unchanged     int    // JS and CSS files MinifyAsset leaves as they are
}

type overlayJSON struct {
	Replace map[string]string
}

// minified reports whether MinifyAsset is one that handles name (a path under the static directory).
func minified(name string) bool {
	name = path.Clean("/" + filepath.ToSlash(name))[1:]
	ext := path.Ext(name)
	return (ext == ".js" || ext == ".css") && !strings.HasPrefix(name, "vendor/") && !strings.HasSuffix(name, ".min.js") && !strings.HasPrefix(name, "agent_docs/")
}

// Generate minifies every file under staticDir that the server minifies and writes the ones that change into outDir, with outDir/overlay.json mapping each source file to its copy. outDir must be a
// folder the caller just made for this run (os.MkdirTemp: owner-only). It fails when nothing changed (a release must not quietly ship the larger source) and when minifying is off.
func Generate(staticDir, outDir string) (Result, error) {
	var r Result
	if os.Getenv("CORAL_NO_MINIFY") == "1" {
		return r, errors.New("CORAL_NO_MINIFY=1 is set: minifying is off, so there is nothing to ship stripped")
	}
	absStatic, err := filepath.Abs(staticDir)
	if err != nil {
		return r, err
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return r, err
	}
	ov := overlayJSON{Replace: map[string]string{}}
	err = filepath.WalkDir(absStatic, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(absStatic, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !minified(rel) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out := frontend.MinifyAsset(rel, data)
		if bytes.Equal(out, data) {
			r.Unchanged++
			return nil
		}
		target := filepath.Join(absOut, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, out, 0o600); err != nil {
			return err
		}
		ov.Replace[p] = target
		r.Files++
		r.SourceBytes += int64(len(data))
		r.StrippedBytes += int64(len(out))
		return nil
	})
	if err != nil {
		return r, err
	}
	if r.Files == 0 {
		return r, fmt.Errorf("nothing under %s changes when minified: refusing to write an empty overlay", staticDir)
	}
	b, err := json.MarshalIndent(ov, "", "  ")
	if err != nil {
		return r, err
	}
	r.Overlay = filepath.Join(absOut, File)
	if err := os.WriteFile(r.Overlay, b, 0o600); err != nil {
		return r, err
	}
	return r, Validate(r.Overlay, absStatic)
}

// Validate checks an overlay file: every key an absolute path to a JS or CSS file under staticDir that MinifyAsset minifies, no ".." in it, and every target a file inside the overlay's own folder. It
// is what a release runs on the overlay it actually used.
func Validate(overlayPath, staticDir string) error {
	raw, err := os.ReadFile(overlayPath)
	if err != nil {
		return err
	}
	var ov overlayJSON
	if err := json.Unmarshal(raw, &ov); err != nil {
		return fmt.Errorf("%s: %w", overlayPath, err)
	}
	if len(ov.Replace) == 0 {
		return fmt.Errorf("%s replaces nothing", overlayPath)
	}
	absStatic, err := filepath.Abs(staticDir)
	if err != nil {
		return err
	}
	folder, err := filepath.Abs(filepath.Dir(overlayPath))
	if err != nil {
		return err
	}
	if st, err := os.Lstat(overlayPath); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", overlayPath)
	} else if err := checkPrivate(overlayPath, st); err != nil {
		return err
	}
	keys := make([]string, 0, len(ov.Replace))
	for k := range ov.Replace {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		target := ov.Replace[key]
		if !filepath.IsAbs(key) || strings.Contains(filepath.ToSlash(key), "..") {
			return fmt.Errorf("overlay key %q is not a clean absolute path", key)
		}
		rel, err := filepath.Rel(absStatic, filepath.Clean(key))
		if err != nil || rel == "." || strings.HasPrefix(filepath.ToSlash(rel), "../") || filepath.IsAbs(rel) {
			return fmt.Errorf("overlay key %q is not under %s: an overlay may only replace files of the static tree", key, staticDir)
		}
		if !minified(rel) {
			return fmt.Errorf("overlay key %q is not a JS or CSS file the dashboard minifies", key)
		}
		if !filepath.IsAbs(target) || strings.Contains(filepath.ToSlash(target), "..") {
			return fmt.Errorf("overlay target %q is not a clean absolute path", target)
		}
		if tr, err := filepath.Rel(folder, filepath.Clean(target)); err != nil || tr == "." || strings.HasPrefix(filepath.ToSlash(tr), "../") || filepath.IsAbs(tr) {
			return fmt.Errorf("overlay target %q is outside the overlay's own folder %s", target, folder)
		}
		// Lstat: a symlink is refused, and the file and every folder between it and the overlay's folder must be the caller's and closed to others
		if st, err := os.Lstat(target); err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("overlay target %q is not a regular file", target)
		} else if err := checkPrivate(target, st); err != nil {
			return err
		}
		for d := filepath.Dir(filepath.Clean(target)); ; d = filepath.Dir(d) {
			st, err := os.Lstat(d)
			if err != nil || !st.IsDir() {
				return fmt.Errorf("overlay folder %q is not a real folder", d)
			}
			if err := checkPrivate(d, st); err != nil {
				return err
			}
			if d == folder {
				break
			}
		}
	}
	return nil
}

// CheckOutDir accepts an -out folder only when it is a real (not symlinked), empty folder named coral-static-*, so -clean can remove it and nothing else is in it.
func CheckOutDir(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(filepath.Base(abs), "coral-static-") {
		return fmt.Errorf("-out %s: the folder must be named coral-static-*", dir)
	}
	st, err := os.Lstat(abs)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("-out %s: not an existing folder (a symlink does not count)", dir)
	}
	if err := checkPrivate(abs, st); err != nil {
		return err
	}
	if ents, err := os.ReadDir(abs); err != nil || len(ents) != 0 {
		return fmt.Errorf("-out %s: the folder must be empty", dir)
	}
	return nil
}
