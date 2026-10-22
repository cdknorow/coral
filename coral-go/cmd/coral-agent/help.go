package main

import (
	"fmt"
	"io"
	"os"

	"github.com/subgentic/coral/internal/clihelp"
)

// commands are the top-level commands main dispatches.
var commands = map[string]bool{
	"bind-codex": true, "ui": true, "launch": true, "artifact": true,
	"task": true, "history": true, "search": true, "notify": true, "docs": true,
}

// textFlags take free text that is sent to the server.
var textFlags = map[string]bool{
	"message": true, "body": true, "prompt": true, "title": true,
	"workflow-instructions": true,
}

// handleHelp prints usage when args ask for it: "--help", "-h" or "help" as
// the command, a help flag as any argument of a known command, or "help" as
// the subcommand of task, ui or history. It reports whether it handled args
// and the exit code.
func handleHelp(args []string, w io.Writer) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	cmd, rest := args[0], args[1:]
	if cmd == "help" && len(rest) > 0 && !clihelp.IsHelpArg(rest[0]) {
		return true, helpTopic("coral-agent", rest[0], w, func(name string) string {
			if commands[name] {
				return commandUsage(name, nil)
			}
			return ""
		})
	}
	if clihelp.IsHelpArg(cmd) || cmd == "help" {
		fmt.Fprintln(w, agentUsageText)
		return true, 0
	}
	subHelp := len(rest) > 0 && rest[0] == "help" && (cmd == "task" || cmd == "ui" || cmd == "history")
	if !commands[cmd] || !(subHelp || clihelp.Requested(rest)) {
		return false, 0
	}
	fmt.Fprintln(w, commandUsage(cmd, rest))
	return true, 0
}

// commandUsage returns the usage for one command, or for one task
// subcommand when rest names it.
func commandUsage(cmd string, rest []string) string {
	switch cmd {
	case "task":
		if len(rest) > 0 {
			if lines := clihelp.UsageLines(taskUsageText(), rest[0]); lines != "" {
				return "Usage: coral-agent task <subcommand> [args]\n\n" + lines
			}
		}
		return taskUsageText()
	case "history", "search":
		return historyUsageText
	case "ui":
		return uiUsageText
	case "artifact":
		return artifactUsageText
	case "notify":
		return notifyUsageText
	case "docs":
		return docsUsageText
	}
	if lines := clihelp.UsageLines(agentUsageText, cmd); lines != "" {
		return "Usage: coral-agent <command> [args]\n\n" + lines
	}
	return agentUsageText
}

// checkFreeText rejects the value of a text-taking option that is itself an
// option (task notes, titles, launch prompts), so it is never sent.
func checkFreeText(args []string) error {
	if len(args) == 0 || !commands[args[0]] {
		return nil
	}
	return clihelp.CheckTextFlags(args[1:], textFlags)
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
