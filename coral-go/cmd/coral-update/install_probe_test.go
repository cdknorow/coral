package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeInstaller struct {
	path, why string
	err       error
}

func (f fakeInstaller) Install() (string, error)  { return f.path, f.err }
func (f fakeInstaller) Unsupported(string) string { return f.why }

// What the status says about an install (#5388): the kind the dashboard and the hub map, the apply's own word for a refusal, and whether this user can replace it.
func TestDescribeInstallNamesTheKindTheApplyWouldTreatItAs(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	for name, tc := range map[string]struct {
		p        fakeInstaller
		goos     string
		writable func(string) bool
		kind     string
		why      string
		w        bool
	}{
		"a Mac app the user can replace":              {fakeInstaller{path: "/Applications/Coral.app"}, "darwin", yes, "app", "", true},
		"a Mac app in a folder the user cannot write": {fakeInstaller{path: "/Applications/Coral.app"}, "darwin", no, "unwritable", "", false},
		"a Homebrew cask":                             {fakeInstaller{path: "/Applications/Coral.app", why: "homebrew"}, "darwin", yes, "homebrew", "homebrew", true},
		"a volume without an atomic swap":             {fakeInstaller{path: "/Volumes/x/Coral.app", why: "no_atomic_swap"}, "darwin", yes, "unwritable", "no_atomic_swap", true},
		"a Linux tarball folder":                      {fakeInstaller{path: "/home/u/coral"}, "linux", yes, "tarball", "", true},
		"a Linux package manager's folder":            {fakeInstaller{path: "/usr/lib/coral", why: "package_manager"}, "linux", yes, "unwritable", "package_manager", true},
		"Linuxbrew":                                   {fakeInstaller{path: "/home/linuxbrew/.linuxbrew/lib/coral", why: "package_manager"}, "linux", yes, "homebrew", "package_manager", true},
		"a Linux folder owned by another user":        {fakeInstaller{path: "/opt/coral", why: "not_owned"}, "linux", no, "unwritable", "not_owned", false},
	} {
		got := describeInstall(tc.p, tc.goos, tc.writable)
		if assert.NotNil(t, got, name) {
			assert.Equal(t, tc.kind, got.Kind, name)
			assert.Equal(t, tc.why, got.Unsupported, name)
			assert.Equal(t, tc.w, got.Writable, name)
		}
	}
	assert.Nil(t, describeInstall(fakeInstaller{err: errors.New("not in an install")}, "darwin", yes), "a development build is in no install")
}

// #6436: a Windows copy is judged against the registered product only when it is the registered folder; a copy run from another folder, beside a registered install, is portable, so the
// click never upgrades the registered install while the copy is the one running.
func TestACopyRunFromAnotherFolderIsPortableWhateverIsRegistered(t *testing.T) {
	registered := fakeInstaller{path: `C:\Program Files\Coral`}
	for name, tc := range map[string]struct {
		p     fakeInstaller
		self  string
		kind  string
		why   string
		write bool
	}{
		"the registered folder itself":              {registered, `C:\Program Files\Coral`, "msi", "", true},
		"the same folder in another case and slash": {registered, `c:/program files/coral/`, "msi", "", true},
		"a portable zip unpacked beside it":         {registered, `C:\Users\x\Downloads\Coral`, "portable", "portable_install", false},
		"a folder inside the registered one":        {registered, `C:\Program Files\Coral\bin`, "portable", "portable_install", false},
		"a folder that cannot be read":              {registered, "", "unwritable", "unreadable_install", false},
		"no registered product":                     {fakeInstaller{err: errors.New("none")}, `C:\Users\x\Coral`, "unregistered", "no_registered_product", false},
		"registered but not under Program Files":    {fakeInstaller{path: `D:\Coral`, why: "portable_install"}, `D:\Coral`, "portable", "portable_install", false},
	} {
		got := describeRegistered(tc.p, tc.self)
		if assert.NotNil(t, got, name) {
			assert.Equal(t, tc.kind, got.Kind, name)
			assert.Equal(t, tc.why, got.Unsupported, name)
			assert.Equal(t, tc.write, got.Writable, name)
		}
	}
}
