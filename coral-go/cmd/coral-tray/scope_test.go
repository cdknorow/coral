package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The tray's files live in the server's data folder (#3463).
func TestGetDataDirFollowsTheServer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CORAL_DIR", "")
	t.Setenv("CORAL_DATA_DIR", dir)
	assert.Equal(t, dir, getDataDir())
	t.Setenv("CORAL_DATA_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	assert.Equal(t, filepath.Join(home, ".coral"), getDataDir(), "the default is unchanged")
}

// Only a coral-app started for this data folder is this tray's to stop (#3463).
func TestAppForDataDir(t *testing.T) {
	mine := "/tmp/coral-test/data"
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"coral-app", "--url", "http://localhost:8471", "--data-dir", mine}, true},
		{[]string{"coral-app", "--data-dir=" + mine + "/"}, true},
		{[]string{"coral-app", "-data-dir", mine}, true},
		{[]string{"coral-app", "--url", "http://localhost:8420", "--data-dir", "/Users/me/.coral"}, false}, // the operator's window
		{[]string{"coral-app", "--url", "http://localhost:8420"}, false},                                   // no data folder: not ours to judge
		{[]string{"coral-app", "--data-dir"}, false},
		{[]string{"coral-app", "--data-dir", mine + "-other"}, false},
	} {
		assert.Equal(t, tc.want, appForDataDir(tc.args, mine), "%v", tc.args)
	}
}
