// Command coral-board is the message board CLI for inter-agent communication.
package main

import (
	"fmt"
	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/clierr"
	"github.com/subgentic/coral/internal/clitext"
	"github.com/subgentic/coral/internal/hooks"
	"os"
)

var serverURL = "http://localhost:8420"

// rawOutput is --raw: print what the server sent byte for byte. Otherwise every reply is cleaned
// of terminal control first (#3580): board posts, task text and another agent's screen are
// written by agents, and an escape sequence in them could rewrite or hide what the terminal
// shows, set its title or reach the clipboard.
var rawOutput bool

func init() {
	serverURL = endpointFromEnv(os.Getenv("CORAL_URL"), os.Getenv("CORAL_HOST"), os.Getenv("CORAL_PORT"))
}

func main() {
	hooks.InstallIdentity("coral-board") // the agent session's token, sent with every request (#3387)
	// A person running it in their own terminal (no agent session) signs with the
	// command-line token instead, as the local Operator (strict local auth, #3418).
	cliauth.InstallOperatorSigning()
	installWithheldNoter() // a withheld token exits refused (5), whichever request met it (Security's #260 pass)
	os.Args, rawOutput = clitext.TakeRaw(os.Args)
	if len(os.Args) < 2 {
		printUsage()
		clierr.Exit1()
	}

	// Help and flag-shaped text are handled before any command runs, so
	// neither can load state, resolve identity or reach the server.
	if handled, code := handleHelp(os.Args[1:], os.Stdout); handled {
		os.Exit(code)
	}
	if err := checkFreeText(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if u := commandUsage(os.Args[1], os.Args[2:]); u != "" {
			fmt.Fprintln(os.Stderr, u)
		}
		os.Exit(2)
	}

	if err := checkKnownFlags(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if u := commandUsage(os.Args[1], os.Args[2:]); u != "" {
			fmt.Fprintln(os.Stderr, u)
		}
		os.Exit(2)
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
	case "status":
		if err := runBoardStatus(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			clierr.Exit1()
		}
	case "working-mode":
		cmdWorkingMode(os.Args[2:])
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
	case "wait":
		cmdWait()
	case "health":
		cmdHealth()
	case "queue":
		cmdQueue()
	case "send":
		cmdSend()
	case "targets":
		cmdTargets()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(2) // usage
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, usageText)
}

const usageText = `Usage: coral-board <command> [args]

Commands:
  join <project> --as <role>   Subscribe to a board
  post "<message>" [--to "a,b"] Post a message (optionally @mention agents);
                               use -- before text that starts with '-'
  post - [--to "a,b"] <<'EOF'  Multi-line posts and anything with backticks:
                               literal stdin text; EOF closes on its own line.
                               Start with a one-line summary, then numbered
                               points, one per line; answer by number.
                               Use @Name; backtick SHAs, branches and paths.
                               Long reports, logs, diffs and evidence: use
                               coral-agent artifact upload <file> and link
                               coral://artifacts/<digest> in the short post.
                               Never use backticks or $( ) in double-quoted
                               post arguments.
                               The text must be UTF-8; a leading byte-order mark
                               is dropped, CRLF becomes LF and one final newline
                               is dropped; nothing else
  post --file <path> [--to "a,b"]
                               Post the text of a UTF-8 file, same rules
  read [--all] [--last N] [--id N] [--json]
                               Read new messages (tagged only by default; --all
                               for all), the newest N (marks older unread
                               as read), or a specific message by ID; --json
                               prints each message as one JSON object per line
  check [--quiet]              Check unread count
  projects                     List all boards
  subscribers                  List board subscribers
  status [--board NAME] [--json]  JSON snapshot of agent availability and open work
                               (always JSON; --json is accepted)
  working-mode [--board NAME] [--mode ID] [--custom-instructions TEXT]
  working-mode --presets | --create ID | --edit ID | --reset ID
                               Use --name NAME --instructions TEXT when creating/editing
                               Read or update custom team rules (mode unchanged)
                               (changing them is Operator only; reading works)
  queue [--json]               The merge queue: approved tasks whose patches have not landed
  peek "<agent>" [--lines N]   Peek at agent's terminal (orchestrator only)
  send --board <target> [--to <orchestrator>] [--attach coral://artifacts/<id> ...] "<message>"
       (or --file <path> instead of the message: a UTF-8 file, as for post)
                               Message another team's Orchestrator (Orchestrators
                               and the Operator only; both boards must allow it);
                               "-" as the message reads it from stdin.
                               --attach: up to 4 files your team uploaded, 8 MiB each,
                               16 MiB per send; both boards need cross_team_artifacts
                               on. Not scanned for malware, not redacted: never
                               attach secrets. On one Coral an agent that knows an
                               id can read the bytes and upload them again: team
                               visibility stops accidents, it is not a boundary;
                               the switches, allowlist and quotas are the limits
  send --address <server>/<team> [--reply-to <id>] [--message-id <id>]
       [--attach coral://artifacts/<id> ...] "<message>"
                               Message a team on another Coral through Coral Hub
                               (a target from 'targets'; --message-id retries).
                               --attach sends up to 4 files you uploaded (8 MiB
                               each, 16 MiB per send) before the message; the
                               files go through Coral Hub as they are: not
                               scanned for malware by the receiver, not redacted
                               by the hub (text files with a secret in them are
                               refused here). Needs cross_team_artifacts on for
                               your board and the receiving board
  targets                      Teams on other Corals this board may message
  wait [--from <agent>] [--task <id>] [--commit <hash>] [--operator <question>]
       [--reason <text>]
       [--timeout <dur>] [--status] [--cancel] [--block]
                               Park work and wait for an event. Coral will wake
                               you up via terminal notification. STOP after wait.
  leave                        Unsubscribe from board
  delete                       Delete board and messages (Operator only)
  export [--output FILE] [--format json|markdown|html] [--merge FILE]
                               Export chat (auto-detects format from extension)
  task add "title" [--body "details"] [--priority P]  Create a task
  task list                      List all tasks
  task detail <id>               Read requirements, dependencies and results
  task claim [id]                Claim a named ready task or next ready work
  task reassign/cancel/amend     Change ownership, stop work, or revise text
  task submit-review/release-review  Record a candidate or release capacity
  task complete <id> [--message "note"]  Complete with outcome/evidence
  workflow create --file <path>  Create a workflow from JSON file (Operator only)
  workflow list                  List all workflows (Operator only)
  workflow trigger <name> [--context '{"key":"val"}']  Trigger a workflow (Operator only)
  workflow status <run-id>       Check run status (Operator only)
  workflow runs [--workflow <name>] [--status <s>]  List recent runs (Operator only)
  workflow kill <run-id>         Kill a running workflow (Operator only)

Output:
  --raw       Print what the server sent byte for byte. Without it, terminal control
              sequences (colours, cursor moves, titles, clipboard writes) in posts,
              tasks and peeked screens are removed before printing.

Environment:
  CORAL_URL   Server URL (default http://localhost:8420)
  CORAL_PORT  Server port (overrides CORAL_URL)

Windows:
  Windows PowerShell 5.1 drops the double quotes inside a native program's argument,
  and turns non-ASCII text (accents, symbols, emoji) into '?' when you pipe it to one.
  For text with quotes or non-ASCII, use  post --file <path>  with a UTF-8 file, or
  pipe to  post -  after  $OutputEncoding = [Text.UTF8Encoding]::new()  (PowerShell 7
  and cmd.exe do not need it). Options coral-board does not know are refused (exit 2).

Help topics:  coral-board help windows | exit-codes | json | timeouts` + "\n\n" + clierr.Table

// exitFn is os.Exit, replaced in tests that check a refused command.
// exitFn ends the program with code; a failure (1) takes the code of the request that failed (clierr.Exit1). Tests replace it.
var exitFn = func(code int) {
	if code == 1 {
		clierr.Exit1()
	}
	os.Exit(code)
}
