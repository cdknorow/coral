// Package backendchoice picks the terminal backend a Coral run uses (#3634, #6064). The built-in terminal (pty) is the default on every system: quitting Coral ends its agents, which is
// what people expect. tmux is a choice (Settings > Terminal, or --backend tmux) for people who want agents to outlive Coral.
package backendchoice

// Resolve picks this run's backend: an explicit --backend (forced); on Windows always pty (no tmux there); else the saved setting when it is exactly "tmux" or "pty"; else the default, pty.
//
// One exception keeps a person's agents from being hidden: with no explicit and no saved choice (an install that ran on the old tmux default), if Coral agents are still running in Coral's
// private tmux server, this start runs tmux so they stay reachable, and kept is true. Nothing is saved: the first start with none left runs pty. agentsInTmux is only called in that
// case; nil means none.
func Resolve(flagValue, saved, goos string, agentsInTmux func() int) (backend string, forced, kept bool) {
	if flagValue != "" {
		if flagValue == "pty" {
			return "pty", true, false
		}
		return "tmux", true, false
	}
	if goos == "windows" {
		return "pty", false, false
	}
	if saved == "tmux" || saved == "pty" {
		return saved, false, false
	}
	if agentsInTmux != nil && agentsInTmux() > 0 {
		return "tmux", false, true
	}
	return "pty", false, false
}
