package main

import (
	"fmt"
	"io"

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
		return "Usage: coral-board <command> [args]\n\n" + lines
	}
	return usageText
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
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--" {
				break
			}
			if rest[i] == "--to" {
				i++
				continue
			}
			if clihelp.IsFlagLike(rest[i]) {
				return fmt.Errorf("unknown option %q for post (put -- before message text that starts with '-')", rest[i])
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
			switch rest[i] {
			case "--board", "--to", "--address", "--reply-to", "--message-id", "--attach":
				i++
				continue
			}
			if clihelp.IsFlagLike(rest[i]) {
				return fmt.Errorf("unknown option %q for send (put -- before message text that starts with '-')", rest[i])
			}
			break
		}
	}
	return clihelp.CheckTextFlags(rest, textFlags)
}
