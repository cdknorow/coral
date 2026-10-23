//go:build windows

package stripstatic

import "os"

// checkPrivate has no mode bits to read on Windows: the build's folder is made with an owner-only ACL by the workflow step that passes -out (Security's #6208 review).
func checkPrivate(string, os.FileInfo) error { return nil }
