package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/trigger"
	"github.com/subgentic/coral/internal/updater/verify"
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
	// A marker older than its life is a click whose updater started late: the server quit all the same (#332), so Coral is started from the folder it names.
	require.NoError(t, trigger.WriteMarker(data, "1.5.0", install, time.Now().Add(-trigger.MarkerTTL-time.Minute)))
	attempted, ok := restartAfterNothing(data, time.Now())
	assert.True(t, attempted, "a late click")
	assert.True(t, ok)
	assert.Equal(t, []string{install}, *started)
	*started = nil

	restartGOOS = "linux"
	require.NoError(t, trigger.WriteMarker(data, "1.5.0", install, time.Now()))
	attempted, _ = restartAfterNothing(data, time.Now())
	assert.False(t, attempted, "off Windows there is nothing to start")
	_, err := trigger.Consume(data, time.Now())
	assert.NoError(t, err, "and the marker is left alone")
}

func TestRestartAfterNothingSaysWhenItCouldNotStartCoral(t *testing.T) {
	registered(t, "", os.ErrNotExist) // no registered install to fall back on
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
		"no registered product":            {apply.Result{Result: apply.ResultUnsupported, FailedStage: "no_registered_product"}, true},
		"a portable install":               {apply.Result{Result: apply.ResultUnsupported, FailedStage: "portable_install"}, true},
		"an unreadable install":            {apply.Result{Result: apply.ResultUnsupported, FailedStage: "unreadable_install"}, true},
		"the apply lock could not be made": {apply.Result{Result: apply.ResultFailed, FailedStage: "apply_lock"}, true},
		"the updates folder":               {apply.Result{Result: apply.ResultFailed, FailedStage: "updates_folder"}, true},
		"a later failure":                  {apply.Result{Result: apply.ResultFailed, FailedStage: "copy"}, false}, // RunMSI started the old Coral itself
		"busy (a check)":                   {apply.Result{Result: apply.ResultBusy, FailedStage: apply.BusyCheck}, true},
		"busy (another apply)":             {apply.Result{Result: apply.ResultBusy, FailedStage: apply.BusyApply}, false}, // it is replacing the install: nothing starts under it
		"busy (older answer)":              {apply.Result{Result: apply.ResultBusy}, true},                                // the server quit for the click: nothing was changed (#323)
		"relaunched":                       {apply.Result{Result: apply.ResultRelaunched}, false},
		"declined in the prompt":           {apply.Result{Result: apply.ResultDeclined}, false},
		"coral still running":              {apply.Result{Result: apply.ResultCoralRunning}, true}, // a program of the install folder outlived the wait (#323)
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
		started := fakeStart(t, nil)
		data, install := t.TempDir(), installFolder(t)
		t.Setenv("CORAL_DATA_DIR", data)
		require.NoError(t, trigger.WriteMarker(data, "1.5.0", install, time.Now()))
		_, out := runOut(t, nil, "apply")
		assert.Equal(t, "not_enabled", out["result"])
		assert.Equal(t, []string{install}, *started)
	})
}

// registered stands for the folder Windows Installer registered.
func registered(t *testing.T, dir string, err error) {
	t.Helper()
	old := registeredInstall
	registeredInstall = func() (string, error) { return dir, err }
	t.Cleanup(func() { registeredInstall = old })
}

// #332 (QA's gap A): a click whose marker is late (request_expired) or damaged (request_invalid) still has no Coral running; the apply starts it again, and says when it cannot.
func TestALateOrDamagedClickStartsCoralAgain(t *testing.T) {
	t.Setenv("CORAL_AUTO_UPDATE", "")
	for name, tc := range map[string]struct {
		write      func(t *testing.T, data, marked string)
		wantResult string
		fromMarker bool
	}{
		"expired": {func(t *testing.T, data, marked string) {
			require.NoError(t, trigger.WriteMarker(data, "1.5.0", marked, time.Now().Add(-trigger.MarkerTTL-time.Minute)))
		}, resultRequestExpired, true},
		"damaged": {func(t *testing.T, data, _ string) {
			require.NoError(t, os.MkdirAll(filepath.Join(data, "updates"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(data, "updates", "install-trigger.json"), []byte("{not json"), 0o600))
		}, resultRequestInvalid, false},
	} {
		t.Run(name, func(t *testing.T) {
			started := fakeStart(t, nil)
			marked, reg := installFolder(t), installFolder(t)
			registered(t, reg, nil)
			data := t.TempDir()
			tc.write(t, data, marked)
			require.Equal(t, tc.wantResult, installRequest(data, time.Now()))
			want := reg
			if tc.fromMarker {
				want = marked
			}
			assert.True(t, restartFromMarker(data, clickedInstallDir))
			assert.Equal(t, []string{want}, *started, "an expired marker names its folder; a damaged one has none, so the registered install is used")
		})
	}
	t.Run("no marker at all: nothing was stopped, nothing is started", func(t *testing.T) {
		started := fakeStart(t, nil)
		registered(t, installFolder(t), nil)
		assert.Equal(t, resultNotRequested, installRequest(t.TempDir(), time.Now()))
		assert.Empty(t, *started)
	})
}

// #332: a marker with no folder (an older server's) is started from the registered install; with none registered the answer says it failed.
func TestAMarkerWithNoFolderUsesTheRegisteredInstall(t *testing.T) {
	started := fakeStart(t, nil)
	reg := installFolder(t)
	registered(t, reg, nil)
	data := t.TempDir()
	require.NoError(t, trigger.WriteMarker(data, "1.5.0", "", time.Now()))
	attempted, ok := restartAfterNothing(data, time.Now())
	assert.True(t, attempted)
	assert.True(t, ok)
	assert.Equal(t, []string{reg}, *started)

	registered(t, "", os.ErrNotExist)
	require.NoError(t, trigger.WriteMarker(data, "1.5.0", "", time.Now()))
	attempted, ok = restartAfterNothing(data, time.Now())
	assert.True(t, attempted)
	assert.False(t, ok, "nothing registered: said, not hidden")
	assert.Len(t, *started, 1)
	registered(t, "relative", nil)
	assert.False(t, restartFromMarker(data, ""), "a registered folder that is not absolute is no folder")
}

// #332 (QA's gap B): the trusted root cannot be loaded (offline, the cached root expired) after a click: the apply says so and starts Coral again.
func TestAnUnloadableTrustedRootStartsCoralAgain(t *testing.T) {
	oldLoad, oldVersion, oldTier, oldApply := loadTrust, config.Version, config.TierChecksForUpdates, applyEnabled
	t.Cleanup(func() {
		loadTrust, config.Version, config.TierChecksForUpdates, applyEnabled = oldLoad, oldVersion, oldTier, oldApply
	})
	applyEnabled = true
	loadTrust = func(context.Context, string) (root.TrustedMaterial, verify.TrustSource, error) {
		return nil, "", errors.New("offline")
	}
	config.Version, config.TierChecksForUpdates = "1.4.130", true
	started := fakeStart(t, nil)
	data, install := t.TempDir(), installFolder(t)
	t.Setenv("CORAL_DATA_DIR", data)
	require.NoError(t, trigger.WriteMarker(data, "1.5.0", install, time.Now()))
	code, out := runOut(t, nil, "apply")
	assert.Equal(t, 0, code)
	assert.Equal(t, "failed", out["result"])
	assert.Equal(t, "trusted_root", out["failed_stage"])
	assert.NotContains(t, out, "restart_failed")
	assert.Equal(t, []string{install}, *started)

	*started = nil
	_, out = runOut(t, nil, "check")
	assert.Equal(t, "trusted_root", out["failed_stage"])
	assert.Empty(t, *started, "a check stopped nothing")
}
