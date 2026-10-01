package ptymanager

import (
	"fmt"
	"os"
	"strings"
)

// inlineCommandLimit stays below the small canonical input buffer used by a
// shell while it is still starting. Longer commands are delivered through a
// temporary script so the shell only has to receive a short path.
const inlineCommandLimit = 900

// commandInvocation returns a short shell command that sources the complete
// command from a private temporary script. The script removes itself before
// running the command; callers should remove it on delivery failure.
func commandInvocation(command string) (invocation string, cleanup func(), err error) {
	file, err := os.CreateTemp("", "coral-command-*.sh")
	if err != nil {
		return "", func() {}, fmt.Errorf("create command handoff: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0700); err != nil {
		file.Close()
		os.Remove(path)
		return "", func() {}, fmt.Errorf("protect command handoff: %w", err)
	}
	content := "#!/bin/sh\nrm -f -- " + shellQuote(path) + "\n" + command + "\n"
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		os.Remove(path)
		return "", func() {}, fmt.Errorf("write command handoff: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", func() {}, fmt.Errorf("close command handoff: %w", err)
	}
	return ". " + shellQuote(path), func() { _ = os.Remove(path) }, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
