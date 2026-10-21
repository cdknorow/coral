package routes

import (
	"regexp"
	"runtime"
	"strings"
)

// gitBashDrive is a Git Bash path on a drive: /c/Users/x is C:\Users\x. Claude Code's Bash tool runs in Git Bash on
// Windows, where "/" is Git's own root and "/c", "/d"... are the drives.
var gitBashDrive = regexp.MustCompile(`^/([A-Za-z])(/.*)?$`)

// rootedPath says whether p names a place by its root rather than relative to the working folder, and, when it can
// be read as a path on a drive, that path. On Windows filepath.IsAbs("/etc") and IsAbs(`\Windows`) are false (no
// drive letter), so such a path used to be joined onto the working folder and judged inside it: `rm -rf /` was
// rated safe. Rooted on Windows: a drive path (C:\x, C:/x), a drive-relative one (C:, C:x: the drive's current
// folder, never readable here), anything starting with / or \ (Git Bash /c/x is the drive path C:\x; /etc,
// \Windows, \\server\share, //server/share and \\?\C:\x have no drive to compare, so abs is ""). A caller takes a
// rooted path with an empty abs as outside the working folder.
func rootedPath(goos, p string) (rooted bool, abs string) {
	if p == "" {
		return false, ""
	}
	if goos != "windows" {
		if strings.HasPrefix(p, "/") {
			return true, p
		}
		return false, ""
	}
	isLetter := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
	switch {
	case len(p) >= 3 && isLetter(p[0]) && p[1] == ':' && (p[2] == '/' || p[2] == '\\'):
		return true, p
	case len(p) >= 2 && isLetter(p[0]) && p[1] == ':':
		return true, "" // C: or C:foo
	}
	if m := gitBashDrive.FindStringSubmatch(p); m != nil {
		rest := strings.ReplaceAll(m[2], "/", `\`)
		if rest == "" {
			rest = `\`
		}
		return true, strings.ToUpper(m[1]) + ":" + rest
	}
	if p[0] == '/' || p[0] == '\\' {
		return true, ""
	}
	return false, ""
}

// rooted is rootedPath for this OS, without the path it reads as.
func rooted(p string) bool {
	r, _ := rootedPath(runtime.GOOS, p)
	return r
}

// unreadablePath: text a shell expands (a variable, a command, ~ or ~user; on Windows also %VAR% for %USERPROFILE%,
// %SystemRoot% and the like) is not a path Coral can read literally, so it counts as outside the working folder.
func unreadablePath(p string) bool {
	return strings.ContainsAny(p, "$`~") || (runtime.GOOS == "windows" && strings.Contains(p, "%"))
}
