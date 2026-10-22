package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/config"
)

// defaultCoralURL is where a command reaches this computer's Coral when neither --url nor CORAL_URL/CORAL_PORT says otherwise:
// the platform's own default port (8450 on Windows, 8420 elsewhere, #235).
func defaultCoralURL() string { return "http://localhost:" + strconv.Itoa(config.DefaultPort) }

// command is one of coral's commands, for its help (#235): one line in the list, and its usage when asked.
type command struct {
	name, summary string
	usage         func() []string
}

// commands lists coral's commands in the order its help shows them.
func commands() []command {
	u := defaultCoralURL()
	return []command{
		{"open", "sign in to the dashboard: opens it in your browser (--print: just print the one-time link)", func() []string {
			return []string{"coral open [--print] [--path /page] [--url " + u + "] [--home DIR]"}
		}},
		{"api-key", "print this Coral's API key (a script sends it as: Authorization: Bearer <key>)", func() []string {
			return []string{"coral api-key [--home DIR]"}
		}},
		{"set-password", "set the dashboard's first password, on a running Coral", func() []string {
			return []string{"coral set-password [--stdin] [--url " + u + "] [--home DIR]"}
		}},
		{"reset-password", "set a new password, or none (--clear); every browser is signed out", func() []string {
			return []string{"coral reset-password [--clear | --stdin] [--url " + u + "] [--home DIR]"}
		}},
		{"setup-code", "print the one-time code that setting the password from another computer asks for", func() []string {
			return []string{"coral setup-code [--url " + u + "] [--home DIR]"}
		}},
		{"cloud", "Coral Hub: connect this Coral, show its status, or disconnect", func() []string {
			return []string{
				"coral cloud connect [--force] [--ticket-file <path|-> [--proof-file <path>]] [--url " + u + "] [--home DIR]",
				"coral cloud status [--url " + u + "] [--home DIR]",
				"coral cloud disconnect [--url " + u + "] [--home DIR]",
			}
		}},
		{"auto-accept", "turn auto-accept off for every agent at once", func() []string {
			return []string{"coral auto-accept off --all [--url " + u + "]"}
		}},
		{"artifacts", "prune stored artifacts (diffs and objects) to free disk", func() []string {
			return []string{
				"coral artifacts prune --older-than 14d [--kind diffs|objects|diff-objects|all] [--dry-run] [--yes] [--url ...] [--home DIR]",
				"coral artifacts prune --shrink [--older-than 0] [--dry-run] [--yes] [--url ...] [--home DIR]",
			}
		}},
		{"db", "compact the database, or rebuild its search index (stop Coral first)", func() []string {
			return []string{
				"coral db compact [--home DIR]        (stop Coral first; needs free disk about twice the database's size)",
				"coral db rebuild-search [--home DIR]  (stop Coral first; repairs the search index, after a backup)",
			}
		}},
		{"version", "print the version (also --version, -v)", func() []string { return []string{"coral version"} }},
		{"help", "this help, or a command's: coral help <command>", func() []string { return []string{"coral help [command]"} }},
	}
}

func findCommand(name string) (command, bool) {
	for _, c := range commands() {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// printHelp is `coral help`, `coral --help` and `coral -h`: the commands first, then the main server flags (#235). The full
// server flag list stays one flag away (--help-all).
func printHelp(w io.Writer) {
	fmt.Fprintln(w, "Coral: AI agents on this computer, in one dashboard.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  coral [flags]              start the Coral server (the dashboard)")
	fmt.Fprintln(w, "  coral <command> [flags]    run one command against this computer's Coral")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-16s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Main server flags:")
	fmt.Fprintf(w, "  %-16s %s\n", "--port N", "port to listen on (default "+strconv.Itoa(config.DefaultPort)+")")
	fmt.Fprintf(w, "  %-16s %s\n", "--host ADDR", "address to listen on (default: this computer only)")
	fmt.Fprintf(w, "  %-16s %s\n", "--home DIR", "data folder (default: CORAL_DATA_DIR, else .coral in your home folder)")
	fmt.Fprintf(w, "  %-16s %s\n", "--no-browser", "don't open the dashboard in the browser at startup")
	fmt.Fprintf(w, "  %-16s %s\n", "--cloud-url URL", "the Coral Hub this server may connect to (\"off\" disables it)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'coral help <command>' for a command's flags, and 'coral --help-all' for every server flag.")
}

// printCommandHelp is `coral help <command>` and `coral <command> --help`: the command's summary and usage. False for an unknown
// command.
func printCommandHelp(w io.Writer, name string) bool {
	c, ok := findCommand(name)
	if !ok {
		return false
	}
	fmt.Fprintf(w, "coral %s: %s\n\nUsage:\n", c.name, c.summary)
	for _, line := range c.usage() {
		fmt.Fprintln(w, "  "+line)
	}
	return true
}

// unknownCommandHints: the commands people type that coral does not have, and what does what they meant.
var unknownCommandHints = map[string]string{
	"stop":     "Coral runs until you stop it: Ctrl+C in its window, or Quit in the Coral tray (coral-tray --stop stops the tray).",
	"quit":     "Coral runs until you stop it: Ctrl+C in its window, or Quit in the Coral tray (coral-tray --stop stops the tray).",
	"status":   "For Coral Hub: coral cloud status. To see the dashboard: coral open.",
	"start":    "Start the server with 'coral' and no command (add --no-browser to keep the browser closed).",
	"serve":    "Start the server with 'coral' and no command (add --no-browser to keep the browser closed).",
	"run":      "Start the server with 'coral' and no command (add --no-browser to keep the browser closed).",
	"login":    "Sign in to the dashboard: coral open.",
	"connect":  "Connect to Coral Hub: coral cloud connect.",
	"password": "coral set-password (the first one) or coral reset-password.",
}

// flagParseFailure is what the binary prints when its flags do not parse, and the exit code (2, usage): for a flag it does not have, one line naming it and the hint for the
// word people type (--stop, --status), never Go's own "flag provided but not defined: -stop"; for a bad value Go's words about it.
func flagParseFailure(w io.Writer, err error) int {
	if name, ok := strings.CutPrefix(err.Error(), "flag provided but not defined: -"); ok {
		fmt.Fprintf(w, "coral: unknown option --%s\n", strings.TrimLeft(name, "-"))
		if hint, found := unknownCommandHints[strings.ToLower(strings.TrimLeft(name, "-"))]; found {
			fmt.Fprintln(w, hint)
		}
	} else {
		fmt.Fprintf(w, "coral: %v\n", err)
	}
	fmt.Fprintln(w, "Run 'coral --help' for the commands and main flags, or 'coral --help-all' for every server flag.")
	return 2
}

// unknownCommand reports a word that is neither a command nor a flag, with a hint where one exists. Exit 2 (usage).
func unknownCommand(w io.Writer, word string) int {
	fmt.Fprintf(w, "coral: unknown command %q.\n", word)
	if hint, ok := unknownCommandHints[strings.ToLower(word)]; ok {
		fmt.Fprintln(w, hint)
	}
	names := make([]string, 0, len(commands()))
	for _, c := range commands() {
		names = append(names, c.name)
	}
	fmt.Fprintf(w, "Commands: %s. Run 'coral help' for more.\n", strings.Join(names, ", "))
	return 2
}

// isHelpFlag: the words that ask for help.
func isHelpFlag(a string) bool { return a == "-h" || a == "--help" || a == "-help" }

// helpRequest answers the help a person asked for before anything else runs: `coral help [command]`, `coral -h/--help`, and
// `coral <command> ... -h/--help` (exit 0, on stdout). handled is false when the arguments are not a help request.
func helpRequest(args []string, stdout, stderr io.Writer) (code int, handled bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch {
	case args[0] == "help":
		if len(args) == 1 || strings.HasPrefix(args[1], "-") { // `coral help`, `coral help --help`, `coral help --no-browser`
			printHelp(stdout)
			return 0, true
		}
		if printCommandHelp(stdout, args[1]) {
			return 0, true
		}
		return unknownCommand(stderr, args[1]), true
	case isHelpFlag(args[0]):
		printHelp(stdout)
		return 0, true
	}
	if _, ok := findCommand(args[0]); ok {
		for _, a := range args[1:] {
			if isHelpFlag(a) {
				printCommandHelp(stdout, args[0])
				return 0, true
			}
		}
	}
	return 0, false
}
