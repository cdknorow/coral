//go:build windows

package dbcrypt

import (
	"errors"
	"fmt"
	"os"

	"github.com/cdknorow/coral/internal/secfile"
)

// The database key file is checked by its access list on Windows, where the mode is always
// 0666 for a writable file (#3693): see secfile.ReadPrivate.
func readExistingKey(name string) ([]byte, error) {
	data, err := secfile.ReadPrivate(name, 4096)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("database key file: %w", err)
	}
	return data, nil
}
