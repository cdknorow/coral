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
	assert.Equal(t, "--"+installerLaunchFlag, string(m[1]))
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
