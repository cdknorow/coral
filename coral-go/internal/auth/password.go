package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

// The browser password (#3949, operator decision): any non-empty password, kept only as an
// argon2id hash in <data>/password (a PHC string, 0600 / owner-only), never logged.

// PasswordFile is the password hash's name in Coral's data folder.
const PasswordFile = "password"

// MaxPasswordBytes bounds a password (after NFKC), so a huge one can't make the hash costly.
const MaxPasswordBytes = 4096

// The argon2id cost of a new hash. A stored hash keeps its own parameters, read back from it.
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 1
	argonSaltLen   = 16
	argonKeyLen    = 32
)

var (
	// ErrPasswordEmpty: an empty password is refused.
	ErrPasswordEmpty = errors.New("the password is empty")
	// ErrPasswordTooLong: over MaxPasswordBytes.
	ErrPasswordTooLong = fmt.Errorf("the password is longer than %d bytes", MaxPasswordBytes)
	// ErrPasswordExists: CreatePasswordHash found one already set.
	ErrPasswordExists = errors.New("a password is already set")
	// ErrNoPassword: none is set.
	ErrNoPassword = errors.New("no password is set")
)

// normalizePassword is the password as hashed: NFKC, nothing else (no trimming, no case).
func normalizePassword(pw string) ([]byte, error) {
	if pw == "" {
		return nil, ErrPasswordEmpty
	}
	b := norm.NFKC.Bytes([]byte(pw))
	if len(b) > MaxPasswordBytes {
		return nil, ErrPasswordTooLong
	}
	return b, nil
}

// HashPassword returns the PHC string for pw: $argon2id$v=19$m=..,t=..,p=..$salt$hash.
func HashPassword(pw string) (string, error) {
	b, err := normalizePassword(pw)
	if err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey(b, salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	key     []byte
}

// parsePHC reads a hash HashPassword wrote. The cost is bounded, so a tampered file can't make
// every sign-in take gigabytes.
func parsePHC(phc string) (argonParams, error) {
	var p argonParams
	parts := strings.Split(strings.TrimSpace(phc), "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, errors.New("the password file is not an argon2id hash")
	}
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if !ok || err != nil {
			return p, errors.New("the password file's parameters can't be read")
		}
		switch k {
		case "m":
			p.memory = uint32(n)
		case "t":
			p.time = uint32(n)
		case "p":
			if n > 255 {
				return p, errors.New("the password file's parameters are out of range")
			}
			p.threads = uint8(n)
		}
	}
	if p.memory < 8*1024 || p.memory > 1024*1024 || p.time < 1 || p.time > 20 || p.threads < 1 || p.threads > 16 {
		return p, errors.New("the password file's parameters are out of range")
	}
	enc := base64.RawStdEncoding
	var err error
	if p.salt, err = enc.DecodeString(parts[4]); err != nil || len(p.salt) < 8 {
		return p, errors.New("the password file's salt can't be read")
	}
	if p.key, err = enc.DecodeString(parts[5]); err != nil || len(p.key) < 16 || len(p.key) > 64 {
		return p, errors.New("the password file's hash can't be read")
	}
	return p, nil
}

var (
	dummyOnce sync.Once
	dummyPHC  string
)

// dummyHash is a hash of a random password with the current parameters: checking against it
// costs what a real check costs, so "no password set" answers like a wrong one.
func dummyHash() string {
	dummyOnce.Do(func() {
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		dummyPHC, _ = HashPassword(base64.RawURLEncoding.EncodeToString(b))
	})
	return dummyPHC
}

// VerifyPassword reports whether pw matches phc, in constant time over the hash output. An
// empty phc (no password set) is checked against a dummy hash and is never a match.
func VerifyPassword(phc, pw string) bool {
	known := phc != ""
	if !known {
		phc = dummyHash()
	}
	p, err := parsePHC(phc)
	if err != nil {
		p, _ = parsePHC(dummyHash())
		known = false
	}
	b, err := normalizePassword(pw)
	if err != nil {
		b = []byte{0} // still costs one hash
		known = false
	}
	got := argon2.IDKey(b, p.salt, p.time, p.memory, p.threads, uint32(len(p.key)))
	return subtle.ConstantTimeCompare(got, p.key) == 1 && known
}

// ReadPasswordHash reads <dir>/password with the API key file's checks (a regular, private
// file, no link). "" and fs.ErrNotExist when none is set.
func ReadPasswordHash(dir string) (string, error) {
	b, err := readKeyFile(filepath.Join(dir, PasswordFile))
	if err != nil {
		return "", err
	}
	phc := strings.TrimSpace(string(b))
	if phc == "" {
		return "", fs.ErrNotExist
	}
	if _, err := parsePHC(phc); err != nil {
		return "", err
	}
	return phc, nil
}

// WritePasswordHash replaces <dir>/password atomically (private temp file, synced, renamed).
func WritePasswordHash(dir, phc string) error {
	return writeKeyFile(filepath.Join(dir, PasswordFile), phc)
}

// CreatePasswordHash writes <dir>/password only if none exists, atomically: a private temp file,
// synced, then hard-linked to the name (which fails if the name exists), so of two concurrent
// creations exactly one wins and the other gets ErrPasswordExists.
func CreatePasswordHash(dir, phc string) error {
	path := filepath.Join(dir, PasswordFile)
	tmpDir := filepath.Join(dir, ".password-new")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(tmpDir, "p-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	f.Close()
	defer os.Remove(tmpDir) // once empty
	defer os.Remove(tmp)
	if err := writeKeyFile(tmp, phc); err != nil {
		return err
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrPasswordExists
		}
		return err
	}
	return nil
}

// RemovePasswordHash deletes <dir>/password; none is not an error.
func RemovePasswordHash(dir string) error {
	err := os.Remove(filepath.Join(dir, PasswordFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
