//go:build windows

package ptymanager

import (
	"fmt"
	"os"

	"github.com/subgentic/coral/internal/secfile"
)

// inlineCommandLimit stays below the small canonical input buffer used by a
// shell while it is still starting. Longer commands are delivered through a
// temporary script so the shell only has to receive a short path.
const inlineCommandLimit = 900

// commandInvocation returns a short PowerShell command that executes the
// complete command from a temporary script. The script removes itself before
// running the command; callers should remove it on delivery failure.
func commandInvocation(command string) (invocation string, cleanup func(), err error) {
	file, err := os.CreateTemp("", "coral-command-*.ps1")
	if err != nil {
		return "", func() {}, fmt.Errorf("create command handoff: %w", err)
	}
	path := file.Name()
	// The script holds the launch line (paths, flags, the session token file's path): only
	// this user and SYSTEM may read it, whoever else can read the temp folder (#3906, sweep
	// item 8). Before anything is written.
	if err := secfile.Protect(path); err != nil {
		file.Close()
		os.Remove(path)
		return "", func() {}, fmt.Errorf("protect command handoff: %w", err)
	}
	content, invocation := psHandoff(path, command)
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		os.Remove(path)
		return "", func() {}, fmt.Errorf("write command handoff: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", func() {}, fmt.Errorf("close command handoff: %w", err)
	}
	return invocation, func() { _ = os.Remove(path) }, nil
}
