//go:build windows

package secfile

// PathWriters on Windows is Writers: the access list of the path's folder (the walk over links is the Unix one's; the one caller does nothing on Windows).
func PathWriters(path string) ([]string, error) { return Writers(path) }

// PathWritersFor ignores uid on Windows: Writers reads the access list for this account.
func PathWritersFor(path string, uid int) ([]string, error) { return Writers(path) }
