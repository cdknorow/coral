//go:build !windows

package firewall

// ReadRules: Windows Firewall rules exist only on Windows.
func ReadRules() ([]Rule, error) { return nil, nil }
