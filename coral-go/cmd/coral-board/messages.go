package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

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
	if sessionName == "" {
		fmt.Fprintln(os.Stderr, "coral-board: not in a Coral agent session (CORAL_SESSION_NAME is not set and Coral doesn't know this process), so there is nothing to join the board with")
		os.Exit(1)
	}
	result, status, err := apiCall("POST", "/"+url.PathEscape(project)+"/subscribe", map[string]string{
		"subscriber_id": subscriberID,
		"session_name":  sessionName,
		"job_title":     jobTitle,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Any non-2xx exits non-zero and names the status (a 401 from a key-protected Coral, a 500),
	// never a silent success; a refusal that has extra guidance (an ambiguous board name, a
	// client error) prints that too (#3589).
	if e := serverError(status, result); e != nil {
		fmt.Fprintln(os.Stderr, e)
		if msg := joinProblem(project, result); msg != "" {
			fmt.Fprint(os.Stderr, msg)
		}
		os.Exit(1)
	}
	// The server answers with the board's id (#3536): a name that is a board's label joins
	// that board, and the state file keeps its id, so a later rename or another board taking
	// the same name never moves this agent.
	boardID := project
	if id, _ := result["project"].(string); id != "" {
		boardID = id
	}
	saveState(&boardState{Project: boardID, JobTitle: jobTitle, ServerURL: serverURL})
	if boardID != project {
		fmt.Printf("Joined '%s' (board %s) as '%s' (subscriber: %s)\n", project, boardID, jobTitle, subscriberID)
		return
	}
	fmt.Printf("Joined '%s' as '%s' (subscriber: %s)\n", project, jobTitle, subscriberID)
}

// joinProblem is what join prints when the server refused it ("" when it didn't): a name
// several boards share lists them by id (#3536), and a new name that breaks the naming
// rule says why (#3534).
// joinProblem is the extra guidance join prints for a refusal that has it (an ambiguous board
// name, or a client error with a message); "" when the result carries no error code. It is shown
// in addition to the status line (#3589).
func joinProblem(project string, result map[string]any) string {
	code, _ := result["error"].(string)
	if code == "" {
		return ""
	}
	var b strings.Builder
	switch code {
	case "ambiguous_board":
		fmt.Fprintf(&b, "More than one board is named '%s'. Join one by its id:\n", project)
		cands, _ := result["candidates"].([]any)
		for _, c := range cands {
			m, _ := c.(map[string]any)
			members, _ := m["members"].(float64)
			fmt.Fprintf(&b, "  coral-board join %v   (%v, created %v, %d members)\n", m["board_id"], m["board_label"], m["created_at"], int(members))
		}
	default:
		msg, _ := result["message"].(string)
		if msg == "" {
			msg = code
		}
		fmt.Fprintf(&b, "Error: %s\n", msg)
	}
	return b.String()
}

func cmdPost() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board. Run: coral-board join <project> --as <role>")
		os.Exit(1)
	}
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, commandUsage("post", nil))
		os.Exit(1)
	}

	// Parse --to flag and collect remaining args as the message
	var toNames string
	var msgParts []string
	literal := false
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--" { // everything after is message text
			msgParts = append(msgParts, os.Args[i+1:]...)
			literal = true
			break
		}
		if os.Args[i] == "--to" && i+1 < len(os.Args) {
			toNames = os.Args[i+1]
			i++ // skip the value
		} else {
			msgParts = append(msgParts, os.Args[i])
		}
	}
	var message string
	if !literal && len(msgParts) == 1 && msgParts[0] == "-" {
		message = stdinTextOrExit("message")
	} else {
		message = strings.Join(msgParts, " ")
		if err := checkShellSyntax(os.Stderr, "post", message); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

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

	result, status, err := apiCall("POST", "/"+url.PathEscape(st.Project)+"/messages", map[string]string{
		"subscriber_id": subscriberID,
		"content":       message,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if e := serverError(status, result); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if id, ok := result["id"]; ok {
		fmt.Printf("Message #%.0f posted to '%s'\n", id, st.Project)
		printMentionWarnings(os.Stderr, result)
	} else {
		fmt.Fprintln(os.Stderr, "Error: Coral accepted the request but returned no message id")
		os.Exit(1)
	}
}

// printMentionWarnings prints what the server said about @mentions that name no member (#4583): the post went out, and a name that
// does not exist is the likeliest reason nobody answers it.
func printMentionWarnings(w io.Writer, result map[string]any) {
	list, _ := result["warnings"].([]any)
	for _, x := range list {
		if text, ok := x.(string); ok && text != "" {
			fmt.Fprintf(w, "Warning: %s\n", text)
		}
	}
}

// readArgs is a read command line: one message by id, the newest N, or the unread (all of them with --all), as
// text or one JSON object per line.
type readArgs struct {
	messageID       int
	useLast         bool
	lastN           int
	readAll, asJSON bool
}

// parseReadArgs reads read's flags anywhere on the command line: --id N, --last N, --all (-a), --json. A later flag
// overrides an earlier one, and a value that is not a number leaves what it had (0), as fmt.Sscanf does.
func parseReadArgs(args []string) readArgs {
	var r readArgs
	for i, arg := range args {
		if arg == "--id" && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &r.messageID)
		}
	}
	for i, arg := range args {
		if arg == "--last" && i+1 < len(args) {
			r.useLast = true
			fmt.Sscanf(args[i+1], "%d", &r.lastN)
		}
	}
	for _, arg := range args {
		if arg == "--all" || arg == "-a" {
			r.readAll = true
		}
		if arg == "--json" {
			r.asJSON = true
		}
	}
	return r
}

// readPath is the request for r on project: the message by id, the newest N, or the subscriber's unread (50; all of
// them, read or not, with --all).
func readPath(r readArgs, project, subscriberID string) string {
	switch {
	case r.messageID > 0:
		return fmt.Sprintf("/%s/messages/all?id=%d", url.PathEscape(project), r.messageID)
	case r.useLast:
		return fmt.Sprintf("/%s/messages/all?limit=%d", url.PathEscape(project), r.lastN)
	case r.readAll:
		return fmt.Sprintf("/%s/messages?subscriber_id=%s&limit=50&all=true", url.PathEscape(project), url.QueryEscape(subscriberID))
	}
	return fmt.Sprintf("/%s/messages?subscriber_id=%s&limit=50", url.PathEscape(project), url.QueryEscape(subscriberID))
}

func cmdRead() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}
	r := parseReadArgs(os.Args)
	subscriberID := resolveSubscriberID()
	data, err := getBoard(readPath(r, st.Project, subscriberID))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var messages []map[string]any
	json.Unmarshal(data, &messages)

	if len(messages) == 0 {
		if r.messageID > 0 {
			fmt.Fprintf(os.Stderr, "Message #%d not found.\n", r.messageID)
			os.Exit(1)
		}
		fmt.Println("No new messages.")
		return
	}
	// --json: one message object per line, exactly as the server returned it (scripts read
	// subscriber_id from it, which the text form doesn't show; #3487).
	if r.asJSON {
		for _, m := range messages {
			line, _ := json.Marshal(m)
			fmt.Println(string(line))
		}
		return
	}
	printMessages(messages)
	if r.useLast && r.messageID == 0 {
		markSkippedRead(st.Project, subscriberID, messages)
	}
}

// printMessages prints each message as "[YYYY-MM-DDTHH:MM] role: content" (the poster's title, else its id).
func printMessages(messages []map[string]any) {
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

// markSkippedRead: --last shows the newest messages rather than reading in order; it marks the board read up to them
// so the unread count (and the nudges about it) covers only what arrives next, and says what was passed over.
func markSkippedRead(project, subscriberID string, messages []map[string]any) {
	var fromID, throughID float64
	for _, m := range messages {
		id, _ := m["id"].(float64)
		if fromID == 0 || id < fromID {
			fromID = id
		}
		if id > throughID {
			throughID = id
		}
	}
	result, _, err := apiCall("POST", "/"+url.PathEscape(project)+"/messages/mark-read", map[string]any{
		"subscriber_id": subscriberID,
		"through_id":    int64(throughID),
		"from_id":       int64(fromID),
	})
	if err != nil {
		return
	}
	if n, _ := result["skipped"].(float64); n > 0 {
		first, _ := result["first_skipped_id"].(float64)
		last, _ := result["last_skipped_id"].(float64)
		plural := "s"
		if n == 1 {
			plural = ""
		}
		fmt.Printf("\n(%.0f earlier unread message%s, #%.0f–#%.0f, skipped and marked read. Read one with: coral-board read --id <id>)\n", n, plural, first, last)
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
		url.PathEscape(st.Project), url.QueryEscape(target), url.QueryEscape(subscriberID), lines)

	resp, err := http.Get(serverURL + "/api/board" + path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	data, _ := readServer(resp.Body)

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
	result, status, err := apiCall("GET", fmt.Sprintf("/%s/messages/check?subscriber_id=%s", url.PathEscape(st.Project), url.QueryEscape(subscriberID)), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if e := serverError(status, result); e != nil {
		fmt.Fprintln(os.Stderr, e)
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
	data, err := getBoard("/projects")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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

	data, err := getBoard("/" + url.PathEscape(st.Project) + "/subscribers")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
	result, status, err := apiCall("DELETE", "/"+url.PathEscape(st.Project)+"/subscribe", map[string]string{
		"subscriber_id": subscriberID,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if e := serverError(status, result); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	deleteState()
	fmt.Printf("Left '%s'\n", st.Project)
}

func cmdDelete() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}
	result, status, err := apiCall("DELETE", "/"+url.PathEscape(st.Project), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if e := serverError(status, result); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	deleteState()
	fmt.Printf("Deleted board '%s'\n", st.Project)
}
