//go:build relaunchfixture

package routes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
)

const goodFixture = `{
  "enabled": true, "installed": "1.4.133", "state": "staged", "supported": true,
  "staged": {"version": "1.4.134", "staged_at": "2026-10-20T10:00:00Z", "verified": true},
  "manifest_sequence": 7, "checked_at": "2026-10-20T11:00:00Z", "install_kind": "tarball",
  "last_rollback": {"at": "2026-10-19T09:00:00Z", "from": "1.4.132", "to": "1.4.133", "reason": "not_healthy", "restart_failed": true},
  "apply": {"exit": false}
}`

// fixtureFolder makes a valid fixture folder (0700) holding body (0600) and points the seam at it.
func fixtureFolder(t *testing.T, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fx")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.Chmod(dir, 0o700))
	if body != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fixtureFile), []byte(body), 0o600))
	}
	t.Setenv(fixtureDirEnv, dir)
	return dir
}

func TestTheFixtureBuildHasTheRelaunchRoutesOnAndAFakeUpdater(t *testing.T) {
	assert.True(t, updateRelaunchFeature)
	assert.NotNil(t, BuildRelaunchUpdater())
}

func TestAValidFixtureIsAcceptedAndBecomesTheStatus(t *testing.T) {
	fixtureFolder(t, goodFixture)
	require.NoError(t, CheckRelaunchFixture())
	rec, err := (&fixtureUpdater{}).Record(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "1.4.133", rec.Installed)
	assert.Equal(t, updStaged, rec.State)
	require.NotNil(t, rec.Staged)
	assert.Equal(t, "1.4.134", rec.Staged.Version)
	assert.True(t, rec.Staged.Verified)
	assert.Equal(t, "tarball", rec.InstallKind)
	assert.EqualValues(t, 7, rec.Trust.ManifestSequence)
	assert.Equal(t, fixtureTrustWord, rec.Trust.Issuer, "the trust fields are fixed words, not fixture content")
	require.NotNil(t, rec.LastRollback)
	assert.True(t, rec.LastRollback.RestartFailed)
}

// Security's condition 4 and 6: the folder is owner-only, owned by this user and not a link; the file is a regular 0600 file; nothing else.
func TestTheFixtureIsRefusedInAFolderOrFileThatIsNotPrivate(t *testing.T) {
	dir := fixtureFolder(t, goodFixture)
	require.NoError(t, CheckRelaunchFixture())

	require.NoError(t, os.Chmod(dir, 0o770))
	assert.ErrorContains(t, CheckRelaunchFixture(), "0700", "group-writable folder")
	require.NoError(t, os.Chmod(dir, 0o750))
	assert.Error(t, CheckRelaunchFixture(), "group-readable folder")
	require.NoError(t, os.Chmod(dir, 0o700))
	require.NoError(t, CheckRelaunchFixture())

	require.NoError(t, os.Chmod(filepath.Join(dir, fixtureFile), 0o640))
	assert.ErrorContains(t, CheckRelaunchFixture(), "0600", "group-readable file")
	require.NoError(t, os.Chmod(filepath.Join(dir, fixtureFile), 0o600))
	require.NoError(t, CheckRelaunchFixture())

	// a link in place of the folder
	link := filepath.Join(t.TempDir(), "link")
	linktest.Symlink(t, dir, link)
	t.Setenv(fixtureDirEnv, link)
	assert.ErrorContains(t, CheckRelaunchFixture(), "link")
	t.Setenv(fixtureDirEnv, dir)

	// a link in place of the file
	real := filepath.Join(dir, "real.json")
	require.NoError(t, os.Rename(filepath.Join(dir, fixtureFile), real))
	linktest.Symlink(t, real, filepath.Join(dir, fixtureFile))
	assert.Error(t, CheckRelaunchFixture(), "a link as the fixture")
	require.NoError(t, os.Remove(filepath.Join(dir, fixtureFile)))
	require.NoError(t, os.Rename(real, filepath.Join(dir, fixtureFile)))
	require.NoError(t, CheckRelaunchFixture())

	// not owned by this user
	old := fixtureUID
	t.Cleanup(func() { fixtureUID = old })
	fixtureUID = func() int { return old() + 1 }
	assert.ErrorContains(t, CheckRelaunchFixture(), "not owned")
	fixtureUID = old

	// no folder named, a relative one, a missing one
	t.Setenv(fixtureDirEnv, "")
	assert.Error(t, CheckRelaunchFixture())
	t.Setenv(fixtureDirEnv, "relative/dir")
	assert.ErrorContains(t, CheckRelaunchFixture(), "absolute")
	t.Setenv(fixtureDirEnv, filepath.Join(dir, "missing"))
	assert.Error(t, CheckRelaunchFixture())
}

// Security's condition 4: strict JSON, the vocabularies of the real status, no path, URL, command or free text, a size cap.
func TestTheFixtureRefusesAnUnknownKeyAPathShapedValueAndAnOversizedFile(t *testing.T) {
	bad := map[string]string{
		"an unknown key":              `{"installed":"1.4.133","command":"rm -rf /"}`,
		"a nested unknown key":        `{"installed":"1.4.133","staged":{"version":"1.4.134","staged_at":"2026-10-20T10:00:00Z","verified":true,"path":"/tmp/x"}}`,
		"a path as the version":       `{"installed":"/etc/passwd"}`,
		"a path in staged.version":    `{"installed":"1.4.133","state":"staged","staged":{"version":"../../x","staged_at":"2026-10-20T10:00:00Z","verified":true}}`,
		"a URL as the install kind":   `{"installed":"1.4.133","install_kind":"https://evil.example/x"}`,
		"free text as the reason":     `{"installed":"1.4.133","not_ready_reason":"because I said so"}`,
		"a command as a rollback":     `{"installed":"1.4.133","last_rollback":{"at":"2026-10-20T10:00:00Z","from":"1.4.132","to":"1.4.133","reason":"sh -c evil"}}`,
		"an e-mail as by":             `{"installed":"1.4.133","last_result":{"at":"2026-10-20T10:00:00Z","from":"1.4.132","to":"1.4.133","result":"relaunched","by":"person@example.invalid"}}`,
		"a state outside the words":   `{"installed":"1.4.133","state":"exploded"}`,
		"a time that is not UTC":      `{"installed":"1.4.133","checked_at":"2026-10-20T10:00:00+02:00"}`,
		"no installed version":        `{"state":"none"}`,
		"a second document":           `{"installed":"1.4.133"} {"installed":"1.4.134"}`,
		"not JSON":                    `installed: 1.4.133`,
		"a negative sequence":         `{"installed":"1.4.133","manifest_sequence":-1}`,
		"a state_after outside words": `{"installed":"1.4.133","apply":{"state_after":"rm"}}`,
		"an oversized file":           `{"installed":"1.4.133"}` + strings.Repeat(" ", fixtureMaxBytes),
	}
	for name, body := range bad {
		fixtureFolder(t, body)
		assert.Error(t, CheckRelaunchFixture(), name)
	}
	fixtureFolder(t, `{"installed":"1.4.133"}`)
	assert.NoError(t, CheckRelaunchFixture(), "the smallest valid fixture")
}

// Apply writes the marker (operation id, the acknowledged version, the hub pseudonym) and nothing else; it ends the process only if the fixture says so.
func TestApplyWritesTheMarkerAndEndsTheProcessOnlyWhenTheFixtureSaysSo(t *testing.T) {
	dir := fixtureFolder(t, goodFixture)
	exited := make(chan int, 1)
	old := exitProcess
	t.Cleanup(func() { exitProcess = old })
	exitProcess = func(code int) { exited <- code }

	u := &fixtureUpdater{}
	op, err := u.Apply(t.Context(), "hub-user-1234abcd")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(op, "fx-"))
	fi, err := os.Stat(filepath.Join(dir, fixtureMarker))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	b, err := os.ReadFile(filepath.Join(dir, fixtureMarker))
	require.NoError(t, err)
	var m map[string]string
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, op, m["operation"])
	assert.Equal(t, "1.4.134", m["version"])
	assert.Equal(t, "hub-user-1234abcd", m["by"])
	assert.Len(t, m, 4, "operation, version, by, at: nothing else")
	entries, _ := os.ReadDir(dir)
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{fixtureFile, fixtureMarker}, names, "no temp file is left")
	select {
	case <-exited:
		t.Fatal("the fixture did not ask for an exit")
	case <-time.After(900 * time.Millisecond):
	}

	// A caller that is not a hub pseudonym, and a fixture with nothing staged, are refused.
	_, err = u.Apply(t.Context(), "person@example.invalid")
	assert.Error(t, err)
	fixtureFolder(t, `{"installed":"1.4.133","state":"none"}`)
	_, err = u.Apply(t.Context(), "hub-user-1234abcd")
	assert.Error(t, err)

	// With "exit": true the process ends with code 0 after the reply.
	fixtureFolder(t, strings.Replace(goodFixture, `"exit": false`, `"exit": true, "state_after": "applying"`, 1))
	_, err = u.Apply(t.Context(), "hub-user-1234abcd")
	require.NoError(t, err)
	select {
	case code := <-exited:
		assert.Equal(t, 0, code)
	case <-time.After(3 * time.Second):
		t.Fatal("the process was not ended")
	}
	rec, err := u.Record(t.Context())
	require.NoError(t, err)
	assert.Equal(t, updApplying, rec.State, "the status shows the state after the apply")
	assert.Equal(t, "1.4.134", rec.HubRelaunched)
}

// Security's condition 5: the checks of a release all run; the fixture only stands in for coral-update. The same harness as the real relaunch tests, with the fixture updater.
func TestTheFullRelaunchPathRunsOverTheFixtureWithEveryCheckOfARelease(t *testing.T) {
	dir := fixtureFolder(t, goodFixture)
	e := newRelaunchEnv(t)
	e.h.Updater = &fixtureUpdater{}
	e.h.CanRestart = func() bool { return true }

	// no stamp, no relaunch: a local caller sees the status and no token, and cannot relaunch
	code, st := e.status(nil)
	assert.Equal(t, 200, code)
	assert.NotContains(t, st["relaunch"], "ack")
	code, _ = e.post(`{"ack":"x"}`, nil)
	assert.NotEqual(t, 202, code)

	// the owner gets a token bound to the staged version, and the relaunch is accepted
	code, st = e.status(hubHdr(ownerA))
	require.Equal(t, 200, code)
	assert.Equal(t, "1.4.134", st["staged"].(map[string]any)["version"])
	assert.Equal(t, true, st["relaunch"].(map[string]any)["available"])
	tok := e.token(ownerA)
	code, out := e.post(`{"ack":"`+tok+`"}`, hubHdr(ownerA))
	require.Equal(t, 202, code, "%v", out)
	op, _ := out["operation"].(string)
	assert.True(t, strings.HasPrefix(op, "fx-"))
	_, err := os.Stat(filepath.Join(dir, fixtureMarker))
	assert.NoError(t, err, "the marker is there")

	// the same checks of a release still refuse: a repeat of the version, a wrong user
	code, _ = e.post(`{"ack":"`+tok+`"}`, hubHdr(ownerB))
	assert.NotEqual(t, 202, code, "a token is bound to the user it was issued to")
	code, st = e.status(hubHdr(ownerA))
	assert.Equal(t, 200, code)
	assert.Equal(t, "already_relaunched", st["relaunch"].(map[string]any)["reason"])
}
