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
	return shellScript(command, false)
}

// ClearScreen is what a quiet launch script prints first: erase the screen and the scrollback (ESC[3J: tmux and a terminal emulator drop their history, and the PTY backend's replay buffer
// restarts from it), and home the cursor, so the short line that sourced the script does not stay above the agent (#5911).
const ClearScreen = `printf '\033[2J\033[3J\033[H'`

// KeepEnv, when set, keeps a quiet launch script after it has run (it does not remove itself), for debugging what a launch did (#5911).
const KeepEnv = "CORAL_KEEP_LAUNCH_SCRIPTS"

// KeepScripts says whether KeepEnv is set.
func KeepScripts() bool { return os.Getenv(KeepEnv) != "" }

// QuietShellScript is ShellScript for an agent's launch line, always handed over as a script, whatever its length: the shell is typed ONE short line that sources it, which
// is erased from the screen and scrollback first thing (ClearScreen), so a new person's terminal opens with the agent and not with a screenful of exports and temp-file paths
// (#5911). The script holds exactly what the typed line used to hold, the session token's value included nowhere (the line reads it from its file at run time, as before).
// With KeepEnv set it stays on disk after it has run.
func QuietShellScript(command string) (invocation string, cleanup func(), err error) {
	return shellScript(ClearScreen+"\n"+command, KeepScripts())
}

func shellScript(command string, keep bool) (invocation string, cleanup func(), err error) {
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
	content := "#!/bin/sh\n"
	if !keep {
		content += "rm -f -- " + Quote(path) + "\n"
	}
	content += command + "\n"
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
