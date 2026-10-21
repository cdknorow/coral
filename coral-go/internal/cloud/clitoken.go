package cloud

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/secfile"
)

// The command-line token (#3149): a same-user proof for the local cloud write
// routes. Loopback requests skip API-key auth, so without it any local user (or
// a pod sidecar) could enroll this machine into its own Coral Hub account. The
// server writes a fresh random token, mode 0600, in its data folder at startup;
// `coral cloud` reads it (only Coral's user can) and proves it knows it without
// ever sending it (cliauth.go). The dashboard keeps its own browser rules.

const (
	CLITokenFile = "cli-token"
	maxCLIToken  = 256
)

// WriteCLIToken writes a new token to dir/cli-token (0600, replaced atomically)
// and returns it.
func WriteCLIToken(dir string) (string, error) {
	// No data directory (a test server, a bare config) means no command-line path:
	// never fall back to the working directory, which put a live token in the
	// source tree (#3149 review).
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("no data directory for the command-line token")
	}
	if err := secureTokenDir(dir); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	f, err := os.CreateTemp(dir, ".cli-token-*") // created 0600
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op once renamed
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return "", err
	}
	if err := secfile.Protect(tmp); err != nil { // Windows ignores the mode (#3905)
		f.Close()
		return "", err
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(dir, CLITokenFile)); err != nil {
		return "", err
	}
	return tok, nil
}

// ReadCLIToken reads the token from dir/cli-token, with the same file checks
// as a credential file (owned by this user, mode 0600, one link).
func ReadCLIToken(dir string) (string, error) {
	b, err := readSecretFile(filepath.Join(dir, CLITokenFile), maxCLIToken)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errors.New("cloud: the command-line token is empty")
	}
	return tok, nil
}

// RemoveCLIToken removes dir/cli-token at a graceful shutdown, if it still holds
// tok: a newer server's token (another start on the same data folder) is left.
func RemoveCLIToken(dir, tok string) error {
	if dir == "" || tok == "" {
		return nil
	}
	cur, err := ReadCLIToken(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !CLITokenMatches(tok, cur) {
		return nil
	}
	if err := os.Remove(filepath.Join(dir, CLITokenFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// CLITokenMatches compares a presented token with the server's, in constant
// time. An empty token never matches.
func CLITokenMatches(want, got string) bool {
	return want != "" && got != "" && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
