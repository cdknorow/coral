//go:build !windows

package secfile

// Protect does nothing outside Windows: the file was created with mode 0600, which is the
// protection there, and the readers check the mode and the owner.
func Protect(string) error { return nil }
