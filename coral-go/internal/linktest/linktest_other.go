//go:build !windows

package linktest

import "testing"

func kinds() []string { return []string{"symlink"} }

func plant(t *testing.T, kind, path string) Planted {
	t.Helper()
	return plantSymlink(t, path)
}
