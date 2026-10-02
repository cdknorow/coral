//go:build windows || plan9 || wasip1

package dbcrypt

import (
	"fmt"
	"io"
	"os"
)

// Windows and other non-Unix platforms do not expose a portable owner API in
// the Go standard library. ACL enforcement remains platform-specific; mode
// checks and regular-file checks still prevent permissive or special files.
func readExistingKey(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat database key file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("database key file must be a regular file")
	}
	if info.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("database key file permissions must be 0600 (got %o)", info.Mode().Perm())
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read database key file: %w", err)
	}
	return data, nil
}
