//go:build windows || plan9 || wasip1

package secretbox

import (
	"fmt"
	"io"
	"os"
)

// Non-Unix platforms lack a portable owner API in the standard library; mode
// and regular-file checks still reject permissive or special files.
func readExistingKeyFile(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat remote secret key file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: must be a regular file", ErrKeyPermissions)
	}
	if info.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("%w: permissions must be 0600 (got %o)", ErrKeyPermissions, info.Mode().Perm())
	}
	data, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return nil, fmt.Errorf("read remote secret key file: %w", err)
	}
	return data, nil
}
