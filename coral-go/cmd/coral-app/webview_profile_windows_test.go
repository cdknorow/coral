//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebViewProfileIsLocalNotRoaming(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", filepath.Join(t.TempDir(), "Roaming"))
	t.Setenv(webviewProfileEnv, "")
	os.Unsetenv(webviewProfileEnv)

	dir := useLocalWebViewProfile()

	assert.Equal(t, filepath.Join(local, "Coral", "WebView2"), dir)
	assert.Equal(t, dir, os.Getenv(webviewProfileEnv), "WebView2 reads the folder from the variable")
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, fi.IsDir())
	assert.False(t, strings.HasPrefix(strings.ToLower(dir), strings.ToLower(os.Getenv("APPDATA"))), "not under the Roaming folder")
}

func TestWebViewProfileKeepsAFolderTheUserChose(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	chosen := filepath.Join(t.TempDir(), "mine")
	t.Setenv(webviewProfileEnv, chosen)

	assert.Equal(t, "", useLocalWebViewProfile())
	assert.Equal(t, chosen, os.Getenv(webviewProfileEnv), "an administrator's or the person's folder is left alone")
}

func TestWebViewProfileWithoutLocalAppDataLeavesTheDefault(t *testing.T) {
	t.Setenv("LOCALAPPDATA", "")
	os.Unsetenv(webviewProfileEnv)
	t.Cleanup(func() { os.Unsetenv(webviewProfileEnv) })

	assert.Equal(t, "", useLocalWebViewProfile())
	assert.Equal(t, "", os.Getenv(webviewProfileEnv))
}

// The variable is read when WebView2 creates its environment, which webview.New does: setting it afterwards does nothing. main must call
// useLocalWebViewProfile before the first webview.New, and nothing before it may create a window.
func TestMainSetsTheWebViewProfileBeforeCreatingTheWindow(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	text := string(src)
	call := strings.Index(text, "useLocalWebViewProfile()")
	newWebview := strings.Index(text, "webview.New(")
	require.True(t, call >= 0, "main.go sets the WebView2 profile folder")
	require.True(t, newWebview >= 0)
	assert.Less(t, call, newWebview, "the profile folder is set before webview.New creates the window")
	assert.Equal(t, 1, strings.Count(text, "webview.New("), "one window, so one place to set the folder before")
}
