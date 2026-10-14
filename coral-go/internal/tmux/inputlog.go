package tmux

import (
	"strings"

	"github.com/cdknorow/coral/internal/inputlog"
)

// recordSendKeys logs a send-keys command (every one goes through run): literal
// text (-l), hex bytes (-H) or key names, with the pane it targets (#3256).
func recordSendKeys(args []string) {
	if len(args) == 0 || args[0] != "send-keys" {
		return
	}
	var target string
	var literal bool
	var hexBytes, rest []string
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
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
		inputlog.RecordHexKeys(target, hexBytes)
	case literal:
		inputlog.Record(target, inputlog.Text, strings.Join(rest, " "))
	default:
		inputlog.Record(target, inputlog.Keys, strings.Join(rest, " "))
	}
}
