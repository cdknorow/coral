//go:build windows

package verify

import "os"

// checkPrivate: on Windows the owner-only ACL is checked by secfile.CheckOpen on the open file.
func checkPrivate(os.FileInfo) error { return nil }
