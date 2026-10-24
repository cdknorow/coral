//go:build !windows

package main

// Closing Coral for an MSI install is Windows-only (install_close_windows.go).

func onQuitForInstallSignal(quit func()) {}

func onSystemClose() {}

func watchEndSession(title string, end func(closeApp bool)) {}
