package tmux

import "os"

// Coral's tmux server starts without the operator's ~/.tmux.conf (#3635). Whatever that file
// sets (key bindings, hooks, run-shell lines, a default-command) would otherwise apply to every
// agent's pane, and its run-shell and hook lines would run inside Coral's server. The server
// gets an empty config instead; an operator who wants one sets CORAL_TMUX_CONF to a file.
//
// tmux reads -f only when that command starts the server, so it is passed with every command
// on Coral's socket: whichever command comes first starts the server with it. It is never
// passed for tmux's default socket, which is not Coral's server.

// ServerConfigEnv names the opt-in config file for Coral's tmux server.
const ServerConfigEnv = "CORAL_TMUX_CONF"

// ServerConfig is the config file Coral's tmux server starts with: CORAL_TMUX_CONF, or an
// empty one.
func ServerConfig() string {
	if p := os.Getenv(ServerConfigEnv); p != "" {
		return p
	}
	return os.DevNull
}

// socketArgs are the global tmux flags for a command on socketPath: Coral's socket with its
// server config, or nothing for tmux's default socket ("").
func socketArgs(socketPath string) []string {
	if socketPath == "" {
		return nil
	}
	return []string{"-S", socketPath, "-f", ServerConfig()}
}
