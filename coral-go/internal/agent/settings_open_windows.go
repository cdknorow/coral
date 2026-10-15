//go:build windows

package agent

// settingsOpenFlags: Windows has neither O_NOFOLLOW nor FIFOs in the file system; the
// handle is still checked to be a regular file.
const settingsOpenFlags = 0
