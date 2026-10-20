//go:build !darwin && !linux

package apply

import "errors"

func swapPaths(a, b string) error { return errors.New("atomic swap is built for macOS and Linux only") }

func swapSupported(string) bool { return false }
