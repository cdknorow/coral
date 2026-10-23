package main

import (
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/updater/status"
)

// installer is the part of an apply platform the status needs: where this program is installed and why that install would not be replaced.
type installer interface {
	Install() (string, error)
	Unsupported(install string) string
}

// describeInstall is what "coral-update status" says about the install (#5388): its kind, the apply's fixed word for an install it will not touch, and whether this user can replace it.
// writable answers for the install's folder and the install itself. nil when this program is not in an install at all (a development build).
func describeInstall(p installer, goos string, writable func(path string) bool) *status.Install {
	install, err := p.Install()
	if err != nil {
		return nil
	}
	why := p.Unsupported(install)
	w := writable(filepath.Dir(install)) && writable(install)
	kind := "unwritable"
	switch {
	case why == "homebrew", why == "package_manager" && homebrewFolder(install):
		kind = "homebrew"
	case why == "" && w && goos == "darwin":
		kind = "app"
	case why == "" && w && goos == "linux":
		kind = "tarball"
	}
	return &status.Install{Kind: kind, Unsupported: word(why), Writable: w}
}

// homebrewFolder: a folder Linuxbrew's default prefix owns. (The environment is not read here: coral-update takes no variable but CORAL_AUTO_UPDATE; a Linuxbrew in another prefix is
// still refused by the apply, as a package manager's folder, and is called unwritable here.)
func homebrewFolder(path string) bool {
	return path == "/home/linuxbrew" || strings.HasPrefix(path, "/home/linuxbrew/") // a Linux path: its separator is "/" on whatever machine the test runs (a Windows runner too)
}

// describeRegistered is the status of a Windows copy of Coral against the per-machine product Windows Installer has registered (#6436, found by the Windows route rehearsal): the status
// judged the REGISTERED product's folder, so a copy run from another folder (a portable zip unpacked beside a registered install) was called msi, the click passed the server's
// preflight, and the updater upgraded the registered install instead of the copy that was running. self is the folder of this program (the one the server asked); a copy whose folder is
// not the registered one is portable, and a folder that cannot be read is not called anything the apply would touch.
func describeRegistered(p installer, self string) *status.Install {
	install, err := p.Install()
	if err != nil {
		return &status.Install{Kind: "unregistered", Unsupported: "no_registered_product"}
	}
	if self == "" {
		return &status.Install{Kind: "unwritable", Unsupported: "unreadable_install"}
	}
	if !sameFolder(self, install) {
		return &status.Install{Kind: "portable", Unsupported: "portable_install"}
	}
	switch why := p.Unsupported(install); why {
	case "":
		return &status.Install{Kind: "msi", Writable: true}
	case "portable_install":
		return &status.Install{Kind: "portable", Unsupported: why}
	default:
		return &status.Install{Kind: "unwritable", Unsupported: why}
	}
}

// sameFolder compares two Windows folders as Windows does: cleaned, case-insensitive, either separator, a trailing separator ignored.
func sameFolder(a, b string) bool {
	norm := func(p string) string { return strings.ToLower(strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`)) }
	return norm(a) != "" && norm(a) == norm(b)
}
