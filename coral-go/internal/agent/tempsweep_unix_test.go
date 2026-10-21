//go:build !windows

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	deadSID    = "11111111-1111-4111-8111-111111111111"
	liveSID    = "22222222-2222-4222-8222-222222222222"
	runningSID = "33333333-3333-4333-8333-333333333333"
	pathSID    = "44444444-4444-4444-8444-444444444444"
)

func writeAged(t *testing.T, path string, age time.Duration) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	old := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, old, old))
}

// #4381 (Security's #4371 SHOULD 1): the startup sweep removes a launch temp file only when every
// rule holds; a live session's file (in this Coral's database, or named by any running process,
// by session id or by path) always survives, and so does anything that is not exactly a launch
// temp file.
func TestSweepStaleTempFiles(t *testing.T) {
	dir := t.TempDir()
	day := 25 * time.Hour
	gone := map[string]string{
		"dead prompt":       filepath.Join(dir, "coral_prompt_"+deadSID+"_123.txt"),
		"dead settings":     filepath.Join(dir, "coral_settings_"+deadSID+"_456.json"),
		"dead instructions": filepath.Join(dir, "coral_codex_instructions_"+deadSID+"_789.md"),
	}
	kept := map[string]string{
		"live in the database":      filepath.Join(dir, "coral_settings_"+liveSID+"_1.json"),
		"session id in a process":   filepath.Join(dir, "coral_prompt_"+runningSID+"_2.txt"),
		"path in a process":         filepath.Join(dir, "coral_settings_"+pathSID+"_3.json"),
		"too new":                   filepath.Join(dir, "coral_prompt_"+deadSID+"_4.txt"),
		"no random suffix":          filepath.Join(dir, "coral_settings_"+deadSID+".json"),
		"another kind":              filepath.Join(dir, "coral_notes_"+deadSID+"_5.txt"),
		"another extension":         filepath.Join(dir, "coral_prompt_"+deadSID+"_6.sh"),
		"not a canonical uuid":      filepath.Join(dir, "coral_prompt_11111111-1111-4111-8111-11111111111X_7.txt"),
		"upper-case uuid":           filepath.Join(dir, "coral_prompt_AAAAAAAA-1111-4111-8111-111111111111_8.txt"),
		"someone else's prefix":     filepath.Join(dir, "xcoral_prompt_"+deadSID+"_9.txt"),
		"a launch script (#4361's)": filepath.Join(dir, "coral-command-123.sh"),
	}
	for _, p := range gone {
		writeAged(t, p, day)
	}
	for name, p := range kept {
		age := day
		if name == "too new" {
			age = time.Hour
		}
		writeAged(t, p, age)
	}
	// A symlink and a folder with launch temp file names: never removed, nor what the link points to.
	target := filepath.Join(t.TempDir(), "elsewhere.txt")
	writeAged(t, target, day)
	link := filepath.Join(dir, "coral_prompt_"+deadSID+"_10.txt")
	require.NoError(t, os.Symlink(target, link))
	folder := filepath.Join(dir, "coral_prompt_"+deadSID+"_11.txt")
	require.NoError(t, os.Mkdir(folder, 0o700))

	procs := "/usr/bin/claude --session-id " + runningSID + " --settings /elsewhere\n" +
		"/usr/bin/claude --settings " + kept["path in a process"] + "\n"
	removed := SweepStaleTempFiles(dir, TempSweep{
		MinAge:  24 * time.Hour,
		Live:    func(id string) bool { return id == liveSID },
		Running: func() (string, error) { return procs, nil },
	})

	assert.ElementsMatch(t, []string{gone["dead prompt"], gone["dead settings"], gone["dead instructions"]}, removed)
	for name, p := range gone {
		_, err := os.Lstat(p)
		assert.True(t, os.IsNotExist(err), "%s is removed", name)
	}
	for name, p := range kept {
		_, err := os.Lstat(p)
		assert.NoError(t, err, "%s survives", name)
	}
	for _, p := range []string{link, folder, target} {
		_, err := os.Lstat(p)
		assert.NoError(t, err, "%s survives", p)
	}
}

// Unsure means keep: no process list (an error, or nothing), no live-session check.
func TestSweepStaleTempFilesKeepsAllWhenUnsure(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "coral_prompt_"+deadSID+"_1.txt")
	writeAged(t, p, 48*time.Hour)
	none := func(string) bool { return false }
	for name, opt := range map[string]TempSweep{
		"ps failed":       {MinAge: time.Hour, Live: none, Running: func() (string, error) { return "", errors.New("no ps") }},
		"ps empty":        {MinAge: time.Hour, Live: none, Running: func() (string, error) { return "  \n", nil }},
		"no live check":   {MinAge: time.Hour, Running: func() (string, error) { return "launchd\n", nil }},
		"no process list": {MinAge: time.Hour, Live: none},
		"no minimum age":  {Live: none, Running: func() (string, error) { return "launchd\n", nil }},
	} {
		assert.Empty(t, SweepStaleTempFiles(dir, opt), name)
		_, err := os.Lstat(p)
		require.NoError(t, err, "%s: the file survives", name)
	}
}

// Launch temp files go to the folder SetTempDir names (private), and a session's teardown removes
// its files there. One in the system temp folder (where launches before #4381 wrote them) is the
// startup sweep's: a teardown never reads that folder (#90).
func TestLaunchTempFilesGoToThePrivateFolder(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // the "system" temp folder is this test's own, never the shared one
	launchTempDirMu.Lock()
	prev := launchTempDirPath
	launchTempDirMu.Unlock()
	t.Cleanup(func() { launchTempDirMu.Lock(); launchTempDirPath = prev; launchTempDirMu.Unlock() })

	dir := filepath.Join(t.TempDir(), "tmp")
	require.NoError(t, SetTempDir(dir))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	p, err := writeTempFile("prompt", deadSID, "txt", []byte("hi"))
	require.NoError(t, err)
	assert.Equal(t, dir, filepath.Dir(p))
	old := filepath.Join(os.TempDir(), "coral_settings_"+deadSID+"_42.json")
	require.NoError(t, os.WriteFile(old, []byte("{}"), 0o600))
	t.Cleanup(func() { os.Remove(old) })

	CleanupTempFiles(deadSID)
	_, err = os.Lstat(p)
	assert.True(t, os.IsNotExist(err), "%s is removed", p)
	assert.FileExists(t, old, "the system temp folder is not read at a teardown")
}
