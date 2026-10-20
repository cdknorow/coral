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

// Strict extraction of a release tarball (#5231 section 6): the package is flat, so an entry is a regular file with a plain name that the
// caller allows, and nothing else gets in: no folder but the top one, no link of either kind, no device, FIFO or socket, no path with a
// separator, a dot dot or a drive, no name twice, and no more files or bytes than the limits. Modes are not taken from the archive: a program
// is 0755 (when the archive marks it executable), anything else 0644, so a setuid, setgid or sticky bit never survives.

// ExtractLimits bound an extraction.
type ExtractLimits struct {
	MaxFiles int
	MaxFile  int64
	MaxTotal int64
}

// DefaultExtractLimits are generous for a package of ten programs and a few notices, and small enough that a bomb is refused.
var DefaultExtractLimits = ExtractLimits{MaxFiles: 64, MaxFile: 512 << 20, MaxTotal: 2 << 30}

var errNotPlainName = errors.New("not a plain file name")

// plainName cleans a tar entry name to a flat file name or says it is not one.
func plainName(name string) (string, error) {
	n := strings.TrimPrefix(name, "./")
	if n == "" || n == "." || strings.ContainsAny(n, "/\\\x00:") || n == ".." || strings.HasPrefix(n, "..") && strings.Trim(n, ".") == "" {
		return "", errNotPlainName
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return "", errNotPlainName
		}
	}
	return n, nil
}

// extractTarGz unpacks the gzip'd tarball at pkg into the existing folder dst and returns the names it wrote and the sha256 of each. allow decides
// each name.
func extractTarGz(pkg, dst string, allow func(name string) bool, lim ExtractLimits) ([]string, map[string]string, error) {
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
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading the archive: %w", err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if n := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/"); n != "" && n != "." {
				return nil, nil, fmt.Errorf("%q: a folder in a flat package", h.Name)
			}
			continue
		case tar.TypeReg:
		default:
			return nil, nil, fmt.Errorf("%q: entry type %q is not a plain file", h.Name, string(h.Typeflag))
		}
		name, err := plainName(h.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("%q: %w", h.Name, err)
		}
		if !allow(name) {
			return nil, nil, fmt.Errorf("%q is not a file of this package", name)
		}
		if seen[name] {
			return nil, nil, fmt.Errorf("%q appears twice", name)
		}
		seen[name] = true
		if len(names) >= lim.MaxFiles {
			return nil, nil, fmt.Errorf("more than %d files", lim.MaxFiles)
		}
		if h.Size < 0 || h.Size > lim.MaxFile || total+h.Size > lim.MaxTotal {
			return nil, nil, fmt.Errorf("%q is too large", name)
		}
		mode := os.FileMode(0o644)
		if h.FileInfo().Mode()&0o111 != 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(filepath.Join(dst, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
			return nil, nil, fmt.Errorf("%q is shorter than its header says", name)
		}
		if err := os.Chmod(filepath.Join(dst, name), mode); err != nil {
			return nil, nil, err
		}
		total += n
		names = append(names, name)
		hashes[name] = hex.EncodeToString(sum.Sum(nil))
	}
	return names, hashes, nil
}
