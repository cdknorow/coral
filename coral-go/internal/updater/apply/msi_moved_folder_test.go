package apply

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/stage"
)

// #321: the Windows packages up to 1.4.156 were 32-bit and installed into Program Files (x86)\Coral; the x64 ones install into Program Files\Coral,
// and the major upgrade from one to the other moves the install. The new Coral is started and checked in the folder Windows Installer has the new
// product in after msiexec, never the folder read before it, so the first update of an x86 install is not taken for a failure and rolled back.

// movedRig is an install in Program Files (x86) that the update moves to Program Files.
func movedRig(t *testing.T) (r *msiRig, x86, x64 string) {
	r = newMSIRig(t)
	root := t.TempDir()
	x86, x64 = filepath.Join(root, "Program Files (x86)", "Coral"), filepath.Join(root, "Program Files", "Coral")
	r.msi.install, r.msi.movedTo = x86, x64
	return r, x86, x64
}

func TestAnUpdateThatMovesTheInstallStartsAndChecksTheNewFolder(t *testing.T) {
	r, x86, x64 := movedRig(t)
	res := r.run()
	assert.Equal(t, ResultRelaunched, res.Result, res.FailedStage)
	assert.Equal(t, []string{"wait", "hold", "authenticode", "productcode", "msiexec /i", "start", "healthy"}, r.msi.calls)
	assert.Equal(t, []string{x64}, r.msi.startedIn, "started where Windows Installer put the new version")
	assert.Equal(t, []string{x64}, r.msi.checkedIn, "and checked there")
	assert.NotContains(t, r.msi.startedIn, x86)
	assert.Equal(t, "1.4.130", r.state().Version)
}

// A moved install that doesn't come up is still rolled back, and the previous version, back in its own folder, is started from there.
func TestAMovedInstallThatDoesNotComeUpIsRolledBackAndTheOldFolderStarted(t *testing.T) {
	r, x86, x64 := movedRig(t)
	r.msi.healthErr = []error{errors.New("not up")}
	res := r.run()
	assert.Equal(t, ResultRolledBack, res.Result, res.FailedStage)
	assert.Equal(t, "health", res.FailedStage)
	assert.Equal(t, []string{x64, x86}, r.msi.startedIn, "the new version where it was installed, then the previous one where it went back")
	assert.Equal(t, []string{x64}, r.msi.checkedIn)
}

// Found again after msiexec, the product must still be the verified package's and its folder one the apply uses: otherwise nothing is started
// from it (Security on #321: the program started after an update is the one the MSI installed, found through Windows Installer).
func TestNothingIsStartedFromAFolderThatIsNotTheInstalledPackages(t *testing.T) {
	r, _, x64 := movedRig(t)
	r.msi.unsupportedIn = map[string]string{x64: "portable_install"}
	res := r.run()
	assert.Equal(t, ResultRolledBack, res.Result, res.FailedStage)
	assert.Equal(t, "installed_folder", res.FailedStage)
	assert.NotContains(t, r.msi.startedIn, x64, "a folder the apply won't touch is never started")
	assert.Empty(t, r.msi.checkedIn)
	assert.Contains(t, strings.Join(r.msi.calls, " "), "msiexec /x", "the verified product is removed and the previous version put back")

	r, _, x64 = movedRig(t)
	r.msi.registeredAfterInstall = "{99999999-0000-0000-0000-000000000000}" // Windows reports another product than the one installed
	res = r.run()
	assert.Equal(t, "rollback_product_mismatch", res.FailedStage)
	assert.NotContains(t, r.msi.startedIn, x64, "nothing is started from another product's folder")
	assert.Empty(t, r.msi.checkedIn)
	assert.NotContains(t, strings.Join(r.msi.calls, " "), "msiexec /x")
}

// A timed-out install that landed anyway is checked in the new folder too.
func TestATimedOutInstallThatMovedTheFolderIsCheckedThere(t *testing.T) {
	r, _, x64 := movedRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.msi.onMsiexec = func() { cancel(); r.msi.registered, r.msi.regVersion, r.msi.install = newProduct, "1.4.130", x64 }
	r.msi.codes = []int{1}
	res := RunMSI(ctx, r.cfg)
	assert.Equal(t, ResultRelaunched, res.Result, res.FailedStage)
	assert.Equal(t, []string{x64}, r.msi.checkedIn)
}

// The next run finishing an interrupted apply reads the folder when it starts, so it already has the moved one; a rollback from there starts the
// previous version in its own folder.
func TestAnInterruptedMovedApplyIsRolledBackIntoTheOldFolder(t *testing.T) {
	r, x86, x64 := movedRig(t)
	r.msi.registered, r.msi.regVersion, r.msi.install, r.msi.origin = newProduct, "1.4.130", x64, x86
	st, err := stage.LoadStaged(r.data)
	require.NoError(t, err)
	j := msiJournal{Phase: "installed", From: "1.4.125", To: "1.4.130", Product: newProduct, Record: st.Record, StartedAt: msiTestNow.Add(-10 * time.Minute)}
	require.NoError(t, writeMSIJournal(filepath.Join(r.data, stage.UpdatesDir), j))
	r.msi.healthErr = []error{errors.New("down"), errors.New("still down")}
	res := r.run()
	assert.Equal(t, ResultRolledBack, res.Result, res.FailedStage)
	assert.Equal(t, []string{x64, x64}, r.msi.checkedIn, "the new version checked where it is")
	assert.Equal(t, x86, r.msi.startedIn[len(r.msi.startedIn)-1], "the previous version started where it went back")
}
