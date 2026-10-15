//go:build !windows

package gitutil

import "os"

// noHooksPath is git's core.hooksPath: /dev/null holds no hooks and never can.
func noHooksPath() string { return os.DevNull }
