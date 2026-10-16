//go:build !windows

package linktest

import "testing"

func kinds() []string { return []string{"symlink", "hardlink"} }

func plant(t *testing.T, kind, path string) Planted {
	t.Helper()
	if kind == "hardlink" {
		return plantHardLink(t, path)
	}
	return plantSymlink(t, path)
}
