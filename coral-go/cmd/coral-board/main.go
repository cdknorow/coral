// Command coral-board is the message board CLI for inter-agent communication.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var serverURL = "http://localhost:8420"

func init() {
	if v := os.Getenv("CORAL_URL"); v != "" {
		serverURL = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("CORAL_PORT"); v != "" {
		serverURL = "http://localhost:" + v
	}
}

// State file stores current project/job_title per session.
type boardState struct {
	Project   string `json:"project"`
	JobTitle  string `json:"job_title"`
	ServerURL string `json:"server_url,omitempty"`
}

func stateFilePath() string {
	home, _ := os.UserHomeDir()
	sessionName := resolveSessionName()
	return filepath.Join(home, ".coral", fmt.Sprintf("board_state_%s.json", sessionName))
}

func loadState() *boardState {
	data, err := os.ReadFile(stateFilePath())
	if err != nil {
		return nil
	}
	var s boardState
	if json.Unmarshal(data, &s) != nil {
		return nil
	}
	if s.ServerURL != "" {
		serverURL = s.ServerURL
	}
	return &s
}

func saveState(s *boardState) {
	data, _ := json.Marshal(s)
	os.MkdirAll(filepath.Dir(stateFilePath()), 0755)
	os.WriteFile(stateFilePath(), data, 0644)
}

func deleteState() {
	os.Remove(stateFilePath())
}

func resolveSessionName() string {
	// Try tmux session name
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		// Query tmux for session name
		// For simplicity, use a fallback
	}
	host, _ := os.Hostname()
	return host
}

func apiCall(method, path string, body any) (map[string]any, error) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, serverURL+"/api/board"+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Cannot reach Coral server at %s: %v", serverURL, err)
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)
	var result map[string]any
	json.Unmarshal(respData, &result)
	return result, nil
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "join":
		cmdJoin()
	case "post":
		cmdPost()
	case "read":
		cmdRead()
	case "check":
		cmdCheck()
	case "projects":
		cmdProjects()
	case "subscribers":
		cmdSubscribers()
	case "leave":
		cmdLeave()
	case "delete":
		cmdDelete()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: coral-board <command> [args]

Commands:
  join <project> --as <role>   Subscribe to a board
  post "<message>"             Post a message
  read [--last N]              Read new messages
  check [--quiet]              Check unread count
  projects                     List all boards
  subscribers                  List board subscribers
  leave                        Unsubscribe from board
  delete                       Delete board and messages

Environment:
  CORAL_URL   Server URL (default http://localhost:8420)
  CORAL_PORT  Server port (overrides CORAL_URL)`)
}

func cmdJoin() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board join <project> --as <role>")
		os.Exit(1)
	}
	project := os.Args[2]
	jobTitle := "Agent"
	for i, arg := range os.Args {
		if arg == "--as" && i+1 < len(os.Args) {
			jobTitle = os.Args[i+1]
		}
	}

	sessionName := resolveSessionName()
	_, err := apiCall("POST", "/"+project+"/subscribe", map[string]string{
		"session_id": sessionName,
		"job_title":  jobTitle,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	saveState(&boardState{Project: project, JobTitle: jobTitle})
	fmt.Printf("Joined '%s' as '%s' (session: %s)\n", project, jobTitle, sessionName)
}

func cmdPost() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board. Run: coral-board join <project> --as <role>")
		os.Exit(1)
	}
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board post \"<message>\"")
		os.Exit(1)
	}
	message := strings.Join(os.Args[2:], " ")
	sessionName := resolveSessionName()

	result, err := apiCall("POST", "/"+st.Project+"/messages", map[string]string{
		"session_id": sessionName,
		"content":    message,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if id, ok := result["id"]; ok {
		fmt.Printf("Message #%.0f posted to '%s'\n", id, st.Project)
	}
}

func cmdRead() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}

	// Check for --last N
	useLast := false
	lastN := 0
	for i, arg := range os.Args {
		if arg == "--last" && i+1 < len(os.Args) {
			useLast = true
			fmt.Sscanf(os.Args[i+1], "%d", &lastN)
		}
	}

	sessionName := resolveSessionName()
	var path string
	if useLast {
		path = fmt.Sprintf("/%s/messages/all?limit=%d", st.Project, lastN)
	} else {
		path = fmt.Sprintf("/%s/messages?session_id=%s&limit=50", st.Project, sessionName)
	}

	resp, err := http.Get(serverURL + "/api/board" + path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var messages []map[string]any
	json.Unmarshal(data, &messages)

	if len(messages) == 0 {
		fmt.Println("No new messages.")
		return
	}

	for _, m := range messages {
		ts, _ := m["created_at"].(string)
		if len(ts) > 16 {
			ts = ts[:16]
		}
		role, _ := m["job_title"].(string)
		if role == "" {
			role, _ = m["session_id"].(string)
		}
		content, _ := m["content"].(string)
		fmt.Printf("[%s] %s: %s\n", ts, role, content)
	}
}

func cmdCheck() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}
	sessionName := resolveSessionName()
	result, err := apiCall("GET", fmt.Sprintf("/%s/messages/check?session_id=%s", st.Project, sessionName), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	unread, _ := result["unread"].(float64)

	quiet := false
	for _, arg := range os.Args {
		if arg == "--quiet" {
			quiet = true
		}
	}
	if quiet {
		fmt.Printf("%.0f\n", unread)
	} else {
		fmt.Printf("%.0f unread message(s) on '%s'\n", unread, st.Project)
	}
}

func cmdProjects() {
	resp, err := http.Get(serverURL + "/api/board/projects")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var projects []map[string]any
	json.Unmarshal(data, &projects)

	st := loadState()
	for _, p := range projects {
		name, _ := p["project"].(string)
		marker := "  "
		if st != nil && st.Project == name {
			marker = "* "
		}
		subs, _ := p["subscriber_count"].(float64)
		msgs, _ := p["message_count"].(float64)
		fmt.Printf("%s%s (%0.f subscribers, %0.f messages)\n", marker, name, subs, msgs)
	}
}

func cmdSubscribers() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}

	resp, err := http.Get(serverURL + "/api/board/" + st.Project + "/subscribers")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var subs []map[string]any
	json.Unmarshal(data, &subs)

	sessionName := resolveSessionName()
	for _, s := range subs {
		role, _ := s["job_title"].(string)
		sid, _ := s["session_id"].(string)
		marker := ""
		if sid == sessionName {
			marker = " (you)"
		}
		fmt.Printf("  %s (%s)%s\n", role, sid, marker)
	}
}

func cmdLeave() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}
	sessionName := resolveSessionName()
	apiCall("DELETE", "/"+st.Project+"/subscribe", map[string]string{
		"session_id": sessionName,
	})
	deleteState()
	fmt.Printf("Left '%s'\n", st.Project)
}

func cmdDelete() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}
	apiCall("DELETE", "/"+st.Project, nil)
	deleteState()
	fmt.Printf("Deleted board '%s'\n", st.Project)
}
