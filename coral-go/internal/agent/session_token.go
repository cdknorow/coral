package agent

import (
	"fmt"
	"strings"

	"github.com/subgentic/coral/internal/agentenv"
	at "github.com/subgentic/coral/internal/agenttypes"
)

// SessionTokenEnv is the variable an agent's tools read its session token from (#3387).
const SessionTokenEnv = "CORAL_SESSION_TOKEN"

// SessionTokenMinter mints a fresh token for a session, writes it to a new 0600 file
// and returns the file's path ("" when there is no token). The server sets it at
// startup; with it unset (tests, other callers) launch lines carry no token.
var SessionTokenMinter func(sessionID, sessionName string) string

// SessionTokenFileRemover deletes a session's token file, if it is still there (a Codex
// agent's, which its launch line keeps, #13): CleanupTempFiles calls it when the session's
// files go. The server sets it with SessionTokenMinter.
var SessionTokenFileRemover func(sessionID string)

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
	if agentType == at.Codex {
		// Codex hides CORAL_SESSION_TOKEN from the commands it runs (its default excludes
		// before 0.161), so its tools also get the file's path, and the file stays: it is
		// private to this user, in <data>/session_tokens, and goes at the next startup once
		// its session's token is revoked (#13). The token is still never in the line.
		switch detectShell() {
		case ShellPowerShell:
			return fmt.Sprintf("$env:%s = (Get-Content -Raw -LiteralPath %s).Trim(); $env:%s = %s;", SessionTokenEnv, q, agentenv.SessionAuthFileEnv, q)
		default:
			return fmt.Sprintf(`export %s="$(cat %s)" %s=%s;`, SessionTokenEnv, q, agentenv.SessionAuthFileEnv, q)
		}
	}
	switch detectShell() {
	case ShellPowerShell:
		return fmt.Sprintf("$env:%s = (Get-Content -Raw -LiteralPath %s).Trim(); Remove-Item -Force -LiteralPath %s;", SessionTokenEnv, q, q)
	default:
		return fmt.Sprintf(`export %s="$(cat %s)"; rm -f %s;`, SessionTokenEnv, q, q)
	}
}

// withSessionToken puts the agent's own Coral environment and then its token prefix first
// in a launch line's parts and names them raw (shell syntax Coral built, #3362).
func withSessionToken(parts, raw []string, p LaunchParams, agentType string) ([]string, []string) {
	var pres []string
	for _, pre := range []string{coralEnvPrefix(p), sessionTokenPrefix(p, agentType)} {
		if pre != "" {
			pres = append(pres, pre)
		}
	}
	return append(pres, parts...), append(raw, pres...)
}

// coralEnvPrefix sets this agent's Coral environment in the shell that starts it (#3421):
// which server (CORAL_PORT/HOST/URL), where its data is (CORAL_DIR/DATA_DIR) and who it
// is (CORAL_SESSION_NAME/ID, CORAL_SUBSCRIBER_ID). A pane's shell starts with the tmux
// server's environment, so without this an agent could inherit another Coral's (a test
// server started from an agent's shell gave its agents that agent's name and server).
// Identity this launch doesn't know is unset; a location it doesn't know is left as the
// server set it at startup.
func coralEnvPrefix(p LaunchParams) string {
	set := CoralEnv(p)
	if p.SessionID != "" {
		set = append(set, [2]string{"CORAL_SESSION_ID", p.SessionID})
	}
	known := map[string]bool{}
	for _, kv := range set {
		known[kv[0]] = true
	}
	var unset []string
	for _, k := range []string{"CORAL_SESSION_NAME", "CORAL_SESSION_ID", "CORAL_SUBSCRIBER_ID", SessionTokenEnv, agentenv.SessionAuthFileEnv} {
		if !known[k] {
			unset = append(unset, k)
		}
	}
	var b strings.Builder
	switch detectShell() {
	case ShellPowerShell:
		for _, k := range unset {
			fmt.Fprintf(&b, "Remove-Item Env:%s -ErrorAction SilentlyContinue; ", k)
		}
		for _, kv := range set {
			fmt.Fprintf(&b, "$env:%s = %s; ", kv[0], shellQuoteLiteral(kv[1]))
		}
		return strings.TrimSuffix(b.String(), " ")
	default:
		// TMUX and TMUX_PANE always go (#3635): with them, the agent and every command it runs
		// can drive Coral's tmux server (its socket is in TMUX), and so every other agent's
		// pane. Coral identifies an agent by CORAL_SESSION_NAME, not by tmux. (PowerShell and
		// cmd run without tmux.)
		unset = append(unset, "TMUX", "TMUX_PANE")
		fmt.Fprintf(&b, "unset %s; ", strings.Join(unset, " "))
		if len(set) > 0 {
			b.WriteString("export")
			for _, kv := range set {
				fmt.Fprintf(&b, " %s=%s", kv[0], singleQuote(kv[1]))
			}
			b.WriteString(";")
		}
	}
	return strings.TrimSpace(b.String())
}
