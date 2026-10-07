//go:build windows

package ptymanager

import (
	"fmt"
	"os"
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
	content := "Remove-Item -Force '" + path + "'\n" + command + "\n"
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		os.Remove(path)
		return "", func() {}, fmt.Errorf("write command handoff: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", func() {}, fmt.Errorf("close command handoff: %w", err)
	}
	return "& '" + path + "'", func() { _ = os.Remove(path) }, nil
}
