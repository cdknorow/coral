//go:build windows

package hooks

// openNoFollow: Windows has no O_NOFOLLOW; the data folder is the user's own.
const openNoFollow = 0
