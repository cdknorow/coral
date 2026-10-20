//go:build !darwin

package apply

import "errors"

func swapPaths(a, b string) error { return errors.New("atomic swap is built for macOS only") }

func swapSupported(string) bool { return false }
