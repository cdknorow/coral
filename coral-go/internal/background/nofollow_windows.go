//go:build windows

package background

// oNoFollow: Windows has no O_NOFOLLOW; callers check with Lstat first.
const oNoFollow = 0
