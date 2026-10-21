//go:build windows

package routes

import "golang.org/x/sys/windows"

// shortPathOf is path's 8.3 form (C:\Users\JOHNSM~1), or "" when it has none or the call fails. os.TempDir() and some tools spell the home
// that way when the user name is over 8 characters, and the short form names the account too (#126 F5).
func shortPathOf(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	n, err := windows.GetShortPathName(p, nil, 0)
	if err != nil || n == 0 {
		return ""
	}
	buf := make([]uint16, n)
	n, err = windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}
