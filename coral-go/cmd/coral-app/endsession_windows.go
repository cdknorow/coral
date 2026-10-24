//go:build webview && windows

package main

import (
	"log"

	"github.com/subgentic/coral/internal/endsession"
)

// watchEndSession lets Windows close this window the normal way (#335): Restart Manager, which an installer uses, and the end of the session
// ask the process's top-level windows with WM_ENDSESSION, which the webview's own window ignores; a hidden window of ours answers by ending the
// event loop, as SIGTERM does.
func watchEndSession(terminate func()) {
	if err := endsession.Watch("Coral window", func(closeApp bool) {
		log.Printf("[SHUTDOWN] Windows asked the window to close (installer: %v)", closeApp)
		terminate()
	}); err != nil {
		log.Printf("[ENDSESSION] %v", err)
	}
}
