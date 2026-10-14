package agent

import "fmt"

// SessionTokenEnv is the variable an agent's tools read its session token from (#3387).
const SessionTokenEnv = "CORAL_SESSION_TOKEN"

// SessionTokenMinter mints a fresh token for a session, writes it to a new 0600 file
// and returns the file's path ("" when there is no token). The server sets it at
// startup; with it unset (tests, other callers) launch lines carry no token.
var SessionTokenMinter func(sessionID, sessionName string) string

// sessionTokenPrefix is the start of a launch line that gives the agent its token
// (#3387): the shell reads the token from the file into CORAL_SESSION_TOKEN and deletes
// the file, before the CLI starts. Only the file's path is typed, so the token is never
// in the launch line, the terminal, its log, argv or the tmux server's environment; the
// variable lives in this pane's shell and what it starts. The file goes even if the
// CLI then fails to start.
func sessionTokenPrefix(p LaunchParams, agentType string) string {
	if SessionTokenMinter == nil || p.SessionID == "" {
		return ""
	}
	name := p.SessionName
	if name == "" {
		name = agentType + "-" + p.SessionID
	}
	path := SessionTokenMinter(p.SessionID, name)
	if path == "" {
		return ""
	}
	q := shellQuoteLiteral(path)
	switch detectShell() {
	case ShellPowerShell:
		return fmt.Sprintf("$env:%s = (Get-Content -Raw %s).Trim(); Remove-Item -Force %s;", SessionTokenEnv, q, q)
	case ShellCmd:
		return fmt.Sprintf(`set /p %s=<"%s" & del /q "%s" &`, SessionTokenEnv, path, path)
	default:
		return fmt.Sprintf(`export %s="$(cat %s)"; rm -f %s;`, SessionTokenEnv, q, q)
	}
}

// withSessionToken puts the token prefix first in a launch line's parts and names it
// raw (it's shell syntax Coral built, #3362).
func withSessionToken(parts, raw []string, p LaunchParams, agentType string) ([]string, []string) {
	pre := sessionTokenPrefix(p, agentType)
	if pre == "" {
		return parts, raw
	}
	return append([]string{pre}, parts...), append(raw, pre)
}
