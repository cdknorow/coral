package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5029: the tray runs the coral-app installed beside it and nothing else. A coral-app elsewhere on PATH is never found.
func TestSiblingCoralAppIsTheOnlyOneFound(t *testing.T) {
	name := "coral-app"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	install := t.TempDir()
	other := t.TempDir() // a folder on PATH with its own coral-app
	require.NoError(t, os.WriteFile(filepath.Join(other, name), []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", other+string(os.PathListSeparator)+os.Getenv("PATH"))
	exe := filepath.Join(install, "coral-tray")

	assert.Empty(t, siblingCoralApp(exe), "no coral-app beside the tray: none is found, PATH is not searched")

	require.NoError(t, os.WriteFile(filepath.Join(install, name), []byte("#!/bin/sh\n"), 0o755))
	assert.Equal(t, filepath.Join(install, name), siblingCoralApp(exe), "the one beside the tray")

	require.NoError(t, os.Remove(filepath.Join(install, name)))
	require.NoError(t, os.Mkdir(filepath.Join(install, name), 0o755))
	assert.Empty(t, siblingCoralApp(exe), "a folder of that name is not a program")
}

// launchCoralApp with no coral-app beside the executable says so (the test binary has none beside it) and starts nothing.
func TestLaunchCoralAppWithNoSiblingIsAnError(t *testing.T) {
	name := "coral-app"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, name), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", other)
	exe, err := os.Executable()
	require.NoError(t, err)
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(exe), name)); statErr == nil {
		t.Skip("a coral-app sits beside the test binary")
	}
	err = launchCoralApp("http://127.0.0.1:1", func() (string, error) { return "", nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found next to the Coral tray")
	assert.Nil(t, coralAppProcess, "nothing was started")
}
