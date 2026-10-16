package safefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3671 (Security's ruling): on Windows the root refuses a path with a device-name component
// (CheckName), for every write and every read, whatever the Windows build does with the name.
// The rule is run here on every system; elsewhere it is off, and "aux.go" is an ordinary file.
func TestRootRefusesDeviceNamesWhenTheRuleIsOn(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRoot(dir)
	require.NoError(t, err)
	defer r.Close()

	old := deviceNamesRefused
	t.Cleanup(func() { deviceNamesRefused = old })

	deviceNamesRefused = false
	require.NoError(t, r.WriteFile("aux.go", []byte("package x"), 0o600), "off (Unix): an ordinary name")
	data, err := r.ReadFile("aux.go", 1<<10)
	require.NoError(t, err)
	assert.Equal(t, "package x", string(data))

	deviceNamesRefused = true
	for _, name := range []string{"nul.json", "CON", "com1.md", "NuL.Json", "aux.go", "lpt9", "name.", "name "} {
		f, err := r.CreateNew(name, 0o600)
		if err == nil {
			f.Close()
		}
		assert.ErrorIs(t, err, ErrReservedName, "CreateNew %q", name)
		assert.ErrorIs(t, r.WriteFile(name, []byte("x"), 0o600), ErrReservedName, "WriteFile %q", name)
		_, _, err = r.Open(name)
		assert.ErrorIs(t, err, ErrReservedName, "Open %q", name)
	}
	assert.ErrorIs(t, r.MkdirAll(filepath.Join("ok", "nul.d"), 0o700), ErrReservedName, "a middle folder")
	assert.ErrorIs(t, r.WriteFile(filepath.Join("ok", "com1.d", "x.txt"), []byte("x"), 0o600), ErrReservedName)
	assert.ErrorIs(t, r.WriteFile(`ok\CON\x.txt`, []byte("x"), 0o600), ErrReservedName, "either separator")
	_, err = r.ReadFile("aux.go", 1<<10)
	assert.ErrorIs(t, err, ErrReservedName, "reads too")

	// Ordinary names still work with the rule on.
	require.NoError(t, r.MkdirAll(filepath.Join("ok", "console.d"), 0o700))
	require.NoError(t, r.WriteFile(filepath.Join("ok", "console.d", "com10.md"), []byte("y"), 0o600))
	data, err = r.ReadFile(filepath.Join(".", "ok", "console.d", "com10.md"), 1<<10)
	require.NoError(t, err)
	assert.Equal(t, "y", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "aux.go (made with the rule off) and ok/")
}
