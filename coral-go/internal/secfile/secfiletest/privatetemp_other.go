//go:build !windows

package secfiletest

// protectInheritable: nothing off Windows (UsePrivateTemp does nothing there either).
func protectInheritable(string) error { return nil }
