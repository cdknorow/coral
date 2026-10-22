package main

// installerLaunchFlag is on the command line of the one start the MSI's "Launch Coral" makes (tools/coral.wxs, #5883).
const installerLaunchFlag = "from-installer"

// installerLaunchRefused reports whether the installer's launch must start nothing: it runs in the installer's client process, as
// whoever ran the installer, so from an elevated prompt (msiexec as administrator) Coral would start elevated, and its server,
// agents and their tools would run as an administrator. A person who wants Coral elevated has the Start Menu entry and Run as
// administrator; the installer never decides it for them. Started by hand or from the shortcut (no flag), Coral starts as before.
func installerLaunchRefused(fromInstaller bool, elevated func() bool) bool {
	return fromInstaller && elevated()
}

// elevatedInstallerNote is what the person is told when the installer's "Launch Coral" is refused because the installer was run as administrator (#6004's W2): without it
// the Finish page's ticked "Launch Coral" leads to nothing and no word.
const (
	elevatedInstallerNoteTitle = "Coral"
	elevatedInstallerNote      = "Coral is installed, but it was not started: the installer was run as administrator, and Coral does not start with administrator rights.\n\nOpen Coral from the Start Menu."
)
