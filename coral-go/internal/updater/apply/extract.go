package apply

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Strict extraction of a release tarball (#5231 section 6). The package is its programs and notice files at the top, plus one folder, licenses/,
// the third-party licence tree release.yml adds (third-party-notices.sh): 8 programs and about 190 folders and 150 files, 8 levels deep in
// v1.4.129. Nothing else gets in: no other folder, no link of either kind, no device, FIFO or socket, no path with a separator inside a name, a
// dot dot or a drive, no entry twice, no name the caller does not allow at the top, and no more entries or bytes than the limits. Modes are not
// taken from the archive: a top-level program is 0755 (when the archive marks it executable), everything else 0644 and folders 0755, so a
// setuid, setgid or sticky bit never survives and nothing under licenses/ is ever executable.

// licensesDir is the one folder a package may hold.
const licensesDir = "licenses"

// ExtractLimits bound an extraction.
type ExtractLimits struct {
	MaxFiles int   // regular files, in all
	MaxDirs  int   // folders under licenses/
	MaxDepth int   // path components of an entry under licenses/ (licenses itself is 1)
	MaxFile  int64 // bytes of one file
	MaxTotal int64 // bytes of all files
}

// DefaultExtractLimits are sized for the real package (10 programs, about 150 licence files, 190 folders, depth 8) with room to grow, and small
// enough that a bomb is refused.
var DefaultExtractLimits = ExtractLimits{MaxFiles: 1000, MaxDirs: 1000, MaxDepth: 12, MaxFile: 512 << 20, MaxTotal: 2 << 30}

var errNotPlainName = errors.New("not a plain file name")

// plainComponent says whether s is one plain name: no separator, drive, control character or dot dot, and not empty or ".".
func plainComponent(s string) bool {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\\x00:") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// entryPath cleans a tar entry name to its components ("./licenses/go/x" -> ["licenses","go","x"]) or says it is not a plain relative path.
func entryPath(name string) ([]string, error) {
	n := strings.TrimPrefix(name, "./")
	n = strings.TrimSuffix(n, "/")
	if n == "" || n == "." {
		return nil, nil // the top folder itself
	}
	parts := strings.Split(n, "/")
	for _, p := range parts {
		if !plainComponent(p) {
			return nil, errNotPlainName
		}
	}
	return parts, nil
}

// extractTarGz unpacks the gzip'd tarball at pkg into the existing folder dst and returns the relative paths of the files it wrote ("coral",
// "licenses/go/x/LICENSE") and the sha256 of each. allowTop decides the names of top-level files.
func extractTarGz(pkg, dst string, allowTop func(name string) bool, lim ExtractLimits) ([]string, map[string]string, error) {
	f, err := os.Open(pkg)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, fmt.Errorf("not a gzip file: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	seen := map[string]bool{}
	var names []string
	hashes := map[string]string{}
	dirs := 0
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading the archive: %w", err)
		}
		parts, err := entryPath(h.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("%q: %w", h.Name, err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if len(parts) == 0 {
				continue // the top folder
			}
			if parts[0] != licensesDir {
				return nil, nil, fmt.Errorf("%q: a folder outside %s/", h.Name, licensesDir)
			}
			if len(parts) > lim.MaxDepth {
				return nil, nil, fmt.Errorf("%q is nested too deep", h.Name)
			}
			if seen["d:"+strings.Join(parts, "/")] {
				continue // a folder listed twice is harmless
			}
			seen["d:"+strings.Join(parts, "/")] = true
			if dirs++; dirs > lim.MaxDirs {
				return nil, nil, fmt.Errorf("more than %d folders", lim.MaxDirs)
			}
			if err := os.MkdirAll(filepath.Join(append([]string{dst}, parts...)...), 0o755); err != nil {
				return nil, nil, err
			}
			continue
		case tar.TypeReg:
		default:
			return nil, nil, fmt.Errorf("%q: entry type %q is not a plain file or folder", h.Name, string(h.Typeflag))
		}
		if len(parts) == 0 {
			return nil, nil, fmt.Errorf("%q: not a file name", h.Name)
		}
		rel := strings.Join(parts, "/")
		switch {
		case len(parts) == 1:
			if !allowTop(parts[0]) {
				return nil, nil, fmt.Errorf("%q is not a file of this package", rel)
			}
		case parts[0] == licensesDir:
			if len(parts) > lim.MaxDepth {
				return nil, nil, fmt.Errorf("%q is nested too deep", rel)
			}
		default:
			return nil, nil, fmt.Errorf("%q: a folder outside %s/", rel, licensesDir)
		}
		if seen["f:"+rel] {
			return nil, nil, fmt.Errorf("%q appears twice", rel)
		}
		seen["f:"+rel] = true
		if len(names) >= lim.MaxFiles {
			return nil, nil, fmt.Errorf("more than %d files", lim.MaxFiles)
		}
		if h.Size < 0 || h.Size > lim.MaxFile || total+h.Size > lim.MaxTotal {
			return nil, nil, fmt.Errorf("%q is too large", rel)
		}
		target := filepath.Join(append([]string{dst}, parts...)...)
		if len(parts) > 1 {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, nil, err
			}
		}
		mode := os.FileMode(0o644)
		if len(parts) == 1 && h.FileInfo().Mode()&0o111 != 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, nil, err
		}
		sum := sha256.New()
		n, cerr := io.Copy(io.MultiWriter(out, sum), io.LimitReader(tr, h.Size))
		if e := out.Close(); cerr == nil {
			cerr = e
		}
		if cerr != nil {
			return nil, nil, cerr
		}
		if n != h.Size {
			return nil, nil, fmt.Errorf("%q is shorter than its header says", rel)
		}
		if err := os.Chmod(target, mode); err != nil {
			return nil, nil, err
		}
		total += n
		names = append(names, rel)
		hashes[rel] = hex.EncodeToString(sum.Sum(nil))
	}
	return names, hashes, nil
}
