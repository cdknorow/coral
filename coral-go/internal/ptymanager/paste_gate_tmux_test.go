//go:build !windows

package ptymanager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/naming"
)

// pasteAwareAgent stands in for Claude in a real tmux pane, reading raw bytes with
// bracketed paste on, as the real CLIs behave (#3457): in its menu a typed digit is
// an answer and a bracketed paste is ignored (recorded in "ignored"); in its input
// box pastes and typed text are appended and Enter submits (recorded in
// "received"). With the file "menu-after-paste" present, a menu opens right after
// the next paste lands in the box; with "late", the box is repainted 800 ms after a
// paste; with "draft", the box opens holding that text once the menu is answered (a
// person's draft). Like Claude 2.1.299 it draws a no-break space after its marker.
const pasteAwareAgent = `#!/bin/bash
dir="$1"; state="${2:-menu}"
stty -icanon -echo min 1 time 0 2>/dev/null
ESC=$'\e'
printf '%s[?2004h' "$ESC"
box=""
draw_menu() { printf '\n Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and always allow\n   3. No\n\n Esc to cancel · Tab to amend\n'; }
NBSP=$'\xc2\xa0'
draw_box() { printf '\n──────────\n❯%s%s\n──────────\n  ? for shortcuts\n' "$NBSP" "$box"; }
if [ "$state" = menu ]; then draw_menu; else box="$(cat "$dir/draft" 2>/dev/null)"; draw_box; fi
acc=""; inpaste=0; paste=""
while IFS= read -r -s -n1 -d '' c; do
  if [ "$inpaste" = 1 ]; then
    paste+="$c"
    if [[ "$paste" == *"${ESC}[201~" ]]; then
      paste="${paste%"${ESC}[201~"}"; inpaste=0
      if [ "$state" = menu ]; then
        printf '%s' "$paste" >> "$dir/ignored"
      else
        box+="$paste"
        if [ -f "$dir/late" ]; then sleep 0.8; fi
        draw_box
        if [ -f "$dir/menu-after-paste" ]; then rm -f "$dir/menu-after-paste"; state=menu; draw_menu; fi
      fi
      paste=""
    fi
    continue
  fi
  acc+="$c"
  if [[ "$acc" == *"${ESC}[200~" ]]; then inpaste=1; acc=""; paste=""; continue; fi
  if [[ "$acc" == "$ESC"* ]]; then
    if [ "${#acc}" -lt 6 ]; then continue; fi
    acc=""; continue
  fi
  acc=""
  if [ "$state" = menu ]; then
    case "$c" in [1-3]) printf '%s' "$c" >> "$dir/answered"; state=box; box="$(cat "$dir/draft" 2>/dev/null)"; draw_box;; esac
  elif [ "$c" = $'\r' ] || [ "$c" = $'\n' ]; then
    printf '%s\n' "$box" >> "$dir/received"; box=""; draw_box
  else
    box+="$c"; draw_box
  fi
done
`

// raceTerminal shows the input box on its first screen read while the pane already
// shows a menu: the menu opened just after the gate looked, just before it sent.
type raceTerminal struct {
	*TmuxSessionTerminal
	lie atomic.Bool
}

func (r *raceTerminal) AgentScreen(ctx context.Context, name, agentType, sessionID string) (string, error) {
	if r.lie.CompareAndSwap(true, false) {
		return "──────────\n❯ \n──────────\n  ? for shortcuts\n", nil
	}
	return r.TmuxSessionTerminal.AgentScreen(ctx, name, agentType, sessionID)
}

const pasteTestText = "You have 1 unread message on the board (2 tasks, 3 waits)"

// startPasteAgent runs pasteAwareAgent in a real tmux pane named like an agent, whose
// pane log says bracketed paste is on.
func startPasteAgent(t *testing.T, state, tag string) (*raceTerminal, string, string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	fastGate(t)
	term := &raceTerminal{TmuxSessionTerminal: newTmuxTestTerminal(t)}
	dir := t.TempDir()
	script := filepath.Join(dir, "agent.sh")
	require.NoError(t, os.WriteFile(script, []byte(pasteAwareAgent), 0o755))
	sid := "0f1e2d3c-4b5a-4968-8776-6554433221" + tag
	name := "claude-" + sid
	logDir := t.TempDir()
	naming.SetDefaultLogDir(logDir)
	t.Cleanup(func() { naming.SetDefaultLogDir("") })
	require.NoError(t, os.WriteFile(naming.LogFile(logDir, "claude", sid), []byte("\x1b[?2004h"), 0o644))
	spawnTmuxSession(t, term.TmuxSessionTerminal, name, dir)
	require.NoError(t, term.SendToTarget(context.Background(), name+".0", "bash "+script+" "+dir+" "+state))
	marker := "Esc to cancel"
	if state == "box" {
		marker = "for shortcuts"
	}
	waitForCapture(t, term.TmuxSessionTerminal, name, marker, 10*time.Second)
	return term, name, dir
}

func readFile(dir, f string) string {
	b, _ := os.ReadFile(filepath.Join(dir, f))
	return string(b)
}

func pasteInput(term *raceTerminal, name string) AutomatedInput {
	return AutomatedInput{Term: term, Name: name, Text: pasteTestText, What: "board nudge", Paste: true,
		Send: func(ctx context.Context) error { return term.SendInput(ctx, name, pasteTestText, "", "") }}
}

// #3457: a menu that opens just after the gate looked, before it sent: the paste
// reaches the menu, which ignores it (digits and all); Enter is withheld and the send
// held. Once a person answers the menu, the text is pasted again into the input box
// and sent, exactly once.
func TestAMenuThatOpensJustBeforeThePasteIgnoresIt(t *testing.T) {
	term, name, dir := startPasteAgent(t, "menu", "a1")
	term.lie.Store(true)
	ctx := context.Background()
	d, err := DeliverAutomated(ctx, pasteInput(term, name))
	require.NoError(t, err)
	assert.Equal(t, Held, d, "Enter withheld: the paste didn't reach the input box")
	require.Eventually(t, func() bool { return readFile(dir, "ignored") == pasteTestText }, 5*time.Second, 50*time.Millisecond, "the menu got the paste and ignored it")
	time.Sleep(300 * time.Millisecond)
	assert.Empty(t, readFile(dir, "answered"), "the menu took no answer from the automated text")

	require.NoError(t, term.SendRawInput(ctx, name, []string{"1"}, "", "")) // a person answers
	require.Eventually(t, func() bool { return readFile(dir, "received") != "" }, 15*time.Second, 100*time.Millisecond, "the held send was pasted again and sent")
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, "1", readFile(dir, "answered"), "only the person's answer")
	assert.Equal(t, pasteTestText+"\n", readFile(dir, "received"), "exactly once")
}

// The paste lands in the input box, then a menu opens before its Enter: the Enter is
// withheld; after the menu is answered only the Enter is sent (the text isn't
// pasted twice).
func TestAHeldPasteSendsOnlyItsEnterWhenItsTextIsInTheBox(t *testing.T) {
	term, name, dir := startPasteAgent(t, "box", "b2")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "menu-after-paste"), nil, 0o644))
	ctx := context.Background()
	d, err := DeliverAutomated(ctx, pasteInput(term, name))
	require.NoError(t, err)
	assert.Equal(t, Held, d, "a menu opened before the Enter")
	time.Sleep(300 * time.Millisecond)
	assert.Empty(t, readFile(dir, "received"))

	require.NoError(t, term.SendRawInput(ctx, name, []string{"1"}, "", ""))
	require.Eventually(t, func() bool { return readFile(dir, "received") != "" }, 15*time.Second, 100*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, pasteTestText+"\n", readFile(dir, "received"), "sent once, not pasted twice")
	assert.Empty(t, readFile(dir, "ignored"))
}

// The hazard this closes: the same race with the text typed (no paste) answers the
// menu with the first digit in the text.
func TestATypedSendInTheSameRaceAnswersTheMenu(t *testing.T) {
	term, name, dir := startPasteAgent(t, "menu", "c3")
	term.lie.Store(true)
	in := pasteInput(term, name)
	in.Paste = false
	_, _ = DeliverAutomated(context.Background(), in)
	require.Eventually(t, func() bool { return readFile(dir, "answered") != "" }, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, "1", readFile(dir, "answered"), "a typed digit in the text answered the menu")
	assert.False(t, strings.Contains(readFile(dir, "ignored"), "unread"))
}

// #3457 v2 (QA's real-Claude check): the agent repaints its box a moment after the
// paste. The gate waits for the text instead of holding and pasting it twice.
func TestALateRepaintIsWaitedForNotPastedTwice(t *testing.T) {
	term, name, dir := startPasteAgent(t, "box", "d4")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "late"), nil, 0o644))
	d, err := DeliverAutomated(context.Background(), pasteInput(term, name))
	require.NoError(t, err)
	assert.Equal(t, Sent, d, "the text showed up within the wait")
	require.Eventually(t, func() bool { return readFile(dir, "received") != "" }, 5*time.Second, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, pasteTestText+"\n", readFile(dir, "received"), "once")
}

// On a held send's turn, an input box holding someone else's text (a person's draft)
// is never added to: the send stays held.
func TestAHeldPasteNeverAddsToSomeoneElsesDraft(t *testing.T) {
	term, name, dir := startPasteAgent(t, "menu", "e5")
	term.lie.Store(true)
	ctx := context.Background()
	d, err := DeliverAutomated(ctx, pasteInput(term, name))
	require.NoError(t, err)
	require.Equal(t, Held, d)
	require.Eventually(t, func() bool { return readFile(dir, "ignored") == pasteTestText }, 10*time.Second, 50*time.Millisecond, "the menu got the paste and ignored it")
	// A person answers the menu; the box comes back holding their draft.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "draft"), []byte("my own draft"), 0o644))
	require.NoError(t, term.SendRawInput(ctx, name, []string{"1"}, "", ""))
	time.Sleep(1500 * time.Millisecond) // several of the gate's turns
	assert.Empty(t, readFile(dir, "received"))
	// A turn takes the send off the queue before looking and puts it back after, so
	// read the count between turns.
	assert.Eventually(t, func() bool { return HeldCount(name) == 1 }, 3*time.Second, 5*time.Millisecond, "still held")
	screen, err := term.AgentScreen(ctx, name, "", "")
	require.NoError(t, err)
	box, ok := inputBoxText(screen)
	require.True(t, ok)
	assert.Equal(t, "my own draft", strings.TrimSpace(box), "nothing added to the draft")
	DropHeld(name, "test done")
}

// #3457 v3 (Lead's follow-up a): a person's draft already in the box before the first
// paste is never added to or submitted: the send is held, nothing is pasted.
func TestTheFirstPasteNeverJoinsAPersonsDraft(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	fastGate(t)
	term := &raceTerminal{TmuxSessionTerminal: newTmuxTestTerminal(t)}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "draft"), []byte("half-written question"), 0o644))
	script := filepath.Join(dir, "agent.sh")
	require.NoError(t, os.WriteFile(script, []byte(pasteAwareAgent), 0o755))
	sid := "0f1e2d3c-4b5a-4968-8776-6554433221f6"
	name := "claude-" + sid
	logDir := t.TempDir()
	naming.SetDefaultLogDir(logDir)
	t.Cleanup(func() { naming.SetDefaultLogDir("") })
	require.NoError(t, os.WriteFile(naming.LogFile(logDir, "claude", sid), []byte("\x1b[?2004h"), 0o644))
	spawnTmuxSession(t, term.TmuxSessionTerminal, name, dir)
	ctx := context.Background()
	require.NoError(t, term.SendToTarget(ctx, name+".0", "bash "+script+" "+dir+" box"))
	waitForCapture(t, term.TmuxSessionTerminal, name, "half-written question", 10*time.Second)

	in := pasteInput(term, name)
	in.AgentType, in.SessionID = "claude", sid
	d, err := DeliverAutomated(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, Held, d)
	time.Sleep(800 * time.Millisecond) // several of the gate's turns
	assert.Empty(t, readFile(dir, "received"), "nothing submitted")
	screen, err := term.AgentScreen(ctx, name, "claude", sid)
	require.NoError(t, err)
	box, ok := inputBoxText(screen)
	require.True(t, ok)
	assert.Equal(t, "half-written question", strings.TrimSpace(box), "the draft is untouched")
	DropHeld(name, "test done")
}
