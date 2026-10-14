//go:build linux || darwin

package cloud

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Credential files (#3149, Coral Hub launch-anywhere bootstrap). Every check is
// made on an open file descriptor, never by re-resolving a path, so nothing can
// be swapped in between a check and its use:
//
//   - the file is opened O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC and must be a regular
//     file owned by Coral's user, mode exactly 0600, with one link, at most the
//     size limit;
//   - it is read through the descriptor and, only if it holds a credential,
//     removed with unlinkat in the directory it was opened from, after checking
//     that name still is that file. If it cannot be removed, the credential is
//     not used.
//
// The well-known path is reached by a walk from the root with openat, every
// step O_NOFOLLOW: /run and /run/coral owned by root and not writable by group
// or others, /run/coral/bootstrap.d owned by Coral's user, mode 0700, on a
// tmpfs. Only the name "credential" in it is read.

const (
	maxCredentialFile = 4096
	maxProofFile      = 16 << 10
)

// The well-known location (Linux; Coral Hub section 11a).
var wellKnownBootstrap = []string{"run", "coral", "bootstrap.d", "credential"}

// Test seams: the walk's root, the owner a root-owned step must have, and the
// tmpfs check.
var (
	walkRoot = "/"
	rootUID  = 0
	onTmpfs  = fdOnTmpfs
)

// ErrUnsafeCredentialFile is why a credential or proof file was refused.
var ErrUnsafeCredentialFile = errors.New("cloud: credential file refused")

func unsafeFile(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsafeCredentialFile, fmt.Sprintf(format, a...))
}

// checkedFile is a file opened and checked for reading, in its directory.
type checkedFile struct {
	dirfd, fd int
	name      string
	st        unix.Stat_t
}

func (c *checkedFile) close() {
	unix.Close(c.fd)
	unix.Close(c.dirfd)
}

// openChecked opens name in dirfd and checks it as a secret file of at most max bytes.
func openChecked(dirfd int, name string, max int64) (*checkedFile, error) {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	c := &checkedFile{dirfd: dirfd, fd: fd, name: name}
	if err := unix.Fstat(fd, &c.st); err != nil {
		unix.Close(fd)
		return nil, err
	}
	switch {
	case c.st.Mode&unix.S_IFMT != unix.S_IFREG:
		err = unsafeFile("not a regular file")
	case int(c.st.Uid) != os.Geteuid():
		err = unsafeFile("not owned by the user Coral runs as")
	case c.st.Mode&0o7777 != 0o600:
		err = unsafeFile("mode is %04o, must be 0600", c.st.Mode&0o7777)
	case c.st.Nlink != 1:
		err = unsafeFile("has %d links, must have 1", c.st.Nlink)
	case c.st.Size > max:
		err = unsafeFile("larger than %d bytes", max)
	}
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return c, nil
}

// readAndRemove reads the file, then removes that name, and refuses the
// contents if it cannot (a credential must not stay on disk).
func (c *checkedFile) readAndRemove(max int64) ([]byte, error) {
	data, err := readFD(c.fd, max)
	if err != nil {
		return nil, err
	}
	// Only a credential is removed: a file named by mistake (anything that is not
	// a Hub credential) is left exactly as it was.
	if _, err := ParseCredential(data); err != nil {
		clear(data)
		return nil, err
	}
	if err := c.remove(); err != nil {
		return nil, err
	}
	return data, nil
}

// remove unlinks the name, if it still is the file that was opened.
func (c *checkedFile) remove() error {
	var now unix.Stat_t
	if err := unix.Fstatat(c.dirfd, c.name, &now, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unsafeFile("could not be removed after reading: %v", err)
	}
	if now.Dev != c.st.Dev || now.Ino != c.st.Ino {
		return unsafeFile("was replaced while being read")
	}
	if err := unix.Unlinkat(c.dirfd, c.name, 0); err != nil {
		return unsafeFile("could not be removed after reading: %v", err)
	}
	return nil
}

// readFD reads an open file to its end, refusing more than max bytes.
func readFD(fd int, max int64) ([]byte, error) {
	buf := make([]byte, 0, 512)
	chunk := make([]byte, 4096)
	for {
		n, err := unix.Read(fd, chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			if int64(len(buf)) > max {
				return nil, unsafeFile("larger than %d bytes", max)
			}
		}
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return buf, nil
		}
	}
}

// openDir opens a directory step O_NOFOLLOW and checks it: owner (any when
// owner < 0) and, unless allowWritable, not writable by group or others.
func openDir(dirfd int, name string, owner int, allowWritable bool) (int, unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, st, err
	}
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, st, err
	}
	if owner >= 0 && int(st.Uid) != owner {
		unix.Close(fd)
		return -1, st, unsafeFile("%s is not owned by uid %d", name, owner)
	}
	if !allowWritable && st.Mode&0o022 != 0 {
		unix.Close(fd)
		return -1, st, unsafeFile("%s is writable by group or others", name)
	}
	return fd, st, nil
}

// openWellKnown walks to the well-known credential directory and returns it
// open, or ok=false when it (or the file in it) does not exist.
func openWellKnown() (dirfd int, ok bool, err error) {
	fd, _, err := openDir(unix.AT_FDCWD, walkRoot, rootUID, false)
	if err != nil {
		return -1, false, err
	}
	dirs := wellKnownBootstrap[:len(wellKnownBootstrap)-1]
	for i, step := range dirs {
		owner := rootUID
		if i == len(dirs)-1 {
			owner = os.Geteuid() // bootstrap.d belongs to Coral's user
		}
		next, st, err := openDir(fd, step, owner, false)
		unix.Close(fd)
		if errors.Is(err, unix.ENOENT) {
			return -1, false, nil
		}
		if err != nil {
			return -1, false, err
		}
		fd = next
		if i == len(dirs)-1 {
			if st.Mode&0o7777 != 0o700 {
				unix.Close(fd)
				return -1, false, unsafeFile("bootstrap.d mode is %04o, must be 0700", st.Mode&0o7777)
			}
			if !onTmpfs(fd) {
				unix.Close(fd)
				return -1, false, unsafeFile("bootstrap.d is not on a tmpfs")
			}
		}
	}
	return fd, true, nil
}

// ReadWellKnownBootstrap reads and removes the bootstrap credential at
// /run/coral/bootstrap.d/credential (Linux). found is false when there is none.
func ReadWellKnownBootstrap() (cred []byte, found bool, err error) {
	if !wellKnownSupported {
		return nil, false, nil
	}
	return readWellKnown()
}

func readWellKnown() (cred []byte, found bool, err error) {
	dirfd, ok, err := openWellKnown()
	if err != nil || !ok {
		return nil, false, err
	}
	c, err := openChecked(dirfd, wellKnownBootstrap[len(wellKnownBootstrap)-1], maxCredentialFile)
	if errors.Is(err, unix.ENOENT) {
		unix.Close(dirfd)
		return nil, false, nil
	}
	if err != nil {
		unix.Close(dirfd)
		return nil, true, err
	}
	defer c.close()
	data, err := c.readAndRemove(maxCredentialFile)
	return data, true, err
}

// RemoveWellKnownBootstrap removes the well-known credential WITHOUT reading it
// (the machine already has an identity). found reports whether there was one.
func RemoveWellKnownBootstrap() (found bool, err error) {
	if !wellKnownSupported {
		return false, nil
	}
	return removeWellKnown()
}

func removeWellKnown() (found bool, err error) {
	dirfd, ok, err := openWellKnown()
	if err != nil || !ok {
		return false, err
	}
	defer unix.Close(dirfd)
	err = unix.Unlinkat(dirfd, wellKnownBootstrap[len(wellKnownBootstrap)-1], 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return true, unsafeFile("could not be removed: %v", err)
	}
	return true, nil
}

// openCheckedPath opens an explicitly named file (--bootstrap-file,
// --ticket-file, --proof-file) for the same checks. Its directory must belong
// to Coral's user or root and not be writable by group or others.
func openCheckedPath(path string, max int64) (*checkedFile, error) {
	dir, name := filepath.Split(filepath.Clean(path))
	if dir == "" {
		dir = "."
	}
	dirfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(dirfd, &st); err != nil {
		unix.Close(dirfd)
		return nil, err
	}
	if (int(st.Uid) != os.Geteuid() && int(st.Uid) != rootUID) || st.Mode&0o022 != 0 {
		unix.Close(dirfd)
		return nil, unsafeFile("its folder must belong to you (or root) and not be writable by others")
	}
	c, err := openChecked(dirfd, name, max)
	if err != nil {
		unix.Close(dirfd)
		return nil, err
	}
	return c, nil
}

// ReadCredentialFile reads and removes a credential file named on the command
// line. notTmpfs reports that it was not on a tmpfs (the caller warns; on
// macOS there is no tmpfs).
func ReadCredentialFile(path string) (cred []byte, notTmpfs bool, err error) {
	c, err := openCheckedPath(path, maxCredentialFile)
	if err != nil {
		return nil, false, err
	}
	defer c.close()
	notTmpfs = !onTmpfs(c.fd)
	data, err := c.readAndRemove(maxCredentialFile)
	return data, notTmpfs, err
}

// HeldCredential is a credential file that was read and checked but is still on
// disk: `coral cloud connect` removes it once the Hub has answered the redeem,
// and leaves it for a retry when the Hub could not be reached (#3149 review).
type HeldCredential struct{ c *checkedFile }

// Remove unlinks the file (if it is still the one read) and releases it.
func (h *HeldCredential) Remove() error {
	defer h.c.close()
	return h.c.remove()
}

// Keep releases the file and leaves it on disk.
func (h *HeldCredential) Keep() { h.c.close() }

// HoldCredentialFile reads a credential file with the same checks as
// ReadCredentialFile, without removing it. A file that is not a Hub credential
// is refused and left as it was.
func HoldCredentialFile(path string) (cred []byte, notTmpfs bool, held *HeldCredential, err error) {
	c, err := openCheckedPath(path, maxCredentialFile)
	if err != nil {
		return nil, false, nil, err
	}
	// It must be removable once used: a credential that would stay on disk is not
	// used at all (hub.md; #3149 review). Removing it needs a writable folder.
	if err := unix.Faccessat(c.dirfd, ".", unix.W_OK, unix.AT_EACCESS); err != nil {
		c.close()
		return nil, false, nil, unsafeFile("its folder is not writable, so it could not be removed after use: move it to a folder you can write to")
	}
	notTmpfs = !onTmpfs(c.fd)
	data, err := readFD(c.fd, maxCredentialFile)
	if err == nil {
		if _, perr := ParseCredential(data); perr != nil {
			clear(data)
			err = perr
		}
	}
	if err != nil {
		c.close()
		return nil, false, nil, err
	}
	return data, notTmpfs, &HeldCredential{c: c}, nil
}

// ReadProofFile reads a proof file (the same checks, up to 16 KiB). It is not
// removed: it is not a credential, and the helper that wrote it owns it.
func ReadProofFile(path string) ([]byte, error) { return readSecretFile(path, maxProofFile) }

// readSecretFile reads a file with the checks above, without removing it.
func readSecretFile(path string, max int64) ([]byte, error) {
	c, err := openCheckedPath(path, max)
	if err != nil {
		return nil, err
	}
	defer c.close()
	return readFD(c.fd, max)
}
