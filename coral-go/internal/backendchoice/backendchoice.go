// Package backendchoice picks the terminal backend a Coral run uses (#3634, #6064). The built-in terminal (pty) is the default on every system: quitting Coral ends its agents, which is
// what people expect. tmux is a choice (Settings > Terminal, or --backend tmux) for people who want agents to outlive Coral.
package backendchoice

import "strings"

// Resolve picks this run's backend: an explicit --backend (forced); on Windows always pty (no tmux there); else the saved setting when it is exactly "tmux" or "pty"; else the default, pty.
//
// One exception keeps a person's agents from being hidden: with no explicit and no saved choice (an install that ran on the old tmux default), if Coral agents are still running in Coral's
// private tmux server, or the server could not be asked (known false: a timeout, a version mismatch), this start runs tmux so they stay reachable, and kept is true. Nothing is saved: the first
// start with none left runs pty. Only a plain "no server" counts as none. agentsInTmux is only called in that case; nil means none.
func Resolve(flagValue, saved, goos string, agentsInTmux func() (n int, known bool)) (backend string, forced, kept bool) {
	if goos == "windows" {
		// Whatever --backend says, even from coral-tray's child or launch-coral: Coral never offers tmux here, and a forced one would run whatever tmux.exe is first on PATH (#304).
		// A flag that asked for pty is still "forced"; one that asked for tmux is not honoured, and the entry points say so (FlagProblem).
		return "pty", flagValue == "pty", false
	}
	if flagValue != "" {
		if flagValue == "pty" {
			return "pty", true, false
		}
		return "tmux", true, false
	}
	if saved == "tmux" || saved == "pty" {
		return saved, false, false
	}
	if agentsInTmux != nil {
		if n, known := agentsInTmux(); n > 0 || !known {
			return "tmux", false, true
		}
	}
	return "pty", false, false
}

// FlagProblem is why --backend cannot be used on goos, or "" when it can: on Windows a forced tmux is refused, because Coral never offers tmux there and would otherwise
// run whatever tmux.exe is first on PATH, with no check of who owns it, and let an agent of the same user send keys into the others (#304). The entry points (coral, coral-tray)
// print it and exit 2; Resolve is the backstop for any caller that does not.
func FlagProblem(goos, flagValue string) string {
	if goos == "windows" && strings.EqualFold(strings.TrimSpace(flagValue), "tmux") {
		return "tmux isn't supported on Windows; Coral uses its own terminals (--backend pty, the default)"
	}
	return ""
}
