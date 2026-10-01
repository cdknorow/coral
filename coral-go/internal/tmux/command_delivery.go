package tmux

import (
	"fmt"
	"os"
	"strings"
)

const inlineCommandLimit = 900

// commandInvocation writes an oversized command to a private script and
// returns a short source command for the target shell. This avoids the small
// canonical input buffer that exists while a fresh shell is initializing.
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
