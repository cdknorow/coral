//go:build windows

package logrotate

// openNoFollow: Windows has no O_NOFOLLOW; the data folder is the user's own (its access list
// is checked at startup).
const openNoFollow = 0
