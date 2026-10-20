package verify

import "os"

// The updater's other files (the staging record, the locks) are kept the way the state is: a 0700 folder, 0600 files written atomically, a
// symlink or a file others can read refused. These are the state's own helpers, for the staging code (#5292).

// EnsureDir makes dir (0700) and refuses one that is a symlink or not a folder.
func EnsureDir(dir string) error { return ensureDir(dir) }

// ReadPrivate reads path (at most 1 MiB), refusing a symlink, a non-regular file and one others can read. A missing file is fs.ErrNotExist.
func ReadPrivate(path string) ([]byte, error) { return readPrivate(path) }

// WritePrivate writes path atomically with mode 0600.
func WritePrivate(path string, data []byte) error { return writePrivate(path, data) }

// PrivateMode is the mode of the updater's files.
const PrivateMode os.FileMode = 0o600
