//go:build windows

package secretbox

import (
	"errors"
	"fmt"
	"os"

	"github.com/cdknorow/coral/internal/secfile"
)

// On Windows the file's mode says nothing (a writable file is always 0666), so the key file is
// checked by its access list instead: a regular file with one name, owned by this user (or
// Administrators), readable only by this user, SYSTEM and Administrators (#3693). Before this, a
// mode check that Windows can never pass refused every existing key file.
func readExistingKeyFile(name string) ([]byte, error) {
	data, err := secfile.ReadPrivate(name, 4096)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if errors.Is(err, secfile.ErrExposed) {
			// Until there is a re-key action (#3698), say how to recover by hand.
			return nil, fmt.Errorf("%w: %w. To recover: stop Coral, delete %s, start Coral, then connect to Coral Hub again and re-enter the API keys of your remote servers (the old ones can't be read without that key)",
				ErrKeyPermissions, err, name)
		}
		return nil, fmt.Errorf("%w: %w", ErrKeyPermissions, err)
	}
	return data, nil
}
