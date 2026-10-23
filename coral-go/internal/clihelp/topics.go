package clihelp

import "strings"

// Topics are the short help pages behind "coral-board help <topic>" and "coral-agent help <topic>" (#236): the things that bite on Windows and
// that a usage line cannot explain. Plain text, under 25 lines each, Windows examples first. In a page, "TOOL" is the program's name and
// "READ" a command of it that reads something (so the examples are commands the tool really has: the CLIs' tests check every "TOOL <word>").

const windowsBoard = `Windows: quoting, encoding and long text

Windows PowerShell 5.1 drops the double quotes inside an argument it hands to a program:
  TOOL post 'she said "hi"'            arrives as:  she said hi
Put the text in a UTF-8 file and use --file (PowerShell 5.1's Set-Content -Encoding UTF8 adds a
byte-order mark; Coral drops it):
  Set-Content -Encoding UTF8 msg.txt 'she said "hi"'
  TOOL post --file msg.txt

When $OutputEncoding is PowerShell 5.1's default (US-ASCII; check with: $OutputEncoding), piping accents,
symbols or emoji turns them into '?' before Coral sees them. Some machines are set to UTF-8 already; a pipe
may then add a byte-order mark and CRLF, which Coral drops. Set the pipe encoding first:
  $OutputEncoding = [Text.UTF8Encoding]::new()
  'café - ok' | TOOL post -
PowerShell 7 and cmd.exe need neither fix. In cmd.exe use double quotes, not single:
  TOOL post "hello world"

Text from "-" (stdin) or --file must be UTF-8 (UTF-16 is refused with this advice). What changes:
a leading byte-order mark is dropped, CRLF becomes LF, one final newline is dropped. Nothing else.

Paths: forward or back slashes both work; quote a path that has spaces. PowerShell and cmd.exe do not
expand '~' (Git Bash does).
More: TOOL help exit-codes | json | timeouts`

const windowsAgent = `Windows: quoting, encoding and paths

Windows PowerShell 5.1 drops the double quotes inside an argument it hands to a program:
  TOOL task add 'she said "hi"'        arrives as:  she said hi
Leave double quotes out of titles and messages there (PowerShell 7 and cmd.exe pass them whole), or put
the text in a file and share the file:
  TOOL artifact upload "C:\Users\me\My Notes\report.md"
Accents, symbols and emoji in an argument arrive intact. Piping them to a program can turn them into '?'
under PowerShell 5.1's default encoding: for posting a message, see  coral-board help windows
(post --file, post -).

In cmd.exe use double quotes, not single:
  TOOL task add "fix the build"

Paths: forward or back slashes both work; quote a path that has spaces. PowerShell and cmd.exe do not
expand '~' (Git Bash does).
More: TOOL help exit-codes | json | timeouts`

const exitCodes = `Exit codes: what each one means and what to do

  0  ok
  1  any other failure: read the message on stderr.
  2  usage: an option or value the command does not take. Nothing was sent. Run: TOOL <command> --help
  3  cannot reach Coral: is it running (the tray icon)? Is CORAL_URL (or CORAL_PORT) the address
     Coral listens on?ADDR3
  4  no answer in time: Coral may be busy. A read can be retried. After a write, check its result
     before you retry: it may have gone through. Or set CORAL_TIMEOUT (TOOL help timeouts).
  5  refused (HTTP 401 or 403): your role or session may not do this. Inside an agent session it is
     your role's rights; from a person's terminal, check you are on the right Coral. It is also the
     code when the tool would not send the agent's token (a program other than Coral on its port, or
     plain http to another computer); the message says which.
  6  not found (HTTP 404): the board, task or message does not exist. Check the id.

Reading the code:
  PowerShell:  TOOL READ; $LASTEXITCODE
  cmd.exe:     TOOL READ
               echo %ERRORLEVEL%        (a line of its own: cmd reads %ERRORLEVEL% before anything on the same line has run)
  Linux/macOS: TOOL READ; echo $?

Messages go to stderr, results to stdout: a failure never prints half a JSON document.`

const timeouts = `Timeouts: CORAL_TIMEOUT

A request waits 10 seconds for Coral by default (15 for docs, 30 for an artifact download, 5 minutes
for an upload). CORAL_TIMEOUT replaces that: a duration such as 30s or 2m, never below 1 second or
above 10 minutes. A value that is not a duration is ignored, with a note.
  PowerShell:  $env:CORAL_TIMEOUT = '30s'; TOOL READ
  cmd.exe:     set CORAL_TIMEOUT=30s & TOOL READ
  Linux/macOS: CORAL_TIMEOUT=30s TOOL READ
When the time runs out the command exits 4 and says so (TOOL help exit-codes). For a command that
writes (a post, a task change), a timeout does not mean it failed: check the result (read or
task detail) before you retry.
A command with its own long wait (coral-board wait) keeps its own timing.`

const jsonBoard = `JSON output: coral-board

  read --json         one JSON object per line, one per message (not an array)
  status              one JSON object (availability and open work); --json is accepted
  task list --json    a JSON array of tasks, [] when there are none
  task detail <id>    one task as a JSON object; --json is accepted
  task show <id> --json  one JSON object: title, status, assignee, body, outcome, result message, and the artifact links with their sizes
  task current --json the task in progress as a JSON object, null when there is none
  queue --json        the merge queue, as the server sent it
Everything else prints text. A failure prints its message on stderr and nothing on stdout.
In PowerShell 5.1 set  [Console]::OutputEncoding = [Text.UTF8Encoding]::new()  before reading
non-ASCII output into a variable, or it is decoded with the OEM code page.
  coral-board task list --json | ConvertFrom-Json`

const jsonAgent = `JSON output: coral-agent

  task list --json    a JSON array of your tasks, [] when there are none
  task detail <id>    one task as a JSON object; --json is accepted
  task current --json the task in progress as a JSON object, null when there is none
Everything else prints text (or the JSON the server sent, as it says in each command's help).
A failure prints its message on stderr and nothing on stdout.
In PowerShell 5.1 set  [Console]::OutputEncoding = [Text.UTF8Encoding]::new()  before reading
non-ASCII output into a variable, or it is decoded with the OEM code page.
  coral-agent task list --json | ConvertFrom-Json`

// TopicNames is the topics a tool answers, in the order its help lists them.
func TopicNames() []string { return []string{"windows", "exit-codes", "json", "timeouts"} }

// TopicLine is the line a tool's usage ends with to name the topics.
func TopicLine(tool string) string {
	return "Help topics:  " + tool + " help " + strings.Join(TopicNames(), " | ")
}

// TopicPage is the page for name as tool (coral-board or coral-agent) prints it, or false when there is no such topic.
func TopicPage(tool, name string) (string, bool) {
	board := tool == "coral-board"
	var page string
	switch name {
	case "windows":
		page = windowsAgent
		if board {
			page = windowsBoard
		}
	case "exit-codes":
		read, addr := "task list", ""
		if board {
			read = "read"
			addr = " After 'coral-board join', the address saved with the\n     board (server_url in its state file) is used instead of CORAL_URL."
		}
		page = strings.NewReplacer("READ", read, "ADDR3", addr).Replace(exitCodes)
	case "timeouts":
		read := "task list"
		if board {
			read = "read"
		}
		page = strings.NewReplacer("READ", read).Replace(timeouts)
	case "json":
		page = jsonAgent
		if board {
			page = jsonBoard
		}
	}
	if page == "" {
		return "", false
	}
	return strings.ReplaceAll(page, "TOOL", tool), true
}

// UnknownTopic is the message for a topic that does not exist, naming the ones that do.
func UnknownTopic(tool, name string) string {
	return "unknown help topic \"" + name + "\". " + TopicLine(tool)
}
