//go:build windows

package main

import (
	"log"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/subgentic/coral/internal/appmarker"
	"github.com/subgentic/coral/internal/config"
)

// webviewArgsEnv is the variable WebView2 reads for extra browser arguments, added to the ones the webview library passes.
const webviewArgsEnv = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"

// webview2ClientKey is where the WebView2 Runtime records its version ("pv"), per machine (both registry views) or per user.
const webview2ClientKey = `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

// webview2RuntimeVersion is the installed WebView2 Runtime's version ("141.0.3537.71"), or "".
func webview2RuntimeVersion() string {
	for _, c := range []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`},
		{registry.LOCAL_MACHINE, webview2ClientKey},
		{registry.CURRENT_USER, webview2ClientKey},
	} {
		k, err := registry.OpenKey(c.root, c.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue("pv")
		k.Close()
		if err == nil && versionOK(v) {
			return v
		}
	}
	return ""
}

// versionOK: digits and dots only, so the version can sit in a User-Agent and on a command line.
func versionOK(v string) bool {
	if v == "" || strings.Trim(v, "0123456789.") != "" {
		return false
	}
	return v[0] != '.'
}

// windowsAppUserAgent is the User-Agent WebView2 sends (Edge's, with the runtime's version) with the app's marker added. "" when the runtime's
// version is unknown: then the web view keeps its own User-Agent and the sessions are not marked.
func windowsAppUserAgent(runtimeVersion, appVersion string) string {
	if !versionOK(runtimeVersion) {
		return ""
	}
	major := runtimeVersion
	if i := strings.IndexByte(major, '.'); i > 0 {
		major = major[:i]
	}
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + major + ".0.0.0 Safari/537.36 Edg/" +
		runtimeVersion + " " + appmarker.Marker(appVersion)
}

// markAppUserAgentBeforeWindow adds the app's marker to the web view's User-Agent, before the window is created (WebView2 reads the
// variable when it creates its environment), so the Sessions page can name the app's sign-ins "Coral app". It returns whether it set it.
func markAppUserAgentBeforeWindow() bool {
	ua := windowsAppUserAgent(webview2RuntimeVersion(), config.ReportedVersion())
	if ua == "" {
		log.Printf("[WEBVIEW] the WebView2 version is unknown: sessions made in this window are not marked as the app's")
		return false
	}
	arg := `--user-agent="` + ua + `"`
	if old := os.Getenv(webviewArgsEnv); old != "" {
		arg = old + " " + arg
	}
	if err := os.Setenv(webviewArgsEnv, arg); err != nil {
		log.Printf("[WEBVIEW] %s could not be set (%v)", webviewArgsEnv, err)
		return false
	}
	return true
}
