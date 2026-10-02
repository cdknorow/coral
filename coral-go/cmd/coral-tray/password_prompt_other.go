//go:build !darwin

package main

import "fmt"

func nativeDatabasePassword() (string, error) {
	return "", fmt.Errorf("secure native database password input is unavailable on this platform; use key-file mode or the CLI TTY")
}
