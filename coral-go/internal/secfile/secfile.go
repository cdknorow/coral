// Package secfile reads and creates files that hold a secret (a master key) so that only this
// user can read them (#3693). On Unix the callers check the mode and the owner themselves; this
// package is what Windows needs instead, where FileInfo's mode says nothing about access (a
// writable file is always 0666) and the access list (DACL) decides.
package secfile

import "errors"

var (
	// ErrNotPrivate: the file can't be trusted as it is (not a regular file, a second name, or
	// its access list couldn't be read). The message says which.
	ErrNotPrivate = errors.New("the secret file is not private")
	// ErrExposed: another account could read the file, or it belongs to someone else, so the
	// secret it holds must be treated as known (coral-hub Security's ruling on #3693).
	ErrExposed = errors.New("the key file was readable by other accounts; stored secrets must be re-created")
)
