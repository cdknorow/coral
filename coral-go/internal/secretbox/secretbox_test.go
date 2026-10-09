package secretbox

import (
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBox(t *testing.T) *Box {
	t.Helper()
	k, err := GenerateKey()
	require.NoError(t, err)
	b, err := New(k)
	require.NoError(t, err)
	return b
}

func TestRoundTrip(t *testing.T) {
	b := newBox(t)
	env, err := b.Seal("srv", []byte("super-secret-key"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(env, "v1:"))
	assert.NotContains(t, env, "super-secret-key")
	pt, err := b.Open("srv", env)
	require.NoError(t, err)
	assert.Equal(t, "super-secret-key", string(pt))

	env2, _ := b.Seal("srv", []byte("super-secret-key"))
	assert.NotEqual(t, env, env2, "fresh nonce per encryption")
}

func TestTamperDetected(t *testing.T) {
	b := newBox(t)
	env, _ := b.Seal("srv", []byte("secret"))
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(env, "v1:"))
	for _, i := range []int{0, 13, len(raw) - 1} { // nonce, ciphertext, tag
		c := append([]byte(nil), raw...)
		c[i] ^= 0x01
		_, err := b.Open("srv", "v1:"+base64.StdEncoding.EncodeToString(c))
		assert.ErrorIs(t, err, ErrDecrypt, "byte %d", i)
	}
	_, err := b.Open("srv", "v2:abc")
	assert.ErrorIs(t, err, ErrBadEnvelope)
	_, err = b.Open("srv", "v1:!!notbase64")
	assert.ErrorIs(t, err, ErrBadEnvelope)
	_, err = b.Open("srv", "v1:AAAA")
	assert.ErrorIs(t, err, ErrBadEnvelope)
}

func TestAADBinding(t *testing.T) {
	b := newBox(t)
	env, _ := b.Seal("a", []byte("secret"))
	_, err := b.Open("b", env)
	assert.ErrorIs(t, err, ErrDecrypt)
}

func TestWrongKey(t *testing.T) {
	env, _ := newBox(t).Seal("a", []byte("secret"))
	_, err := newBox(t).Open("a", env)
	assert.ErrorIs(t, err, ErrDecrypt)
}

func TestLoadKey_MissingNeverCreates(t *testing.T) {
	t.Setenv(EnvKey, "")
	dir := t.TempDir()
	_, err := LoadKey(dir)
	assert.ErrorIs(t, err, ErrKeyMissing)
	_, statErr := os.Stat(KeyFilePath(dir))
	assert.True(t, os.IsNotExist(statErr))
}

func TestCreateKeyFile_ModesAndReload(t *testing.T) {
	t.Setenv(EnvKey, "")
	dir := t.TempDir() + "/newdir"
	b, err := CreateKeyFile(dir)
	require.NoError(t, err)
	di, _ := os.Stat(dir)
	assert.Equal(t, os.FileMode(0700), di.Mode().Perm())
	fi, _ := os.Stat(KeyFilePath(dir))
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())

	env, _ := b.Seal("x", []byte("v"))
	b2, err := LoadKey(dir)
	require.NoError(t, err)
	pt, err := b2.Open("x", env)
	require.NoError(t, err)
	assert.Equal(t, "v", string(pt))

	// O_EXCL: never overwrite an existing key.
	_, err = CreateKeyFile(dir)
	assert.Error(t, err)
}

func TestLoadKey_PermissionRefusal(t *testing.T) {
	t.Setenv(EnvKey, "")
	dir := t.TempDir()
	_, err := CreateKeyFile(dir)
	require.NoError(t, err)
	for _, mode := range []os.FileMode{0640, 0604, 0644, 0666} {
		require.NoError(t, os.Chmod(KeyFilePath(dir), mode))
		_, err := LoadKey(dir)
		assert.ErrorIs(t, err, ErrKeyPermissions, "mode %o", mode)
	}
	require.NoError(t, os.Chmod(KeyFilePath(dir), 0600))
	_, err = LoadKey(dir)
	assert.NoError(t, err)
}

func TestLoadKey_RefusesSymlink(t *testing.T) {
	t.Setenv(EnvKey, "")
	dir := t.TempDir()
	real := dir + "/real"
	require.NoError(t, os.WriteFile(real, []byte(strings.Repeat("ab", 32)), 0600))
	require.NoError(t, os.Symlink(real, KeyFilePath(dir)))
	_, err := LoadKey(dir)
	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrKeyMissing))
}

func TestLoadKey_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	hexKey := strings.Repeat("0f", 32)

	t.Setenv(EnvKey, hexKey)
	b, err := LoadKey(dir)
	require.NoError(t, err)
	assert.Equal(t, SourceEnv, b.Source())
	_, statErr := os.Stat(KeyFilePath(dir))
	assert.True(t, os.IsNotExist(statErr), "env key is never written to disk")

	env, _ := b.Seal("a", []byte("v"))
	t.Setenv(EnvKey, "rawhex:"+hexKey)
	b2, err := LoadKey(dir)
	require.NoError(t, err)
	_, err = b2.Open("a", env)
	assert.NoError(t, err)

	// Env wins over a file holding a different key.
	t.Setenv(EnvKey, "")
	fb, err := CreateKeyFile(dir)
	require.NoError(t, err)
	t.Setenv(EnvKey, hexKey)
	b3, _ := LoadKey(dir)
	_, err = b3.Open("a", env)
	assert.NoError(t, err)
	envF, _ := fb.Seal("a", []byte("v"))
	_, err = b3.Open("a", envF)
	assert.ErrorIs(t, err, ErrDecrypt)

	// Malformed env fails closed and does not leak the value.
	t.Setenv(EnvKey, "not-hex-secret-value")
	_, err = LoadKey(dir)
	assert.ErrorIs(t, err, ErrKeyInvalid)
	assert.NotContains(t, err.Error(), "not-hex-secret-value")

	// Refuse to create a file when env is set.
	_, err = CreateKeyFile(t.TempDir())
	assert.Error(t, err)
}

func TestReencrypt(t *testing.T) {
	a, b := newBox(t), newBox(t)
	e1, _ := a.Seal("one", []byte("k1"))
	e2, _ := a.Seal("two", []byte("k2"))
	out, err := Reencrypt(a, b, []Row{{"one", e1}, {"two", e2}})
	require.NoError(t, err)
	pt, err := b.Open("two", out[1].Envelope)
	require.NoError(t, err)
	assert.Equal(t, "k2", string(pt))

	// A bad row fails the whole batch.
	_, err = Reencrypt(a, b, []Row{{"one", e1}, {"two", "v1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}})
	assert.Error(t, err)
}
