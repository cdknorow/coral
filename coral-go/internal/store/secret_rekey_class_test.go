package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/secfile"
	"github.com/subgentic/coral/pkg/secretbox"
)

// #3698 (Security's SHOULD): only a refusal of the key itself offers a re-key; an error reaching
// the file is "unreadable". The errors as secretbox builds them on each system.
func TestKeyRefusedTellsARefusalFromAReadError(t *testing.T) {
	perm := secretbox.ErrKeyPermissions
	pathErr := &os.PathError{Op: "open", Path: "k", Err: syscall.EACCES}
	for name, c := range map[string]struct {
		err     error
		refused bool
	}{
		"exposed (Windows)":             {fmt.Errorf("%w: %w", perm, fmt.Errorf("%w (k grants access to S-1-5-32-545)", secfile.ErrExposed)), true},
		"not private (Windows)":         {fmt.Errorf("%w: %w", perm, fmt.Errorf("%w: k has another name", secfile.ErrNotPrivate)), true},
		"wrong mode (Unix)":             {fmt.Errorf("%w: permissions must be 0600 (got 644)", perm), true},
		"other owner (Unix)":            {fmt.Errorf("%w: must be owned by the current user", perm), true},
		"malformed key":                 {fmt.Errorf("%w: expected 64 hex characters", secretbox.ErrKeyInvalid), true},
		"open denied (Windows)":         {fmt.Errorf("%w: %w", perm, pathErr), false},
		"open denied (Unix)":            {pathErr, false},
		"I/O error":                     {fmt.Errorf("read remote secret key file: %w", io.ErrUnexpectedEOF), false},
		"an errno wrapped in a refusal": {fmt.Errorf("%w: %w", perm, syscall.EIO), false},
	} {
		assert.Equal(t, c.refused, keyRefused(c.err), name)
	}
	assert.False(t, keyRefused(errors.New("anything else")))
}
