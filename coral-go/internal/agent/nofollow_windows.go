//go:build windows

package agent

// Windows has no O_NOFOLLOW; the per-user data dir is the user's own, and O_EXCL still
// refuses an existing path (#3582).
const oNoFollow = 0
