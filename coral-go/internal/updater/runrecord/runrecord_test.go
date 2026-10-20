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

// #5414: a value on the allowlist that is a URL with a password in it is not recorded, whatever variable it is in.
func TestAURLWithCredentialsIsNotRecordedEvenInAnAllowedVariable(t *testing.T) {
	got := AllowedEnv([]string{
		"CORAL_CLOUD_URL=https://user:s3cret@app.example.dev/path", "CORAL_HUB=http://token@h", "CORAL_HOST=a b://x@y",
		"CORAL_CLOUD_URL=https://app.example.dev", "TZ=Europe/Paris", "PATH=/a:/b@c", "CORAL_DATA_DIR=/srv/x@y",
	})
	assert.Equal(t, []string{"CORAL_CLOUD_URL=https://app.example.dev", "TZ=Europe/Paris", "PATH=/a:/b@c", "CORAL_DATA_DIR=/srv/x@y"}, got,
		"credentials in a URL are dropped; an @ in a path is not a URL")
	assert.True(t, hasUserinfo("https://a:b@c"))
	assert.True(t, hasUserinfo("x https://ok.example/ ftp://u@h"))
	assert.False(t, hasUserinfo("https://h/path@x"))
	assert.False(t, hasUserinfo("mail me@h"))
}

// #5414: the updates folder is not a link, and one left looser than owner-only is tightened before the record goes in it.
func TestWriteRefusesALinkedUpdatesFolderAndTightensALooseOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes and unprivileged links are not a Windows thing")
	}
	data := t.TempDir()
	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(data, "updates")))
	assert.Error(t, Write(data, []string{"/opt/coral/coral"}, nil, "/"), "a link is not the updates folder")
	entries, _ := os.ReadDir(elsewhere)
	assert.Empty(t, entries, "nothing was written where the link leads")

	loose := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(loose, "updates"), 0o755))
	require.NoError(t, os.Chmod(filepath.Join(loose, "updates"), 0o755))
	require.NoError(t, Write(loose, []string{"/opt/coral/coral"}, nil, "/"))
	fi, err := os.Stat(filepath.Join(loose, "updates"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())

	// An argument that is a URL with a password in it is not recorded, whatever the flag is called.
	assert.ErrorIs(t, Write(t.TempDir(), []string{"/opt/coral/coral", "--cloud-url", "https://u:pw@hub.example"}, nil, "/"), ErrSecretFlag)
	assert.NoError(t, Write(t.TempDir(), []string{"/opt/coral/coral", "--cloud-url", "https://hub.example"}, nil, "/"))
}

// #5419: the server and the apply ask the same questions of the record.
func TestRestartableIsTheRecordBeingOneStartWouldAccept(t *testing.T) {
	if runtime.GOOS != "linux" {
		assert.NoError(t, Restartable(t.TempDir(), "/x/coral"), "off Linux nothing is recorded: always restartable")
		return
	}
	t.Setenv("INVOCATION_ID", "")
	install := t.TempDir()
	install, err := filepath.EvalSymlinks(install)
	require.NoError(t, err)
	exe := filepath.Join(install, "coral")
	require.NoError(t, os.WriteFile(exe, []byte("x"), 0o755))
	data := t.TempDir()
	assert.Error(t, Restartable(data, exe), "no record")
	require.NoError(t, Write(data, []string{exe, "--port", "8420"}, []string{"PATH=/usr/bin"}, "/"))
	assert.NoError(t, Restartable(data, exe))
	assert.Error(t, Restartable(data, filepath.Join(t.TempDir(), "coral")), "a record for another install folder")
	require.NoError(t, Write(data, []string{exe}, []string{"ANTHROPIC_API_KEY=k"}, "/"))
	assert.Error(t, Restartable(data, exe), "no allowlisted environment")
	t.Setenv("INVOCATION_ID", "abc")
	assert.NoError(t, Restartable(t.TempDir(), exe), "under a service manager no record is needed")
	t.Setenv("INVOCATION_ID", "")
	require.NoError(t, Write(data, []string{exe}, []string{"PATH=/usr/bin"}, "/"))
	require.NoError(t, os.Chmod(filepath.Join(data, "updates", File), 0o644))
	assert.Error(t, Restartable(data, exe), "a record others can read is not trusted")
}
