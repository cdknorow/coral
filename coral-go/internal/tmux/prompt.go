package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ErrBracketedPasteUnavailable means the target application is not currently
// accepting bracketed paste, so a multi-line prompt cannot be delivered as one
// logical input without risking early submission at the first newline.
var ErrBracketedPasteUnavailable = errors.New("the agent terminal is not accepting multi-line paste right now (bracketed paste mode is off or could not be confirmed); try again when the agent is at its prompt")

type pasteHintKey struct{}

// WithBracketedPasteHint tells SendPrompt what the caller knows about the
// pane's bracketed paste mode (for example from the pane's output log), since
// tmux exposes no format variable for it. A hint of false makes SendPrompt
// refuse; with no hint, SendPrompt also refuses without sending any input.
func WithBracketedPasteHint(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, pasteHintKey{}, enabled)
}

// BracketedPasteHint returns a hint set with WithBracketedPasteHint.
func BracketedPasteHint(ctx context.Context) (enabled, known bool) {
	enabled, known = ctx.Value(pasteHintKey{}).(bool)
	return enabled, known
}

// promptSubmitDelay separates the paste from the submitting Enter.
var promptSubmitDelay = 300 * time.Millisecond

// SanitizePaste prepares text for a bracketed paste: it keeps the text intact
// (including newlines and tabs) but removes ESC and other control characters
// that could end the paste early (ESC [ 2 0 1 ~) or act as input, and
// normalizes CRLF/CR to LF.
func SanitizePaste(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ErrDeliveryUnknown marks a failure after input may already have reached the
// pane (a paste or Enter that errored or timed out). The prompt may be fully,
// partly or not at all in the agent's input line, so callers must NOT resend it.
var ErrDeliveryUnknown = errors.New("prompt delivery state is unknown: input may have reached the agent")

// DeliveryUnknownError wraps the underlying failure; errors.Is matches
// ErrDeliveryUnknown.
type DeliveryUnknownError struct{ Err error }

func (e *DeliveryUnknownError) Error() string {
	return ErrDeliveryUnknown.Error() + ": " + e.Err.Error()
}
func (e *DeliveryUnknownError) Unwrap() error        { return e.Err }
func (e *DeliveryUnknownError) Is(target error) bool { return target == ErrDeliveryUnknown }

// DeliveryUnknown lets packages that cannot import tmux detect the condition.
func (e *DeliveryUnknownError) DeliveryUnknown() bool { return true }

// MarkDeliveryUnknown wraps err as a delivery-unknown failure.
func MarkDeliveryUnknown(err error) error { return &DeliveryUnknownError{Err: err} }

// promptFinishTimeout bounds the detached tail of a delivery (paste, delay,
// Enter) once input has started, so cancellation cannot strand a half-sent
// prompt but a wedged tmux cannot hang forever either.
var promptFinishTimeout = 15 * time.Second

var (
	promptLocksMu sync.Mutex
	promptLocks   = map[string]chan struct{}{}
)

// lockPromptTarget serializes whole prompts (paste, delay, Enter) per pane so
// concurrent prompts to one agent cannot interleave. Waiting honours ctx; no
// input has been sent while waiting.
func lockPromptTarget(ctx context.Context, key string) (unlock func(), err error) {
	promptLocksMu.Lock()
	ch := promptLocks[key]
	if ch == nil {
		ch = make(chan struct{}, 1)
		promptLocks[key] = ch
	}
	promptLocksMu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SendPrompt delivers text to an agent's pane as ONE logical input: a single
// bracketed paste (never the shell-script handoff used for long commands),
// followed by Enter. paste-buffer -p brackets the paste only when the pane's
// application enabled bracketed paste; tmux has no format variable to query
// that, so SendPrompt requires an affirmative hint (see WithBracketedPasteHint).
// Unknown or disabled mode returns ErrBracketedPasteUnavailable without input.
//
// Retry safety: a cancelled ctx is honoured only BEFORE any input is sent
// (zero bytes, safe to retry). Once the paste starts, paste and Enter finish
// under a bounded context detached from ctx, and any later failure is returned
// as ErrDeliveryUnknown, which must not be retried.
func (c *Client) SendPrompt(ctx context.Context, agentName, text, agentType, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("pane %q not found in any tmux session", agentName)
	}
	if enabled, known := BracketedPasteHint(ctx); !known || !enabled {
		return ErrBracketedPasteUnavailable
	}
	unlock, err := lockPromptTarget(ctx, c.SocketPath+"|"+target)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	buffer, err := c.loadPromptBuffer(ctx, SanitizePaste(text))
	if err != nil {
		return err
	}
	// Last chance to abort with nothing sent.
	if err := ctx.Err(); err != nil {
		_, _ = c.run(context.WithoutCancel(ctx), "delete-buffer", "-b", buffer)
		return err
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), promptFinishTimeout)
	defer cancel()
	if _, err := c.run(dctx, "paste-buffer", "-d", "-p", "-r", "-b", buffer, "-t", target); err != nil {
		_, _ = c.run(context.WithoutCancel(ctx), "delete-buffer", "-b", buffer)
		return MarkDeliveryUnknown(fmt.Errorf("paste-buffer failed: %w", err))
	}
	select {
	case <-time.After(promptSubmitDelay):
	case <-dctx.Done():
		return MarkDeliveryUnknown(fmt.Errorf("pasted the prompt but timed out before Enter: %w", dctx.Err()))
	}
	if _, err := c.run(dctx, "send-keys", "-t", target, "Enter"); err != nil {
		return MarkDeliveryUnknown(fmt.Errorf("pasted the prompt but sending Enter failed: %w", err))
	}
	return nil
}

// loadPromptBuffer loads text into a private named buffer; paste-buffer -d then
// deletes it after pasting, so prompt text does not linger in the buffer stack.
// -r (at paste time) keeps LF as LF, which tmux would otherwise rewrite to CR.
func (c *Client) loadPromptBuffer(ctx context.Context, text string) (string, error) {
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	buffer := "coral-prompt-" + hex.EncodeToString(nonce[:])
	args := []string{"load-buffer", "-b", buffer, "-"}
	if c.SocketPath != "" {
		args = append([]string{"-S", c.SocketPath}, args...)
	}
	bin := c.resolveTmuxBin()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = tmuxEnv(bin)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("load-buffer failed: %w", err)
	}
	return buffer, nil
}
