// mock-agent is a lightweight stand-in for the Claude CLI used in stress tests.
// It accepts the same flags as claude (--session-id, --settings, prompt file)
// and runs a heartbeat loop that posts to and reads from the message board via
// the Coral HTTP API. This keeps the tmux session alive and exercises the board.
// Each line typed into it is echoed as "[mock-agent] input: ..." for tests.
//
// With --mock-transcript (or MOCK_AGENT_TRANSCRIPT=1) it also writes a Claude Code transcript
// (transcript.go): a turn for every line typed in and one every 30 seconds, with --model's
// model (default claude-opus-5-5), so stress and soak runs read real transcripts.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

func main() {
	// A CLI answers its version and help calls and exits: Coral's version probe runs "<cli> --version" and waits for it, and a mock that went on to start its agent loop
	// kept that child running in every suite that launched mocks (#5330).
	if answerInfoCall(os.Args[1:], os.Stdout) {
		return
	}

	// A one-shot "--print" call (Coral's goal generator, #4204) is answered as the claude CLI
	// answers it with --output-format json, so a harness using the mock as its CLI sees goals
	// stored, not a timeout per call.
	for _, a := range os.Args[1:] {
		if a == "--print" || a == "-p" {
			printOneShot(os.Stdin, os.Stdout)
			return
		}
	}

	// Parse flags loosely — we only need to consume them so the process doesn't error.
	var sessionID, settingsFile, model string
	var positionalArgs []string
	writeTranscript := os.Getenv("MOCK_AGENT_TRANSCRIPT") == "1"
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session-id":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "--resume":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "--settings":
			if i+1 < len(args) {
				settingsFile = args[i+1]
				i++
			}
		case "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case "--mock-transcript":
			writeTranscript = true
		default:
			if !strings.HasPrefix(args[i], "-") {
				positionalArgs = append(positionalArgs, args[i])
			}
		}
	}

	// Extract board name and env vars from the settings JSON written by the server
	boardName, settingsEnv := parseSettings(settingsFile)

	// The prompt (a positional arg: the PTY backend expands $(cat prompt.txt)) names the board
	// by its label; it is the board name when the settings give none.
	boardLabel := ""
	for _, arg := range positionalArgs {
		if found := extractBoard(arg); found != "" {
			boardLabel = found
			break
		}
	}
	if boardName == "" {
		boardName = boardLabel
	}

	// Apply env vars from settings (CORAL_SESSION_NAME, CORAL_PORT, etc.)
	// Settings env overrides process env only if not already set.
	for k, v := range settingsEnv {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}

	sessionName := os.Getenv("CORAL_SESSION_NAME")
	port := os.Getenv("CORAL_PORT")
	if port == "" {
		port = "8420"
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%s", port)

	// Identity stress test (#3387): record what this agent was given, in the test's own
	// folder (never Coral's): its session token and session name.
	// A board named in MOCK_AGENT_BLIND_PREFIX is a "blind" team: its agents never read
	// the token at all (no dump), like a real agent; they only record what Coral handed
	// them as prompt and settings, which must not mention the token.
	if dir := os.Getenv("MOCK_AGENT_ENV_DUMP"); dir != "" && sessionID != "" {
		// A board's name is a label since #3536 (the id is b-...): the prompt names the label, the
		// settings the id, so either may carry the blind prefix.
		prefix := os.Getenv("MOCK_AGENT_BLIND_PREFIX")
		blind := prefix != "" && (strings.HasPrefix(boardName, prefix) || strings.HasPrefix(boardLabel, prefix))
		if !blind {
			writeAtomic(filepath.Join(dir, sessionID+".env"),
				"CORAL_SESSION_TOKEN="+os.Getenv("CORAL_SESSION_TOKEN")+"\nCORAL_SESSION_NAME="+sessionName+"\n")
		}
		// Every CORAL_ variable the agent was started with, in any case (#3421: none may come from
		// the environment of whatever started the Coral server; Windows reads coral_session_token as
		// CORAL_SESSION_TOKEN, so a lower- or mixed-case name from the parent counts too).
		var coralEnv []string
		for _, kv := range os.Environ() {
			if strings.HasPrefix(strings.ToUpper(kv), "CORAL_") {
				coralEnv = append(coralEnv, kv)
			}
		}
		sort.Strings(coralEnv)
		writeAtomic(filepath.Join(dir, sessionID+".coralenv"), strings.Join(coralEnv, "\n")+"\n")
		settingsText, _ := os.ReadFile(settingsFile)
		writeAtomic(filepath.Join(dir, sessionID+".handed"), strings.Join(os.Args[1:], "\n")+"\n"+string(settingsText))
		if blind {
			writeAtomic(filepath.Join(dir, sessionID+".blind"), sessionName+"\n")
		}
	}

	// Log startup
	fmt.Fprintf(os.Stderr, "[mock-agent] started session_id=%s session_name=%s board=%s port=%s\n",
		short(sessionID), sessionName, boardName, port)

	// Post an introduction
	if boardName != "" {
		post(baseURL, boardName, sessionName,
			fmt.Sprintf("Mock agent online (session=%s)", short(sessionID)))
	}

	var tr *transcript
	if writeTranscript {
		if tr = newTranscript(sessionID, model); tr != nil {
			fmt.Fprintf(os.Stderr, "[mock-agent] writing a transcript: %s\n", tr.path)
		}
	}
	turn := func(prompt string) {
		if err := tr.Turn(prompt); err != nil {
			fmt.Fprintf(os.Stderr, "[mock-agent] transcript: %v\n", err)
		}
	}

	// Echo every line typed into the agent (what Coral sends: prompts, task
	// notifications) with a fixed prefix, so tests can check the pane for the
	// exact input.
	go func() {
		if dir := rawInputDir(); dir != "" {
			runRawInput(dir, sessionID, settingsFile, turn)
			return
		}
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			fmt.Printf("[mock-agent] input: %s\n", sc.Text())
			turn(sc.Text())
		}
	}()

	// Heartbeat loop: post & read every 5s
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	// MOCK_AGENT_IDLE=1: an agent that finished its turn and waits (#4154, the idle-server profile):
	// one turn (so a transcript, when written, ends like Claude's), then nothing until a line is
	// typed in or it is stopped: no reads, posts or output.
	if os.Getenv("MOCK_AGENT_IDLE") == "1" {
		turn("ready")
		<-sig
		fmt.Fprintf(os.Stderr, "[mock-agent] shutting down session=%s\n", short(sessionID))
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	count := 0
	for {
		select {
		case <-sig:
			fmt.Fprintf(os.Stderr, "[mock-agent] shutting down session=%s\n", short(sessionID))
			return
		case <-ticker.C:
			count++
			// Read messages (exercises the read path)
			if boardName != "" {
				msgs := readMessages(baseURL, boardName)
				fmt.Fprintf(os.Stderr, "[mock-agent] heartbeat #%d board=%s msgs=%d\n", count, boardName, msgs)
			}
			// Post a heartbeat every 3rd tick
			if boardName != "" && count%3 == 0 {
				post(baseURL, boardName, sessionName,
					fmt.Sprintf("heartbeat #%d from %s", count, short(sessionID)))
			}
			// Print to stdout so tmux pipe-pane captures activity
			fmt.Printf("[%s] heartbeat #%d\n", time.Now().Format("15:04:05"), count)
			if count%6 == 0 {
				turn(fmt.Sprintf("heartbeat #%d: check the board", count))
			}
		}
	}
}

// writeAtomic writes a dump file whole (temp file, then rename), so a test polling the
// folder never reads it half written.
func writeAtomic(path, content string) {
	tmp := path + ".tmp"
	if os.WriteFile(tmp, []byte(content), 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// extractBoard searches text for a board name pattern like: board "NAME" or message board "NAME"
func extractBoard(text string) string {
	for _, prefix := range []string{`message board "`, `board "`} {
		if idx := strings.Index(text, prefix); idx >= 0 {
			rest := text[idx+len(prefix):]
			if end := strings.Index(rest, `"`); end >= 0 {
				return rest[:end]
			}
		}
	}
	return ""
}

// parseSettings reads the settings JSON and extracts the board name from the
// system prompt and any env vars from the env block.
func parseSettings(path string) (boardName string, env map[string]string) {
	env = make(map[string]string)
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var settings map[string]any
	if json.Unmarshal(data, &settings) != nil {
		return
	}

	// Extract env vars
	if envBlock, ok := settings["env"].(map[string]any); ok {
		for k, v := range envBlock {
			if s, ok := v.(string); ok {
				env[k] = s
			}
		}
	}

	// Extract board name from system prompt
	if sp, ok := settings["systemPrompt"].(string); ok {
		boardName = extractBoard(sp)
	}
	return
}

func post(baseURL, board, sessionName, message string) {
	// Like a real agent's tool call (#3387): post through coral-board, which adds the
	// session token from its inherited environment; the mock never touches the token.
	if cb := coralBoard(); cb != "" {
		cmd := exec.Command(cb, "post", message)
		cmd.Env = append(os.Environ(), "CORAL_URL="+baseURL)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "[mock-agent] coral-board post failed: %v %s\n", err, out)
		}
		return
	}
	subscriberID := os.Getenv("CORAL_SUBSCRIBER_ID")
	if subscriberID == "" {
		subscriberID = sessionName
	}
	body, _ := json.Marshal(map[string]string{
		"subscriber_id": subscriberID,
		"content":       message,
	})
	resp, err := http.Post(fmt.Sprintf("%s/api/board/%s/messages", baseURL, board), "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	resp.Body.Close()
}

func readMessages(baseURL, board string) int {
	// Like a real agent (#3418): read through coral-board, which carries the session token,
	// so the stand-in also works with strict local auth. The count is only for the log.
	if cb := coralBoard(); cb != "" {
		cmd := exec.Command(cb, "read")
		cmd.Env = append(os.Environ(), "CORAL_URL="+baseURL)
		out, err := cmd.Output()
		if err != nil {
			return -1
		}
		return strings.Count(string(out), "\n[")
	}
	resp, err := http.Get(fmt.Sprintf("%s/api/board/%s/messages", baseURL, board))
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var msgs []any
	json.Unmarshal(data, &msgs)
	return len(msgs)
}

// coralBoard is the coral-board the stand-in posts and reads through, like a real agent, so its
// requests carry the session's token (strict local auth, the default since #3936):
// MOCK_AGENT_CORAL_BOARD, else a coral-board next to this executable. "" posts directly, which
// strict local auth refuses.
func coralBoard() string {
	if cb := os.Getenv("MOCK_AGENT_CORAL_BOARD"); cb != "" {
		return cb
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	name := "coral-board"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cb := filepath.Join(filepath.Dir(exe), name)
	if fi, err := os.Stat(cb); err == nil && fi.Mode().IsRegular() {
		return cb
	}
	return ""
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// printOneShot answers a --print prompt read from in: the first line after "LATEST USER
// REQUEST:" (the goal prompt's request) as the result, else a fixed line.
func printOneShot(in io.Reader, out io.Writer) {
	prompt, _ := io.ReadAll(io.LimitReader(in, 1<<20))
	result := "Mock agent goal"
	if _, rest, ok := strings.Cut(string(prompt), "LATEST USER REQUEST:\n"); ok {
		if line, _, _ := strings.Cut(rest, "\n"); strings.TrimSpace(line) != "" {
			result = strings.TrimSpace(line)
		}
	}
	_ = json.NewEncoder(out).Encode(map[string]any{
		"type": "result", "result": result, "is_error": false, "total_cost_usd": 0,
		"usage": map[string]int{"input_tokens": len(prompt) / 4, "output_tokens": 8},
	})
}

// answerInfoCall answers a lone "--version" (or -v, -V) and a lone "--help" (or -h) the way a CLI does: one line on stdout, and the caller then exits 0. It reports whether
// it was one of these calls. The version line carries no version number on purpose: Coral records the number a CLI prints (agent.CLIVersion), and a mock has always
// recorded none, so the mock's sessions keep their empty cli_version (tests that read it are unchanged). Only a call of exactly one argument counts: a launch always
// passes more, and a flag inside one is the agent's own.
func answerInfoCall(args []string, out io.Writer) bool {
	// Claude's sign-in check ("claude auth status --json", #5469) is asked once after Coral launches a Claude agent (#5838) and by background jobs: a CLI answers it and exits, a
	// mock that went on into its agent loop would stay running, register and dump its environment like a launched agent. The mock is "signed in".
	if len(args) >= 2 && len(args) <= 3 && args[0] == "auth" && args[1] == "status" && (len(args) == 2 || args[2] == "--json") {
		fmt.Fprintln(out, `{"loggedIn": true}`)
		return true
	}
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "--version", "-v", "-V":
		fmt.Fprintln(out, "mock-agent (a test double: it has no version)")
		return true
	case "--help", "-h":
		fmt.Fprintln(out, "usage: mock-agent [--session-id ID] [--resume ID] [--settings FILE] [--model NAME] [--mock-transcript] [--print]\n\nA stand-in agent CLI for Coral's tests and harnesses. It is not an agent.")
		return true
	}
	return false
}
