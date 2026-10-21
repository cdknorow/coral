package executil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// OpenBrowser opens url in the user's browser. Only a plain http or https address (WebURL) is
// handed to the system's opener, which would otherwise open or run a file or start a custom
// scheme's handler (#78 T1); anything else is not opened. A file Coral wants shown (its log) goes
// through OpenFile.
func OpenBrowser(url string) {
	u, ok := WebURL(url)
	if !ok {
		return
	}
	argv := browserCommand(runtime.GOOS, u)
	cmd := exec.Command(argv[0], argv[1:]...)
	if runtime.GOOS == "windows" {
		HideWindow(cmd)
	}
	cmd.Start()
}

// browserCommand is the argv that opens url on goos. On Windows the URL goes to the URL
// protocol handler directly, never through cmd.exe, which would read '&' or '|' in it as a
// command separator (#3690, #3689 finding 3). The handler is named by its absolute path in the
// system folder, so a rundll32 elsewhere on PATH is never the one started (#78 T1).
func browserCommand(goos, url string) []string {
	switch goos {
	case "darwin":
		return []string{"open", url}
	case "windows":
		return []string{systemRundll32(), "url.dll,FileProtocolHandler", url}
	default:
		return []string{"xdg-open", url}
	}
}

// insideDir reports whether path, cleaned, is below dir, cleaned (case-insensitive on Windows, where file names are).
func insideDir(dir, path string) bool {
	dir, path = filepath.Clean(dir), filepath.Clean(path)
	if runtime.GOOS == "windows" {
		dir, path = strings.ToLower(dir), strings.ToLower(path)
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// openableFileExts are the files OpenFile shows: Coral's own logs.
var openableFileExts = map[string]bool{".log": true, ".txt": true}

// OpenFile shows a log file Coral wrote (the tray's "Open Log") in the system's default program:
// a regular file inside dir (Coral's data folder) with a .log or .txt extension, never a link, a
// program or a script, and nothing outside dir (the opener runs what it is given, #78 T1). path and
// dir must be absolute. It reports whether it started the opener.
func OpenFile(dir, path string) bool {
	if dir == "" || path == "" || !filepath.IsAbs(dir) || !filepath.IsAbs(path) || strings.HasPrefix(path, "-") ||
		!openableFileExts[strings.ToLower(filepath.Ext(path))] || !insideDir(dir, path) {
		return false
	}
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		argv = []string{"open", "-t", path}
	case "windows":
		argv = []string{systemRundll32(), "url.dll,FileProtocolHandler", path}
	default:
		argv = []string{"xdg-open", path}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if runtime.GOOS == "windows" {
		HideWindow(cmd)
	}
	return cmd.Start() == nil
}
