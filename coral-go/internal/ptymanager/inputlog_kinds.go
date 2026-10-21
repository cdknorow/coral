package ptymanager

import (
	"context"
	"fmt"
	"strings"

	"github.com/subgentic/coral/internal/inputlog"
)

// logTerminalInput records input written to a PTY session the way the tmux backend sends, and so
// logs, the same input (tmux.SendTerminalInputToTarget): a single control character is a key
// (Enter, BSpace, Escape, Tab, C-<letter>), an escape sequence is raw bytes, text ending in line
// breaks is the text and then Enter, multi-line text is a paste, and anything else is text. Both
// backends then log the same entries for the same input (#60 step 4). Nothing is stored that the
// write did not already log: the text is redacted (and a person's kept as its length) by inputlog.
func logTerminalInput(ctx context.Context, name, data string) {
	if data == "" {
		return
	}
	if len(data) == 1 {
		if key := ctrlKeyName(data[0]); key != "" {
			inputlog.RecordCtx(ctx, name, inputlog.Keys, key)
			return
		}
	}
	if data[0] == '\x1b' {
		inputlog.RecordCtx(ctx, name, inputlog.Raw, data)
		return
	}
	if body := strings.TrimRight(data, "\r\n"); body != data {
		logTerminalInput(ctx, name, body)
		enters := strings.Count(data[len(body):], "\r")
		if enters == 0 {
			enters = 1 // a bare \n submits too
		}
		inputlog.RecordCtx(ctx, name, inputlog.Keys, strings.TrimSpace(strings.Repeat("Enter ", enters)))
		return
	}
	if strings.ContainsAny(data, "\r\n") {
		inputlog.RecordCtx(ctx, name, inputlog.Paste, data)
		return
	}
	inputlog.RecordCtx(ctx, name, inputlog.Text, data)
}

// ctrlKeyName is tmux's name for a control byte as SendTerminalInputToTarget sends it, or "".
func ctrlKeyName(b byte) string {
	switch b {
	case '\r':
		return "Enter"
	case 0x7f:
		return "BSpace"
	case 0x1b:
		return "Escape"
	case '\t':
		return "Tab"
	}
	if b >= 1 && b <= 26 {
		return fmt.Sprintf("C-%c", b+96)
	}
	return ""
}
