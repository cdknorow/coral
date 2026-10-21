package auth

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/subgentic/coral/internal/secfile"
)

// SessionsRevokedFile marks a data folder whose password `coral reset-password` changed while
// Coral was stopped: the next start ends every browser session (#3949).
const SessionsRevokedFile = "sessions_revoked"

// ApplyRevokeMarker ends every browser session if an offline reset left the marker, then removes
// it. Call it once the session backend is set.
func (ks *KeyStore) ApplyRevokeMarker() {
	marker := filepath.Join(ks.dataDir(), SessionsRevokedFile)
	if _, err := os.Lstat(marker); err != nil {
		return
	}
	n, err := ks.RevokeAllSessions()
	if err != nil {
		log.Printf("[auth] the password was reset while Coral was stopped, but the sessions couldn't be ended: %v", err)
		return
	}
	_ = os.Remove(marker)
	log.Printf("[auth] the password was reset while Coral was stopped: %d browser session(s) ended", n)
}

// ApplyInitialPasswordFile sets the first password from a file (CORAL_INITIAL_PASSWORD_FILE, for
// a scripted install; Security 1: never an environment variable, which the same user can read
// back from the process). The file must be a regular file private to this user; it is used only
// when no password is set, and deleted once read. Nothing about the password is logged.
func (ks *KeyStore) ApplyInitialPasswordFile(path string) {
	if ks.PasswordSet() {
		log.Printf("[auth] CORAL_INITIAL_PASSWORD_FILE ignored: a password is already set (it was not read)")
		return
	}
	pw, err := readPrivateSecret(path)
	if err != nil {
		log.Printf("[auth] CORAL_INITIAL_PASSWORD_FILE not used: %v", err)
		return
	}
	phc, err := HashPassword(pw)
	if err != nil {
		log.Printf("[auth] CORAL_INITIAL_PASSWORD_FILE not used: %v", err)
		return
	}
	if err := WritePasswordHash(ks.dataDir(), phc); err != nil {
		log.Printf("[auth] CORAL_INITIAL_PASSWORD_FILE: the password couldn't be saved: %v", err)
		return
	}
	if err := os.Remove(path); err != nil {
		log.Printf("[auth] a password was set from CORAL_INITIAL_PASSWORD_FILE, but the file couldn't be deleted: delete it by hand (%v)", err)
		return
	}
	log.Printf("[auth] a password was set from CORAL_INITIAL_PASSWORD_FILE, and the file was deleted")
}

// readPrivateSecret reads a small secret file: a regular file (not a link), private to this
// user (no group or other bits; on Windows an owner-only access list), at most
// MaxPasswordBytes plus a line ending. One trailing newline (\n or \r\n) is dropped.
func readPrivateSecret(path string) (string, error) {
	li, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !li.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && li.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s can be read by other users (mode %o): make it 0600", path, li.Mode().Perm())
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !os.SameFile(li, fi) {
		return "", fmt.Errorf("%s changed while it was opened", path)
	}
	if err := secfile.CheckOpen(f); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxPasswordBytes+3))
	if err != nil {
		return "", err
	}
	if len(b) > MaxPasswordBytes+2 {
		return "", ErrPasswordTooLong
	}
	s := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if s == "" {
		return "", ErrPasswordEmpty
	}
	return s, nil
}
