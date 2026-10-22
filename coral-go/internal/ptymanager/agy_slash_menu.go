package ptymanager

import (
	"regexp"
	"strings"
)

// agySlashHintRe is the hint line agy draws under its slash-completion list ("↑/↓ Navigate · enter Select · tab Complete"). The list is
// agy's own completion of the command being typed, not a question to the person; a real prompt has another hint (the trust prompt's is
// "enter Confirm"), and tab Complete is only in the completion list.
var agySlashHintRe = regexp.MustCompile(`^↑/↓ Navigate · enter Select · tab Complete$`)

// agySlashItemRe is the highlighted completion: "> /help  Show available commands and keybindings".
var agySlashItemRe = regexp.MustCompile(`^>\s+(/\S+)(\s|$)`)

// AgySlashMenuFor reports whether screen is agy's slash-completion menu opened by the command text just typed, and nothing else: the Enter
// then confirms that very command, and holding it as a prompt means the command never runs (#210). It is true only when
//   - the agent is agy, and text is one slash command line (its first word starts with "/"),
//   - the screen ends in the completion list: a highlighted "> /command" item, the "tab Complete" hint and the "esc to cancel" footer, and
//   - the highlighted command is the one typed (so Enter does not pick another command),
//
// and when PromptOnScreen sees nothing but that footer (a dialog, a selection menu or a question is still a prompt).
func AgySlashMenuFor(agentType, text, screen string) bool {
	if !isAgyType(agentType) {
		return false
	}
	typed := strings.Fields(text)
	if len(typed) == 0 || !strings.HasPrefix(typed[0], "/") || strings.ContainsAny(text, "\r\n") {
		return false
	}
	if open, why := PromptOnScreen(screen); !open || !strings.HasPrefix(why, footerWhy) {
		return false
	}
	lines := tailLines(screenText(screen), promptScreenLines)
	item, hint, footer := "", false, false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if m := agySlashItemRe.FindStringSubmatch(t); m != nil {
			item = m[1]
		}
		if agySlashHintRe.MatchString(t) {
			hint = true
		}
		if strings.HasPrefix(t, "esc to cancel") {
			footer = true
		}
	}
	return hint && footer && item != "" && item == typed[0]
}
