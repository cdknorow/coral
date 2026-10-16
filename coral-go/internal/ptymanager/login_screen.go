package ptymanager

import (
	"regexp"
	"strings"
)

// claudeLoginRe matches the lines of Claude Code's sign-in screens (#3830, captured from
// 2.1.301 in testdata/screens/claude/2.1.301): the first-run login method menu, and the OAuth
// step that shows a URL and waits for the code. Both are drawn before the CLI writes a
// transcript, so no CLI signal reports them; once a transcript exists, its API errors do.
var claudeLoginRe = regexp.MustCompile(`^\s*(Select login method:|Paste code here if prompted|Browser didn't open\? Use the url below to sign in)`)

// loginScreenLines: the bottom lines read. The OAuth screen's URL wraps over several lines
// between its first line and the code prompt; an earlier screen still in the scrollback above a
// finished login is never read.
const loginScreenLines = 14

// ClaudeLoginOnScreen reports whether the bottom of a Claude agent's screen is one of its
// sign-in screens, and which line said so. Only what is below the agent's input box counts: a
// reply that quotes a login line sits above the box (the login method menu's "❯ 1." marker is
// a menu option, not the box).
func ClaudeLoginOnScreen(screen string) (bool, string) {
	lines := tailLines(screenText(screen), loginScreenLines)
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if isInputBoxLine(l) {
			break
		}
		if m := claudeLoginRe.FindStringSubmatch(l); m != nil {
			return true, strings.TrimSuffix(m[1], ":")
		}
	}
	return false, ""
}
