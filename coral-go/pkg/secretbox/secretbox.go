// Package secretbox provides field-level AES-256-GCM encryption for small
// secrets stored in the database (remote server API keys). It is independent of
// the optional SQLCipher mode in internal/dbcrypt and works in every build.
//
// Envelope format: "v1:" + base64(nonce || ciphertext || tag). The caller
// supplies additional authenticated data (the row id) so a ciphertext copied to
// another row fails to decrypt.
//
// The master key comes from CORAL_SECRET_KEY (64 hex characters or "rawhex:" +
// 64 hex) or, if unset, from <dir>/.remote_secret_key (0600). It is never
// derived from the machine fingerprint. Key material is never logged and never
// included in error messages.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvKey is the environment variable that overrides the key file.
	EnvKey = "CORAL_SECRET_KEY"
	// KeyFileName is the master key file inside the Coral data directory.
	KeyFileName = ".remote_secret_key"
	// EnvelopePrefix marks the current envelope version.
	EnvelopePrefix = "v1:"
	keyLen         = 32
)

var (
	// ErrKeyMissing means no master key is available (no env var, no file).
	ErrKeyMissing = errors.New("remote secret key is missing")
	// ErrKeyPermissions means the key file exists but is unsafe to use.
	ErrKeyPermissions = errors.New("remote secret key file has unsafe permissions or type")
	// ErrKeyInvalid means the configured key is malformed.
	ErrKeyInvalid = errors.New("remote secret key is malformed")
	// ErrDecrypt means authentication failed: wrong key, tampering, or wrong AAD.
	ErrDecrypt = errors.New("cannot decrypt secret")
	// ErrBadEnvelope means the stored value is not a recognised envelope.
	ErrBadEnvelope = errors.New("unrecognised secret envelope")
)

// Source says where a master key came from.
type Source int

const (
	SourceFile Source = iota
	SourceEnv
)

// Box encrypts and decrypts with one master key.
type Box struct {
	aead   cipher.AEAD
	source Source
}

// New builds a Box from a raw 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != keyLen {
		return nil, ErrKeyInvalid
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrKeyInvalid
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrKeyInvalid
	}
	return &Box{aead: aead}, nil
}

// Source reports where the key was loaded from (file by default).
func (b *Box) Source() Source { return b.source }

// GenerateKey returns 32 random bytes.
func GenerateKey() ([]byte, error) {
	k := make([]byte, keyLen)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return k, nil
}

// ParseKey parses 64 hex characters, optionally prefixed with "rawhex:".
// Error messages never contain the input.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "rawhex:"))
	if len(s) != keyLen*2 {
		return nil, fmt.Errorf("%w: expected 64 hex characters", ErrKeyInvalid)
	}
	k, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: not valid hex", ErrKeyInvalid)
	}
	return k, nil
}

// Seal encrypts plaintext bound to aad and returns the v1 envelope.
func (b *Box) Seal(aad string, plaintext []byte) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	out := b.aead.Seal(nonce, nonce, plaintext, []byte(aad))
	return EnvelopePrefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a v1 envelope. The caller should Zero the result when done.
func (b *Box) Open(aad, envelope string) ([]byte, error) {
	if !strings.HasPrefix(envelope, EnvelopePrefix) {
		return nil, ErrBadEnvelope
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(envelope, EnvelopePrefix))
	if err != nil {
		return nil, ErrBadEnvelope
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns+b.aead.Overhead() {
		return nil, ErrBadEnvelope
	}
	pt, err := b.aead.Open(nil, raw[:ns], raw[ns:], []byte(aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// Zero overwrites a byte slice.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// KeyFilePath returns the master key path inside dir.
func KeyFilePath(dir string) string { return filepath.Join(dir, KeyFileName) }

// LoadKey returns a Box for the configured master key without ever creating
// one. ErrKeyMissing is returned when neither the env var nor the file exists.
func LoadKey(dir string) (*Box, error) {
	if v := os.Getenv(EnvKey); v != "" {
		k, err := ParseKey(v)
		if err != nil {
			return nil, err
		}
		defer Zero(k)
		b, err := New(k)
		if err != nil {
			return nil, err
		}
		b.source = SourceEnv
		return b, nil
	}
	data, err := readExistingKeyFile(KeyFilePath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrKeyMissing
		}
		return nil, err
	}
	defer Zero(data)
	k, err := ParseKey(string(data))
	if err != nil {
		return nil, err
	}
	defer Zero(k)
	return New(k)
}

// CreateKeyFile generates a fresh master key and writes it atomically with
// O_EXCL (mode 0600) in a 0700 directory (created if absent). It fails if the
// file already exists. Callers must only do this when no encrypted rows exist,
// or when the user has explicitly chosen to re-enter secrets.
func CreateKeyFile(dir string) (*Box, error) {
	if os.Getenv(EnvKey) != "" {
		return nil, fmt.Errorf("%s is set; refusing to create a key file", EnvKey)
	}
	k, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	defer Zero(k)
	if err := writeNewKeyFile(KeyFilePath(dir), dir, k); err != nil {
		return nil, err
	}
	return New(k)
}

func writeNewKeyFile(path, dir string, key []byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create remote secret key file: %w", err)
	}
	hexKey := []byte(hex.EncodeToString(key) + "\n")
	defer Zero(hexKey)
	if _, err := f.Write(hexKey); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("write remote secret key file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("sync remote secret key file: %w", err)
	}
	return f.Close()
}

// Row is one encrypted value with its AAD, for rotation.
type Row struct {
	AAD      string
	Envelope string
}

// Reencrypt decrypts every row with old and encrypts it with next, returning
// the new envelopes in the same order. It performs no I/O so callers can apply
// the result inside a single database transaction. Any failure returns an
// error and no partial result.
func Reencrypt(old, next *Box, rows []Row) ([]Row, error) {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		pt, err := old.Open(r.AAD, r.Envelope)
		if err != nil {
			return nil, fmt.Errorf("row %q: %w", r.AAD, err)
		}
		env, err := next.Seal(r.AAD, pt)
		Zero(pt)
		if err != nil {
			return nil, err
		}
		out = append(out, Row{AAD: r.AAD, Envelope: env})
	}
	return out, nil
}

// StageRotatedKey generates a new master key, writes it to a sibling temp
// file (0600, O_EXCL) and returns the Box plus commit/abort functions. Commit
// atomically renames the temp file over the real key file; abort removes it.
func StageRotatedKey(dir string) (next *Box, commit func() error, abort func(), err error) {
	if os.Getenv(EnvKey) != "" {
		return nil, nil, nil, fmt.Errorf("%s is set; rotate the injected key externally", EnvKey)
	}
	k, err := GenerateKey()
	if err != nil {
		return nil, nil, nil, err
	}
	defer Zero(k)
	tmp := KeyFilePath(dir) + ".new"
	os.Remove(tmp) // stale leftover from a crashed rotation
	if err := writeNewKeyFile(tmp, dir, k); err != nil {
		return nil, nil, nil, err
	}
	box, err := New(k)
	if err != nil {
		os.Remove(tmp)
		return nil, nil, nil, err
	}
	commit = func() error { return os.Rename(tmp, KeyFilePath(dir)) }
	abort = func() { os.Remove(tmp) }
	return box, commit, abort, nil
}
