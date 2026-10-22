//go:build !windows

package executil

// StartDir is dir unchanged off Windows (see startdir_windows.go).
func StartDir(dir string) (string, error) { return dir, nil }
