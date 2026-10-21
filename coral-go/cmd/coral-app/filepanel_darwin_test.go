//go:build webview && darwin

package main

import (
	"os"
	"runtime"
	"testing"

	webview "github.com/webview/webview_go"
)

// A web view and the window around it must be made on the main thread, and the run loop turned there: the main goroutine is locked to it,
// makes the two windows, and runs the tests on the other threads.
func init() { runtime.LockOSThread() }

var untouched, withPanel webview.WebView

func TestMain(m *testing.M) {
	untouched = webview.New(false)
	withPanel = webview.New(false)
	installFilePanel(withPanel.Window())
	spinRunLoop(0.5) // what webview_go handed over autoreleased is released now, as it is in the app's run loop
	code := m.Run()
	untouched.Destroy()
	withPanel.Destroy()
	os.Exit(code)
}

// #5743: why Import Template and Import from Folder opened nothing on macOS. webview_go gives the web view its UI delegate (the one that
// answers <input type="file">) autoreleased, and WKWebView.UIDelegate is weak: after the run loop has turned, a web view made by the library alone
// has no delegate. If a newer webview_go keeps it, this fails: then drop installFilePanel.
func TestWebviewGoLosesItsFileChooserDelegate(t *testing.T) {
	if got := filePanelState(untouched.Window()); got != 0 {
		t.Fatalf("a web view made by webview_go alone has file chooser state %d, expected 0 (no delegate): the library may keep it now, and installFilePanel is not needed", got)
	}
}

func TestInstalledFileChooserStays(t *testing.T) {
	if got := filePanelState(withPanel.Window()); got != 1 {
		t.Fatalf("the window's web view has file chooser state %d after the run loop turned, expected 1 (a delegate that answers the file chooser)", got)
	}
}
