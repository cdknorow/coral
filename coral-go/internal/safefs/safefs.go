// Package safefs opens, reads and writes files at paths that someone other than the user
// chose: an HTTP request, a hook payload, board text, a transcript, a repository's contents
// (#3596). Every check the reviews of #3571, #3587 and the path sweep asked for lives here once:
//
//   - a path is decided on its RESOLVED form, against a root that is resolved too, and a root
//     that no longer exists is an error, never "allow everything";
//   - files are opened through an os.Root, so no path, symlink or "..", even one swapped in
//     after the check, leads out of the root;
//   - the file opened must be a regular file (checked on the open handle, never the path),
//     reads are non-blocking (a FIFO can't hang them) and capped;
//   - a write to an existing file refuses one with more than one hard link (another name
//     for a file outside the root), and CreateNew refuses any existing name;
//   - UNC paths (\\host\share, //host/share) are refused before anything resolves them, and a
//     reparse point or symlink as the final name is refused (Windows too);
//   - ids that end up in paths have one checking function per shape.
package safefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrRootGone   = errors.New("safefs: the root folder doesn't exist")
	ErrOutside    = errors.New("safefs: the path is outside the root")
	ErrNotRegular = errors.New("safefs: not a regular file")
	ErrTooLarge   = errors.New("safefs: the file is larger than allowed")
	ErrLinked     = errors.New("safefs: the file has another name (a hard link)")
	ErrUNC        = errors.New("safefs: a network (UNC) path is refused")
)

// IsUNC reports whether p names a network share (\\host\share or //host/share). Resolving one
// makes Windows connect to that host (and send the user's credentials); macOS's /net does the
// same through the automounter, so callers keep paths under their roots before resolving.
func IsUNC(p string) bool {
	return strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") || strings.HasPrefix(filepath.VolumeName(p), `\\`)
}

// Resolve returns the real path of an existing p (symlinks resolved, cleaned, absolute).
func Resolve(p string) (string, error) {
	if p == "" {
		return "", ErrRootGone
	}
	if IsUNC(p) {
		return "", ErrUNC
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// Within resolves root (which must exist) and p (absolute, or relative to root), and returns
// p's path relative to the resolved root, or ErrOutside. For a p that doesn't exist yet, its
// longest existing ancestor is resolved and the rest appended, so a symlinked ancestor is seen.
// p is cleaned first: callers act on the relative path returned, never on the original.
func Within(root, p string) (string, error) {
	realRoot, err := Resolve(root)
	if err != nil {
		if errors.Is(err, ErrUNC) {
			return "", err
		}
		return "", ErrRootGone
	}
	if fi, err := os.Stat(realRoot); err != nil || !fi.IsDir() {
		return "", ErrRootGone
	}
	if IsUNC(p) {
		return "", ErrUNC
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(realRoot, p)
	}
	real, err := resolveExisting(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", ErrOutside
	}
	return rel, nil
}

// resolveExisting resolves the longest existing ancestor of the clean, absolute p and appends
// the rest.
func resolveExisting(p string) (string, error) {
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", ErrRootGone
		}
		rest = filepath.Join(filepath.Base(cur), rest) // p is clean: no ".." is left in it
		cur = parent
	}
}

// Root is a folder held open: every name below it is resolved inside it.
type Root struct {
	root *os.Root
	dir  string
}

// OpenRoot opens the existing folder dir (resolved first).
func OpenRoot(dir string) (*Root, error) {
	real, err := Resolve(dir)
	if err != nil {
		if errors.Is(err, ErrUNC) {
			return nil, err
		}
		return nil, ErrRootGone
	}
	r, err := os.OpenRoot(real)
	if err != nil {
		return nil, ErrRootGone
	}
	return &Root{root: r, dir: real}, nil
}

// Dir is the root's resolved path.
func (r *Root) Dir() string { return r.dir }

func (r *Root) Close() error { return r.root.Close() }

// MkdirAll makes rel and its parents inside the root.
func (r *Root) MkdirAll(rel string, perm os.FileMode) error {
	if rel == "." || rel == "" {
		return nil
	}
	return r.root.MkdirAll(rel, perm)
}

// final checks the last name of rel: if it exists it must not be a symlink or a reparse point.
func (r *Root) final(rel string) (fs.FileInfo, error) {
	fi, err := r.root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return nil, ErrNotRegular
	}
	return fi, nil
}

// Open opens rel for reading: a regular file (on the handle), non-blocking, never through a
// symlink as its last name.
func (r *Root) Open(rel string) (*os.File, fs.FileInfo, error) {
	pre, err := r.final(rel)
	if err != nil {
		return nil, nil, err
	}
	f, err := r.root.OpenFile(rel, os.O_RDONLY|oNoFollow|oNonBlock, 0)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(pre, fi) {
		f.Close()
		return nil, nil, ErrNotRegular
	}
	return f, fi, nil
}

// ReadFile reads rel, at most max bytes (a bigger file is ErrTooLarge).
func (r *Root) ReadFile(rel string, max int64) ([]byte, error) {
	f, fi, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi.Size() > max {
		return nil, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, ErrTooLarge
	}
	return data, nil
}

// WriteFile writes data to rel inside the root, creating it (perm) or replacing the content of
// an existing regular file with one link. It is truncated only after those checks pass.
func (r *Root) WriteFile(rel string, data []byte, perm os.FileMode) error {
	if _, err := r.final(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := r.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|oNoFollow, perm)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return ErrNotRegular
	}
	if fileLinks(fi) > 1 {
		f.Close()
		return ErrLinked
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// CreateNew makes a new file rel (it must not exist, under any kind of name).
func (r *Root) CreateNew(rel string, perm os.FileMode) (*os.File, error) {
	return r.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, perm)
}

// Lstat reports on rel inside the root without following its last name.
func (r *Root) Lstat(rel string) (fs.FileInfo, error) { return r.root.Lstat(rel) }

// IsCanonicalUUID reports whether id is a UUID in the canonical 36-character lower-case form
// (8-4-4-4-12 hex), the form Coral generates for session ids.
func IsCanonicalUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}

// IsSafeID reports whether id is safe as one path element and as a command-line argument: 1 to
// 128 ASCII letters, digits, '-' and '_', starting with a letter or digit (so never a flag, ".",
// ".." or a separator).
func IsSafeID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || (c != '-' && c != '_')) {
			return false
		}
	}
	return true
}

// String forms for messages.
func (r *Root) String() string { return fmt.Sprintf("safefs.Root(%s)", r.dir) }
