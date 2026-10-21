package tmux

import (
	"context"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/inputlog"
)

// recordSendKeys logs a send-keys command (every one goes through run): literal
// text (-l), hex bytes (-H) or key names, with the pane it targets (#3256).
func recordSendKeys(ctx context.Context, args []string) {
	if len(args) == 0 || args[0] != "send-keys" {
		return
	}
	var target string
	var literal bool
	var hexBytes, rest []string
	for i := 1; i < len(args); i++ {
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
		inputlog.RecordCtx(ctx, target, inputlog.Raw, string(b))
	case literal:
		inputlog.RecordCtx(ctx, target, inputlog.Text, strings.Join(rest, " "))
	default:
		inputlog.RecordCtx(ctx, target, inputlog.Keys, strings.Join(rest, " "))
	}
}
