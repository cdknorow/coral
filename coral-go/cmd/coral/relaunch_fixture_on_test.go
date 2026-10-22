//go:build relaunchfixture

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

func goodFixtureFolder(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fx")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.Chmod(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "relaunch.json"), []byte(`{"installed":"1.4.133"}`), 0o600))
	t.Setenv("CORAL_RELAUNCH_FIXTURE_DIR", dir)
	return dir
}

// Security's condition 5: a fixture build listens on this computer only, connects to no hub unless the test names one, and does not start without a valid fixture folder.
func TestAFixtureBuildRefusesToBeExposedOrToStartWithoutAFixture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the relaunch fixture is for macOS and Linux (#175): on Windows a fixture build refuses to start, which TestAFixtureBuildRefusesToStartOnWindows checks")
	}
	goodFixtureFolder(t)
	no := false
	yes := true

	cfg := &config.Config{Host: "", CloudURL: "https://app.coralhub.dev"}
	require.NoError(t, applyRelaunchFixtureBuild(cfg, "", &no, false))
	assert.Equal(t, "127.0.0.1", cfg.Host, "no host: this computer only")
	assert.Empty(t, cfg.CloudURL, "no --cloud-url: no hub, whatever the environment or the default says")
	assert.False(t, cfg.CloudURLExplicit)

	cfg = &config.Config{CloudURL: "http://127.0.0.1:9"}
	require.NoError(t, applyRelaunchFixtureBuild(cfg, "127.0.0.1,::1,localhost", &no, true))
	assert.Equal(t, "http://127.0.0.1:9", cfg.CloudURL, "a hub the test names itself is kept")

	for _, host := range []string{"0.0.0.0", "192.168.1.5", "example.invalid", "127.0.0.1,0.0.0.0", "::"} {
		assert.Error(t, applyRelaunchFixtureBuild(&config.Config{}, host, &no, false), "--host %s", host)
	}
	assert.Error(t, applyRelaunchFixtureBuild(&config.Config{}, "", &yes, false), "--remote")

	t.Setenv("CORAL_RELAUNCH_FIXTURE_DIR", "")
	assert.Error(t, applyRelaunchFixtureBuild(&config.Config{}, "", &no, false), "no fixture folder: the server does not start")
	dir := goodFixtureFolder(t)
	require.NoError(t, os.Chmod(dir, 0o770))
	assert.Error(t, applyRelaunchFixtureBuild(&config.Config{}, "", &no, false), "a folder that is not owner-only: the server does not start")
}

// #5456: a fixture build never runs on the default data folder (~/.coral), spelled directly or through a link.
func TestAFixtureBuildRefusesTheDefaultDataFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the relaunch fixture is for macOS and Linux (#175): on Windows a fixture build refuses to start, which TestAFixtureBuildRefusesToStartOnWindows checks")
	}
	goodFixtureFolder(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	live := filepath.Join(home, ".coral")
	require.NoError(t, os.MkdirAll(live, 0o700))
	no := false

	cfg := &config.Config{}
	cfg.UseCoralDir(live)
	assert.Error(t, applyRelaunchFixtureBuild(cfg, "", &no, false), "the default folder")
	link := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.Symlink(live, link))
	cfg = &config.Config{}
	cfg.UseCoralDir(link)
	assert.Error(t, applyRelaunchFixtureBuild(cfg, "", &no, false), "the default folder through a link")

	own := filepath.Join(t.TempDir(), "own")
	require.NoError(t, os.MkdirAll(own, 0o700))
	cfg = &config.Config{}
	cfg.UseCoralDir(own)
	assert.NoError(t, applyRelaunchFixtureBuild(cfg, "", &no, false), "a folder of its own")
}

// #175: a fixture build on Windows refuses to start, saying why (Windows never relaunches from Coral Hub).
func TestAFixtureBuildRefusesToStartOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	no := false
	t.Setenv("CORAL_RELAUNCH_FIXTURE_DIR", t.TempDir())
	err := applyRelaunchFixtureBuild(&config.Config{}, "", &no, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "macOS and Linux")
}
