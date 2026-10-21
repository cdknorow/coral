//go:build windows

package agent

import "golang.org/x/sys/windows"

// shortPathName is p's 8.3 short form (GetShortPathNameW), or "" when Windows has none, as
// on a volume with 8.3 names turned off, where it returns the long path unchanged.
func shortPathName(p string) string {
	lp, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return ""
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(lp, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}
