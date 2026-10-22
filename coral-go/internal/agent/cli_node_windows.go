//go:build windows

package agent

import "errors"

// trustedNodeDir is never reached on Windows: NodeDirFor does nothing there, npm's shims name their own node.
func trustedNodeDir(dir string) error { return errors.New("not used on Windows") }
