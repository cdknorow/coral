//go:build windows

package routes

import "os"

// fileIdentity: not available from os.FileInfo on Windows; every file counts on its own.
func fileIdentity(os.FileInfo) (string, bool) { return "", false }

// fileLinks: Windows reports no link count here; every file counts as its only link.
func fileLinks(os.FileInfo) uint64 { return 1 }
