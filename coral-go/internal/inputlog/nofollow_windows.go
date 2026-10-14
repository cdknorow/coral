//go:build windows

package inputlog

// openNoFollow: Windows has no O_NOFOLLOW; the data directory is the user's own.
const openNoFollow = 0
