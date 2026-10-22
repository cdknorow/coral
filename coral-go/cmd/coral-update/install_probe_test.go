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
