package cmdhandoff

import (
	"fmt"
	"os"
	"strings"
)

// InlineLimit stays below the small canonical input buffer a shell has while it is still starting:
// a command longer than this is handed over as a script (ShellScript) so the shell only has to
// receive a short path. internal/tmux and internal/ptymanager both use it.
const InlineLimit = 900

// ShellScript writes command to a private script (mode 0700, in the per-user temp folder, named
// coral-command-<digits>.sh, which Sweep recognises) and returns the short command that sources it:
// ". '<path>'". The script removes itself before running the command; the caller runs cleanup when
// the delivery fails, so a script that was never sourced does not stay behind.
func ShellScript(command string) (invocation string, cleanup func(), err error) {
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
	content := "#!/bin/sh\nrm -f -- " + Quote(path) + "\n" + command + "\n"
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		os.Remove(path)
		return "", func() {}, fmt.Errorf("write command handoff: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", func() {}, fmt.Errorf("close command handoff: %w", err)
	}
	return ". " + Quote(path), func() { _ = os.Remove(path) }, nil
}

// Quote is value as one single-quoted shell word.
func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
