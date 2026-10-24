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
//
// On Windows (no O_NOFOLLOW, and older Go versions don't report every reparse point as
// ModeIrregular) write protection rests on the root plus the regular-file check on the handle,
// and the link count comes from the handle (GetFileInformationByHandle). os.Root refuses a
// name with a colon (a drive or a stream), a rooted name and a reserved device name there;
// safefs_windows_test.go runs these on Windows (#3615).
package safefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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

// IsUNC reports whether p names a network share or a device namespace: \\host\share and
// //host/share, either separator in either place (Windows reads / and \ alike), the \\?\ and
// \\.\ prefixes (\\?\UNC\host\share is a share too) and the NT form \??\. Resolving a share
// makes Windows connect to that host (and send the user's credentials); macOS's /net does the
// same through the automounter, so callers keep paths under their roots before resolving.
func IsUNC(p string) bool {
	if len(p) >= 2 && isSep(p[0]) && isSep(p[1]) {
		return true
	}
	if len(p) >= 4 && isSep(p[0]) && p[1] == '?' && p[2] == '?' && isSep(p[3]) {
		return true
	}
	return strings.HasPrefix(filepath.VolumeName(p), `\\`)
}

func isSep(c byte) bool { return c == '/' || c == '\\' }

// RefuseRemote is ErrUNC for a path naming a network share or a device namespace (IsUNC), nil
// otherwise. Call it on a path from a request before anything stats, opens or resolves it: on
// Windows even a stat of \\host\share connects to that host as the user (#3669 finding 1).
func RefuseRemote(p string) error {
	if IsUNC(p) {
		return ErrUNC
	}
	return nil
}

// ErrReservedName is a file name Windows maps to a device or alters (#3669 finding 4).
var ErrReservedName = errors.New("safefs: a reserved file name (a Windows device name, a trailing dot or space, or one of < > \" | ? *)")

// windowsNameCharsRefused: on Windows CheckName also refuses the characters its file systems do not allow in a name (< > \" | ? *); on every other system a name with them is an ordinary one (a variable so the tests run the rule everywhere).
var windowsNameCharsRefused = runtime.GOOS == "windows"

// CheckName refuses a name that becomes one path element of a file Coral writes, when Windows
// would not store it as given: a reserved device name (CON, PRN, AUX, NUL, COM0-9, LPT0-9, the
// superscript-digit COM and LPT, CONIN$ and CONOUT$), in any case, with or without an extension
// ("nul.json" is the NUL device too), and a name ending in a dot or a space (Windows drops them,
// so two names would share a file). Checked on every system, so a name works wherever Coral runs.
func CheckName(name string) error {
	if name == "" {
		return ErrReservedName
	}
	if last := name[len(name)-1]; last == '.' || last == ' ' {
		return ErrReservedName
	}
	if windowsNameCharsRefused && strings.ContainsAny(name, "<>\"|?*") { // Windows cannot make a file or folder of such a name: refuse it in one plain line, not as a failure from the file system or git
		return ErrReservedName
	}
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return ErrReservedName
	}
	if len(stem) > 3 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) {
		switch stem[3:] {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "\u00b9", "\u00b2", "\u00b3":
			return ErrReservedName
		}
	}
	return nil
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
	return realPath(abs)
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
		if r, err := realPath(cur); err == nil {
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

// deviceNamesRefused: on Windows the root refuses a path with a component CheckName refuses (a
// device name, with or without an extension, or a trailing dot or space), whatever that
// Windows build does with it: older builds still map "nul.json" to the NUL device, and a read or
// a create on COM1 can block (#3671, Security's ruling). Elsewhere such a name is an ordinary
// file ("aux.go"), so it is left alone. A var so tests run the rule on every system.
var deviceNamesRefused = runtime.GOOS == "windows"

// checkNames refuses rel if any of its components is a name CheckName refuses (on Windows).
func checkNames(rel string) error {
	if !deviceNamesRefused {
		return nil
	}
	for _, c := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if c == "." {
			continue
		}
		if err := CheckName(c); err != nil {
			return fmt.Errorf("%w: %q", ErrReservedName, c)
		}
	}
	return nil
}

// MkdirAll makes rel and its parents inside the root.
func (r *Root) MkdirAll(rel string, perm os.FileMode) error {
	if rel == "." || rel == "" {
		return nil
	}
	if err := checkNames(rel); err != nil {
		return err
	}
	return r.root.MkdirAll(rel, perm)
}

// final checks the last name of rel: if it exists it must be a regular file (not a symlink, a
// reparse point, a FIFO, a device or a folder).
func (r *Root) final(rel string) (fs.FileInfo, error) {
	fi, err := r.root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	return fi, nil
}

// testBeforeOpen, when a test sets it, runs between WriteFile's checks and its open: where a
// racing writer would swap a folder for a link.
var testBeforeOpen func(rel string)

// ErrDenied: after the open, the file turned out to be at a path the caller refuses.
var ErrDenied = errors.New("safefs: the path is not allowed")

// Open opens rel for reading: a regular file (on the handle), non-blocking, never through a
// symlink as its last name.
func (r *Root) Open(rel string) (*os.File, fs.FileInfo, error) {
	if err := checkNames(rel); err != nil {
		return nil, nil, err
	}
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

// ReadFileOneLink is ReadFile for a file that must have no other name (#3672): one with more
// than one hard link, or whose link count the platform can't report, is ErrLinked, as for a
// write. A hard link inside the root to a file outside it (a credential file, say) is the same
// file under an innocent name, so a read through the root would otherwise reach it.
func (r *Root) ReadFileOneLink(rel string, max int64) ([]byte, error) {
	f, fi, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if n, ok := LinkCount(f, fi); !ok || n > 1 {
		return nil, ErrLinked
	}
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
	return r.WriteFileUnless(rel, data, perm, nil)
}

// WriteFileUnless is WriteFile with a deny check that can't be raced: the file is opened FIRST,
// then its path is resolved afresh and must not be one deny refuses, and the open handle must
// be the file at that path. A folder swapped for a link before the open resolves to where it
// leads (and is refused there); a swap after the open can't move the handle. Nothing is written
// before these pass; a file the open itself created is removed again.
func (r *Root) WriteFileUnless(rel string, data []byte, perm os.FileMode, deny func(freshRel string) bool) error {
	if err := checkNames(rel); err != nil {
		return err
	}
	if _, err := r.final(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if testBeforeOpen != nil {
		testBeforeOpen(rel)
	}
	// Whether this call creates the file is decided by the open itself (O_EXCL), never by the
	// check above, which a racing swap could make stale.
	created := true
	f, err := r.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow|oNonBlock, perm)
	if errors.Is(err, fs.ErrExist) {
		created = false
		f, err = r.root.OpenFile(rel, os.O_WRONLY|oNoFollow|oNonBlock, 0)
	}
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	refuse := func(e error) error {
		f.Close()
		// Remove only the file this call made, and only while it is still that file.
		if created && fi != nil {
			if fresh, werr := Within(r.dir, filepath.Join(r.dir, rel)); werr == nil {
				if at, lerr := r.root.Lstat(fresh); lerr == nil && os.SameFile(fi, at) {
					_ = r.root.Remove(fresh)
				}
			}
		}
		return e
	}
	if err != nil || !fi.Mode().IsRegular() {
		return refuse(ErrNotRegular)
	}
	if n, ok := LinkCount(f, fi); !ok || n > 1 {
		return refuse(ErrLinked)
	}
	fresh, err := Within(r.dir, filepath.Join(r.dir, rel))
	if err != nil {
		return refuse(err)
	}
	if deny != nil && deny(fresh) {
		return refuse(ErrDenied)
	}
	if at, err := r.root.Lstat(fresh); err != nil || !os.SameFile(fi, at) {
		return refuse(ErrNotRegular)
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
	if err := checkNames(rel); err != nil {
		return nil, err
	}
	return r.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, perm)
}

// Lstat reports on rel inside the root without following its last name.
func (r *Root) Lstat(rel string) (fs.FileInfo, error) { return r.root.Lstat(rel) }

// Remove removes the regular file rel inside the root, by that exact name (#4447): never a symlink, a reparse point, a folder or a device as its last
// name, never through a link in the path, nothing outside the root, and on Windows not a name CheckName refuses. A name that is not there is an error.
func (r *Root) Remove(rel string) error {
	if err := checkNames(rel); err != nil {
		return err
	}
	if _, err := r.final(rel); err != nil {
		return err
	}
	return r.root.Remove(rel)
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
