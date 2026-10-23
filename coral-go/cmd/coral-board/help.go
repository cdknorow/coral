package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/subgentic/coral/internal/clihelp"
)

// commands are the top-level commands main dispatches.
var commands = map[string]bool{
	"join": true, "post": true, "read": true, "check": true, "projects": true,
	"subscribers": true, "status": true, "working-mode": true, "leave": true,
	"delete": true, "export": true, "peek": true, "task": true, "workflow": true,
	"wait": true, "send": true, "targets": true, "health": true,
	"queue": true,
}

// textFlags take free text that is sent to the server.
var textFlags = map[string]bool{
	"message": true, "reason": true, "body": true, "instructions": true,
	"custom-instructions": true, "workflow-instructions": true,
}

// handleHelp prints usage when args ask for it: "--help", "-h" or "help" as
// the command, or a help flag as any argument of a known command (also
// "task help" and "workflow help"). It reports whether it handled args and
// the exit code.
func handleHelp(args []string, w io.Writer) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	cmd, rest := args[0], args[1:]
	if cmd == "help" && len(rest) > 0 && !clihelp.IsHelpArg(rest[0]) {
		return true, helpTopic("coral-board", rest[0], w, func(name string) string {
			if commands[name] {
				return commandUsage(name, nil)
			}
			return ""
		})
	}
	if clihelp.IsHelpArg(cmd) || cmd == "help" {
		fmt.Fprintln(w, usageText)
		return true, 0
	}
	subHelp := (cmd == "task" || cmd == "workflow") && len(rest) > 0 && rest[0] == "help"
	if !commands[cmd] || !(subHelp || clihelp.Requested(rest)) {
		return false, 0
	}
	fmt.Fprintln(w, commandUsage(cmd, rest))
	return true, 0
}

// commandUsage returns the usage for one command, or for one task or
// workflow subcommand when rest names it.
func commandUsage(cmd string, rest []string) string {
	switch cmd {
	case "task", "workflow":
		full := taskUsageText
		if cmd == "workflow" {
			full = workflowUsageText
		}
		if len(rest) > 0 {
			if lines := clihelp.UsageLines(full, rest[0]); lines != "" {
				return "Usage: coral-board " + cmd + " <subcommand> [args]\n\n" + lines
			}
		}
		return full
	}
	if lines := clihelp.UsageLines(usageText, cmd); lines != "" {
		switch cmd {
		case "post":
			lines += "\n\n" + postNote + "\n\n" + windowsTextNote
		case "send":
			lines += "\n\n" + sendNote + "\n\n" + windowsTextNote
		case "wait":
			lines += "\n\n" + waitNote
		}
		return "Usage: coral-board <command> [args]\n\n" + lines
	}
	return usageText
}

// windowsTextNote is added to post's and send's help: the two things that damage a message on Windows before Coral sees it.
const windowsTextNote = `On Windows: PowerShell 5.1 drops the double quotes inside a native
program's argument. With its default US-ASCII $OutputEncoding, piped
non-ASCII text becomes '?'. For text with quotes or non-ASCII use
--file <path> (a UTF-8 file), or set
$OutputEncoding = [Text.UTF8Encoding]::new()  before piping to "-".
PowerShell 7 and cmd.exe need neither. More: coral-board help windows`

// postNote is how to write a post: in post's help, not in the list of every command.
const postNote = `Writing a post: start with a one-line summary, then numbered points, one
per line, and answer by number. Use @Name; put SHAs, branches and paths in
backticks (in Bash use post - <<'EOF', never double quotes: backticks and
$( ) run commands there). For long reports, logs, diffs and evidence, use
coral-agent artifact upload <file> and link coral://artifacts/<digest> in a
short post. Text from "-" or --file must be UTF-8: a leading byte-order mark
is dropped, CRLF becomes LF and one final newline is dropped; nothing else.`

// sendNote is what --attach sends and what it does not guarantee: in send's help.
const sendNote = `--attach to a team on this Coral: up to 4 files your team uploaded, 8 MiB each,
16 MiB per send; both boards need cross_team_artifacts on. The files are not
scanned for malware and not redacted: never attach secrets. An agent that knows
an artifact id on this Coral can read its bytes and upload them again: team
visibility stops accidents, it is not a boundary; the switches, allowlist and
quotas are the limits.
--attach with --address: up to 4 files you uploaded (8 MiB each, 16 MiB per
send) go through Coral Hub before the message, as they are: not scanned for
malware by the receiver, not redacted by the hub (a text file with a secret in
it is refused here). Your board and the receiving board both need
cross_team_artifacts on.`

// waitNote is what wait's options mean, in wait's help.
const waitNote = `wait registers the wait and returns at once: stop your turn then; Coral wakes
you with a terminal notification when it resolves. A wait lasts 2 hours unless
you set --timeout (a duration such as 30m or 2h). A new wait replaces your old
one on the board.
  --status    show your active wait (or that there is none)
  --cancel    remove it
  --block     do not return: wait in this process until it resolves
  --reason    a short note shown with the wait
Choose one target at most: --from, --task, --commit or --operator; with none,
any board message ends the wait.`

// postFlags and sendFlags are the options of post and of send. They are read in any case (--TO is --to) and anywhere before or among the words of the message.
var (
	postFlags = []string{"--to", "--file"}
	sendFlags = []string{"--board", "--to", "--address", "--reply-to", "--message-id", "--attach", "--file"}
)

// foldOpt is arg lower-cased when that is one of known (so --TO is --to), else arg as it is.
func foldOpt(arg string, known []string) string {
	if strings.HasPrefix(arg, "--") {
		low := strings.ToLower(arg)
		for _, k := range known {
			if low == k {
				return k
			}
		}
	}
	return arg
}

// unknownOptionError is the refusal of an option-shaped first word of a message, with a "did you mean" when it is a typo of a real option.
func unknownOptionError(cmd, arg string, known []string) error {
	hint := "To post text that starts with '-', put -- before it."
	if near, ok := clihelp.NearFlag(arg, known); ok {
		hint = "Did you mean " + near + "? " + hint
	}
	return fmt.Errorf("unknown option %q for %s. %s", arg, cmd, hint)
}

// checkFreeText rejects free text that is really an option, so a mistyped
// flag is never posted or stored: the first word of a post, and the value of
// any text-taking option.
func checkFreeText(args []string) error {
	if len(args) == 0 || !commands[args[0]] {
		return nil
	}
	rest := args[1:]
	if args[0] == "post" {
		// Options are read before the message and among its words; once the text has begun, a "--word" that is not an option is text
		// ("post use --force here" posts as written, "post hello --fix" too). Only a first word that looks like an option is refused.
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--" {
				break
			}
			if opt := foldOpt(rest[i], postFlags); opt == "--to" || opt == "--file" {
				i++
				continue
			}
			if clihelp.IsFlagLike(rest[i]) {
				return unknownOptionError("post", rest[i], postFlags)
			}
			break
		}
	}
	if args[0] == "send" {
		// The message's first word must not be an option.
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--" {
				break
			}
			switch foldOpt(rest[i], sendFlags) {
			case "--board", "--to", "--address", "--reply-to", "--message-id", "--attach", "--file":
				i++
				continue
			}
			if clihelp.IsFlagLike(rest[i]) {
				return unknownOptionError("send", rest[i], sendFlags)
			}
			break
		}
	}
	return clihelp.CheckTextFlags(rest, textFlags)
}

// helpTopic answers "help <topic>": the topic's page, the usage of a command it names, or (for anything else) an error that lists the topics.
func helpTopic(tool string, name string, w io.Writer, commandUsage func(string) string) (code int) {
	if page, ok := clihelp.TopicPage(tool, name); ok {
		fmt.Fprintln(w, page)
		return 0
	}
	if u := commandUsage(name); u != "" {
		fmt.Fprintln(w, u)
		return 0
	}
	fmt.Fprintln(os.Stderr, "Error: "+clihelp.UnknownTopic(tool, name))
	return 2
}
