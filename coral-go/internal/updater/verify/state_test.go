package verify

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateRoundTripsPrivately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "updates")
	st, err := LoadState(dir)
	require.NoError(t, err, "no file is a first run")
	assert.Equal(t, State{}, st)
	rec := &Record{Version: "1.4.130", Sequence: 7, Revoked: []string{"1.4.99"}}
	require.NoError(t, st.Accept(rec))
	require.NoError(t, SaveState(dir, st))
	back, err := LoadState(dir)
	require.NoError(t, err)
	assert.Equal(t, "1.4.130", back.Version)
	assert.Equal(t, int64(7), back.Sequence)
	assert.Equal(t, []string{"1.4.99"}, back.Revoked)
	if runtime.GOOS != "windows" {
		di, err := os.Stat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
		fi, err := os.Stat(filepath.Join(dir, StateFile))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "no temp file is left")
}

func TestAcceptOnlyMovesForwardAndRevokedIsSticky(t *testing.T) {
	st := State{Version: "1.4.130", Sequence: 7, Revoked: []string{"1.4.99"}}
	assert.Error(t, st.Accept(&Record{Version: "1.4.129", Sequence: 8}), "a lower version")
	assert.Error(t, st.Accept(&Record{Version: "1.4.131", Sequence: 6}), "a lower sequence")
	assert.Equal(t, "1.4.130", st.Version, "a refused accept changes nothing")
	require.NoError(t, st.Accept(&Record{Version: "1.4.131", Sequence: 8, Revoked: []string{"1.4.100", "1.4.99"}}))
	assert.Equal(t, []string{"1.4.100", "1.4.99"}, st.Revoked, "the union, each once")
	// A later manifest that omits a revoked version does not un-revoke it.
	require.NoError(t, st.Accept(&Record{Version: "1.4.132", Sequence: 9, Revoked: nil}))
	assert.Contains(t, st.Revoked, "1.4.99")
	st.Refuse("1.4.133", &Failure{Stage: StageManifestRules, Reason: "x"}, time.Unix(0, 0))
	require.NotNil(t, st.LastFailure)
	require.NoError(t, st.Accept(&Record{Version: "1.4.133", Sequence: 10}))
	assert.Nil(t, st.LastFailure, "an install clears the last refusal")
}

func TestACorruptStateIsAFirstRunNeverPermission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StateFile)
	for name, content := range map[string]string{
		"garbage":           "not json",
		"a bad version":     `{"version":"latest","sequence":1}`,
		"a negative number": `{"version":"1.4.1","sequence":-5}`,
		"a bad revoked":     `{"revoked":["x"]}`,
	} {
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600), name)
		st, err := LoadState(dir)
		assert.ErrorIs(t, err, ErrCorrupt, name)
		assert.Equal(t, State{}, st, name)
	}
	// What a first run still has to pass: the rules do not need the state to refuse a downgrade or an expired manifest.
	r := newRelease(t)
	r.installed = "1.4.131"
	_, err := r.verify(&fakeSigner{})
	assert.Error(t, err)
	// A state that is too large is corrupt, not read whole.
	require.NoError(t, os.WriteFile(path, make([]byte, maxStateSize+1), 0o600))
	_, err = LoadState(dir)
	assert.ErrorIs(t, err, ErrCorrupt)
}

func TestFilesThatAreNotPrivateOrNotRegularAreNotTrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes are the owner-only ACL on Windows (secfile)")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, StateFile)
	require.NoError(t, os.WriteFile(path, []byte(`{"version":"1.4.1","sequence":1}`), 0o600))
	require.NoError(t, os.Chmod(path, 0o644))
	_, err := LoadState(dir)
	assert.ErrorIs(t, err, ErrCorrupt, "readable by others")
	require.NoError(t, os.Chmod(path, 0o666))
	_, err = LoadState(dir)
	assert.ErrorIs(t, err, ErrCorrupt, "writable by others")
	// A symlink in the place of the file (to a file that is itself fine) is refused, for a read and for a write.
	real := filepath.Join(dir, "real.json")
	require.NoError(t, os.WriteFile(real, []byte(`{"version":"1.4.1","sequence":1}`), 0o600))
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(real, path))
	_, err = LoadState(dir)
	assert.ErrorIs(t, err, ErrCorrupt)
	assert.Error(t, SaveState(dir, State{Version: "1.4.2"}))
	b, _ := os.ReadFile(real)
	assert.Contains(t, string(b), "1.4.1", "the symlink's target was not written through")
	// A symlink in the place of the directory is refused.
	link := filepath.Join(t.TempDir(), "updates")
	require.NoError(t, os.Symlink(dir, link))
	assert.Error(t, SaveState(link, State{Version: "1.4.2"}))
}

func TestHistoryIsCapped(t *testing.T) {
	dir := t.TempDir()
	var st State
	for i := 0; i < maxHistory+20; i++ {
		st.History = append(st.History, HistoryEntry{At: "t", From: "1.4.1", To: "1.4.2", By: "local", Result: "relaunched"})
	}
	require.NoError(t, SaveState(dir, st))
	back, err := LoadState(dir)
	require.NoError(t, err)
	assert.Len(t, back.History, maxHistory)
}

func TestAckStoreRemembersAUsedTokenAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_000_000, 0)
	a := NewAckStore(dir)
	first, err := a.Use("tok-1", now.Add(time.Minute), now)
	require.NoError(t, err)
	assert.True(t, first)
	// A new store over the same directory (an updater restart): the token is still used.
	again, err := NewAckStore(dir).Use("tok-1", now.Add(time.Minute), now.Add(time.Second))
	require.NoError(t, err)
	assert.False(t, again, "single use")
	other, err := NewAckStore(dir).Use("tok-2", now.Add(time.Minute), now)
	require.NoError(t, err)
	assert.True(t, other)
	// Expired entries are dropped, so the store does not grow; the id can be used once more after its own expiry (a new token never repeats an id).
	later, err := NewAckStore(dir).Use("tok-1", now.Add(3*time.Minute), now.Add(2*time.Minute))
	require.NoError(t, err)
	assert.True(t, later)
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, AckUsedFile))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	_, err = a.Use("", now.Add(time.Minute), now)
	assert.Error(t, err)
}

func TestAckStoreFailsClosedWhenItCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, AckUsedFile), []byte("garbage"), 0o600))
	ok, err := NewAckStore(dir).Use("tok", time.Now().Add(time.Minute), time.Now())
	assert.Error(t, err)
	assert.False(t, ok, "an unreadable record of what was used never means unused")
}

func TestAckStoreIsBounded(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_000_000, 0)
	a := NewAckStore(dir)
	var f ackFile
	f.Used = map[string]int64{}
	for i := 0; i < maxAckEntries; i++ {
		f.Used[time.Duration(i).String()] = now.Add(time.Hour).Unix()
	}
	require.NoError(t, writePrivate(filepath.Join(dir, AckUsedFile), mustJSON(t, f)))
	ok, err := a.Use("one-more", now.Add(time.Minute), now)
	assert.Error(t, err)
	assert.False(t, ok)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := jsonMarshal(v)
	require.NoError(t, err)
	return b
}
