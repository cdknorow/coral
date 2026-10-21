//go:build !windows

package ptymanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// fakePromptAgent stands in for Claude in a real tmux pane: it draws a
// permission menu and reads the answer, then draws its input box and records
// every line typed there. It doesn't clear the answered menu (the pane may
// have no TERM to clear with), so the gate must see that the input box drawn
// below it means the menu is closed.
const fakePromptAgent = `#!/bin/sh
dir="$1"
printf ' Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and don'"'"'t ask again\n   3. No\n\n Esc to cancel · Tab to amend\n'
IFS= read -r answer
printf '%s\n' "$answer" > "$dir/answered"
printf '⏺ Done.\n\n╭────────╮\n│ >      │\n╰────────╯\n  ? for shortcuts\n'
while IFS= read -r line; do printf '%s\n' "$line" >> "$dir/received"; done
`

// The gate with a real tmux pane (#3431): an automated message sent while the
// pane shows a permission menu is not typed (the menu reads no answer); after a
// person answers it, the message is typed into the input box exactly once.
func TestGateHoldsAutomatedInputInARealTmuxPane(t *testing.T) {
	fastGate(t)
	term := newTmuxTestTerminal(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "agent.sh")
	require.NoError(t, os.WriteFile(script, []byte(fakePromptAgent), 0o755))
	name := "claude-0f1e2d3c-4b5a-4968-8776-655443322110"
	spawnTmuxSession(t, term, name, dir)
	ctx := context.Background()
	require.NoError(t, term.SendToTarget(ctx, terminalinput.Unclassified, name+".0", "sh "+script+" "+dir))
	waitForCapture(t, term, name, "Esc to cancel", 10*time.Second)

	in := AutomatedInput{Term: term, Name: name, Text: "You have 1 unread message", What: "board nudge",
		Send: func(ctx context.Context) error {
			return term.SendInput(ctx, terminalinput.Unclassified, name, "You have 1 unread message", "", "")
		}}
	d, err := DeliverAutomated(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	time.Sleep(time.Second)
	_, statErr := os.Stat(filepath.Join(dir, "answered"))
	assert.True(t, os.IsNotExist(statErr), "the menu got no answer from automated input")

	// A person answers the menu (an explicit human action, not gated).
	require.NoError(t, term.SendInput(ctx, terminalinput.Unclassified, name, "1", "", ""))
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(dir, "answered"))
		return err == nil && strings.TrimSpace(string(b)) == "1"
	}, 10*time.Second, 50*time.Millisecond, "the person's answer reached the menu")

	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "received"))
		return strings.Contains(string(b), "You have 1 unread message")
	}, 15*time.Second, 100*time.Millisecond, "the held message was typed once the menu closed")
	time.Sleep(500 * time.Millisecond)
	b, _ := os.ReadFile(filepath.Join(dir, "received"))
	assert.Equal(t, "You have 1 unread message\n", string(b), "exactly once")
}

// A first prompt held for one launch never lands in the next launch in the
// same terminal (Backend's #3431 review, required 2): killing the terminal and
// creating it again under the same name drops what was held for the old one.
func TestAHeldFirstPromptNeverReachesTheNextLaunch(t *testing.T) {
	fastGate(t)
	term := newTmuxTestTerminal(t)
	dir := t.TempDir()
	name := "claude-1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	spawnTmuxSession(t, term, name, dir)
	ctx := context.Background()
	in := AutomatedInput{Term: term, Name: name, Key: "initial-prompt", Text: "old launch's prompt", What: "initial prompt", RequireInputBox: true,
		Send: func(ctx context.Context) error {
			return term.SendInput(ctx, terminalinput.Unclassified, name, "old launch's prompt", "", "")
		}}
	d, err := DeliverAutomated(ctx, in)
	require.NoError(t, err)
	require.Equal(t, Held, d, "a plain shell shows no input box yet")

	require.NoError(t, term.KillSession(ctx, name, "", ""))
	assert.Zero(t, HeldCount(name), "killing the terminal drops what was held for it")
	spawnTmuxSession(t, term, name, dir)
	// The new launch reaches its input box; nothing from the old one may arrive.
	script := filepath.Join(dir, "box.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '\\n╭────────╮\\n│ >      │\\n╰────────╯\\n'\nwhile IFS= read -r l; do printf '%s\\n' \"$l\" >> \"$1/received\"; done\n"), 0o755))
	require.NoError(t, term.SendToTarget(ctx, terminalinput.Unclassified, name+".0", "sh "+script+" "+dir))
	time.Sleep(time.Second)
	_, statErr := os.Stat(filepath.Join(dir, "received"))
	assert.True(t, os.IsNotExist(statErr), "the old launch's prompt never reached the new launch")
}
