//go:build windows

package executil

import (
	"fmt"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// maxStartDirUnits is the longest working folder (in UTF-16 units) CreateProcess accepts: a path of 259 fails with "The directory name is invalid", the
// "long paths" setting and a \?\ prefix notwithstanding (measured on Windows 11: 258 starts, 259 does not).
const maxStartDirUnits = 258

// StartDir is dir as a program can be started in it. A folder whose path is too long for CreateProcess is given as its 8.3 short path when it has one;
// otherwise the error says why, instead of Windows' "The directory name is invalid".
func StartDir(dir string) (string, error) {
	n := len(utf16.Encode([]rune(dir)))
	if n <= maxStartDirUnits {
		return dir, nil
	}
	if short := shortPath(dir); short != "" && len(utf16.Encode([]rune(short))) <= maxStartDirUnits {
		return short, nil
	}
	return "", fmt.Errorf("the folder %s is %d characters long, and Windows can't start a program in a folder whose path is longer than %d (long-path support does not lift this limit): use a shorter path", dir, n, maxStartDirUnits)
}

// shortPath is p's 8.3 short form, or "" when there is none (a volume with 8.3 names off gives the long path back).
func shortPath(p string) string {
	lp, err := windows.UTF16PtrFromString(`\?\` + p)
	if err != nil {
		return ""
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(lp, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return ""
	}
	s := windows.UTF16ToString(buf[:n])
	if len(s) > 4 && s[:4] == `\?\` {
		s = s[4:]
	}
	return s
}
