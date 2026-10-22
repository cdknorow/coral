package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// A lone "-" as a post's or send's message, or as the value of a task's --message or --body,
// reads the text from stdin (#3691). Before this, "coral-board post -" with text piped in posted
// a literal "-". Reading stdin also avoids the shell's quoting: a quoted heredoc
// (coral-board post - <<'EOF') passes backticks and $( ) through as text. "-- -" still posts a
// literal dash.

// maxStdinMessage caps the text read from stdin. The board's routes set no size limit of their own,
// so this is the CLI's: far above any real post, well below anything that would hurt the server.
const maxStdinMessage = 256 << 10

// stdinReader and stdinIsTerminal are variables so tests can stand in for os.Stdin.
var (
	stdinReader     io.Reader = os.Stdin
	stdinIsTerminal           = func() bool {
		fi, err := os.Stdin.Stat()
		return err == nil && fi.Mode()&os.ModeCharDevice != 0
	}
	stdinRead bool
)

// errUsage marks an error that exits 2 (a usage error, nothing sent).
type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

// readStdinText reads one message from stdin. A terminal on stdin, no text, text over the cap or
// text that isn't UTF-8 is a usage error. Trailing newlines (a heredoc's last one) are dropped;
// everything else is kept as written.
func readStdinText(what string) (string, error) {
	if stdinRead {
		return "", errUsage{"only one of the message and the --message or --body values can be '-' (stdin)"}
	}
	stdinRead = true
	if stdinIsTerminal() {
		return "", errUsage{fmt.Sprintf("'-' reads the %s from stdin, but stdin is a terminal. For a short post with backticks, use coral-board post - <<'EOF' (body and closing EOF on separate lines), or put -- before a literal '-'. Upload long reports/logs/diffs/evidence with coral-agent artifact upload <file> and link the returned coral://artifacts/<digest>.", what)}
	}
	data, err := io.ReadAll(io.LimitReader(stdinReader, maxStdinMessage+1))
	if err != nil {
		return "", fmt.Errorf("reading the %s from stdin: %w", what, err)
	}
	if len(data) > maxStdinMessage {
		return "", errUsage{fmt.Sprintf("the %s on stdin is over %d KiB", what, maxStdinMessage>>10)}
	}
	if !utf8.Valid(data) {
		return "", errUsage{fmt.Sprintf("the %s on stdin isn't UTF-8 text", what)}
	}
	text := strings.TrimRight(string(data), "\r\n")
	if strings.TrimSpace(text) == "" {
		return "", errUsage{fmt.Sprintf("the %s on stdin is empty; nothing was sent", what)}
	}
	return text, nil
}

// stdinTextOrExit is readStdinText for the commands that exit on error: 2 for a usage error, 1
// otherwise.
func stdinTextOrExit(what string) string {
	text, err := readStdinText(what)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		var u errUsage
		if errors.As(err, &u) {
			os.Exit(2)
		}
		os.Exit(1)
	}
	return text
}

// flagText is a --message or --body value: "-" reads it from stdin.
func flagText(v, what string) string {
	if v == "-" {
		return stdinTextOrExit(what)
	}
	return v
}

// startsCommandSubstitution reports whether trimmed text starts with an unexpanded
// shell command substitution ($(...) or `...`).
func startsCommandSubstitution(text string) bool {
	s := strings.TrimSpace(text)
	// Strip any leading @mention(s), e.g. "@Orchestrator $(cat file)"
	for strings.HasPrefix(s, "@") {
		idx := strings.IndexAny(s, " \t\n")
		if idx == -1 {
			break
		}
		s = strings.TrimSpace(s[idx:])
	}
	if strings.HasPrefix(s, "$(") && strings.Contains(s, ")") {
		return true
	}
	if strings.HasPrefix(s, "`") && !strings.HasPrefix(s, "```") && strings.Contains(s[1:], "`") {
		return true
	}
	return false
}

// checkShellSyntax verifies that text given as arguments does not start with an unexpanded
// shell command substitution ($(...) or `...`). If it does, it returns an error refusing the message
// and showing the right form. For messages that merely contain shell syntax, it prints a warning to w.
func checkShellSyntax(w io.Writer, cmdName, text string) error {
	if startsCommandSubstitution(text) {
		cmdExample := "coral-board " + cmdName
		if cmdName == "send" {
			cmdExample = "coral-board send --board <target>"
		}
		return fmt.Errorf("error: refusing message with literal unexpanded command substitution.\nFor a short post with backticks, pass literal text through a quoted heredoc:\n  %s - <<'EOF'\n@Lead Developer: review requested.\n1. Review the linked evidence.\nEOF\nStart with a one-line summary, then numbered points, one per line; answer by number.\nUse @Name; put SHAs, branches and paths in backticks.\nLong reports/logs/diffs/evidence: coral-agent artifact upload <file>, then link the returned coral://artifacts/<digest>.\nNever put backticks or $( ) inside a double-quoted post argument.", cmdExample)
	}
	warnShellSyntax(w, text)
	return nil
}

// warnShellSyntax notes, on stderr, text given as arguments that still holds a command
// substitution or a backtick: the shell ran nothing (single quotes, or an escape), and the text
// goes out as written. Only a warning: posts quote shell syntax on purpose too.
func warnShellSyntax(w io.Writer, text string) {
	if strings.Contains(text, "$(") || strings.Contains(text, "`") {
		fmt.Fprintln(w, "Note: the message holds a literal $( or backtick, sent as written. For a short post with backticks use coral-board post - <<'EOF' (body and closing EOF on separate lines); never put backticks or $( ) in double-quoted post arguments. Long reports/logs/diffs/evidence: coral-agent artifact upload <file>, then link coral://artifacts/<digest> in the short post.")
	}
}
