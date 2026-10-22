package main

import (
	"os"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallerLaunchRefusedOnlyWhenElevated(t *testing.T) {
	yes, no := func() bool { return true }, func() bool { return false }
	assert.True(t, installerLaunchRefused(true, yes), "the installer's launch from an elevated prompt starts nothing")
	assert.False(t, installerLaunchRefused(true, no), "from a normal prompt it starts Coral as the person")
	assert.False(t, installerLaunchRefused(false, yes), "a start by hand or from the shortcut is the person's own choice")
	assert.False(t, installerLaunchRefused(false, no))
	called := false
	installerLaunchRefused(false, func() bool { called = true; return true })
	assert.False(t, called, "no flag: the token is not even read")
}

// GitHub's Windows runner runs as an elevated administrator: the token check sees it. The dev and CI checkouts elsewhere
// can be either, so nothing is asserted off CI.
func TestProcessElevatedOnTheCIRunner(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("only on GitHub's Windows runner, an elevated administrator")
	}
	assert.True(t, processElevated())
}

func TestProcessElevatedOffWindowsIsFalse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows")
	}
	assert.False(t, processElevated())
}

// The MSI's Launch Coral passes the flag this program refuses elevated starts on: renamed in one place, the installer would
// start Coral as administrator again.
func TestMSILaunchPassesTheInstallerFlag(t *testing.T) {
	wxs, err := os.ReadFile("../../../tools/coral.wxs")
	require.NoError(t, err)
	m := regexp.MustCompile(`<CustomAction Id="LaunchCoral"[^>]*ExeCommand="([^"]*)"[^>]*/>`).FindSubmatch(wxs)
	require.NotNil(t, m, "the LaunchCoral action")
	// The quoted program in its install folder (a Directory-based action), then the flag as its one argument.
	assert.Equal(t, "&quot;[INSTALLFOLDER]coral-tray.exe&quot; --"+installerLaunchFlag, string(m[1]))
}

// #205: Launch Coral works however the person ran the installer, and exactly one action runs, by MsiRunningElevated: a normal client
// starts coral-tray (with the from-installer backstop), an elevated one hands coral-tray to explorer.exe, which starts it unelevated
// through its Elevated-Unelevated Explorer Factory (#199). Both share the checkbox, first-install and not-uninstall conditions.
func TestMSILaunchHasOneActionPerElevation(t *testing.T) {
	wxs, err := os.ReadFile("../../../tools/coral.wxs")
	require.NoError(t, err)
	actions := map[string]string{} // action -> its ExeCommand
	for _, m := range regexp.MustCompile(`<CustomAction Id="(LaunchCoral[A-Za-z]*)"[^>]*ExeCommand="([^"]*)"[^>]*/>`).FindAllSubmatch(wxs, -1) {
		actions[string(m[1])] = string(m[2])
	}
	conditions := map[string]string{} // action -> its Finish condition
	for _, m := range regexp.MustCompile(`<Publish Dialog="ExitDialog" Control="Finish" Event="DoAction" Value="([^"]+)"\s+Condition="([^"]*)"`).FindAllSubmatch(wxs, -1) {
		conditions[string(m[1])] = string(m[2])
	}
	assert.Len(t, conditions, 2, "two launch actions on Finish: one per elevation")
	assert.Equal(t, "&quot;[INSTALLFOLDER]coral-tray.exe&quot; --"+installerLaunchFlag, actions["LaunchCoral"])
	assert.Equal(t, "&quot;[WindowsFolder]explorer.exe&quot; &quot;[INSTALLFOLDER]coral-tray.exe&quot;", actions["LaunchCoralUnelevated"],
		"explorer.exe by its full path, never searched for")
	const common = `WIXUI_EXITDIALOGOPTIONALCHECKBOX = 1 AND NOT Installed AND NOT REMOVE~=&quot;ALL&quot;`
	assert.Equal(t, common+" AND NOT MsiRunningElevated", conditions["LaunchCoral"])
	assert.Equal(t, common+" AND MsiRunningElevated", conditions["LaunchCoralUnelevated"])
	for _, id := range []string{"LaunchCoral", "LaunchCoralUnelevated"} {
		ca := regexp.MustCompile(`<CustomAction Id="` + id + `"[^>]*/>`).Find(wxs)
		require.NotNil(t, ca, id)
		assert.Contains(t, string(ca), `Directory="INSTALLFOLDER"`, "%s starts in the install folder", id)
		assert.Contains(t, string(ca), `Execute="immediate"`, "%s runs in the client, as the person (never deferred)", id)
	}
}

// #199: the Finish button runs LaunchCoral in the installer's client, where Windows Installer fails an asynchronous EXE custom action
// with 1631 and starts nothing (asyncNoWait and asyncWait alike, elevated client or not; measured on Windows 11). Every EXE custom
// action the Finish button publishes must be synchronous.
func TestMSILaunchIsSynchronous(t *testing.T) {
	wxs, err := os.ReadFile("../../../tools/coral.wxs")
	require.NoError(t, err)
	published := regexp.MustCompile(`<Publish Dialog="ExitDialog" Control="Finish" Event="DoAction" Value="([^"]+)"`).FindAllSubmatch(wxs, -1)
	require.NotEmpty(t, published, "the Finish button's launch")
	for _, p := range published {
		ca := regexp.MustCompile(`<CustomAction Id="` + regexp.QuoteMeta(string(p[1])) + `"[^>]*/>`).Find(wxs)
		require.NotNil(t, ca, "the custom action %s", p[1])
		ret := regexp.MustCompile(`Return="([^"]*)"`).FindSubmatch(ca)
		require.NotNil(t, ret, "%s names its Return (the default, check, would fail the install on a non-zero exit)", p[1])
		assert.Equal(t, "ignore", string(ret[1]), "%s must be synchronous and unchecked: an async one is never started from the client", p[1])
	}
}

// An XML comment cannot hold a double hyphen: wix build stops with WIX0104 and the release has no MSI (QA's run 39717624469 on the
// first version of the launch comment, which quoted the flag). This runs everywhere; the Windows job builds the MSI when the file changes.
func TestWxsCommentsHaveNoDoubleHyphen(t *testing.T) {
	wxs, err := os.ReadFile("../../../tools/coral.wxs")
	require.NoError(t, err)
	comments := regexp.MustCompile(`(?s)<!--(.*?)-->`).FindAllSubmatch(wxs, -1)
	require.NotEmpty(t, comments)
	for _, c := range comments {
		assert.NotContains(t, string(c[1]), "--", "an XML comment in tools/coral.wxs holds a double hyphen: %.60q", c[1])
	}
}

// The person is told why, in words that name the cause and the way on: the installer was run as administrator, and Coral opens from the Start Menu.
func TestTheElevatedInstallerNoteSaysWhyAndWhatToDo(t *testing.T) {
	assert.Contains(t, elevatedInstallerNote, "administrator")
	assert.Contains(t, elevatedInstallerNote, "Start Menu")
	assert.Contains(t, elevatedInstallerNote, "not started")
	assert.NotEmpty(t, elevatedInstallerNoteTitle)
	assert.Less(t, len(elevatedInstallerNote), 300, "a message box, not a page")
}
