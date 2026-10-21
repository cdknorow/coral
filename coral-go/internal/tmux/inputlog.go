package tmux

import (
	"context"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/terminalinput"
)

// sendKeys runs one tmux send-keys command on behalf of o, and records it: the ONE place a key or text reaches a pane through
// send-keys (a census test fails on any other, #5706). args are send-keys' arguments, without the word itself.
func (c *Client) sendKeys(ctx context.Context, o terminalinput.Origin, args ...string) (string, error) {
	recordSendKeys(o, args)
	return c.run(ctx, append([]string{"send-keys"}, args...)...)
}

// recordSendKeys logs a send-keys command: literal text (-l), hex bytes (-H) or key names, with the pane it targets (#3256).
func recordSendKeys(o terminalinput.Origin, args []string) {
	var target string
	var literal bool
	var hexBytes, rest []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--": // the end of the options: the rest is text, even "-t" (#3690)
			rest = append(rest, args[i+1:]...)
			i = len(args)
		case a == "-t" && i+1 < len(args):
			target = args[i+1]
			i++
		case a == "-l":
			literal = true
		case a == "-H" && i+1 < len(args):
			hexBytes = append(hexBytes, args[i+1])
			i++
		default:
			rest = append(rest, a)
		}
	}
	switch {
	case len(hexBytes) > 0:
		// Decoded: the log (and its secret redaction) sees the bytes, never a hex copy of them
		// (#60, Security's condition a).
		b := make([]byte, 0, len(hexBytes))
		for _, h := range hexBytes {
			if v, err := strconv.ParseUint(h, 16, 8); err == nil {
				b = append(b, byte(v))
			}
		}
		terminalinput.Record(o, target, terminalinput.Raw, string(b))
	case literal:
		terminalinput.Record(o, target, terminalinput.Text, strings.Join(rest, " "))
	default:
		terminalinput.Record(o, target, terminalinput.Keys, strings.Join(rest, " "))
	}
}
