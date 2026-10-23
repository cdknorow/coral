package main

import (
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
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
// text that isn't UTF-8 is a usage error. What changes, and nothing else: a leading UTF-8 byte-order mark is
// dropped, CRLF line ends become LF, and one final newline is dropped (a heredoc's last one).
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
	return normalizeInputText(data, what, "on stdin")
}

// normalizeInputText turns the bytes of a message read from stdin or a file into its text: it must be UTF-8 and not over the cap, a leading
// byte-order mark is dropped, CRLF becomes LF and one final newline is dropped. Empty text is refused.
func normalizeInputText(data []byte, what, where string) (string, error) {
	if len(data) > maxStdinMessage {
		return "", errUsage{fmt.Sprintf("The %s %s is over %d KiB. Upload long text with coral-agent artifact upload <file> and post its link.", what, where, maxStdinMessage>>10)}
	}
	if len(data) >= 2 && ((data[0] == 0xFF && data[1] == 0xFE) || (data[0] == 0xFE && data[1] == 0xFF)) {
		return "", errUsage{fmt.Sprintf("the %s %s is UTF-16 text, not UTF-8. In Windows PowerShell 5.1 set $OutputEncoding = [Text.UTF8Encoding]::new() before piping, or use --file with a UTF-8 file", what, where)}
	}
	if !utf8.Valid(data) {
		return "", errUsage{fmt.Sprintf("the %s %s isn't UTF-8 text. In Windows PowerShell 5.1 set $OutputEncoding = [Text.UTF8Encoding]::new() before piping, or use --file with a UTF-8 file", what, where)}
	}
	text := strings.TrimPrefix(string(data), string(rune(0xFEFF))) // a UTF-8 byte-order mark (a file saved by Notepad or Windows PowerShell)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if strings.TrimSpace(text) == "" {
		return "", errUsage{fmt.Sprintf("the %s %s is empty; nothing was sent", what, where)}
	}
	return text, nil
}

// readFileText reads a message from the UTF-8 file at path (the --file option), with the same rules as stdin.
func readFileText(path, what string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errUsage{fmt.Sprintf("cannot read the %s from %s: %v", what, path, err)}
	}
	defer f.Close()
	if fi, serr := f.Stat(); serr == nil && fi.IsDir() {
		return "", errUsage{fmt.Sprintf("%s is a folder; give the path of a UTF-8 text file", path)}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxStdinMessage+1))
	if err != nil {
		return "", errUsage{fmt.Sprintf("cannot read the %s from %s: %v", what, path, err)}
	}
	return normalizeInputText(data, what, "in "+path)
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
		clierr.Exit1()
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
