//go:build windows

package routes

import "os"

// fileIdentity: not available from os.FileInfo on Windows; every file counts on its own.
func fileIdentity(os.FileInfo) (string, bool) { return "", false }
