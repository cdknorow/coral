//go:build !windows

package hooks

// checkListener: no listener-owner check on this platform (#224 B is Windows'): the token goes as before.
func checkListener(host string, port int) error { return nil }
