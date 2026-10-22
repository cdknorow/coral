//go:build windows

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/appmarker"
)

// #6100: WebView2 sends Edge's User-Agent; the app adds its marker so the Sessions page names the app's sign-ins "Coral app".
func TestWindowsAppUserAgentIsEdgesWithTheMarker(t *testing.T) {
	ua := windowsAppUserAgent("141.0.3537.71", "1.4.157")
	assert.Equal(t, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.3537.71 CoralApp/1.4.157", ua)
	assert.True(t, appmarker.In(ua))
	assert.False(t, strings.ContainsAny(ua, "\"\r\n"), "it sits inside one quoted command-line argument")
}

func TestWindowsAppUserAgentNeedsAKnownRuntimeVersion(t *testing.T) {
	for _, v := range []string{"", ".141", "141.0.3537.71\"--evil", "141 0", "abc"} {
		assert.Equal(t, "", windowsAppUserAgent(v, "1.4.157"), "version %q: the web view keeps its own User-Agent", v)
	}
}

func TestMarkingTheAppUserAgentAddsToTheArgumentsAlreadySet(t *testing.T) {
	if webview2RuntimeVersion() == "" {
		t.Skip("no WebView2 Runtime on this machine")
	}
	t.Setenv(webviewArgsEnv, "--mute-audio")
	require.True(t, markAppUserAgentBeforeWindow())
	got := os.Getenv(webviewArgsEnv)
	assert.True(t, strings.HasPrefix(got, "--mute-audio --user-agent=\"Mozilla/5.0 "), "an argument that was set stays first: %s", got)
	assert.True(t, appmarker.In(got), "the marker is in the arguments: %s", got)
	assert.True(t, strings.HasSuffix(got, "\""), "the argument is closed: %s", got)
}

// WebView2 reads the variable when it creates its environment, which webview.New does: main must mark the User-Agent before it.
func TestMainMarksTheUserAgentBeforeCreatingTheWindow(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	text := string(src)
	call := strings.Index(text, "markAppUserAgentBeforeWindow()")
	newWebview := strings.Index(text, "webview.New(")
	require.True(t, call >= 0, "main.go marks the app's User-Agent")
	require.True(t, newWebview >= 0)
	assert.Less(t, call, newWebview, "the marker is set before webview.New creates the window")
	assert.Contains(t, text, "markAppUserAgent(w.Window())", "and the macOS call after the window exists")
}
