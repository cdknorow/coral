package main

import "strings"

// backendFlagProblem is why --backend cannot be used on goos, or "" when it can. On Windows Coral never offers tmux (the setting is fixed to its own terminals and PUT refuses
// it), so a forced --backend tmux would run whatever tmux.exe is first on PATH, with no check of who owns it, and let a same-user agent send keys into the others (#304).
func backendFlagProblem(goos, backend string) string {
	if goos == "windows" && strings.EqualFold(strings.TrimSpace(backend), "tmux") {
		return "tmux isn't supported on Windows; Coral uses its own terminals (--backend pty, the default)"
	}
	return ""
}
