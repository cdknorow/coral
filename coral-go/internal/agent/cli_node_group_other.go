//go:build !darwin && !windows

package agent

// groupIsTheUsersAlone: only macOS (Directory Services) can say a group is the user's alone here; elsewhere a group write bit is never trusted.
func groupIsTheUsersAlone(uint32) (bool, error) { return false, nil }
