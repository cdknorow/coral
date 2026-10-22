//go:build !windows && !darwin && !linux

package secfile

// pathACLWriters: this system's access lists are not read (the mode bits are checked), so none are reported.
func pathACLWriters(path string, uid int, name string) ([]string, error) { return nil, nil }
