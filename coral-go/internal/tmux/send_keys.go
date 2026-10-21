package tmux

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/inputlog"
)

// SendKeys types text into a tmux pane: a message, an answer, anything a person or Coral wrote
// for the agent. It arrives as that text at any length (multi-line or long text as one paste
// that respects the pane's bracketed paste mode, a short line with send-keys -l), and is NEVER
// turned into a script the pane's shell would run (#4361: an over-900-character dashboard
// message reached the agent as ". '/tmp/coral-command-N.sh'").
func (c *Client) SendKeys(ctx context.Context, agentName, command, agentType, sessionID string) error {
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}

	return c.GoneAfter(ctx, agentName, agentType, sessionID, c.typeToTarget(ctx, target, command, false))
}

// errEmptyDisplay stands for a display-message that answered nothing, for GoneAfter.
var errEmptyDisplay = errors.New("tmux display-message answered nothing")

// GoneAfter is err, the failure of a command on the agent's pane, unless the pane is gone now:
// then ErrTerminalGone, as the lookup itself reported before the shared pane list (#4049). The
// target can come from that list, up to a second old, so a pane killed since still resolves and
// only the command fails; the list is dropped and the pane looked up again. A delivery whose
// state is unknown, a withheld Enter or a cancelled ctx stays what it was.
func (c *Client) GoneAfter(ctx context.Context, agentName, agentType, sessionID string, err error) error {
	if err == nil || ctx.Err() != nil || errors.Is(err, ErrTerminalGone) || errors.Is(err, ErrDeliveryUnknown) ||
		errors.Is(err, ErrEnterWithheld) {
		return err
	}
	c.forgetPaneStates()
	if target, ferr := c.FindPaneTarget(ctx, agentName, agentType, sessionID); ferr == nil && target == "" {
		return fmt.Errorf("%w: pane %q not found in any tmux session (%v)", ErrTerminalGone, agentName, err)
	}
	return err
}

// ErrEnterWithheld is returned when a send typed its text but withheld the
// final Enter because the WithBeforeEnter check failed (#3431).
var ErrEnterWithheld = errors.New("the text was typed but its Enter was withheld: a prompt opened")

type beforeEnterKey struct{}

// WithBeforeEnter installs a check run just before a send's final Enter (after
// its text or paste). If it reports false, the Enter is not sent and the send
// returns ErrEnterWithheld: the automated-input gate uses it so a permission
// prompt that opens between the text and the Enter doesn't take the Enter.
func WithBeforeEnter(ctx context.Context, ok func(context.Context) bool) context.Context {
	return context.WithValue(ctx, beforeEnterKey{}, ok)
}

type automatedKey struct{}

// WithAutomated marks a send as Coral's own (the automated-input gate's: a board message, a task notice, a nudge,
// an agent's /send), as opposed to a person's. Text typed under it is made safe first (AutomatedText), on both backends.
func WithAutomated(ctx context.Context) context.Context {
	return context.WithValue(ctx, automatedKey{}, true)
}

// Automated says whether ctx carries WithAutomated.
func Automated(ctx context.Context) bool { v, _ := ctx.Value(automatedKey{}).(bool); return v }

// EnterAllowed runs ctx's WithBeforeEnter check (true when there is none).
func EnterAllowed(ctx context.Context) bool { return enterAllowed(ctx) }

// enterAllowed runs ctx's WithBeforeEnter check (true when there is none).
func enterAllowed(ctx context.Context) bool {
	ok, _ := ctx.Value(beforeEnterKey{}).(func(context.Context) bool)
	return ok == nil || ok(ctx)
}

// sendKeysSettle is the pause between typing a command and its Enter.
var sendKeysSettle = 300 * time.Millisecond

// SetSendKeysSettleForTest shortens the pause SendKeysToTarget takes before
// Enter and returns a func that restores it. Call it from TestMain, before
// any test runs: the value is read without a lock.
func SetSendKeysSettleForTest(d time.Duration) (restore func()) {
	old := sendKeysSettle
	sendKeysSettle = d
	return func() { sendKeysSettle = old }
}

// SendKeysToTarget types a COMMAND Coral built (a launch line, a test's shell command) into a
// specific tmux target: for a shell, never for text written for an agent (that is SendKeys, which
// never wraps). A command over inlineCommandLimit is handed over as a private script the shell
// sources, because a fresh shell's input buffer is small; so only a command Coral composed may come
// here (TestOnlyLaunchPathsWrapACommand pins its callers).
// Multi-line text uses tmux paste-buffer -p which respects the pane's
// bracketed paste mode — if the shell has it enabled, tmux wraps the
// paste in \e[200~...\e[201~; otherwise it sends raw text.
func (c *Client) SendKeysToTarget(ctx context.Context, target, command string) error {
	return c.typeToTarget(ctx, target, command, true)
}

// typeToTarget is the typing both paths share. launch: the text is a command Coral composed, and a
// long one goes through a script (commandInvocation); otherwise it is delivered as text at any
// length.
func (c *Client) typeToTarget(ctx context.Context, target, command string, launch bool) error {
	if !launch && Automated(ctx) {
		// Typed, automated text: no keystroke from the text itself, and no line break (which would submit line by line
		// into whatever is reading: tmux cannot tell whether the pane asked for bracketed paste, and the gate pastes
		// when it did, so what is typed here is the case it did not).
		command = AutomatedText(command)
	}
	originalCommandLen := len(command)
	cleanup := func() {}
	if launch && len(command) > inlineCommandLimit {
		var err error
		command, cleanup, err = commandInvocation(command)
		if err != nil {
			return err
		}
	}
	// A launch command that is not submitted must not stay typed in the shell: the next text typed into the pane (a board nudge) would be
	// appended to it, and run (#5171). abandon, once the line is typed, clears it (best effort, whatever became of the request) and removes the script.
	abandon := func() {
		if launch {
			clearInputLine(c, target)
		}
		cleanup()
	}
	var sendErr error
	// A long single line is pasted too (not an argument of send-keys): text for an agent has no
	// length limit, and a launch command that became a short ". path" line is under it anyway.
	if strings.Contains(command, "\n") || (!launch && len(command) > inlineCommandLimit) {
		sendErr = c.pasteToTarget(ctx, target, command)
	} else {
		// "--": text starting with '-' ("- fix the tests", "--help") is typed, not read as
		// tmux options (#3690).
		_, sendErr = c.run(ctx, "send-keys", "-t", target, "-l", "--", command)
	}
	if sendErr != nil {
		// No Enter after a failed send: it would submit whatever was already in the agent's
		// input instead of this text (#3690). Nothing was typed, so nothing is cleared.
		cleanup()
		// Coalesced like the exec failure it follows (Security's #4112 note): one line, then a
		// count every few minutes, for a send failing again and again on one pane.
		if ok, heldBack := execFailures.Allow("send-keys error\x00" + c.SocketPath + "\x00" + target + "\x00" + sendErr.Error()); ok {
			more := ""
			if heldBack > 0 {
				more = fmt.Sprintf(" (and %d more like it in the last %s)", heldBack, execFailureEvery)
			}
			log.Printf("[tmux] send-keys error (target=%s, cmd_len=%d): %v%s", target, originalCommandLen, sendErr, more)
		}
		return fmt.Errorf("send-keys failed: %w", sendErr)
	}

	// Brief pause for tmux to deliver keystrokes
	time.Sleep(sendKeysSettle)

	if !enterAllowed(ctx) {
		abandon()
		return ErrEnterWithheld
	}

	// The line is typed: a launch command's Enter finishes whatever became of the request (its context ended, the client went away), as the
	// PTY path's does, or the shell is left with a typed line and no script (#5171).
	enterCtx := ctx
	if launch {
		var cancel context.CancelFunc
		enterCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), launchEnterTimeout)
		defer cancel()
	}
	if _, err := c.run(enterCtx, "send-keys", "-t", target, "Enter"); err != nil {
		abandon()
		return fmt.Errorf("send Enter failed: %w", err)
	}
	return nil
}

// launchEnterTimeout bounds the Enter of a launch command typed into a shell, which no request context can cut short.
const launchEnterTimeout = 5 * time.Second

// clearInputLine empties the shell's input line on target (Ctrl-U kills the line in bash, zsh and fish), best effort, on a context of its own.
func clearInputLine(c *Client, target string) {
	ctx, cancel := context.WithTimeout(context.Background(), launchEnterTimeout)
	defer cancel()
	_, _ = c.run(ctx, "send-keys", "-t", target, "C-u")
}

// pasteToTarget loads text into a tmux buffer and pastes it into the target pane.
// Uses paste-buffer -p which respects the pane's bracketed paste mode setting.
func (c *Client) pasteToTarget(ctx context.Context, target, text string) error {
	inputlog.Record(target, inputlog.Paste, text)
	// A private named buffer, deleted by the paste (-d) or on an error, as SendPrompt's (#4381,
	// Security's #4371 SHOULD 2): the unnamed one stayed in tmux's buffer stack (its last 50), on a
	// tmux server that outlives Coral's upgrades.
	countExec("load-buffer")
	buffer, err := c.loadPromptBuffer(ctx, text)
	if err != nil {
		return err
	}
	// -p: bracketed paste where the pane asked for it.
	if _, err := c.run(ctx, "paste-buffer", "-d", "-p", "-b", buffer, "-t", target); err != nil {
		_, _ = c.run(context.WithoutCancel(ctx), "delete-buffer", "-b", buffer)
		return fmt.Errorf("paste-buffer failed: %w", err)
	}
	return nil
}

// SendRawKeys sends raw tmux key names (e.g. "BTab", "Escape") to a pane.
func (c *Client) SendRawKeys(ctx context.Context, agentName string, keys []string, agentType, sessionID string) error {
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}

	for i, key := range keys {
		// "--": a key from the request that starts with "-" is a key, never a tmux option (-X, -F,
		// -H...) (#3930, Security's #3927 finding 5).
		if _, err := c.run(ctx, "send-keys", "-t", target, "--", key); err != nil {
			// Never the key itself: errors reach the log, and keys are what was typed (#4112).
			return c.GoneAfter(ctx, agentName, agentType, sessionID, fmt.Errorf("send-keys failed (key %d of %d): %w", i+1, len(keys), err))
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// SendTerminalInputToTarget sends raw terminal input data to a resolved tmux target.
// Handles control characters, escape sequences, and multi-line text.
func (c *Client) SendTerminalInputToTarget(ctx context.Context, target, data string) error {
	// Control character map
	ctrlMap := map[string]string{
		"\r":   "Enter",
		"\x7f": "BSpace",
		"\x1b": "Escape",
		"\t":   "Tab",
	}

	// Single control character
	if len(data) == 1 {
		if key, ok := ctrlMap[data]; ok {
			_, err := c.run(ctx, "send-keys", "-t", target, "--", key)
			return err
		}
		// Ctrl+<letter> (0x01-0x1a)
		b := data[0]
		if b >= 1 && b <= 26 {
			key := fmt.Sprintf("C-%c", b+96)
			_, err := c.run(ctx, "send-keys", "-t", target, "--", key)
			return err
		}
	}

	// Escape sequences (arrow keys, function keys, etc.)
	if len(data) > 0 && data[0] == '\x1b' {
		hexArgs := []string{"send-keys", "-t", target}
		for _, b := range []byte(data) {
			hexArgs = append(hexArgs, "-H", fmt.Sprintf("%02x", b))
		}
		_, err := c.run(ctx, hexArgs...)
		return err
	}

	// Text that ends in a line break ("ls\r"): the Enter is a key press, not part of the
	// text. Pasted, the \r stays inside the bracketed paste and zsh (and any readline
	// shell that honours bracketed paste) takes it as pasted text: the command sits on
	// the line, unsubmitted. Send the text, then Enter as a key.
	if body := strings.TrimRight(data, "\r\n"); body != data {
		if body != "" {
			if err := c.SendTerminalInputToTarget(ctx, target, body); err != nil {
				return err
			}
		}
		enters := strings.Count(data[len(body):], "\r")
		if enters == 0 {
			enters = 1 // a bare \n submits too
		}
		for i := 0; i < enters; i++ {
			if _, err := c.run(ctx, "send-keys", "-t", target, "Enter"); err != nil {
				return err
			}
		}
		return nil
	}

	// Multi-line text: use tmux paste-buffer -p (bracket paste aware)
	if strings.ContainsAny(data, "\n\r") {
		return c.pasteToTarget(ctx, target, data)
	}

	// Single-line literal text ("--": a leading '-' is text, not a tmux option, #3690)
	_, err := c.run(ctx, "send-keys", "-t", target, "-l", "--", data)
	return err
}
