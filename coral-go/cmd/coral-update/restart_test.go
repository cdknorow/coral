package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/trigger"
)

// The Windows "Install update" quits the server first, so an apply that installs nothing must start Coral again (#5946, #5931's F1).

// installFolder is a folder with a coral-tray.exe in it, as an installed or a portable Coral has.
func installFolder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "coral-tray.exe"), []byte("x"), 0o755))
	return dir
}

// fakeStart records what would have been started.
func fakeStart(t *testing.T, err error) *[]string {
	t.Helper()
	var started []string
	oldStart, oldOS := startCoralFrom, restartGOOS
	startCoralFrom = func(_, dir string) error { started = append(started, dir); return err }
	restartGOOS = "windows"
	t.Cleanup(func() { startCoralFrom, restartGOOS = oldStart, oldOS })
	return &started
}

func TestRestartAfterNothingStartsCoralFromTheMarkersFolder(t *testing.T) {
	started := fakeStart(t, nil)
	data, install := t.TempDir(), installFolder(t)
	require.NoError(t, trigger.WriteMarker(data, "1.5.0", install, time.Now()))
	attempted, ok := restartAfterNothing(data, time.Now())
	assert.True(t, attempted)
	assert.True(t, ok)
	assert.Equal(t, []string{install}, *started)
	attempted, _ = restartAfterNothing(data, time.Now())
	assert.False(t, attempted, "the marker is used once: a second run stopped nothing")
	assert.Len(t, *started, 1)
}

func TestRestartAfterNothingNeedsAGoodClick(t *testing.T) {
	started := fakeStart(t, nil)
	data, install := t.TempDir(), installFolder(t)
	attempted, _ := restartAfterNothing(data, time.Now())
	assert.False(t, attempted, "run by hand: no marker, nothing was stopped")
	require.NoError(t, trigger.WriteMarker(data, "1.5.0", install, time.Now().Add(-trigger.MarkerTTL-time.Minute)))
	attempted, _ = restartAfterNothing(data, time.Now())
	assert.False(t, attempted, "a leftover marker is no click")
	assert.Empty(t, *started)

	restartGOOS = "linux"
	require.NoError(t, trigger.WriteMarker(data, "1.5.0", install, time.Now()))
	attempted, _ = restartAfterNothing(data, time.Now())
	assert.False(t, attempted, "off Windows there is nothing to start")
	_, err := trigger.Consume(data, time.Now())
	assert.NoError(t, err, "and the marker is left alone")
}

func TestRestartAfterNothingSaysWhenItCouldNotStartCoral(t *testing.T) {
	data := t.TempDir()
	for name, install := range map[string]string{
		"a marker with no folder (an older server)": "",
		"a folder with no coral-tray.exe":           t.TempDir(),
	} {
		started := fakeStart(t, nil)
		require.NoError(t, trigger.WriteMarker(data, "1.5.0", install, time.Now()))
		attempted, ok := restartAfterNothing(data, time.Now())
		assert.True(t, attempted, name)
		assert.False(t, ok, name)
		assert.Empty(t, *started, name)
	}
	fakeStart(t, os.ErrPermission)
	require.NoError(t, trigger.WriteMarker(data, "1.5.0", installFolder(t), time.Now()))
	attempted, ok := restartAfterNothing(data, time.Now())
	assert.True(t, attempted)
	assert.False(t, ok, "Coral's start failed")
}

// A folder that is a link to somewhere else, or a tray that is a folder, is no Coral.
func TestRestartFromMarkerStartsOnlyARealTrayProgram(t *testing.T) {
	started := fakeStart(t, nil)
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "coral-tray.exe"), 0o755))
	assert.False(t, restartFromMarker(t.TempDir(), dir))
	assert.False(t, restartFromMarker(t.TempDir(), "relative"))
	// A relative folder that DOES hold a coral-tray.exe (relative to where this runs) is still refused: only an absolute folder is one to start Coral from.
	work := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(work, "rel"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, "rel", "coral-tray.exe"), []byte("x"), 0o755))
	here, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(work))
	t.Cleanup(func() { _ = os.Chdir(here) })
	assert.False(t, restartFromMarker(t.TempDir(), "rel"), "a relative folder with a tray in it")
	assert.False(t, restartFromMarker(t.TempDir(), "."+string(filepath.Separator)+"rel"))
	require.NoError(t, os.Chdir(here))
	assert.Empty(t, *started)
	assert.True(t, restartFromMarker(t.TempDir(), installFolder(t)))
}

// Every answer of the apply that installed nothing and left no Coral running starts it again; the others already did, or have a Coral running.
func TestWhichAnswersStartCoralAgain(t *testing.T) {
	for name, tc := range map[string]struct {
		res  apply.Result
		want bool
	}{
		"no registered product":  {apply.Result{Result: apply.ResultUnsupported, FailedStage: "no_registered_product"}, true},
		"a portable install":     {apply.Result{Result: apply.ResultUnsupported, FailedStage: "portable_install"}, true},
		"an unreadable install":  {apply.Result{Result: apply.ResultUnsupported, FailedStage: "unreadable_install"}, true},
		"the updates folder":     {apply.Result{Result: apply.ResultFailed, FailedStage: "updates_folder"}, true},
		"a later failure":        {apply.Result{Result: apply.ResultFailed, FailedStage: "copy"}, false}, // RunMSI started the old Coral itself
		"busy (another apply)":   {apply.Result{Result: apply.ResultBusy}, false},
		"relaunched":             {apply.Result{Result: apply.ResultRelaunched}, false},
		"declined in the prompt": {apply.Result{Result: apply.ResultDeclined}, false},
		"coral still running":    {apply.Result{Result: apply.ResultCoralRunning}, false},
	} {
		assert.Equal(t, tc.want, installedNothingAndLeftNoCoral(tc.res), name)
	}
}

// The answers before the apply is even reached: a build that does not update itself, and an apply that is not turned on.
func TestAnApplyThatStopsAtItsOffSwitchesStartsCoralAgain(t *testing.T) {
	oldVersion := config.Version
	t.Cleanup(func() { config.Version = oldVersion })
	config.Version = "1.4.130"

	t.Run("updates off: the environment's switch", func(t *testing.T) {
		started := fakeStart(t, nil)
		data, install := t.TempDir(), installFolder(t)
		t.Setenv("CORAL_DATA_DIR", data)
		require.NoError(t, trigger.WriteMarker(data, "1.5.0", install, time.Now()))
		code, out := runOut(t, map[string]string{"CORAL_AUTO_UPDATE": "off"}, "apply")
		assert.Equal(t, 0, code)
		assert.Equal(t, "off", out["result"])
		assert.Equal(t, []string{install}, *started)
		assert.NotContains(t, out, "restart_failed")
	})
	t.Run("updates off, and Coral cannot be started: the answer says so", func(t *testing.T) {
		fakeStart(t, os.ErrPermission)
		data := t.TempDir()
		t.Setenv("CORAL_DATA_DIR", data)
		require.NoError(t, trigger.WriteMarker(data, "1.5.0", installFolder(t), time.Now()))
		_, out := runOut(t, map[string]string{"CORAL_AUTO_UPDATE": "off"}, "apply")
		assert.Equal(t, true, out["restart_failed"])
	})
	t.Run("run by hand (no marker): nothing was stopped, nothing is started", func(t *testing.T) {
		started := fakeStart(t, nil)
		t.Setenv("CORAL_DATA_DIR", t.TempDir())
		runOut(t, map[string]string{"CORAL_AUTO_UPDATE": "off"}, "apply")
		assert.Empty(t, *started)
	})
	t.Run("check never restarts anything", func(t *testing.T) {
		started := fakeStart(t, nil)
		data := t.TempDir()
		t.Setenv("CORAL_DATA_DIR", data)
		require.NoError(t, trigger.WriteMarker(data, "1.5.0", installFolder(t), time.Now()))
		runOut(t, map[string]string{"CORAL_AUTO_UPDATE": "off"}, "check")
		assert.Empty(t, *started)
	})
	t.Run("the apply is not turned on (a prod build)", func(t *testing.T) {
		if !config.TierChecksForUpdates {
			t.Skip("a build of another tier answers off")
		}
		old := applyEnabled
		t.Cleanup(func() { applyEnabled = old })
		applyEnabled = false // a platform whose switch is off (Windows, today)
		started := fakeStart(t, nil)
		data, install := t.TempDir(), installFolder(t)
		t.Setenv("CORAL_DATA_DIR", data)
		require.NoError(t, trigger.WriteMarker(data, "1.5.0", install, time.Now()))
		_, out := runOut(t, nil, "apply")
		assert.Equal(t, "not_enabled", out["result"])
		assert.Equal(t, []string{install}, *started)
	})
}
