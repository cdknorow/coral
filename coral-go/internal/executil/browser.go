package executil

import (
	"os/exec"
	"runtime"
)

// OpenBrowser opens url in the user's browser.
func OpenBrowser(url string) {
	argv := browserCommand(runtime.GOOS, url)
	cmd := exec.Command(argv[0], argv[1:]...)
	if runtime.GOOS == "windows" {
		HideWindow(cmd)
	}
	cmd.Start()
}

// browserCommand is the argv that opens url on goos. On Windows the URL goes to the URL
// protocol handler directly, never through cmd.exe, which would read '&' or '|' in it as a
// command separator (#3690, #3689 finding 3).
func browserCommand(goos, url string) []string {
	switch goos {
	case "darwin":
		return []string{"open", url}
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", url}
	default:
		return []string{"xdg-open", url}
	}
}
