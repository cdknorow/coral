//go:build !linux && !darwin

package cloud

import (
	"errors"
	"io"
	"os"
)

// Credential files are read with descriptor-level checks on Linux and macOS.
// Elsewhere (Windows) the owner and access checks need the security descriptor:
// not supported yet, so a credential file is refused rather than read unchecked.

const (
	maxCredentialFile = 4096
	maxProofFile      = 16 << 10
)

// ErrUnsafeCredentialFile is why a credential or proof file was refused.
var ErrUnsafeCredentialFile = errors.New("cloud: credential files are not supported on this platform yet")

func ReadWellKnownBootstrap() ([]byte, bool, error)   { return nil, false, nil }
func RemoveWellKnownBootstrap() (bool, error)         { return false, nil }
func ReadCredentialFile(string) ([]byte, bool, error) { return nil, false, ErrUnsafeCredentialFile }
func ReadProofFile(string) ([]byte, error)            { return nil, ErrUnsafeCredentialFile }

// HeldCredential is not supported on this platform (see above).
type HeldCredential struct{}

func (*HeldCredential) Remove() error { return ErrUnsafeCredentialFile }
func (*HeldCredential) Keep()         {}
func HoldCredentialFile(string) ([]byte, bool, *HeldCredential, error) {
	return nil, false, nil, ErrUnsafeCredentialFile
}

// readSecretFile reads a file of at most max bytes. Without the descriptor
// checks, it relies on the folder's own access control (the user's profile).
func readSecretFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrUnsafeCredentialFile
	}
	return b, nil
}

// secureTokenDir: nothing to do where the token's folder isn't checked.
func secureTokenDir(string) error { return nil }
