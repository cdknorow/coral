//go:build !windows

package agent

// CheckRunnable: exec.LookPath already checks the execute bit here.
func CheckRunnable(string) error { return nil }
