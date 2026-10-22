//go:build !windows

package main

// showElevatedInstallerNote is Windows': the installer's launch is Windows-only, and elsewhere the line on stdout is all there is.
func showElevatedInstallerNote() {}
