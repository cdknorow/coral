package ptymanager

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// F17 (#18 audit): on tmux the gate's Enter-only retry submitted a person's draft. The gate
// read the input box as still holding its own notice after the notice was submitted, and as
// "ours" once a person had typed "xyz" into it, so it pressed Enter three times.
//
// boxStartLine finds the lowest input-box line, then walks UP through every non-blank line and
// takes the highest line that starts with a box marker (>, ❯, ›) as the box's first line. Only a
// blank line or a box border stops the walk. On a screen with no border and no blank line above
// the box, the "box" therefore starts at any older line that happens to begin with a marker (a
// wrapped "< > !bang", a quoted reply, an echoed prompt), and the notice echoed in between is
// read as box text: boxTextState finds the notice's start in it and says boxOurs.
//
// Each case below holds a person's draft (or nothing) in the box; none of them holds the notice.

const f17Notice = "You have a new task in Coral (#1: Fidelity task). Claim it with `coral-agent task claim` to see the details."

func TestF17_NoticeEchoedAboveABorderlessBoxIsNotInTheBox(t *testing.T) {
	cases := map[string]struct {
		screen string
		want   boxState
	}{
		// What the raw-mode mock agent showed on tmux (no border, no blank lines): an older
		// echoed line that wrapped so a row begins with "> ", the echo of the submitted notice,
		// then the box holding the person's draft.
		"mock agent, draft after a submitted notice": {
			"[mock-agent] raw: \"quote' dq back slash $(whoami) %PATH% ^caret & | <\n" +
				"> !bang C:\\\\Program Files (x86)\\\\Coral\\\\\"\n" +
				"[mock-agent] raw: \"" + f17Notice + "\"\n" +
				"> xyz\n",
			boxOther,
		},
		// The same screen just after the notice was submitted: the box is empty.
		"mock agent, empty box after a submitted notice": {
			"[mock-agent] raw: \"quote' dq back slash $(whoami) %PATH% ^caret & | <\n" +
				"> !bang C:\\\\Program Files (x86)\\\\Coral\\\\\"\n" +
				"[mock-agent] raw: \"" + f17Notice + "\"\n" +
				"> \n",
			boxEmpty,
		},
		// A CLI that echoes each submitted prompt above its prompt line with the same marker and
		// no blank line or border between (an unbordered "›" prompt, as Codex's is drawn).
		"echoed prompt directly above an unbordered box, draft in the box": {
			"› " + f17Notice + "\n" +
				"› xyz\n" +
				"  gpt-6-astra medium · /private/tmp/cx\n",
			boxOther,
		},
	}
	for name, c := range cases {
		box, _ := inputBoxTextAny(c.screen)
		got := inputBoxState(c.screen, f17Notice, false)
		assert.Equal(t, c.want, got, "%s: box read as %q", name, strings.TrimSpace(box))
	}
}

// The control: Claude Code's box is drawn between rules, which stop the walk, so a prompt echoed
// above it is never taken as box text.
func TestF17_ControlBorderedBoxIsReadCorrectly(t *testing.T) {
	rule := strings.Repeat("─", 80)
	screen := "> " + f17Notice + "\n\n⏺ On it.\n" + rule + "\n❯\u00a0xyz\n" + rule + "\n  ⏸ manual mode on\n"
	assert.Equal(t, boxOther, inputBoxState(screen, f17Notice, false))
}
