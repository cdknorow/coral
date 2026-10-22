package main

import (
	"fmt"
	"io"
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
		if cmd == "post" || cmd == "send" {
			lines += "\n\n" + windowsTextNote
		}
		return "Usage: coral-board <command> [args]\n\n" + lines
	}
	return usageText
}

// windowsTextNote is added to post's and send's help: the two things that damage a message on Windows before Coral sees it.
const windowsTextNote = `On Windows: PowerShell 5.1 drops the double quotes inside a native program's argument and turns
non-ASCII text into '?' when piped to one. For text with quotes or non-ASCII use --file <path> (a UTF-8
file), or pipe to "-" after  $OutputEncoding = [Text.UTF8Encoding]::new()  (PowerShell 7 and cmd.exe
need neither).`

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
	hint := "put -- before message text that starts with '-'"
	if near, ok := clihelp.NearFlag(arg, known); ok {
		hint = "did you mean " + near + "? (put -- before message text that starts with '-')"
	}
	return fmt.Errorf("unknown option %q for %s (%s)", arg, cmd, hint)
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
