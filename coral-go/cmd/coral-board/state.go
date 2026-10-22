package main

import (
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/hooks"
	"github.com/subgentic/coral/internal/testguard"
	"github.com/subgentic/coral/internal/tmux"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// State file stores current project/job_title per session.
type boardState struct {
	Project   string `json:"project"`
	JobTitle  string `json:"job_title"`
	ServerURL string `json:"server_url,omitempty"`
}

// coralDir returns the data directory this agent's Coral server uses, falling
// back to ~/.coral.
//
// Without this the CLI wrote its board state into ~/.coral no matter which
// server launched it, so an agent belonging to a server started with --home
// both failed to find its own subscription and wrote a file into the user's
// real install. Coral sets CORAL_DATA_DIR in every agent's environment.
func coralDir() string {
	dir := cliauth.DataDir()
	testguard.CheckDataPath(dir)
	return dir
}

// stateFilePath is this agent session's board state file, or "" outside an agent session.
func stateFilePath() string {
	sessionName := resolveSessionName()
	if sessionName == "" {
		return ""
	}
	return filepath.Join(coralDir(), hooks.BoardStateFileName(sessionName))
}

func loadState() *boardState {
	path := stateFilePath()
	if path == "" {
		return nil // no agent session: no board, never a shared default (#3864)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// Fallback: PID-based resolution (for sandboxed environments)
		if resolved := resolveViaPID(); resolved != nil && resolved.Project != "" {
			return &boardState{Project: resolved.Project, JobTitle: resolved.SubscriberID}
		}
		return nil
	}
	var s boardState
	if json.Unmarshal(data, &s) != nil {
		return nil
	}
	if s.ServerURL != "" {
		// Only this agent's own Coral: anything else would get the session token (#3581).
		if hooks.TrustedServerURL(s.ServerURL, serverURL) {
			serverURL = s.ServerURL
		} else {
			fmt.Fprintln(os.Stderr, "[coral-board] ignoring the board state's server_url: it isn't a loopback address or this agent's Coral")
		}
	}
	return &s
}

func saveState(s *boardState) {
	data, _ := json.Marshal(s)
	path := stateFilePath()
	if path == "" {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	if os.WriteFile(path, data, 0o600) == nil {
		os.Chmod(path, 0o600) // a file an older version wrote 0644
	}
}

func deleteState() {
	if path := stateFilePath(); path != "" {
		os.Remove(path)
	}
}

func resolveSessionName() string {
	// Check explicit session name (set by both PTY and tmux backends)
	if name := os.Getenv("CORAL_SESSION_NAME"); name != "" {
		return name
	}
	// Fallback: try tmux session name
	if os.Getenv("TMUX") != "" {
		out, err := exec.Command(tmux.BinaryFromEnv(), "display-message", "-p", "#S").Output()
		if err == nil {
			name := strings.TrimSpace(string(out))
			if name != "" {
				return name
			}
		}
	}
	// Where the environment was stripped (a sandbox), the server can name the session from
	// this process's ancestors (verified against the session's live process).
	if resolved := resolveViaPID(); resolved != nil && resolved.SessionName != "" {
		return resolved.SessionName
	}
	// The Operator's own coral-board in a plain terminal: no agent session in this process's
	// tree and none of the variables Coral sets for one, so one fixed name (#3864). Never the
	// hostname, which every sessionless process shared, and never for a process with an
	// agent's marker (cliauth's list): that one has no board rather than the Operator's.
	if !cliauth.InsideAgentSession() {
		return operatorSessionName
	}
	return ""
}

// operatorSessionName names the Operator's command-line board state; no agent session has
// it (theirs are "<type>-<uuid>").
const operatorSessionName = "operator"

// resolveSubscriberID returns the stable board identity for this agent.
// Priority: env var > board_state file > PID resolution > session name.
func resolveSubscriberID() string {
	if id := os.Getenv("CORAL_SUBSCRIBER_ID"); id != "" {
		return id
	}
	// Fallback: read from board_state file
	if st := loadState(); st != nil && st.JobTitle != "" {
		return st.JobTitle
	}
	// Fallback: PID-based resolution (for sandboxed environments like Codex)
	if resolved := resolveViaPID(); resolved != nil {
		return resolved.SubscriberID
	}
	return resolveSessionName()
}

// pidResolution caches the result of PID-based identity resolution.
type pidResolution struct {
	SubscriberID string
	Project      string
	SessionName  string
}

var cachedPIDResolution *pidResolution

var pidResolutionDone bool

// resolveViaPID walks the process tree and asks the server to identify the agent.
func resolveViaPID() *pidResolution {
	if pidResolutionDone {
		return cachedPIDResolution
	}
	pidResolutionDone = true

	pids := getAncestorPIDs()
	fmt.Fprintf(os.Stderr, "[coral-board] PID resolution: ancestors=%v serverURL=%s\n", pids, redactedServerURL())
	if len(pids) == 0 {
		return nil
	}

	// Build comma-separated PID list
	pidStrs := make([]string, len(pids))
	for i, p := range pids {
		pidStrs[i] = strconv.Itoa(p)
	}

	resp, err := http.Get(serverURL + "/api/sessions/resolve?pids=" + strings.Join(pidStrs, ","))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	var result map[string]string
	data, _ := readServer(resp.Body)
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}

	cachedPIDResolution = &pidResolution{
		SubscriberID: result["subscriber_id"],
		Project:      result["project"],
		SessionName:  result["session_name"],
	}
	return cachedPIDResolution
}

// getAncestorPIDs returns the current process's PID and its ancestor PIDs.
func getAncestorPIDs() []int {
	var pids []int
	pid := os.Getpid()
	for i := 0; i < 20 && pid > 1; i++ {
		pids = append(pids, pid)
		ppid := getParentPID(pid)
		if ppid <= 1 || ppid == pid {
			break
		}
		pid = ppid
	}
	return pids
}

// getParentPID returns the parent PID of the given process.
func getParentPID(pid int) int {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return 0
		}
		// Format: pid (comm) state ppid ...
		// Find the closing paren to skip the comm field (may contain spaces)
		s := string(data)
		idx := strings.LastIndex(s, ")")
		if idx < 0 || idx+2 >= len(s) {
			return 0
		}
		fields := strings.Fields(s[idx+2:])
		if len(fields) < 2 {
			return 0
		}
		ppid, _ := strconv.Atoi(fields[1])
		return ppid
	}
	// macOS/BSD: use ps
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	ppid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return ppid
}
