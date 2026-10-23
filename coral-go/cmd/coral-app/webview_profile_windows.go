//go:build webview && windows

package main

import (
	"log"
	"os"
	"path/filepath"
)

// webviewProfileEnv is the variable WebView2 reads for its user data folder; it overrides the folder the webview library asks for, which
// is %APPDATA%\coral-app.exe\EBWebView, the Roaming folder.
const webviewProfileEnv = "WEBVIEW2_USER_DATA_FOLDER"

// useLocalWebViewProfile points WebView2 at a folder under %LOCALAPPDATA%\Coral, before the window is created, so the dashboard's sign-in
// cookie, cache and local storage stay on this computer: the Roaming folder is copied to a profile server and into its backups. A folder the
// person or an administrator already set in the variable is theirs and stays. It returns the folder the profile is in, or "" when it
// left WebView2 to its default (no LOCALAPPDATA, or the folder could not be made).
func useLocalWebViewProfile() string {
	if set := os.Getenv(webviewProfileEnv); set != "" {
		log.Printf("[WEBVIEW] %s is already set; the profile stays there", webviewProfileEnv)
		return ""
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		log.Printf("[WEBVIEW] no LOCALAPPDATA: the profile is in WebView2's default folder")
		return ""
	}
	dir := filepath.Join(local, "Coral", "WebView2")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[WEBVIEW] the local profile folder could not be made (%v): the profile is in WebView2's default folder", err)
		return ""
	}
	if err := os.Setenv(webviewProfileEnv, dir); err != nil {
		log.Printf("[WEBVIEW] %s could not be set (%v): the profile is in WebView2's default folder", webviewProfileEnv, err)
		return ""
	}
	return dir
}
