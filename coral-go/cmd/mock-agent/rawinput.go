package main

// Raw-input mode for input-fidelity tests (tests/stress/run_input_fidelity.sh).
//
// With MOCK_AGENT_RAW_DIR set, the mock reads its terminal the way an agent TUI does: stdin in
// raw mode (no line editing, no echo, Ctrl-C is a byte, not a signal), and bracketed paste
// enabled (ESC[?2004h, as Claude Code and Codex print it) unless MOCK_AGENT_BRACKETED_PASTE=0.
// Every byte it reads is appended, unchanged, to MOCK_AGENT_RAW_DIR/<session-id>.raw, so a test
// can compare what a terminal backend delivered with what was sent, byte for byte.
//
// It also draws an input box the way Claude Code does, so Coral's input-box checks (a person's
// send waits for the box and confirms its text there, #4587) see an agent they know: a "> " line
// showing what has been typed, or "[Pasted text #N +M lines]" for a paste of several lines or
// more than 400 bytes, emptied when a CR outside a paste submits it. Each submission is echoed as
// "[mock-agent] raw: <Go-quoted text>". Off by default: the line-mode echo in main is what every
// other suite reads.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// rawInputDir is the folder raw-input mode records into, "" when it is off.
func rawInputDir() string { return os.Getenv("MOCK_AGENT_RAW_DIR") }

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// inputBox is the mock's input line: what was typed since the last submit, and how it is shown.
type inputBox struct {
	text    bytes.Buffer // the submission so far, paste markers removed
	pasted  bool         // part of it came in a bracketed paste
	inPaste bool
	pastes  int    // pastes so far, for the placeholder's number
	pending []byte // a partial paste marker split across reads

	// trigger (MOCK_AGENT_PROMPT_TRIGGER): once the box holds it, a permission menu opens, as a
	// tool call's prompt can open while a send is being typed (#3431, audit F4). An Enter while it
	// is open answers it (option 1, as Claude Code takes it) and is logged; Esc dismisses it.
	trigger    string
	promptOpen bool
}

// promptMenu is drawn like Claude Code's permission prompt, as Coral's PromptOnScreen knows it.
const promptMenu = "\r\n Do you want to proceed?\r\n ❯ 1. Yes\r\n   2. No\r\n\r\n Esc to cancel · Tab to amend\r\n"

// display is the box's text as Claude Code shows it: a long or multi-line paste as a placeholder.
func (b *inputBox) display() string {
	s := b.text.String()
	if b.pasted && (strings.Contains(s, "\n") || len(s) > 400) {
		return fmt.Sprintf("[Pasted text #%d +%d lines]", b.pastes, strings.Count(s, "\n")+1)
	}
	s = strings.ReplaceAll(s, "\n", "⏎")
	// Keep the box on one screen row (a wrapped box would leave a stale row behind on redraw):
	// show the start, as the box's own check reads the start.
	if max := boxWidth(); utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max]) + "…"
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '·'
		}
		return r
	}, s)
}

// boxWidth is how much of the box's text fits on one row of the terminal, after "> " and "…".
func boxWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		return min(w-4, 150)
	}
	return 76
}

func drawBox(b *inputBox) { fmt.Printf("\r\x1b[K> %s", b.display()) }

// feed takes bytes read from the terminal; submit is called with each submitted text.
func (b *inputBox) feed(data []byte, submit func(string)) {
	data = append(b.pending, data...)
	b.pending = nil
	for i := 0; i < len(data); {
		rest := data[i:]
		switch {
		case bytes.HasPrefix(rest, pasteStart):
			b.inPaste, b.pasted = true, true
			b.pastes++
			i += len(pasteStart)
			continue
		case bytes.HasPrefix(rest, pasteEnd):
			b.inPaste = false
			i += len(pasteEnd)
			continue
		case rest[0] == 0x1b && len(rest) < len(pasteStart) && (bytes.HasPrefix(pasteStart, rest) || bytes.HasPrefix(pasteEnd, rest)):
			b.pending = append([]byte(nil), rest...) // a marker cut by the read: finish it next time
			return
		}
		c := rest[0]
		i++
		if b.promptOpen {
			switch c {
			case '\r':
				fmt.Print("\r\n[mock-agent] PROMPT ANSWERED by an Enter (option 1)\r\n")
				b.promptOpen = false
			case 0x1b:
				fmt.Print("\r\n[mock-agent] prompt dismissed\r\n")
				b.promptOpen = false
			}
			continue
		}
		switch {
		case c == '\r' && !b.inPaste:
			s := b.text.String()
			b.text.Reset()
			b.pasted = false
			fmt.Printf("\r\x1b[K[mock-agent] raw: %q\r\n", s)
			submit(s)
		case c == '\r' && b.inPaste:
			b.text.WriteByte('\n')
		case c == 0x15 && !b.inPaste: // Ctrl-U: clear the line, as Claude Code's box does
			b.text.Reset()
			b.pasted = false
		case (c == 0x7f || c == 0x08) && !b.inPaste: // Backspace: drop the last character
			if r := []rune(b.text.String()); len(r) > 0 {
				b.text.Reset()
				b.text.WriteString(string(r[:len(r)-1]))
			}
		case c == 0x1b && !b.inPaste:
			// A key's escape sequence (an arrow, Shift-Tab, F-keys: ESC [ ... final or ESC O x) is a key,
			// not text in the box, as a TUI reads it. A lone Esc is skipped too.
			if i < len(data) && (data[i] == '[' || data[i] == 'O') {
				j := i + 1
				if data[i] == '[' {
					for j < len(data) && (data[j] < 0x40 || data[j] > 0x7e) {
						j++
					}
				}
				i = min(j+1, len(data))
			}
		default:
			b.text.WriteByte(c)
			if b.trigger != "" && !b.inPaste && strings.Contains(b.text.String(), b.trigger) {
				b.promptOpen = true
				drawBox(b)
				fmt.Print(promptMenu)
			}
		}
	}
}

// runRawInput reads stdin in raw mode until it ends, recording every byte. It never returns
// an error to the caller: a test that finds no .raw file reports the failure.
func runRawInput(dir, sessionID, settingsFile string, turn func(string)) {
	// Hook events in order, off the read loop (a hook is a process and an HTTP call).
	hooks := newHookRunner(settingsFile, sessionID)
	events := make(chan func(), 256)
	go func() {
		for ev := range events {
			ev()
		}
	}()
	submit := func(s string) {
		// Test controls: a submitted line that is exactly one of these switches the program's cursor-key
		// mode (DECCKM), as a TUI does, so a test can check arrows are encoded for it (audit F13).
		switch s {
		case "MOCK_DECCKM_ON":
			fmt.Print("\x1b[?1h")
		case "MOCK_DECCKM_OFF":
			fmt.Print("\x1b[?1l")
		case "MOCK_BRACKETED_OFF": // an agent that dropped bracketed paste (a shell, an app that never asked)
			fmt.Print("\x1b[?2004l")
		case "MOCK_BRACKETED_ON":
			fmt.Print("\x1b[?2004h")
		}
		turn(s)
		events <- func() {
			hooks.fire("UserPromptSubmit", map[string]any{"prompt": s})
			hooks.fire("Stop", map[string]any{"stop_hook_active": false})
		}
	}

	if term.IsTerminal(int(os.Stdin.Fd())) {
		if old, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
			defer term.Restore(int(os.Stdin.Fd()), old)
		} else {
			fmt.Fprintf(os.Stderr, "[mock-agent] raw mode: %v\r\n", err)
		}
	}
	if os.Getenv("MOCK_AGENT_BRACKETED_PASTE") != "0" {
		fmt.Print("\x1b[?2004h")
	}
	name := sessionID
	if name == "" {
		name = fmt.Sprintf("pid-%d", os.Getpid())
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".raw"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[mock-agent] raw log: %v\r\n", err)
		return
	}
	defer f.Close()
	fmt.Print("[mock-agent] raw input ready\r\n")

	events <- func() {
		hooks.fire("SessionStart", map[string]any{"source": "startup"})
		hooks.fire("Stop", map[string]any{"stop_hook_active": false})
	}
	box := &inputBox{trigger: os.Getenv("MOCK_AGENT_PROMPT_TRIGGER")}
	drawBox(box)
	buf := make([]byte, 64*1024)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			_, _ = f.Write(buf[:n])
			box.feed(buf[:n], submit)
			if !box.promptOpen {
				drawBox(box) // a redraw would hide the open menu under the box line
			}
		}
		if err != nil {
			return
		}
	}
}
