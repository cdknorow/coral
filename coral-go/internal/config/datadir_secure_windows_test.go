//go:build windows

package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// usersCanRead makes dir's list the way a folder on a drive root looks: this user, SYSTEM and BUILTIN\Users with read, and what is
// made in it inherits that (no protection, nothing explicit but those).
func usersCanRead(t *testing.T, dir string) {
	t.Helper()
	me, err := currentUserSID()
	require.NoError(t, err)
	out, err := exec.Command("icacls", dir, "/inheritance:r", "/grant:r", "*"+me.String()+":(OI)(CI)F", "/grant:r", "*S-1-5-18:(OI)(CI)F",
		"/grant:r", "*S-1-5-32-545:(OI)(CI)RX").CombinedOutput()
	require.NoError(t, err, string(out))
}

func readersOf(t *testing.T, path string) []string {
	t.Helper()
	g, err := DataDirBroadReaders(path)
	require.NoError(t, err)
	return g
}

// G2A (#96): an existing data folder that Users can read is made private to this user, and so are the files and folders in it.
func TestExistingDataFolderOthersCanReadIsTightened(t *testing.T) {
	root := privateRoot(t)
	dir := filepath.Join(root, "coral")
	require.NoError(t, os.Mkdir(dir, 0o700))
	usersCanRead(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessions.db"), []byte("db"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "artifacts", "sub"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifacts", "sub", "obj"), []byte("x"), 0o600))
	require.NotEmpty(t, readersOf(t, dir), "the setup: Users can read the folder")
	require.NotEmpty(t, readersOf(t, filepath.Join(dir, "sessions.db")), "and what is in it, by inheritance")
	// A file with an EXPLICIT entry for Users, which a change to the folder's list would not remove.
	explicit := filepath.Join(dir, "api-key")
	require.NoError(t, os.WriteFile(explicit, []byte("secret"), 0o600))
	out, err := exec.Command("icacls", explicit, "/inheritance:d", "/grant", "*S-1-5-32-545:R").CombinedOutput()
	require.NoError(t, err, string(out))
	require.NotEmpty(t, readersOf(t, explicit))

	require.NoError(t, EnsureDataDir(dir))

	assert.Empty(t, readersOf(t, dir), "the folder")
	for _, p := range []string{"sessions.db", "api-key", filepath.Join("artifacts", "sub"), filepath.Join("artifacts", "sub", "obj")} {
		assert.Empty(t, readersOf(t, filepath.Join(dir, p)), p)
	}
	// The data is still this user's to read and write, and a file made now is private too.
	data, err := os.ReadFile(filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	assert.Equal(t, "db", string(data))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.db"), []byte("n"), 0o600))
	assert.Empty(t, readersOf(t, filepath.Join(dir, "new.db")))
	// What was done is told once, as a fix; the folder is private now, so it is no longer a warning (#295).
	assert.Empty(t, DataDirWarning(dir), "a fixed folder is not a warning")
	w := TakeDataDirFixed()
	assert.Contains(t, w, "made its data folder")
	assert.Contains(t, w, "Users")
	assert.NotContains(t, w, "WARNING")
	assert.Empty(t, TakeDataDirFixed(), "told once")
	assert.Empty(t, DataDirWarning(dir), "and not again")
	// A second start finds nothing to do and keeps saying what happened.
	require.NoError(t, EnsureDataDir(dir))
	assert.Empty(t, readersOf(t, dir))
}

// A folder that is already private is left alone, with no notice.
func TestPrivateDataFolderIsLeftAlone(t *testing.T) {
	setDataDirNotice("")
	setDataDirFixed("")
	dir := filepath.Join(privateRoot(t), "coral")
	require.NoError(t, EnsureDataDir(dir)) // new: protected
	require.NoError(t, EnsureDataDir(dir)) // existing, already private
	assert.Empty(t, DataDirWarning(dir))
}

// A folder another account owns can't be made private by this one: Coral refuses to start, naming the folder and the way out; the
// opt-out lets it run, and says so.
func TestDataFolderAnotherAccountOwnsIsRefusedUnlessAllowed(t *testing.T) {
	dir := filepath.Join(privateRoot(t), "coral")
	require.NoError(t, os.Mkdir(dir, 0o700))
	usersCanRead(t, dir)
	prev := ownedByUs
	ownedByUs = func(windows.Handle) (bool, error) { return false, nil }
	t.Cleanup(func() { ownedByUs = prev; SetAllowSharedDataDir(false); setDataDirNotice(""); setDataDirFixed("") })

	err := EnsureDataDir(dir)
	require.ErrorIs(t, err, ErrSharedDataDir)
	assert.Contains(t, err.Error(), dir, "the message names the folder")
	assert.Contains(t, err.Error(), "--allow-shared-data-dir", "and the way out")
	assert.True(t, strings.HasPrefix(err.Error(), "Coral will not start: its data folder "), "the refusal says what happens first, in plain words (#5755)")
	assert.NotContains(t, err.Error(), ErrSharedDataDir.Error(), "the sentinel's own words are not repeated in front of the message")
	assert.Contains(t, err.Error(), "Users")
	assert.Contains(t, err.Error(), "takeown /F", "the exact command to take it over")
	assert.NotEmpty(t, readersOf(t, dir), "nothing was changed")

	SetAllowSharedDataDir(true)
	require.NoError(t, EnsureDataDir(dir))
	assert.NotEmpty(t, readersOf(t, dir), "still shared, as the opt-out says")
	assert.NotEmpty(t, DataDirWarning(dir), "and still reported")
	assert.Contains(t, DataDirWarning(dir), "can be read by")

	SetAllowSharedDataDir(false)
	t.Setenv("CORAL_ALLOW_SHARED_DATA_DIR", "1")
	assert.NoError(t, EnsureDataDir(dir), "the environment variable is the same opt-out")
}

// Nothing is followed: a junction inside the data folder is left alone and not entered, so a folder outside it keeps its list; and a
// data folder that is itself a junction is refused rather than changed.
func TestDataFolderLinksAreNeverFollowed(t *testing.T) {
	root := privateRoot(t)
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.Mkdir(outside, 0o700))
	usersCanRead(t, outside)
	secret := filepath.Join(outside, "other-apps-file")
	require.NoError(t, os.WriteFile(secret, []byte("not coral's"), 0o600))
	require.NotEmpty(t, readersOf(t, secret))

	dir := filepath.Join(root, "coral")
	require.NoError(t, os.Mkdir(dir, 0o700))
	usersCanRead(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessions.db"), []byte("db"), 0o600)) // Coral's own folder (a folder that is not is refused, #5698)
	out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(dir, "link"), outside).CombinedOutput()
	require.NoError(t, err, string(out))

	require.NoError(t, EnsureDataDir(dir))
	assert.Empty(t, readersOf(t, dir), "the data folder is private")
	assert.NotEmpty(t, readersOf(t, outside), "the folder the junction points to is not touched")
	assert.NotEmpty(t, readersOf(t, secret), "nor what is in it")
	assert.Empty(t, DataDirWarning(dir))
	assert.Contains(t, TakeDataDirFixed(), "links were left alone")

	// The data folder is itself a junction to a shared folder: refused, not changed.
	link := filepath.Join(root, "datalink")
	out, err = exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() { SetAllowSharedDataDir(false); setDataDirNotice(""); setDataDirFixed("") })
	err = EnsureDataDir(link)
	require.ErrorIs(t, err, ErrSharedDataDir)
	assert.Contains(t, err.Error(), "link or junction")
	assert.NotEmpty(t, readersOf(t, outside), "the target keeps its list")
}

// N1 (#96): a folder that grants a named account or a custom group is tightened too, and so is an old file inside it that carries an
// explicit entry for one, though the folder itself lists only this user and SYSTEM.
func TestExistingDataFolderGrantingOtherAccountsIsTightened(t *testing.T) {
	dir := filepath.Join(privateRoot(t), "coral")
	require.NoError(t, os.Mkdir(dir, 0o700))
	// "Guest" stands for a named account; "Backup Operators" for a custom group.
	for _, who := range []string{"Guest", "Backup Operators"} {
		out, err := exec.Command("icacls", dir, "/grant", who+":(OI)(CI)R").CombinedOutput()
		require.NoError(t, err, string(out))
	}
	old := filepath.Join(dir, "old.db")
	require.NoError(t, os.WriteFile(old, []byte("x"), 0o600))
	out, err := exec.Command("icacls", old, "/inheritance:d", "/grant", "Guest:R").CombinedOutput()
	require.NoError(t, err, string(out))
	got := readersOf(t, dir)
	require.Len(t, got, 2, got)

	require.NoError(t, EnsureDataDir(dir))

	assert.Empty(t, readersOf(t, dir))
	assert.Empty(t, readersOf(t, old), "the explicit entry on the old file did not survive")
	assert.Contains(t, TakeDataDirFixed(), "Guest")
}

// A data folder whose only extra entries are Administrators, SYSTEM and this user is already private: nothing is done.
func TestDataFolderWithOnlyAdministratorsIsLeftAlone(t *testing.T) {
	setDataDirNotice("")
	setDataDirFixed("")
	dir := filepath.Join(privateRoot(t), "coral")
	require.NoError(t, os.Mkdir(dir, 0o700))
	out, err := exec.Command("icacls", dir, "/grant", "*S-1-5-32-544:(OI)(CI)F").CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, EnsureDataDir(dir))
	assert.Empty(t, DataDirWarning(dir))
}

// privateRoot is a temp folder only this user and SYSTEM can use, so a folder made in it starts private wherever TEMP is (a drive root's
// entries for Users and Authenticated Users are inherited otherwise).
func privateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, protectDataDir(root))
	return root
}

// #5698 (review of #96): a folder that is not Coral's own (a project, a drive) is never re-protected file by file: Coral refuses and changes nothing.
func TestFolderThatIsNotCoralsIsRefusedAndLeftAlone(t *testing.T) {
	dir := filepath.Join(privateRoot(t), "project")
	require.NoError(t, os.Mkdir(dir, 0o700))
	usersCanRead(t, dir)
	other := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(other, []byte("package main"), 0o600))
	t.Cleanup(func() { SetAllowSharedDataDir(false); setDataDirNotice(""); setDataDirFixed("") })

	err := EnsureDataDir(dir)
	require.ErrorIs(t, err, ErrSharedDataDir)
	assert.Contains(t, err.Error(), "main.go", "the message names a file that is not Coral's")
	assert.NotEmpty(t, readersOf(t, dir), "the folder was not changed")
	assert.NotEmpty(t, readersOf(t, other), "nor the file")
}

// #5698 (review of the review): the folder the MSI makes holds only the EULA marker; when a broad reader can use it, it is tightened, not refused.
func TestFolderTheInstallerMadeWithOnlyTheEulaMarkerIsTightened(t *testing.T) {
	dir := filepath.Join(privateRoot(t), "coral")
	require.NoError(t, os.Mkdir(dir, 0o700))
	usersCanRead(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".eula-accepted"), []byte("1"), 0o600))
	require.NotEmpty(t, readersOf(t, dir))
	t.Cleanup(func() { SetAllowSharedDataDir(false); setDataDirNotice(""); setDataDirFixed("") })

	require.NoError(t, EnsureDataDir(dir))
	assert.Empty(t, readersOf(t, dir), "the folder is private now")
	assert.Empty(t, readersOf(t, filepath.Join(dir, ".eula-accepted")))
}
