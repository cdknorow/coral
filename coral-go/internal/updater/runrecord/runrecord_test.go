package runrecord

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteKeepsTheCommandLineOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Write(dir, []string{"/opt/coral/coral", "--port", "8420"}, nil, "/srv"))
	b, err := os.ReadFile(filepath.Join(dir, "updates", File))
	require.NoError(t, err)
	var r Record
	require.NoError(t, json.Unmarshal(b, &r))
	assert.Equal(t, Record{Argv: []string{"/opt/coral/coral", "--port", "8420"}, Cwd: "/srv"}, r)
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "updates", File))
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
		di, _ := os.Stat(filepath.Join(dir, "updates"))
		assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	}
	// Written again: replaced, nothing left beside it.
	require.NoError(t, Write(dir, []string{"/opt/coral/coral"}, nil, "/"))
	left, _ := os.ReadDir(filepath.Join(dir, "updates"))
	assert.Len(t, left, 1)
	assert.Error(t, Write(dir, nil, nil, "/"))
	assert.Error(t, Write(dir, []string{strings.Repeat("a", 70<<10)}, nil, "/"))
}

func TestAFlagThatCarriesASecretIsNotRecorded(t *testing.T) {
	for _, argv := range [][]string{
		{"/opt/coral/coral", "--token", "abc"}, {"/opt/coral/coral", "--api-key=abc"}, {"/opt/coral/coral", "--Password=x"}, {"/opt/coral/coral", "-secret"},
		{"/opt/coral/coral", "--cloud-private-key", "k"}, {"/opt/coral/coral", "--port", "1", "--client_secret=z"},
	} {
		dir := t.TempDir()
		assert.ErrorIs(t, Write(dir, argv, nil, "/"), ErrSecretFlag, "%v", argv)
		_, err := os.Stat(filepath.Join(dir, "updates", File))
		assert.ErrorIs(t, err, os.ErrNotExist, "nothing was written")
	}
	// Ordinary flags, and a value that merely looks like one, are fine.
	require.NoError(t, Write(t.TempDir(), []string{"/opt/coral/coral", "--host", "0.0.0.0", "--port", "8420", "--cloud-url", "off", "--home", "/data/token-store"}, nil, "/"))
}

// #5410: only the named variables are recorded: not a token, a key, an agent's variable or anything the allowlist does not name.
func TestOnlyTheAllowlistedEnvironmentIsRecorded(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin:/bin", "HOME=/home/u", "CORAL_DATA_DIR=/srv/coral", "CORAL_STRICT_LOCAL_AUTH=1",
		"CORAL_SESSION_TOKEN=secret-session", "CORAL_API_KEY=secret-key", "ANTHROPIC_API_KEY=sk-ant-x", "GITHUB_TOKEN=ghp_x", "AWS_SECRET_ACCESS_KEY=x",
		"CORAL_SESSION_NAME=agent-1", "CORAL_CLOUD_URL=off", "LANG=C.UTF-8", "EMPTY=", "CORAL_HOST=", "NOEQUALS", "PATH=/second/path",
		"TZ=" + strings.Repeat("x", 5000), "TMPDIR=/tmp\nINJECTED=1",
	}
	got := AllowedEnv(environ)
	assert.Equal(t, []string{"PATH=/usr/bin:/bin", "HOME=/home/u", "CORAL_DATA_DIR=/srv/coral", "CORAL_STRICT_LOCAL_AUTH=1", "CORAL_CLOUD_URL=off", "LANG=C.UTF-8"}, got,
		"in order, each name once, no empty, over-long or multi-line value")
	dir := t.TempDir()
	require.NoError(t, Write(dir, []string{"/opt/coral/coral"}, environ, "/"))
	b, err := os.ReadFile(filepath.Join(dir, "updates", File))
	require.NoError(t, err)
	for _, bad := range []string{"secret", "sk-ant", "ghp_", "AWS_", "agent-1", "SESSION"} {
		assert.NotContains(t, string(b), bad)
	}
	var r Record
	require.NoError(t, json.Unmarshal(b, &r))
	assert.Equal(t, got, r.Env)
	assert.Nil(t, AllowedEnv(nil))
}
