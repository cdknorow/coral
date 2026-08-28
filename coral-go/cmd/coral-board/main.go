// Command coral-board is the message board CLI for inter-agent communication.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"flag"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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

// coralDir returns the data directory this agent's Coral server uses, falling
// back to ~/.coral.
//
// Without this the CLI wrote its board state into ~/.coral no matter which
// server launched it, so an agent belonging to a server started with --home
// both failed to find its own subscription and wrote a file into the user's
// real install. Coral sets CORAL_DATA_DIR in every agent's environment.
func coralDir() string {
	for _, k := range []string{"CORAL_DATA_DIR", "CORAL_DIR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".coral")
}

func stateFilePath() string {
	sessionName := resolveSessionName()
	return filepath.Join(coralDir(), fmt.Sprintf("board_state_%s.json", sessionName))
}

func loadState() *boardState {
	data, err := os.ReadFile(stateFilePath())
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
	// Check explicit session name (set by both PTY and tmux backends)
	if name := os.Getenv("CORAL_SESSION_NAME"); name != "" {
		return name
	}
	// Fallback: try tmux session name
	if os.Getenv("TMUX") != "" {
		out, err := exec.Command("tmux", "display-message", "-p", "#S").Output()
		if err == nil {
			name := strings.TrimSpace(string(out))
			if name != "" {
				return name
			}
		}
	}
	host, _ := os.Hostname()
	return host
}

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
	fmt.Fprintf(os.Stderr, "[coral-board] PID resolution: ancestors=%v serverURL=%s\n", pids, serverURL)
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
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
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
		return nil, fmt.Errorf("cannot reach Coral server at %s: %v", serverURL, err)
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
	case "export":
		cmdExport()
	case "peek":
		cmdPeek()
	case "task":
		cmdTask()
	case "workflow":
		cmdWorkflow()
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
  post "<message>" [--to "a,b"] Post a message (optionally @mention agents)
  read [--last N] [--id N]     Read new messages (or a specific message by ID)
  check [--quiet]              Check unread count
  projects                     List all boards
  subscribers                  List board subscribers
  peek "<agent>" [--lines N]   Peek at agent's terminal (orchestrator only)
  leave                        Unsubscribe from board
  delete                       Delete board and messages
  export [--output FILE] [--format json|markdown|html] [--merge FILE]
                               Export chat (auto-detects format from extension)
  task add "title" [--body "details"] [--priority P]  Create a task
  task list                      List all tasks
  task claim                     Claim next available task
  task complete <id> [--message "note"]  Complete a task
  workflow create --file <path>  Create a workflow from JSON file
  workflow list                  List all workflows
  workflow trigger <name> [--context '{"key":"val"}']  Trigger a workflow
  workflow status <run-id>       Check run status
  workflow runs [--workflow <name>] [--status <s>]  List recent runs
  workflow kill <run-id>         Kill a running workflow

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

	subscriberID := resolveSubscriberID()
	sessionName := resolveSessionName()
	_, err := apiCall("POST", "/"+project+"/subscribe", map[string]string{
		"subscriber_id": subscriberID,
		"session_name":  sessionName,
		"job_title":     jobTitle,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	saveState(&boardState{Project: project, JobTitle: jobTitle})
	fmt.Printf("Joined '%s' as '%s' (subscriber: %s)\n", project, jobTitle, subscriberID)
}

func cmdPost() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board. Run: coral-board join <project> --as <role>")
		os.Exit(1)
	}
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board post \"<message>\" [--to \"agent1,agent2\"]")
		os.Exit(1)
	}

	// Parse --to flag and collect remaining args as the message
	var toNames string
	var msgParts []string
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--to" && i+1 < len(os.Args) {
			toNames = os.Args[i+1]
			i++ // skip the value
		} else {
			msgParts = append(msgParts, os.Args[i])
		}
	}
	message := strings.Join(msgParts, " ")

	// Prepend @mentions if --to was provided
	if toNames != "" {
		var mentions []string
		for _, name := range strings.Split(toNames, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				mentions = append(mentions, "@"+name)
			}
		}
		if len(mentions) > 0 {
			message = strings.Join(mentions, " ") + " " + message
		}
	}

	subscriberID := resolveSubscriberID()

	result, err := apiCall("POST", "/"+st.Project+"/messages", map[string]string{
		"subscriber_id": subscriberID,
		"content":       message,
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

	// Check for --id N
	messageID := 0
	for i, arg := range os.Args {
		if arg == "--id" && i+1 < len(os.Args) {
			fmt.Sscanf(os.Args[i+1], "%d", &messageID)
		}
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

	subscriberID := resolveSubscriberID()
	var path string
	if messageID > 0 {
		path = fmt.Sprintf("/%s/messages/all?id=%d", st.Project, messageID)
	} else if useLast {
		path = fmt.Sprintf("/%s/messages/all?limit=%d", st.Project, lastN)
	} else {
		path = fmt.Sprintf("/%s/messages?subscriber_id=%s&limit=50", st.Project, url.QueryEscape(subscriberID))
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
		if messageID > 0 {
			fmt.Fprintf(os.Stderr, "Message #%d not found.\n", messageID)
			os.Exit(1)
		}
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
			role, _ = m["subscriber_id"].(string)
		}
		content, _ := m["content"].(string)
		fmt.Printf("[%s] %s: %s\n", ts, role, content)
	}
}

func cmdPeek() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}

	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board peek \"<agent-name>\" [--lines N]")
		os.Exit(1)
	}

	target := os.Args[2]
	lines := 30
	for i, arg := range os.Args {
		if arg == "--lines" && i+1 < len(os.Args) {
			fmt.Sscanf(os.Args[i+1], "%d", &lines)
		}
	}

	subscriberID := resolveSubscriberID()
	path := fmt.Sprintf("/%s/peek?target=%s&subscriber_id=%s&lines=%d",
		st.Project, url.QueryEscape(target), url.QueryEscape(subscriberID), lines)

	resp, err := http.Get(serverURL + "/api/board" + path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusForbidden {
		var errResp map[string]string
		json.Unmarshal(data, &errResp)
		fmt.Fprintf(os.Stderr, "Permission denied: %s\n", errResp["error"])
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		json.Unmarshal(data, &errResp)
		fmt.Fprintf(os.Stderr, "Error: %s\n", errResp["error"])
		os.Exit(1)
	}

	var result map[string]any
	json.Unmarshal(data, &result)
	output, _ := result["output"].(string)
	if output == "" {
		fmt.Println("(no output captured)")
	} else {
		fmt.Print(output)
	}
}

func cmdCheck() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}
	subscriberID := resolveSubscriberID()
	result, err := apiCall("GET", fmt.Sprintf("/%s/messages/check?subscriber_id=%s", st.Project, url.QueryEscape(subscriberID)), nil)
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

	subscriberID := resolveSubscriberID()
	for _, s := range subs {
		role, _ := s["job_title"].(string)
		sid, _ := s["subscriber_id"].(string)
		marker := ""
		if sid == subscriberID {
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
	subscriberID := resolveSubscriberID()
	apiCall("DELETE", "/"+st.Project+"/subscribe", map[string]string{
		"subscriber_id": subscriberID,
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

// exportEntry is a unified message for export, sortable by timestamp.
type exportEntry struct {
	Timestamp string `json:"timestamp"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Source    string `json:"source"` // "board" or "side-chat"
}

// exportData is the canonical JSON structure for a board export.
type exportData struct {
	Project     string            `json:"project"`
	ExportedAt  string            `json:"exported_at"`
	Subscribers []exportSubscriber `json:"subscribers"`
	Messages    []exportEntry     `json:"messages"`
	Stats       exportStats       `json:"stats"`
}

type exportSubscriber struct {
	SubscriberID string `json:"subscriber_id"`
	Role         string `json:"role"`
}

type exportStats struct {
	Total    int `json:"total"`
	Board    int `json:"board"`
	SideChat int `json:"side_chat"`
}

// parseMergeFile reads a side-chat markdown file with entries in the format:
//
//	[2026-03-25 01:22] Speaker: message content
//	that can span multiple lines
//
//	[2026-03-25 01:23] Other Speaker: next message
func parseMergeFile(path string) ([]exportEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	var entries []exportEntry
	var current *exportEntry

	for _, line := range lines {
		// Check if this line starts a new entry: [YYYY-MM-DD HH:MM] Role: ...
		if len(line) > 18 && line[0] == '[' {
			closeBracket := strings.Index(line, "]")
			if closeBracket > 0 && closeBracket < 25 {
				ts := line[1:closeBracket]
				rest := strings.TrimSpace(line[closeBracket+1:])
				colonIdx := strings.Index(rest, ":")
				if colonIdx > 0 {
					// Save previous entry
					if current != nil {
						current.Content = strings.TrimSpace(current.Content)
						entries = append(entries, *current)
					}
					role := strings.TrimSpace(rest[:colonIdx])
					content := strings.TrimSpace(rest[colonIdx+1:])
					// Normalize timestamp to match board format (YYYY-MM-DDTHH:MM)
					normTS := strings.ReplaceAll(ts, " ", "T")
					current = &exportEntry{
						Timestamp: normTS,
						Role:      role,
						Content:   content,
						Source:    "side-chat",
					}
					continue
				}
			}
		}
		// Continuation line
		if current != nil {
			current.Content += "\n" + line
		}
	}
	// Save last entry
	if current != nil {
		current.Content = strings.TrimSpace(current.Content)
		entries = append(entries, *current)
	}
	return entries, nil
}

func cmdExport() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}

	// Parse flags
	outputFile := ""
	mergeFile := ""
	format := "markdown"
	for i, arg := range os.Args {
		if arg == "--output" && i+1 < len(os.Args) {
			outputFile = os.Args[i+1]
		}
		if arg == "--merge" && i+1 < len(os.Args) {
			mergeFile = os.Args[i+1]
		}
		if arg == "--format" && i+1 < len(os.Args) {
			format = os.Args[i+1]
		}
	}
	// Auto-detect format from output file extension
	if format == "markdown" && outputFile != "" {
		if strings.HasSuffix(outputFile, ".html") || strings.HasSuffix(outputFile, ".htm") {
			format = "html"
		} else if strings.HasSuffix(outputFile, ".json") {
			format = "json"
		}
	}

	// Fetch all messages
	resp, err := http.Get(serverURL + "/api/board/" + st.Project + "/messages/all?limit=10000")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var messages []map[string]any
	json.Unmarshal(data, &messages)

	// Convert board messages to exportEntry
	var entries []exportEntry
	for _, m := range messages {
		ts, _ := m["created_at"].(string)
		if len(ts) > 16 {
			ts = ts[:16]
		}
		role, _ := m["job_title"].(string)
		if role == "" {
			role, _ = m["subscriber_id"].(string)
		}
		content, _ := m["content"].(string)
		entries = append(entries, exportEntry{
			Timestamp: ts,
			Role:      role,
			Content:   content,
			Source:    "board",
		})
	}

	// Merge side-chat file if provided
	if mergeFile != "" {
		sideEntries, err := parseMergeFile(mergeFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading merge file: %v\n", err)
			os.Exit(1)
		}
		entries = append(entries, sideEntries...)
		fmt.Fprintf(os.Stderr, "Merged %d side-chat entries\n", len(sideEntries))
	}

	// Sort all entries by timestamp
	sortEntries(entries)

	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "No messages to export.")
		os.Exit(0)
	}

	// Get subscribers for context
	subsResp, _ := http.Get(serverURL + "/api/board/" + st.Project + "/subscribers")
	var rawSubs []map[string]any
	if subsResp != nil {
		subsData, _ := io.ReadAll(subsResp.Body)
		subsResp.Body.Close()
		json.Unmarshal(subsData, &rawSubs)
	}
	var subs []exportSubscriber
	for _, s := range rawSubs {
		sid, _ := s["subscriber_id"].(string)
		role, _ := s["job_title"].(string)
		if role == "" {
			role = sid
		}
		subs = append(subs, exportSubscriber{SubscriberID: sid, Role: role})
	}

	// Count by source
	boardCount, sideCount := 0, 0
	for _, e := range entries {
		if e.Source == "board" {
			boardCount++
		} else {
			sideCount++
		}
	}

	// Build canonical export data
	export := exportData{
		Project:     st.Project,
		ExportedAt:  time.Now().Format(time.RFC3339),
		Subscribers: subs,
		Messages:    entries,
		Stats: exportStats{
			Total:    len(entries),
			Board:    boardCount,
			SideChat: sideCount,
		},
	}

	var buf bytes.Buffer
	switch format {
	case "json":
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		enc.Encode(export)
	case "html":
		renderHTML(&buf, &export)
	default:
		renderMarkdown(&buf, &export)
	}

	if outputFile != "" {
		if err := os.WriteFile(outputFile, buf.Bytes(), 0644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("Exported %d messages (%s) to %s\n", len(entries), format, outputFile)
	} else {
		fmt.Print(buf.String())
	}
}

func renderMarkdown(buf *bytes.Buffer, d *exportData) {
	buf.WriteString(fmt.Sprintf("# Board Chat Export: %s\n\n", d.Project))
	buf.WriteString(fmt.Sprintf("**Exported**: %s\n", d.ExportedAt))
	buf.WriteString(fmt.Sprintf("**Messages**: %d\n", d.Stats.Total))
	if d.Stats.SideChat > 0 {
		buf.WriteString(fmt.Sprintf("**Board messages**: %d | **Side-chat messages**: %d\n", d.Stats.Board, d.Stats.SideChat))
	}

	if len(d.Subscribers) > 0 {
		buf.WriteString(fmt.Sprintf("**Subscribers**: %d\n\n", len(d.Subscribers)))
		buf.WriteString("| Agent | Role |\n|-------|------|\n")
		for _, s := range d.Subscribers {
			buf.WriteString(fmt.Sprintf("| %s | %s |\n", s.SubscriberID, s.Role))
		}
	}

	buf.WriteString("\n---\n\n## Messages\n\n")

	for _, e := range d.Messages {
		tag := ""
		if e.Source == "side-chat" {
			tag = " 💬 SIDE CHAT"
		}
		buf.WriteString(fmt.Sprintf("**[%s] %s%s:**\n\n", e.Timestamp, e.Role, tag))
		buf.WriteString(e.Content)
		buf.WriteString("\n\n---\n\n")
	}
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

var (
	reCodeBlock   = regexp.MustCompile("(?s)```(\\w*)\\n(.*?)```")
	reHeading     = regexp.MustCompile(`(?m)^(#{1,4})\s+(.+)$`)
	reBold        = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic      = regexp.MustCompile(`(?:^|[^*])\*([^*]+?)\*(?:[^*]|$)`)
	reInlineCode  = regexp.MustCompile("`([^`]+)`")
	reUlItem      = regexp.MustCompile(`(?m)^[-*]\s+(.+)$`)
	reOlItem      = regexp.MustCompile(`(?m)^\d+\.\s+(.+)$`)
	reBlockquote  = regexp.MustCompile(`(?m)^>\s*(.+)$`)
)

// simpleMarkdownToHTML converts common markdown to HTML for the CLI export.
func simpleMarkdownToHTML(s string) string {
	// Code blocks first (before escaping)
	var codeBlocks []string
	s = reCodeBlock.ReplaceAllStringFunc(s, func(match string) string {
		parts := reCodeBlock.FindStringSubmatch(match)
		placeholder := fmt.Sprintf("\x00CODEBLOCK%d\x00", len(codeBlocks))
		codeBlocks = append(codeBlocks, "<pre><code>"+htmlEscape(parts[2])+"</code></pre>")
		return placeholder
	})

	// Inline code (before escaping)
	var inlineCodes []string
	s = reInlineCode.ReplaceAllStringFunc(s, func(match string) string {
		parts := reInlineCode.FindStringSubmatch(match)
		placeholder := fmt.Sprintf("\x00INLINECODE%d\x00", len(inlineCodes))
		inlineCodes = append(inlineCodes, "<code>"+htmlEscape(parts[1])+"</code>")
		return placeholder
	})

	s = htmlEscape(s)

	// Headings
	s = reHeading.ReplaceAllStringFunc(s, func(match string) string {
		parts := reHeading.FindStringSubmatch(match)
		level := len(parts[1])
		return fmt.Sprintf("<h%d>%s</h%d>", level, parts[2], level)
	})

	// Bold and italic
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reItalic.ReplaceAllString(s, "<em>$1</em>")

	// Blockquotes
	s = reBlockquote.ReplaceAllString(s, "<blockquote>$1</blockquote>")

	// Lists (simple single-level)
	s = reUlItem.ReplaceAllString(s, "<li>$1</li>")
	s = reOlItem.ReplaceAllString(s, "<li>$1</li>")

	// Restore code blocks and inline code
	for i, block := range codeBlocks {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00CODEBLOCK%d\x00", i), block)
	}
	for i, code := range inlineCodes {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00INLINECODE%d\x00", i), code)
	}

	// Paragraphs: convert double newlines
	s = strings.ReplaceAll(s, "\n\n", "</p><p>")
	s = "<p>" + s + "</p>"
	s = strings.ReplaceAll(s, "<p></p>", "")

	return s
}

// agentColor returns a consistent HSL color for an agent name.
func agentColor(name string) string {
	h := 0
	for _, c := range name {
		h = (h*31 + int(c)) & 0xFFFFFF
	}
	hue := h % 360
	return fmt.Sprintf("hsl(%d, 60%%, 45%%)", hue)
}

func renderHTML(buf *bytes.Buffer, d *exportData) {
	buf.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Board Chat Export: ` + htmlEscape(d.Project) + `</title>
<style>
  :root { --bg: #0d1117; --surface: #161b22; --border: #30363d; --text: #e6edf3; --muted: #8b949e; --accent: #58a6ff; --side-chat: #1a1a2e; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif; background: var(--bg); color: var(--text); line-height: 1.6; }
  .container { max-width: 900px; margin: 0 auto; padding: 2rem 1rem; }
  h1 { font-size: 1.8rem; margin-bottom: 0.5rem; }
  .stats { color: var(--muted); margin-bottom: 1.5rem; font-size: 0.9rem; }
  .stats span { margin-right: 1.5rem; }
  .subscribers { margin-bottom: 2rem; }
  .subscribers summary { cursor: pointer; color: var(--accent); font-weight: 600; margin-bottom: 0.5rem; }
  .sub-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 0.5rem; padding: 0.5rem 0; }
  .sub-chip { background: var(--surface); border: 1px solid var(--border); border-radius: 6px; padding: 0.4rem 0.8rem; font-size: 0.85rem; }
  .sub-chip .role { font-weight: 600; }
  .messages { display: flex; flex-direction: column; gap: 0.75rem; }
  .msg { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 1rem; border-left: 3px solid var(--accent); }
  .msg.side-chat { background: var(--side-chat); border-left-color: #f0883e; }
  .msg-header { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; flex-wrap: wrap; }
  .msg-role { font-weight: 700; font-size: 0.95rem; }
  .msg-time { color: var(--muted); font-size: 0.8rem; }
  .msg-tag { background: #f0883e33; color: #f0883e; font-size: 0.7rem; padding: 0.15rem 0.5rem; border-radius: 10px; font-weight: 600; }
  .msg-content { word-wrap: break-word; font-size: 0.9rem; }
  .msg-content p { margin: 0.4em 0; }
  .msg-content h1, .msg-content h2, .msg-content h3, .msg-content h4 { margin: 0.8em 0 0.3em; }
  .msg-content code { background: #1a1e24; padding: 0.15rem 0.4rem; border-radius: 3px; font-size: 0.85em; }
  .msg-content pre { background: #1a1e24; padding: 0.8rem; border-radius: 6px; overflow-x: auto; margin: 0.5rem 0; }
  .msg-content ul, .msg-content ol { padding-left: 1.5em; margin: 0.3em 0; }
  .msg-content li { margin: 0.2em 0; }
  .msg-content blockquote { border-left: 3px solid var(--border); padding-left: 0.8em; color: var(--muted); margin: 0.5em 0; }
  .msg-content strong { font-weight: 700; }
  .msg-content table { border-collapse: collapse; margin: 0.5em 0; width: 100%; }
  .msg-content th, .msg-content td { border: 1px solid var(--border); padding: 0.4em 0.6em; text-align: left; font-size: 0.85em; }
  .msg-content pre code { background: none; padding: 0; }
</style>
</head>
<body>
<div class="container">
`)
	buf.WriteString(fmt.Sprintf("<h1>%s</h1>\n", htmlEscape(d.Project)))
	buf.WriteString("<div class=\"stats\">\n")
	buf.WriteString(fmt.Sprintf("  <span>Exported: %s</span>\n", d.ExportedAt))
	buf.WriteString(fmt.Sprintf("  <span>Messages: %d</span>\n", d.Stats.Total))
	if d.Stats.SideChat > 0 {
		buf.WriteString(fmt.Sprintf("  <span>Board: %d</span>\n", d.Stats.Board))
		buf.WriteString(fmt.Sprintf("  <span>Side-chat: %d</span>\n", d.Stats.SideChat))
	}
	buf.WriteString("</div>\n")

	if len(d.Subscribers) > 0 {
		buf.WriteString("<details class=\"subscribers\">\n")
		buf.WriteString(fmt.Sprintf("  <summary>%d Subscribers</summary>\n", len(d.Subscribers)))
		buf.WriteString("  <div class=\"sub-grid\">\n")
		for _, s := range d.Subscribers {
			buf.WriteString(fmt.Sprintf("    <div class=\"sub-chip\"><span class=\"role\">%s</span></div>\n", htmlEscape(s.Role)))
		}
		buf.WriteString("  </div>\n</details>\n")
	}

	buf.WriteString("<div class=\"messages\">\n")
	for _, e := range d.Messages {
		cls := "msg"
		if e.Source == "side-chat" {
			cls = "msg side-chat"
		}
		color := agentColor(e.Role)
		buf.WriteString(fmt.Sprintf("<div class=\"%s\">\n", cls))
		buf.WriteString("  <div class=\"msg-header\">\n")
		buf.WriteString(fmt.Sprintf("    <span class=\"msg-role\" style=\"color: %s\">%s</span>\n", color, htmlEscape(e.Role)))
		buf.WriteString(fmt.Sprintf("    <span class=\"msg-time\">%s</span>\n", htmlEscape(e.Timestamp)))
		if e.Source == "side-chat" {
			buf.WriteString("    <span class=\"msg-tag\">SIDE CHAT</span>\n")
		}
		buf.WriteString("  </div>\n")
		buf.WriteString(fmt.Sprintf("  <div class=\"msg-content\">%s</div>\n", simpleMarkdownToHTML(e.Content)))
		buf.WriteString("</div>\n")
	}
	buf.WriteString("</div>\n</div>\n</body>\n</html>\n")
}

// apiCallRaw performs an HTTP request and returns the response body, status code, and error.
// Unlike apiCall, it preserves the status code for conflict/not-found handling.
func apiCallRaw(method, path string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, serverURL+"/api/board"+path, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot reach Coral server at %s: %v", serverURL, err)
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)
	return respData, resp.StatusCode, nil
}

func cmdTask() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, `Usage: coral-board task <subcommand> [args]

Subcommands:
  add "title" [--body "details"] [--priority P]
  list
  claim
  complete <id> [--message "note"]`)
		os.Exit(1)
	}

	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board. Run: coral-board join <project> --as <role>")
		os.Exit(1)
	}

	sub := os.Args[2]
	taskArgs := os.Args[3:]

	switch sub {
	case "add":
		cmdTaskAdd(st, taskArgs)
	case "list":
		cmdTaskList(st)
	case "claim":
		cmdTaskClaim(st)
	case "current", "detail":
		cmdTaskCurrent(st)
	case "complete":
		cmdTaskComplete(st, taskArgs)
	case "cancel":
		cmdTaskCancel(st, taskArgs)
	case "reassign":
		cmdTaskReassign(st, taskArgs)
	case "--help", "-h", "help":
		fmt.Fprintln(os.Stderr, `Usage: coral-board task <subcommand> [args]

Subcommands:
  add "title" [--body "details"] [--priority P] [--assignee "Agent Name"]
  list
  claim
  current                          Show your current in-progress task
  complete <id> [--message "note"]
  cancel <id> [--message "reason"]
  reassign <id> [--to "Agent Name"]`)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "Unknown task subcommand: %s\n", sub)
		os.Exit(1)
	}
}

func cmdTaskAdd(st *boardState, args []string) {
	// Reorder args: pull the title (first non-flag arg) out so flags can appear after it.
	// Go's flag package stops at the first non-flag arg, so we move flags before the title.
	var reordered []string
	var title string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			reordered = append(reordered, args[i])
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				reordered = append(reordered, args[i+1])
				i++ // skip flag value
			}
		} else if title == "" {
			title = args[i]
		}
	}

	fs := flag.NewFlagSet("task-add", flag.ExitOnError)
	priority := fs.String("priority", "medium", "Task priority (critical, high, medium, low)")
	taskBody := fs.String("body", "", "Detailed description/instructions")
	assignee := fs.String("assignee", "", "Assign task to a specific agent")
	fs.Parse(reordered)

	if title == "" {
		fmt.Fprintln(os.Stderr, `Usage: coral-board task add "title" [--body "details"] [--priority P] [--assignee "Agent Name"]`)
		os.Exit(1)
	}
	subscriberID := resolveSubscriberID()

	reqBody := map[string]any{
		"title":         title,
		"priority":      *priority,
		"subscriber_id": subscriberID,
	}
	if *taskBody != "" {
		reqBody["body"] = *taskBody
	}
	if *assignee != "" {
		reqBody["assigned_to"] = *assignee
	}

	data, status, err := apiCallRaw("POST", "/"+st.Project+"/tasks", reqBody)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusCreated && status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error creating task: %s\n", string(data))
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	id := task["id"]
	fmt.Printf("Created Task #%.0f: %s\n", id, title)
}

func cmdTaskList(st *boardState) {
	data, statusCode, err := apiCallRaw("GET", "/"+st.Project+"/tasks", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if statusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error listing tasks: %s\n", string(data))
		os.Exit(1)
	}

	var result map[string]any
	json.Unmarshal(data, &result)
	tasks, _ := result["tasks"].([]any)

	if len(tasks) == 0 {
		fmt.Println("No tasks found.")
		return
	}

	fmt.Printf("%-4s %-12s %-9s %-15s %s\n", "ID", "Status", "Priority", "Assignee", "Title")
	for _, t := range tasks {
		task, _ := t.(map[string]any)
		id, _ := task["id"].(float64)
		tStatus, _ := task["status"].(string)
		tPriority, _ := task["priority"].(string)
		tAssignee := "—"
		if a, ok := task["assigned_to"].(string); ok && a != "" {
			tAssignee = a
		}
		tTitle, _ := task["title"].(string)

		fmt.Printf("#%-3.0f %-12s %-9s %-15s %s\n", id, tStatus, tPriority, tAssignee, tTitle)
	}
}

func cmdTaskClaim(st *boardState) {
	subscriberID := resolveSubscriberID()
	body := map[string]string{"subscriber_id": subscriberID}

	data, status, err := apiCallRaw("POST", "/"+st.Project+"/tasks/claim", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if status == http.StatusNotFound {
		fmt.Println("No available tasks")
		return
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error claiming task: %s\n", string(data))
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	id, _ := task["id"].(float64)
	title, _ := task["title"].(string)
	taskBody, _ := task["body"].(string)
	priority, _ := task["priority"].(string)
	fmt.Printf("Claimed Task #%.0f (%s): %s\n", id, priority, title)
	if taskBody != "" {
		fmt.Printf("\n%s\n", taskBody)
	}
}

func cmdTaskCurrent(st *boardState) {
	subscriberID := resolveSubscriberID()
	body := map[string]string{"subscriber_id": subscriberID}

	data, status, err := apiCallRaw("POST", "/"+st.Project+"/tasks/current", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if status == http.StatusNotFound {
		fmt.Println("No active task")
		return
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", string(data))
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	id, _ := task["id"].(float64)
	title, _ := task["title"].(string)
	taskBody, _ := task["body"].(string)
	priority, _ := task["priority"].(string)
	status_, _ := task["status"].(string)
	fmt.Printf("Task #%.0f (%s) [%s]: %s\n", id, priority, status_, title)
	if taskBody != "" {
		fmt.Printf("\n%s\n", taskBody)
	}
}

func cmdTaskComplete(st *boardState, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board task complete <id> [--message \"note\"]")
		os.Exit(1)
	}

	taskID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid task ID: %s\n", args[0])
		os.Exit(1)
	}

	fs := flag.NewFlagSet("task-complete", flag.ExitOnError)
	message := fs.String("message", "", "Completion message")
	fs.Parse(args[1:])

	subscriberID := resolveSubscriberID()
	body := map[string]any{
		"subscriber_id": subscriberID,
	}
	if *message != "" {
		body["message"] = *message
	}

	data, status, err := apiCallRaw("POST", fmt.Sprintf("/%s/tasks/%d/complete", st.Project, taskID), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error completing task: %s\n", string(data))
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	title, _ := task["title"].(string)
	fmt.Printf("Completed Task #%d: %s\n", taskID, title)
}

func cmdTaskCancel(st *boardState, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board task cancel <id> [--message \"reason\"]")
		os.Exit(1)
	}

	taskID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid task ID: %s\n", args[0])
		os.Exit(1)
	}

	fs := flag.NewFlagSet("task-cancel", flag.ExitOnError)
	message := fs.String("message", "", "Cancellation reason")
	fs.Parse(args[1:])

	subscriberID := resolveSubscriberID()
	body := map[string]any{
		"subscriber_id": subscriberID,
	}
	if *message != "" {
		body["message"] = *message
	}

	data, status, err := apiCallRaw("POST", fmt.Sprintf("/%s/tasks/%d/cancel", st.Project, taskID), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error cancelling task: %s\n", string(data))
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	title, _ := task["title"].(string)
	fmt.Printf("Cancelled Task #%d: %s\n", taskID, title)
}

func cmdTaskReassign(st *boardState, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `Usage: coral-board task reassign <id> [--to "Agent Name"]`)
		os.Exit(1)
	}

	taskID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid task ID: %s\n", args[0])
		os.Exit(1)
	}

	fs := flag.NewFlagSet("task-reassign", flag.ExitOnError)
	assignee := fs.String("to", "", "New assignee (empty = unassigned)")
	fs.Parse(args[1:])

	subscriberID := resolveSubscriberID()
	body := map[string]any{
		"subscriber_id": subscriberID,
		"assignee":      *assignee,
	}

	data, status, err := apiCallRaw("POST", fmt.Sprintf("/%s/tasks/%d/reassign", st.Project, taskID), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error reassigning task: %s\n", string(data))
		os.Exit(1)
	}

	var task map[string]any
	json.Unmarshal(data, &task)
	title, _ := task["title"].(string)
	if *assignee != "" {
		fmt.Printf("Reassigned Task #%d to %s: %s\n", taskID, *assignee, title)
	} else {
		fmt.Printf("Reassigned Task #%d (now unassigned): %s\n", taskID, title)
	}
}

// ── Workflow subcommands ─────────────────────────────────────────────

// workflowAPICall performs an HTTP request against /api/workflows (not /api/board).
func workflowAPICall(method, path string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, serverURL+"/api/workflows"+path, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot reach Coral server at %s: %v", serverURL, err)
	}
	defer resp.Body.Close()
	respData, _ := io.ReadAll(resp.Body)
	return respData, resp.StatusCode, nil
}

func cmdWorkflow() {
	if len(os.Args) < 3 {
		printWorkflowUsage()
		os.Exit(1)
	}

	sub := os.Args[2]
	switch sub {
	case "create":
		cmdWorkflowCreate()
	case "list":
		cmdWorkflowList()
	case "trigger":
		cmdWorkflowTrigger()
	case "status":
		cmdWorkflowStatus()
	case "runs":
		cmdWorkflowRuns()
	case "kill":
		cmdWorkflowKill()
	case "--help", "-h", "help":
		printWorkflowUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "Unknown workflow subcommand: %s\n", sub)
		printWorkflowUsage()
		os.Exit(1)
	}
}

func printWorkflowUsage() {
	fmt.Fprintln(os.Stderr, `Usage: coral-board workflow <subcommand> [args]

Subcommands:
  create --file <path>                    Create a workflow from a JSON file
  list                                    List all workflows
  trigger <name> [--context '{"k":"v"}']  Trigger a workflow by name
  status <run-id>                         Check run status with step details
  runs [--workflow <name>] [--status <s>] List recent runs
  kill <run-id>                           Kill a running workflow`)
}

func cmdWorkflowCreate() {
	args := os.Args[3:]
	filePath := ""
	for i, arg := range args {
		if (arg == "--file" || arg == "-f") && i+1 < len(args) {
			filePath = args[i+1]
		}
	}
	if filePath == "" {
		fmt.Fprintln(os.Stderr, "Usage: coral-board workflow create --file <path>")
		os.Exit(1)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		os.Exit(1)
	}

	// Validate it's valid JSON
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid JSON: %v\n", err)
		os.Exit(1)
	}

	respData, status, err := workflowAPICall("POST", "", json.RawMessage(data))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusCreated && status != http.StatusOK {
		var errResp map[string]string
		json.Unmarshal(respData, &errResp)
		fmt.Fprintf(os.Stderr, "Error creating workflow: %s\n", errResp["error"])
		os.Exit(1)
	}

	var wf map[string]any
	json.Unmarshal(respData, &wf)
	name, _ := wf["name"].(string)
	id, _ := wf["id"].(float64)
	fmt.Printf("Created workflow '%s' (id: %.0f)\n", name, id)
}

func cmdWorkflowList() {
	respData, status, err := workflowAPICall("GET", "", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error listing workflows: %s\n", string(respData))
		os.Exit(1)
	}

	var result map[string]any
	json.Unmarshal(respData, &result)
	workflows, _ := result["workflows"].([]any)

	if len(workflows) == 0 {
		fmt.Println("No workflows found.")
		return
	}

	fmt.Printf("%-4s %-20s %-7s %-5s %-12s %s\n", "ID", "Name", "Enabled", "Steps", "Last Run", "Description")
	for _, w := range workflows {
		wf, _ := w.(map[string]any)
		id, _ := wf["id"].(float64)
		name, _ := wf["name"].(string)
		desc, _ := wf["description"].(string)
		enabled, _ := wf["enabled"].(float64)
		stepCount, _ := wf["step_count"].(float64)

		enabledStr := "yes"
		if enabled == 0 {
			enabledStr = "no"
		}

		lastRunStr := "—"
		if lr, ok := wf["last_run"].(map[string]any); ok && lr != nil {
			lrStatus, _ := lr["status"].(string)
			lastRunStr = lrStatus
		}

		// Truncate description
		if len(desc) > 40 {
			desc = desc[:37] + "..."
		}

		fmt.Printf("#%-3.0f %-20s %-7s %-5.0f %-12s %s\n", id, name, enabledStr, stepCount, lastRunStr, desc)
	}
}

func cmdWorkflowTrigger() {
	args := os.Args[3:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board workflow trigger <name> [--context '{...}']")
		os.Exit(1)
	}

	name := args[0]
	var triggerCtx string
	for i, arg := range args {
		if arg == "--context" && i+1 < len(args) {
			triggerCtx = args[i+1]
		}
	}

	body := map[string]any{
		"trigger_type": "cli",
	}
	if triggerCtx != "" {
		var ctx json.RawMessage
		if err := json.Unmarshal([]byte(triggerCtx), &ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Invalid context JSON: %v\n", err)
			os.Exit(1)
		}
		body["context"] = ctx
	}

	respData, status, err := workflowAPICall("POST", "/by-name/"+url.PathEscape(name)+"/trigger", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		var errResp map[string]string
		json.Unmarshal(respData, &errResp)
		fmt.Fprintf(os.Stderr, "Error triggering workflow: %s\n", errResp["error"])
		os.Exit(1)
	}

	var result map[string]any
	json.Unmarshal(respData, &result)
	runID, _ := result["run_id"].(float64)
	wfName, _ := result["workflow_name"].(string)
	fmt.Printf("Triggered workflow '%s' — run #%.0f (status: pending)\n", wfName, runID)
}

func cmdWorkflowStatus() {
	args := os.Args[3:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board workflow status <run-id>")
		os.Exit(1)
	}

	runID := args[0]
	respData, status, err := workflowAPICall("GET", "/runs/"+runID, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		var errResp map[string]string
		json.Unmarshal(respData, &errResp)
		fmt.Fprintf(os.Stderr, "Error: %s\n", errResp["error"])
		os.Exit(1)
	}

	var run map[string]any
	json.Unmarshal(respData, &run)

	runIDFloat, _ := run["id"].(float64)
	wfName, _ := run["workflow_name"].(string)
	runStatus, _ := run["status"].(string)
	currentStep, _ := run["current_step"].(float64)
	triggerType, _ := run["trigger_type"].(string)
	startedAt, _ := run["started_at"].(string)
	finishedAt, _ := run["finished_at"].(string)
	errorMsg, _ := run["error_msg"].(string)

	fmt.Printf("Run #%.0f — %s\n", runIDFloat, wfName)
	fmt.Printf("  Status:       %s\n", runStatus)
	fmt.Printf("  Trigger:      %s\n", triggerType)
	fmt.Printf("  Current step: %.0f\n", currentStep)
	if startedAt != "" {
		fmt.Printf("  Started:      %s\n", startedAt)
	}
	if finishedAt != "" {
		fmt.Printf("  Finished:     %s\n", finishedAt)
	}
	if errorMsg != "" {
		fmt.Printf("  Error:        %s\n", errorMsg)
	}

	// Print step details
	steps, _ := run["steps"].([]any)
	if len(steps) > 0 {
		fmt.Println("\n  Steps:")
		for _, s := range steps {
			step, _ := s.(map[string]any)
			idx, _ := step["index"].(float64)
			sName, _ := step["name"].(string)
			sType, _ := step["type"].(string)
			sStatus, _ := step["status"].(string)

			statusIcon := statusSymbol(sStatus)
			line := fmt.Sprintf("    %s [%.0f] %s (%s)", statusIcon, idx, sName, sType)

			// Add timing info if available
			sStarted, _ := step["started_at"].(string)
			sFinished, _ := step["finished_at"].(string)
			if sStarted != "" && sFinished != "" {
				line += fmt.Sprintf(" — %s to %s", truncTime(sStarted), truncTime(sFinished))
			} else if sStarted != "" {
				line += fmt.Sprintf(" — started %s", truncTime(sStarted))
			}

			fmt.Println(line)

			// Show output tail for completed/failed steps
			if tail, ok := step["output_tail"].(string); ok && tail != "" {
				// Indent and truncate output
				lines := strings.Split(strings.TrimSpace(tail), "\n")
				maxLines := 5
				start := 0
				if len(lines) > maxLines {
					start = len(lines) - maxLines
					fmt.Printf("      ... (%d lines truncated)\n", start)
				}
				for _, l := range lines[start:] {
					fmt.Printf("      %s\n", l)
				}
			}
		}
	}
}

func cmdWorkflowRuns() {
	args := os.Args[3:]
	workflowName := ""
	statusFilter := ""
	limit := "20"
	for i, arg := range args {
		if arg == "--workflow" && i+1 < len(args) {
			workflowName = args[i+1]
		}
		if arg == "--status" && i+1 < len(args) {
			statusFilter = args[i+1]
		}
		if arg == "--limit" && i+1 < len(args) {
			limit = args[i+1]
		}
	}

	var path string
	if workflowName != "" {
		// First, look up the workflow by name to get its ID
		wfData, wfStatus, err := workflowAPICall("GET", "/by-name/"+url.PathEscape(workflowName), nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if wfStatus != http.StatusOK {
			fmt.Fprintf(os.Stderr, "Workflow '%s' not found\n", workflowName)
			os.Exit(1)
		}
		var wf map[string]any
		json.Unmarshal(wfData, &wf)
		wfID, _ := wf["id"].(float64)
		path = fmt.Sprintf("/%.0f/runs?limit=%s", wfID, limit)
		if statusFilter != "" {
			path += "&status=" + url.QueryEscape(statusFilter)
		}
	} else {
		path = "/runs/recent?limit=" + limit
		if statusFilter != "" {
			path += "&status=" + url.QueryEscape(statusFilter)
		}
	}

	respData, status, err := workflowAPICall("GET", path, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", string(respData))
		os.Exit(1)
	}

	var result map[string]any
	json.Unmarshal(respData, &result)
	runs, _ := result["runs"].([]any)

	if len(runs) == 0 {
		fmt.Println("No runs found.")
		return
	}

	fmt.Printf("%-6s %-20s %-10s %-8s %-20s %s\n", "Run", "Workflow", "Status", "Trigger", "Started", "Finished")
	for _, r := range runs {
		run, _ := r.(map[string]any)
		id, _ := run["id"].(float64)
		wfName, _ := run["workflow_name"].(string)
		rStatus, _ := run["status"].(string)
		trigger, _ := run["trigger_type"].(string)
		started, _ := run["started_at"].(string)
		finished, _ := run["finished_at"].(string)

		if wfName == "" {
			wfName = "—"
		}

		fmt.Printf("#%-5.0f %-20s %-10s %-8s %-20s %s\n",
			id, wfName, rStatus, trigger, truncTime(started), truncTime(finished))
	}
}

func cmdWorkflowKill() {
	args := os.Args[3:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: coral-board workflow kill <run-id>")
		os.Exit(1)
	}

	runID := args[0]
	respData, status, err := workflowAPICall("POST", "/runs/"+runID+"/kill", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		var errResp map[string]string
		json.Unmarshal(respData, &errResp)
		fmt.Fprintf(os.Stderr, "Error: %s\n", errResp["error"])
		os.Exit(1)
	}

	fmt.Printf("Killed workflow run #%s\n", runID)
}

// statusSymbol returns a unicode symbol for a step status.
func statusSymbol(s string) string {
	switch s {
	case "completed":
		return "+"
	case "running":
		return ">"
	case "failed":
		return "x"
	case "skipped":
		return "-"
	case "killed":
		return "!"
	default:
		return "."
	}
}

// truncTime truncates a timestamp to the first 19 chars (YYYY-MM-DDTHH:MM:SS).
func truncTime(t string) string {
	if len(t) > 19 {
		return t[:19]
	}
	return t
}

// sortEntries sorts by timestamp string (ISO format sorts lexicographically).
func sortEntries(entries []exportEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Timestamp < entries[j-1].Timestamp; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}
