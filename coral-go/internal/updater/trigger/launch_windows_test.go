//go:build windows

package trigger

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// The test program stands for coral-update.exe: copied as that name into a fake install folder, then started by Launch like the real one. Run with
// CORAL_TRIGGER_TEST_CHILD set it only records what it sees and exits.
func TestMain(m *testing.M) {
	if out := os.Getenv("CORAL_TRIGGER_TEST_CHILD"); out != "" {
		childRecord(out)
		return
	}
	os.Exit(m.Run())
}

var procGetHandleInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")

func handleIsValid(h windows.Handle) bool {
	var flags uint32
	r, _, _ := procGetHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&flags)))
	return r != 0
}

type seen struct {
	Args         []string
	Exe          string
	Cwd          string
	HandleLeaked bool
	MarkerThere  bool
	Env          bool
	SawSecret    bool
}

func childRecord(out string) {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	s := seen{Args: os.Args[1:], Exe: exe, Cwd: cwd}
	if h, err := strconv.ParseUint(os.Getenv("CORAL_TRIGGER_TEST_HANDLE"), 10, 64); err == nil {
		// A handle value the parent had open and inheritable: valid here only if it was inherited.
		s.HandleLeaked = handleIsValid(windows.Handle(h))
	}
	if d := os.Getenv("CORAL_TRIGGER_TEST_DATA"); d != "" {
		_, err := os.Stat(filepath.Join(d, UpdatesDir, markerFile))
		s.MarkerThere = err == nil
	}
	s.Env = os.Getenv("CORAL_TRIGGER_TEST_DATA") != ""
	s.SawSecret = os.Getenv("CORAL_SESSION_TOKEN") != "" || os.Getenv("CORAL_SESSION_NAME") != ""
	b, _ := json.Marshal(s)
	_ = os.WriteFile(out, b, 0o600)
}

func fileHash(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	require.NoError(t, err)
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	require.NoError(t, err)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func fakeInstall(t *testing.T) (installDir, dataDir string) {
	t.Helper()
	updaterEnvExtra = map[string]bool{"CORAL_TRIGGER_TEST_CHILD": true, "CORAL_TRIGGER_TEST_DATA": true, "CORAL_TRIGGER_TEST_HANDLE": true, "CORAL_TRIGGER_TEST_SECRET": false}
	t.Cleanup(func() { updaterEnvExtra = nil })
	installDir, dataDir = t.TempDir(), t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)
	in, err := os.Open(self)
	require.NoError(t, err)
	defer in.Close()
	out, err := os.Create(filepath.Join(installDir, updaterExe))
	require.NoError(t, err)
	_, err = io.Copy(out, in)
	require.NoError(t, err)
	require.NoError(t, out.Close())
	old := updaterInstallDir
	updaterInstallDir = func() (string, error) { return installDir, nil }
	t.Cleanup(func() { updaterInstallDir = old })
	return installDir, dataDir
}

func waitSeen(t *testing.T, out string) seen {
	t.Helper()
	var s seen
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(out)
		return err == nil && json.Unmarshal(b, &s) == nil
	}, 20*time.Second, 50*time.Millisecond, "the updater ran")
	return s
}

func TestLaunchRunsACopyOutsideTheInstallFolderWithNoInheritedHandles(t *testing.T) {
	installDir, dataDir := fakeInstall(t)
	out := filepath.Join(t.TempDir(), "seen.json")
	t.Setenv("CORAL_TRIGGER_TEST_CHILD", out)
	t.Setenv("CORAL_TRIGGER_TEST_DATA", dataDir)
	t.Setenv("CORAL_SESSION_TOKEN", "a-session-token")
	t.Setenv("CORAL_SESSION_NAME", "claude-x")

	// A handle of this process that is inheritable: a child started with bInheritHandles=true would hold it.
	sa := &windows.SecurityAttributes{Length: uint32(24), InheritHandle: 1}
	ev, err := windows.CreateEvent(sa, 1, 0, nil)
	require.NoError(t, err)
	defer windows.CloseHandle(ev)
	require.NoError(t, windows.SetHandleInformation(ev, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT))
	t.Setenv("CORAL_TRIGGER_TEST_HANDLE", strconv.FormatUint(uint64(ev), 10))

	require.NoError(t, Launch(t.Context(), dataDir, "1.5.0"))
	s := waitSeen(t, out)

	assert.Equal(t, []string{"apply"}, s.Args, "the argument is the fixed word")
	applyDir := filepath.Join(dataDir, UpdatesDir, updaterApplyDir)
	assert.True(t, strings.EqualFold(filepath.Dir(s.Exe), applyDir), "runs from %s, ran from %s", applyDir, s.Exe)
	assert.False(t, strings.HasPrefix(strings.ToLower(s.Exe), strings.ToLower(installDir)), "not from the install folder, which the MSI replaces")
	assert.True(t, strings.EqualFold(s.Cwd, applyDir), "its working folder is its own")
	assert.False(t, s.HandleLeaked, "an inheritable handle of the server is not inherited by the updater")
	assert.False(t, s.SawSecret, "the server's session values do not reach the updater")
	assert.True(t, s.MarkerThere, "the install marker is down before the updater starts")
	assert.Equal(t, fileHash(t, filepath.Join(installDir, updaterExe)), fileHash(t, s.Exe), "the copy is the installed program, byte for byte")
	m, err := Consume(dataDir, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "1.5.0", m.Version)
}

func TestLaunchLeavesNoMarkerWhenTheUpdaterIsNotThere(t *testing.T) {
	installDir, dataDir := fakeInstall(t)
	require.NoError(t, os.Remove(filepath.Join(installDir, updaterExe)))
	err := Launch(t.Context(), dataDir, "1.5.0")
	require.Error(t, err)
	_, cerr := Consume(dataDir, time.Now())
	assert.ErrorIs(t, cerr, ErrNoMarker, "no click, no marker")
}

func TestLaunchRefusesAnInstalledUpdaterThatIsALink(t *testing.T) {
	installDir, dataDir := fakeInstall(t)
	real := filepath.Join(installDir, "real.exe")
	require.NoError(t, os.Rename(filepath.Join(installDir, updaterExe), real))
	if err := os.Symlink(real, filepath.Join(installDir, updaterExe)); err != nil {
		t.Skipf("this user cannot make a symbolic link: %v", err)
	}
	require.Error(t, Launch(t.Context(), dataDir, "1.5.0"))
	_, cerr := Consume(dataDir, time.Now())
	assert.ErrorIs(t, cerr, ErrNoMarker)
}

func TestUpdaterEnvKeepsWhatItNeedsAndNothingElse(t *testing.T) {
	in := []string{
		"SystemRoot=C:\\Windows", "Path=C:\\x", "TEMP=C:\\t", "CORAL_DATA_DIR=D:\\data", "CORAL_AUTO_UPDATE=off",
		"CORAL_SESSION_TOKEN=secret", "CORAL_SESSION_NAME=claude-x", "CORAL_URL=http://127.0.0.1:8420", "CORAL_API_KEY=k", "AWS_SECRET_ACCESS_KEY=s", "=C:=C:\\x", "NOEQUALS",
	}
	got := updaterEnv(in)
	assert.ElementsMatch(t, []string{"SystemRoot=C:\\Windows", "Path=C:\\x", "TEMP=C:\\t", "CORAL_DATA_DIR=D:\\data", "CORAL_AUTO_UPDATE=off"}, got)
}

func TestLaunchRemovesOlderCopies(t *testing.T) {
	_, dataDir := fakeInstall(t)
	dir := filepath.Join(dataDir, UpdatesDir, updaterApplyDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	stale := filepath.Join(dir, "coral-update-old.exe")
	require.NoError(t, os.WriteFile(stale, []byte("old"), 0o600))
	out := filepath.Join(t.TempDir(), "seen.json")
	t.Setenv("CORAL_TRIGGER_TEST_CHILD", out)
	require.NoError(t, Launch(t.Context(), dataDir, "1.5.0"))
	waitSeen(t, out)
	_, err := os.Stat(stale)
	assert.True(t, os.IsNotExist(err), "an earlier install's copy is gone")
}

func TestTheCopyIsHeldAgainstWritingDeletingAndRenamingUntilClosed(t *testing.T) {
	installDir, dataDir := fakeInstall(t)
	dir := filepath.Join(dataDir, UpdatesDir, updaterApplyDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	copyPath, hold, err := copyUpdater(filepath.Join(installDir, updaterExe), dir)
	require.NoError(t, err)

	_, werr := os.OpenFile(copyPath, os.O_WRONLY, 0)
	assert.Error(t, werr, "no write while held")
	assert.Error(t, os.Remove(copyPath), "no delete while held")
	assert.Error(t, os.Rename(copyPath, copyPath+".x"), "no rename while held")
	r, rerr := os.Open(copyPath)
	require.NoError(t, rerr, "others can still read it (CreateProcess maps it)")
	r.Close()

	require.NoError(t, hold.Close())
	assert.NoError(t, os.Remove(copyPath), "free once closed")
}
